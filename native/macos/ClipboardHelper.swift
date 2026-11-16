import AppKit

// A narrow platform adapter used by fen on an explicit image-paste gesture.
// Exit 2 means no image; stdout contains only PNG bytes on success.
// With --probe it only checks the pasteboard types (no image is read or
// encoded) and exits 0 when an image is available, 2 otherwise.
// With --from-file <path> it converts that image instead of the pasteboard
// (used by tests; it never touches the user's clipboard).
//
// Images larger than maxEdge on their long side are scaled down before
// upload. The agents downscale screenshots to this size or smaller anyway
// (OpenAI fits into 2048 px, Claude into about 1568 px), so no detail they use
// is lost, while a Retina screenshot shrinks several times and uploads in a
// fraction of the time. FENNEC_IMAGE_MAX_EDGE overrides it; 0 disables.
let board = NSPasteboard.general
let arguments = Array(CommandLine.arguments.dropFirst())
if arguments.first == "--probe" {
    let types = board.types ?? []
    exit(types.contains(.png) || types.contains(.tiff) ? 0 : 2)
}
let limit = 20 * 1024 * 1024
var maxEdge = 2048
if let value = ProcessInfo.processInfo.environment["FENNEC_IMAGE_MAX_EDGE"], let n = Int(value), n >= 0 {
    maxEdge = n
}

func decodable(_ bitmap: NSBitmapImageRep) -> Bool {
    return bitmap.pixelsWide > 0 && bitmap.pixelsHigh > 0 &&
        bitmap.pixelsWide <= 16384 && bitmap.pixelsHigh <= 16384 &&
        bitmap.pixelsWide * bitmap.pixelsHigh <= 32 * 1024 * 1024
}

func scaled(_ bitmap: NSBitmapImageRep, maxEdge: Int) -> NSBitmapImageRep? {
    let w = bitmap.pixelsWide, h = bitmap.pixelsHigh
    let longest = max(w, h)
    if maxEdge <= 0 || longest <= maxEdge { return nil }
    let factor = Double(maxEdge) / Double(longest)
    let nw = max(1, Int((Double(w) * factor).rounded()))
    let nh = max(1, Int((Double(h) * factor).rounded()))
    guard let out = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: nw, pixelsHigh: nh,
                                     bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                     colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
          let context = NSGraphicsContext(bitmapImageRep: out) else { return nil }
    out.size = NSSize(width: nw, height: nh)
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    context.imageInterpolation = .high
    let drawn = bitmap.draw(in: NSRect(x: 0, y: 0, width: nw, height: nh), from: .zero,
                            operation: .copy, fraction: 1, respectFlipped: false, hints: nil)
    NSGraphicsContext.restoreGraphicsState()
    return drawn ? out : nil
}

var original: Data?
if arguments.first == "--from-file", arguments.count == 2 {
    original = FileManager.default.contents(atPath: arguments[1])
} else {
    original = board.data(forType: .png) ?? board.data(forType: .tiff)
}
guard let input = original else { exit(2) }
guard input.count <= 128 * 1024 * 1024,
      let bitmap = NSBitmapImageRep(data: input), decodable(bitmap) else { exit(1) }
var png: Data?
if let smaller = scaled(bitmap, maxEdge: maxEdge) {
    png = smaller.representation(using: .png, properties: [:])
} else if input.starts(with: [0x89, 0x50, 0x4E, 0x47]) {
    png = input // already PNG and small enough: pass through unchanged
} else {
    png = bitmap.representation(using: .png, properties: [:])
}
guard let data = png, !data.isEmpty, data.count <= limit else { exit(1) }
do { try FileHandle.standardOutput.write(contentsOf: data) }
catch { exit(1) }
