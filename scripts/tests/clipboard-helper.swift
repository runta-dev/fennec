import AppKit

// Run explicitly on a logged-in Mac; preserve every pasteboard representation
// and never overwrite a concurrent clipboard change while restoring the fixture.
func run() throws {
let board = NSPasteboard.general
let saved = (board.pasteboardItems ?? []).map { item -> NSPasteboardItem in
    let copy = NSPasteboardItem()
    for type in item.types {
        if let data = item.data(forType: type) { copy.setData(data, forType: type) }
    }
    return copy
}
var expected = board.changeCount
defer {
    if board.changeCount == expected {
        board.clearContents()
        if !saved.isEmpty { board.writeObjects(saved) }
    }
}

func check(_ condition: Bool, _ message: String) throws {
    if !condition { throw NSError(domain: message, code: 1) }
}

func readFixture(_ data: Data, _ type: NSPasteboard.PasteboardType) throws -> (Int32, Data) {
    try check(board.changeCount == expected, "Clipboard changed during test")
    board.clearContents()
    board.setData(data, forType: type)
    expected = board.changeCount
    let process = Process(), pipe = Pipe()
    process.executableURL = URL(fileURLWithPath: CommandLine.arguments[1])
    process.standardOutput = pipe
    try process.run()
    let output = pipe.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    return (process.terminationStatus, output)
}

    let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 3, pixelsHigh: 2,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 12, bitsPerPixel: 32)!
    let red = NSColor(deviceRed: 1, green: 0, blue: 0, alpha: 1)
    for y in 0..<2 { for x in 0..<3 { bitmap.setColor(red, atX: x, y: y) } }
    let png = bitmap.representation(using: .png, properties: [:])!
    let (pngStatus, pngData) = try readFixture(png, .png)
    try check(pngStatus == 0 && pngData == png, "PNG clipboard image changed")
    let (tiffStatus, tiffData) = try readFixture(bitmap.tiffRepresentation!, .tiff)
    let decoded = NSBitmapImageRep(data: tiffData)
    try check(tiffStatus == 0 && decoded?.pixelsWide == 3 && decoded?.pixelsHigh == 2,
              "TIFF clipboard conversion failed")
    let (textStatus, textData) = try readFixture(Data("plain text".utf8), .string)
    try check(textStatus == 2 && textData.isEmpty, "Text clipboard was treated as an image")
    print("PASS native PNG/TIFF clipboard reads and text fallback")
    if ProcessInfo.processInfo.environment["FENNEC_LIVE_IMAGE_CLIENT_BINARY"] != nil {
        _ = try readFixture(png, .png)
        let test = Process()
        test.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        test.arguments = ["go", "test", "-count=1", "-timeout=90s", "-run", "^TestLiveImagePasteClient$", "-v", "./internal/ope2e"]
        try test.run()
        test.waitUntilExit()
        try check(test.terminationStatus == 0, "Installed client image paste failed")
    }
}
do { try run() } catch {
    FileHandle.standardError.write(Data("FAIL clipboard helper: \(error)\n".utf8))
    exit(1)
}
