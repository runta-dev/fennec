import CryptoKit
import FennecCore
import ImageIO
import SwiftUI
import UniformTypeIdentifiers

struct DraftAttachment: Identifiable, Hashable {
    var id = UUID()
    var url: URL
    var name: String
    var isImage: Bool
    var uploaded: UploadedAttachment?
}

struct WorkspaceFile: Decodable, Hashable, Identifiable {
    var ref: String
    var name: String
    var title: String
    var kind: String
    var id: String { ref }
}

struct UploadedAttachment: Decodable, Hashable {
    var path: String
    var preview: WorkspaceFile
}

struct PreviewPart: Decodable {
    var text: String?
    var ref: String?

    static func parse(_ text: String) -> [PreviewPart] {
        let json = FennecmobilePreviewParts(text)
        return (try? JSONDecoder().decode([Self].self, from: Data(json.utf8))) ?? []
    }
}

/// Picker/clipboard data is copied into an app-owned draft before provider access ends.
enum AttachmentImport {
    static let maximumBytes = 64 * 1024 * 1024

    static func discard(_ attachments: [DraftAttachment]) {
        for attachment in attachments {
            try? FileManager.default.removeItem(at: attachment.url.deletingLastPathComponent())
        }
    }

    static func files(_ urls: [URL]) throws -> [DraftAttachment] {
        var attachments: [DraftAttachment] = []
        do {
            for url in urls { attachments.append(try file(url)) }
            return attachments
        } catch {
            discard(attachments)
            throw error
        }
    }

    static func file(_ url: URL) throws -> DraftAttachment {
        let access = url.startAccessingSecurityScopedResource()
        defer { if access { url.stopAccessingSecurityScopedResource() } }
        var coordinationError: NSError?
        var result: Result<DraftAttachment, Error>?
        NSFileCoordinator().coordinate(readingItemAt: url, options: [], error: &coordinationError) { source in
            result = Result {
                let size = try source.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0
                guard size <= maximumBytes else { throw AttachmentError.tooLarge }
                if UTType(filenameExtension: source.pathExtension)?.conforms(to: .image) == true {
                    return try image(Data(contentsOf: source), name: source.deletingPathExtension().lastPathComponent)
                }
                let destination = try directory().appending(path: source.lastPathComponent)
                try FileManager.default.copyItem(at: source, to: destination)
                return DraftAttachment(url: destination, name: source.lastPathComponent, isImage: false)
            }
        }
        if let coordinationError { throw coordinationError }
        guard let result else { throw AttachmentError.unavailable }
        return try result.get()
    }

    static func image(_ data: Data, name: String = "Image") throws -> DraftAttachment {
        guard data.count <= maximumBytes else { throw AttachmentError.tooLarge }
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceThumbnailMaxPixelSize: 2048
              ] as CFDictionary), let png = UIImage(cgImage: image).pngData() else { throw AttachmentError.unavailable }
        guard png.count <= 20 * 1024 * 1024 else { throw AttachmentError.tooLarge }
        let filename = URL(fileURLWithPath: name).lastPathComponent + ".png"
        let destination = try directory().appending(path: filename)
        try png.write(to: destination, options: .atomic)
        return DraftAttachment(url: destination, name: filename, isImage: true)
    }

    private static func directory() throws -> URL {
        let directory = URL.cachesDirectory.appending(path: "attachment-drafts/" + UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory
    }
}

enum AttachmentError: LocalizedError {
    case tooLarge, unavailable, tooMany
    var errorDescription: String? {
        switch self {
        case .tooLarge: L10n.text("ios.attachment_too_large")
        case .unavailable: L10n.text("ios.attachment_unavailable")
        case .tooMany: L10n.text("ios.attachment_too_many")
        }
    }
}

extension SessionStore {
    func addAttachments(_ attachments: [DraftAttachment], to id: String) throws {
        guard (attachmentDrafts[id]?.count ?? 0) + attachments.count <= 8 else {
            AttachmentImport.discard(attachments)
            throw AttachmentError.tooMany
        }
        attachmentDrafts[id, default: []] += attachments
    }

    func removeAttachment(_ attachment: DraftAttachment, from id: String) {
        attachmentDrafts[id]?.removeAll { $0.id == attachment.id }
        try? FileManager.default.removeItem(at: attachment.url.deletingLastPathComponent())
    }

    func preview(_ file: WorkspaceFile, sessionID: String) async throws -> URL {
        guard let core else { throw AttachmentError.unavailable }
        let key = sessionID + "\n" + file.ref
        if let task = previewDownloads[key] { return try await task.value }
        let digest = SHA256.hash(data: Data(key.utf8)).map { String(format: "%02x", $0) }.joined()
        let directory = URL.cachesDirectory.appending(path: "workspace-previews/" + digest + "/" + UUID().uuidString, directoryHint: .isDirectory)
        let destination = directory.appending(path: file.name)
        let task = Task<URL, Error> {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            // A new view reloads the file; concurrent requests share this transfer.
            try await Task.detached {
                try core.downloadPreview(sessionID, ref: file.ref, destination: destination.path)
            }.value
            return destination
        }
        previewDownloads[key] = task
        defer { previewDownloads[key] = nil }
        return try await task.value
    }

