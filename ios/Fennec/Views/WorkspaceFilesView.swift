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
    @State private var showShare = false
    @State private var thumbnail: UIImage?
    @State private var loading = false
    @State private var failed = false

    var body: some View {
        Button {
            Task {
                await load(download: true)
                if let localURL {
                    if file?.kind == "download" { showShare = true }
                    else { previewURL = localURL }
                }
            }
        } label: {
            VStack(alignment: .leading, spacing: 8) {
                if file?.kind == "image" {
                    Group {
                        if let thumbnail { Image(uiImage: thumbnail).resizable().scaledToFit() }
                        else { ZStack { Theme.surface; if loading { ProgressView() } else { Image(systemName: "photo") } } }
                    }
                    .frame(maxWidth: .infinity).frame(height: 180)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                }
                HStack(spacing: 8) {
                    Image(systemName: symbol)
                    Text(file?.title ?? L10n.text("ios.attachments")).lineLimit(1).truncationMode(.middle)
                    Spacer(minLength: 0)
                    if loading && file?.kind != "image" { ProgressView() }
                    else { Image(systemName: failed ? "arrow.clockwise" : "arrow.up.right") }
                }
                .font(.system(size: 13)).foregroundStyle(Theme.fg2)
                if failed { Text(L10n.text("ios.preview_retry")).font(.caption).foregroundStyle(Theme.fg2) }
            }
            .padding(10)
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: 14))
            .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(Theme.line))
        }
        .buttonStyle(.plain)
        .disabled(loading)
        .accessibilityLabel(L10n.text("ios.preview_file", values: ["name": file?.title ?? L10n.text("ios.attachments")]))
        .accessibilityIdentifier("workspace-file-preview")
        .task(id: ref) { await load(download: false) }
        .quickLookPreview($previewURL)
        .sheet(isPresented: $showShare) { if let localURL { AttachmentShareSheet(url: localURL) } }
    }

    private var symbol: String {
        switch file?.kind { case "image": "photo"; case "video": "play.rectangle"; case "audio": "waveform"; default: "doc" }
    }

    private func load(download: Bool) async {
        guard !loading else { return }
        loading = true
        defer { loading = false }
        do {
            if file == nil { file = try await store.previewMetadata(ref, sessionID: sessionID) }
            guard let file else { return }
            if localURL == nil && (download || file.kind == "image") {
                let url = try await store.preview(file, sessionID: sessionID)
                let image = await Task.detached { () -> UIImage? in
                    guard let source = CGImageSourceCreateWithURL(url as CFURL, nil),
                          let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [kCGImageSourceCreateThumbnailFromImageAlways: true,
                            kCGImageSourceCreateThumbnailWithTransform: true, kCGImageSourceThumbnailMaxPixelSize: 720] as CFDictionary) else { return nil }
                    return UIImage(cgImage: image)
                }.value
                localURL = url
                thumbnail = image
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
