// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "lichen-mac",
    platforms: [.macOS(.v13)],
    targets: [
        // What the app knows about lichen: what `lichen ... --json` says, and
        // running the lichen command for everything it changes.
        .target(name: "LichenKit"),
        // The app.
        .executableTarget(name: "Lichen", dependencies: ["LichenKit"]),
        .testTarget(name: "LichenKitTests", dependencies: ["LichenKit"]),
    ]
)
