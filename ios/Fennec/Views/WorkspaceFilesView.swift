import SwiftUI
import QuickLook
import ImageIO

/// The raw conversation stays unchanged for the desktop TUI. Only explicit
/// resource references in a message become native cards in this presentation.
struct PreviewMessageText: View {
    var text: String
    var lineLimit: Int?
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(PreviewPart.parse(text).enumerated()), id: \.offset) { _, part in
                if let ref = part.ref { WorkspaceFileCard(ref: ref) }
                else if let text = part.text {
                    MarkdownText(text: text, lineLimit: lineLimit).font(.system(size: 15.5)).textSelection(.enabled)
                }
            }
        }
    }
}

struct WorkspaceFileCard: View {
    @Environment(SessionStore.self) private var store
    @Environment(\.workspaceSessionID) private var sessionID
    var ref: String
    @State private var file: WorkspaceFile?
    @State private var localURL: URL?
    @State private var previewURL: URL?
    @State private var webURL: URL?
    @State private var showShare = false
    @State private var thumbnail: UIImage?
    @State private var loading = false
    @State private var failed = false

    /// Images and web pages show what they look like; other files their name.
    private var visual: Bool { file?.kind == "image" || file?.isWebPage == true }

    var body: some View {
        Button {
            Task {
                await load(download: true)
                guard let localURL else { return }
                if file?.isWebPage == true { webURL = localURL }
                else if file?.kind == "download" { showShare = true }
                else { previewURL = localURL }
            }
        } label: {
            VStack(spacing: 8) {
                if visual {
                    ZStack {
                        Theme.surface2
                        if let thumbnail {
                            if file?.isWebPage == true {
                                // The top of the page, as it is laid out.
                                Image(uiImage: thumbnail).resizable().scaledToFill()
                                    .frame(maxHeight: .infinity, alignment: .top)
                            } else {
                                Image(uiImage: thumbnail).resizable().scaledToFit()
                            }
                        } else if loading {
                            ProgressView()
                        }
                    }
                    .frame(maxWidth: .infinity).frame(height: file?.isWebPage == true ? 220 : 180)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                }
                Text(caption)
                    .font(.system(size: 13)).foregroundStyle(Theme.fg2)
                    .lineLimit(2).multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity)
                    .overlay(alignment: .trailing) { if loading && !visual { ProgressView().controlSize(.small) } }
                if failed { Text(L10n.text("ios.preview_retry")).font(.caption).foregroundStyle(Theme.fg2) }
            }
            .padding(10)
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: 14))
            .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(Theme.line))
            .contentShape(RoundedRectangle(cornerRadius: 14))
        }
        .buttonStyle(.plain)
        .disabled(loading)
        .accessibilityLabel(L10n.text("ios.preview_file", values: ["name": caption]))
        .accessibilityIdentifier("workspace-file-preview")
        .task(id: ref) { await load(download: false) }
        .quickLookPreview($previewURL)
        .fullScreenCover(isPresented: Binding(get: { webURL != nil }, set: { if !$0 { webURL = nil } })) {
            if let webURL { WebPagePreview(url: webURL, title: caption) { self.webURL = nil } }
        }
        .sheet(isPresented: $showShare) { if let localURL { AttachmentShareSheet(url: localURL) } }
    }

    private var caption: String {
        guard let file else { return L10n.text("ios.attachments") }
        return file.title.isEmpty ? file.name : file.title
    }

    private func load(download: Bool) async {
        guard !loading else { return }
        loading = true
        defer { loading = false }
        do {
            if file == nil { file = try await store.previewMetadata(ref, sessionID: sessionID) }
            guard let file else { return }
            let visual = file.kind == "image" || file.isWebPage
            if localURL == nil && (download || visual) {
                let url = try await store.preview(file, sessionID: sessionID)
                localURL = url
                if file.isWebPage {
                    thumbnail = await WebPageSnapshot.render(url)
                } else if file.kind == "image" {
                    thumbnail = await Task.detached { () -> UIImage? in
                        guard let source = CGImageSourceCreateWithURL(url as CFURL, nil),
                              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [kCGImageSourceCreateThumbnailFromImageAlways: true,
                                kCGImageSourceCreateThumbnailWithTransform: true, kCGImageSourceThumbnailMaxPixelSize: 720] as CFDictionary) else { return nil }
                        return UIImage(cgImage: image)
                    }.value
                }
            }
            failed = false
        } catch { failed = true }
    }
}

private struct AttachmentShareSheet: UIViewControllerRepresentable {
    var url: URL
    func makeUIViewController(context: Context) -> UIActivityViewController { UIActivityViewController(activityItems: [url], applicationActivities: nil) }
    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
