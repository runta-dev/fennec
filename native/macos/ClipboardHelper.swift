import AppKit

// A narrow platform adapter used by fen on an explicit image-paste gesture.
// Exit 2 means no image; stdout contains only PNG bytes on success.
let board = NSPasteboard.general
let limit = 20 * 1024 * 1024
var png = board.data(forType: .png)
if png == nil, let tiff = board.data(forType: .tiff) {
    guard tiff.count <= 128 * 1024 * 1024,
          let bitmap = NSBitmapImageRep(data: tiff),
          bitmap.pixelsWide > 0, bitmap.pixelsHigh > 0,
          bitmap.pixelsWide <= 16384, bitmap.pixelsHigh <= 16384,
          bitmap.pixelsWide * bitmap.pixelsHigh <= 32 * 1024 * 1024 else { exit(1) }
    png = bitmap.representation(using: .png, properties: [:])
}
guard let data = png else { exit(2) }
guard !data.isEmpty, data.count <= limit else { exit(1) }
do { try FileHandle.standardOutput.write(contentsOf: data) }
catch { exit(1) }
