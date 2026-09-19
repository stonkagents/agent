import Foundation

struct Component: Identifiable {
    let id = UUID()
    let name: String
    let bundleName: String       // "StonkAgents.app"
    let destination: String      // "/Applications/"
    let registerLogin: Bool      // Register via SMAppService
    let launchAfter: Bool        // Launch after install

    var destinationURL: URL {
        URL(fileURLWithPath: destination).appendingPathComponent(bundleName)
    }
}

enum ComponentManifest {
    static let components: [Component] = [
        Component(
            name: "StonkAgents Daemon",
            bundleName: "StonkAgents.app",
            destination: "/Applications",
            registerLogin: true,
            launchAfter: true
        )
    ]

    static var totalSizeMB: Int {
        // Approximate: Go binaries ~40MB + Node.pkg ~50MB + stonkagents-cli ~10MB + app ~20MB
        120
    }
}
