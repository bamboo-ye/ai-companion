import XCTest
@testable import CompanionCore

final class AppEnvironmentTests: XCTestCase {
    func testDevelopmentEnvironmentUsesLocalAPI() {
        XCTAssertEqual(AppEnvironment.development.apiBaseURL.absoluteString, "http://127.0.0.1:8080")
    }
}

