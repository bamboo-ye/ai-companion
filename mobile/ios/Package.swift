// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "AICompanionMobile",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "CompanionCore", targets: ["CompanionCore"]),
    ],
    targets: [
        .target(name: "CompanionCore"),
        .testTarget(name: "CompanionCoreTests", dependencies: ["CompanionCore"]),
    ]
)

