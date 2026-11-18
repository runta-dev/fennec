import FennecCore
import XCTest
@testable import Fennec

final class NativeLoginTests: XCTestCase {
    func testBridgeRoutesCallbacksWithoutTreatingThemAsPairing() {
        XCTAssertEqual(FennecmobileDeepLinkRoute("fennec://auth/callback?code=example"), "auth/callback")
        XCTAssertEqual(FennecmobileDeepLinkRoute("fennec://pair#code=example&api=https%3A%2F%2Fapi.runta.com"), "pair")
        for url in ["fennec://auth/callback?code=a&code=b", "fennec://auth/complete?state=example", "fennec://other", "fennec://auth/callback#access_token=example"] {
            XCTAssertEqual(FennecmobileDeepLinkRoute(url), "")
        }
    }

    func testEmailSnapshotKeepsAppSignedOutUntilAPIKeyArrives() throws {
        let json = #"{"status":"authorizing","epoch":7,"revision":8,"email_code":true}"#
        let snapshot = try JSONDecoder().decode(AccountSnapshot.self, from: Data(json.utf8))
        XCTAssertEqual(snapshot.epoch, 7)
        XCTAssertEqual(snapshot.email_code, true)
        XCTAssertEqual(RuntaAuth.status(for: snapshot), .authorizing(nil))
    }
}
