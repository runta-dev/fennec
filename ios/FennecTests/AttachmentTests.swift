import XCTest
@testable import Fennec

@MainActor
final class AttachmentTests: XCTestCase {
    func testExplicitPreviewPreservesSurroundingTextAndCodeExamples() {
        let ref = "pv_" + String(repeating: "a", count: 32)
        let marker = "![Chart](fennec-preview:\(ref))"
        let parts = PreviewPart.parse("Before\n\(marker)\nAfter")
        XCTAssertEqual(parts.count, 3)
        XCTAssertEqual(parts[0].text, "Before\n")
        XCTAssertEqual(parts[1].ref, ref)
        XCTAssertEqual(parts[2].text, "After")
        XCTAssertTrue(PreviewPart.parse("```md\n\(marker)\n```").allSatisfy { $0.ref == nil })
        XCTAssertTrue(PreviewPart.parse("/root/.fen/dreamy-meadow/chart.png").allSatisfy { $0.ref == nil })
    }

    func testFailedBatchImportRemovesEarlierDrafts() throws {
        let input = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: input, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: input) }
        let good = input.appending(path: "report.txt")
        try Data("Report".utf8).write(to: good)
        let drafts = URL.cachesDirectory.appending(path: "attachment-drafts")
        let before = Set((try? FileManager.default.contentsOfDirectory(atPath: drafts.path)) ?? [])
        XCTAssertThrowsError(try AttachmentImport.files([good, input.appending(path: "missing.txt")]))
        let after = Set((try? FileManager.default.contentsOfDirectory(atPath: drafts.path)) ?? [])
        XCTAssertEqual(after, before)
    }
}