    func previewMetadata(_ ref: String, sessionID: String) async throws -> WorkspaceFile {
        guard let core else { throw AttachmentError.unavailable }
        let json = try await Task.detached { try goCall { core.previewMetadata(sessionID, ref: ref, error: $0) } }.value
        return try JSONDecoder().decode(WorkspaceFile.self, from: Data(json.utf8))
    }

    func sendAttachments(_ text: String, to id: String, onSent: @escaping () -> Void) {
        if core == nil {
            sendDemoAttachments(text, to: id, onSent: onSent)
            return
        }
        guard let core, let session = session(id), let address = session.address,
              pendingSends[id] == nil, let attachments = attachmentDrafts[id], !attachments.isEmpty else { return }
        let images = attachments.filter(\.isImage).compactMap { try? Data(contentsOf: $0.url) }
        pendingSends[id] = PendingSend(message: .init(kind: .user(text), time: .now, images: images),
                                      previousMatches: matchingPrompts(text, in: session.transcriptItems))
        let messageID = pendingSends[id]?.message.id
        Task {
            var submitted = false
            do {
                var paths: [String] = []
                for attachment in attachments where !attachment.isImage {
                    let uploaded: UploadedAttachment
                    if let existing = attachment.uploaded { uploaded = existing }
                    else {
                        let json = try await Task.detached {
                            try goCall { core.uploadFile(address, sessionID: id, uploadID: attachment.id.uuidString.replacingOccurrences(of: "-", with: "").lowercased(), localPath: attachment.url.path, name: attachment.name, error: $0) }
                        }.value
                        uploaded = try JSONDecoder().decode(UploadedAttachment.self, from: Data(json.utf8))
                        if let index = attachmentDrafts[id]?.firstIndex(where: { $0.id == attachment.id }) {
                            attachmentDrafts[id]?[index].uploaded = uploaded
                        }
                    }
                    paths.append(uploaded.path)
                    paths.append("![Attachment](fennec-preview:\(uploaded.preview.ref))")
                }
                let prompt = ([text].filter { !$0.isEmpty } + paths).joined(separator: "\n")
                pendingSends[id]?.message.kind = .user(prompt)
                pendingSends[id]?.previousMatches = matchingPrompts(prompt, in: session.transcriptItems)
                let imagePaths = attachments.filter(\.isImage).map { $0.url.path }
                let json = String(data: try JSONEncoder().encode(imagePaths), encoding: .utf8)!
                submitted = true
                _ = try await Task.detached {
                    try goCall {
                        imagePaths.isEmpty ? core.send(address, sessionID: id, text: prompt, error: $0)
                        : core.sendImages(address, sessionID: id, text: prompt, imagePathsJSON: json, error: $0)
                    }
                }.value
                if pendingSends[id]?.message.id == messageID { pendingSends[id]?.delivery = .sent }
                attachmentDrafts[id] = []
                onSent()
                for attachment in attachments { try? FileManager.default.removeItem(at: attachment.url.deletingLastPathComponent()) }
            } catch {
                if submitted {
                    if pendingSends[id]?.message.id == messageID { pendingSends[id]?.delivery = .unconfirmed }
                } else {
                    pendingSends[id] = nil
                }
                attachmentErrors[id] = (error as NSError).localizedDescription
            }
            await loadTranscript(id)
        }
    }

    /// Demo sessions keep attachments on the phone: images show in the
    /// message, other files by name, and the agent replies as usual.
    private func sendDemoAttachments(_ text: String, to id: String, onSent: () -> Void) {
        guard session(id) != nil, let attachments = attachmentDrafts[id], !attachments.isEmpty else { return }
        let images = attachments.filter(\.isImage).compactMap { try? Data(contentsOf: $0.url) }
        let prompt = ([text] + attachments.filter { !$0.isImage }.map(\.name)).filter { !$0.isEmpty }.joined(separator: "\n")
        update(id) {
            $0.messages.append(.init(kind: .user(prompt), time: .now, images: images))
            if $0.title == "Untitled session", !text.isEmpty { $0.title = Self.demoTitle(text) }
        }
        attachmentDrafts[id] = []
        onSent()
        for attachment in attachments { try? FileManager.default.removeItem(at: attachment.url.deletingLastPathComponent()) }
        playDemoReply(id)
    }
}

private struct WorkspaceSessionKey: EnvironmentKey { static let defaultValue = "" }
extension EnvironmentValues {
    var workspaceSessionID: String {
        get { self[WorkspaceSessionKey.self] }
        set { self[WorkspaceSessionKey.self] = newValue }
    }
}
