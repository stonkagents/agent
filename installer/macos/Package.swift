// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "InstallStonkAgents",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(
            name: "InstallStonkAgents",
            path: "Sources/InstallStonkAgents"
        )
    ]
)
