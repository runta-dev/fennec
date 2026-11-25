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
    private var visual: Bool { shownFile?.kind == "image" || shownFile?.isWebPage == true }

    /// The conversation is lazy: a card scrolled away and back is a new view.
    /// What an earlier one loaded is read here, in its first layout, so it
    /// comes back at its full height at once instead of growing a frame
    /// later (`.task` runs after the first layout) and moving the page.
    private var cached: WorkspacePreviewCache.Entry? {
        file == nil || thumbnail == nil ? WorkspacePreviewCache.shared[cacheKey] : nil
    }
    private var shownFile: WorkspaceFile? { file ?? cached?.file }
    private var shownThumbnail: UIImage? { thumbnail ?? cached?.thumbnail }

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
                        if let thumbnail = shownThumbnail {
                            if shownFile?.isWebPage == true {
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
                    .frame(maxWidth: .infinity).frame(height: shownFile?.isWebPage == true ? 220 : 180)
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
        .task(id: ref) {
            // Take what an earlier card loaded instead of loading it again.
            if file == nil, let cached = WorkspacePreviewCache.shared[cacheKey] {
                file = cached.file
                localURL = cached.localURL
                thumbnail = cached.thumbnail
            }
            await load(download: false)
        }
        .quickLookPreview($previewURL)
        .fullScreenCover(isPresented: Binding(get: { webURL != nil }, set: { if !$0 { webURL = nil } })) {
            if let webURL { WebPagePreview(url: webURL, title: caption) { self.webURL = nil } }
        }
        .sheet(isPresented: $showShare) { if let localURL { AttachmentShareSheet(url: localURL) } }
    }

    private var caption: String {
        guard let file = shownFile else { return L10n.text("ios.attachments") }
        return file.title.isEmpty ? file.name : file.title
    }

    private var cacheKey: String { sessionID + "\n" + ref }

    private func remember() {
        guard let file else { return }
        let earlier = WorkspacePreviewCache.shared[cacheKey]
        WorkspacePreviewCache.shared[cacheKey] = .init(file: file, localURL: localURL ?? earlier?.localURL,
                                                       thumbnail: thumbnail ?? earlier?.thumbnail)
    }

    private func load(download: Bool) async {
        guard !loading else { return }
        loading = true
        defer {
            loading = false
            remember()
        }
        do {
            if file == nil {
                file = try await store.previewMetadata(ref, sessionID: sessionID)
                // Known now, not only when the thumbnail is done (a web page
                // takes seconds): a card rebuilt meanwhile keeps its height.
                remember()
            }
            guard let file else { return }
            let visual = file.kind == "image" || file.isWebPage
            if localURL == nil && (download || visual) {
                localURL = try await store.preview(file, sessionID: sessionID)
            }
            // Also when an earlier card's thumbnail was cut short (scrolled away).
            if visual, thumbnail == nil, let url = localURL, !Task.isCancelled {
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

/// What workspace file cards have loaded, for the app's lifetime: the
/// file's details, its downloaded copy and its thumbnail.
@MainActor
final class WorkspacePreviewCache {
    struct Entry {
        var file: WorkspaceFile
        var localURL: URL?
        var thumbnail: UIImage?
    }

    static let shared = WorkspacePreviewCache()
    private var entries: [String: Entry] = [:]

    subscript(key: String) -> Entry? {
        get { entries[key] }
        set { entries[key] = newValue }
    }
}

private struct AttachmentShareSheet: UIViewControllerRepresentable {
    var url: URL
    func makeUIViewController(context: Context) -> UIActivityViewController { UIActivityViewController(activityItems: [url], applicationActivities: nil) }
    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
