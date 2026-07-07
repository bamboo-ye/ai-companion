import Foundation

public struct AppEnvironment: Equatable, Sendable {
    public let apiBaseURL: URL

    public init(apiBaseURL: URL) {
        self.apiBaseURL = apiBaseURL
    }

    public static let development = AppEnvironment(
        apiBaseURL: URL(string: "http://127.0.0.1:8080")!
    )
}

