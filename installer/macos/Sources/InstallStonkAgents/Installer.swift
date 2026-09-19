import Foundation
import AppKit

enum InstallerError: LocalizedError {
    case payloadNotFound(String)
    case copyFailed(String)
    case nodeIncompatible(String)
    case nodeInstallFailed(String)
    case launcherInstallFailed(String)
    case insufficientSpace(needed: Int, available: Int)

    var errorDescription: String? {
        switch self {
        case .payloadNotFound(let name):
            return "Payload not found: \(name)"
        case .copyFailed(let detail):
            return "Copy failed: \(detail)"
        case .nodeIncompatible(let detail):
            return "Node.js requirement not met: \(detail)"
        case .nodeInstallFailed(let detail):
            return "Node.js install failed: \(detail)"
        case .launcherInstallFailed(let detail):
            return "Configuration failed: \(detail)"
        case .insufficientSpace(let needed, let available):
            return "Not enough space (need \(needed)MB, have \(available)MB)"
        }
    }
}

enum InstallStep: String, CaseIterable, Identifiable {
    case migrating = "Removing older StonkAgents files"
    case checkingNode = "Checking required tools"
    case copying = "Installing StonkAgents app"
    case configuring = "Finishing setup"

    var id: String { rawValue }
}

private struct NodeSemver: Comparable {
    let major: Int
    let minor: Int
    let patch: Int

    static func < (lhs: NodeSemver, rhs: NodeSemver) -> Bool {
        if lhs.major != rhs.major { return lhs.major < rhs.major }
        if lhs.minor != rhs.minor { return lhs.minor < rhs.minor }
        return lhs.patch < rhs.patch
    }
}

private struct NodeRuntime {
    let path: String
    let version: NodeSemver
}

@MainActor
class Installer: ObservableObject {
    @Published var currentStep: InstallStep?
    @Published var completedSteps: Set<InstallStep> = []
    @Published var progress: Double = 0.0
    @Published var stepDetail: String = "We'll show each install step here."
    @Published var isComplete = false
    @Published var error: InstallerError?
    @Published var isExistingInstall = false

    private let components = ComponentManifest.components

    private static let daemonProcessNames = ["stonkagents"]
    private static let launchAgentPrefixes = ["com.stonkagents."]

    func checkExistingInstall() {
        let fm = FileManager.default
        for component in components {
            if fm.fileExists(atPath: component.destinationURL.path) {
                isExistingInstall = true
                return
            }
        }
    }

    func install() async {
        let steps = InstallStep.allCases
        let stepWeight = 1.0 / Double(steps.count)

        // Step 1: Migrate existing
        if isExistingInstall {
            await performStep(.migrating, progress: stepWeight) {
                try self.migrateExisting()
            }
            if error != nil { return }
        } else {
            progress += stepWeight
        }

        // Step 2: Ensure Node v22+ (install bundled Node.pkg if needed)
        await performStep(.checkingNode, progress: stepWeight) {
            try self.ensureCompatibleNode()
        }
        if error != nil { return }

        // Step 3: Copy payload
        await performStep(.copying, progress: stepWeight) {
            try self.copyPayload()
        }
        if error != nil { return }

        // Step 4: Run launcher in install mode (single orchestrator path)
        await performStep(.configuring, progress: stepWeight) {
            try self.runLauncherInstallWorkflow()
        }
        if error != nil { return }

        isComplete = true
    }

    private func performStep(_ step: InstallStep, progress stepWeight: Double, action: () throws -> Void) async {
        currentStep = step
        stepDetail = defaultStepDetail(for: step)
        // Small delay for UI feedback
        try? await Task.sleep(nanoseconds: 400_000_000)
        do {
            try action()
            completedSteps.insert(step)
            progress += stepWeight
        } catch let err as InstallerError {
            error = err
        } catch {
            self.error = .copyFailed(error.localizedDescription)
        }
    }

    private func defaultStepDetail(for step: InstallStep) -> String {
        switch step {
        case .migrating:
            return "Removing files from an older StonkAgents install."
        case .checkingNode:
            return "Checking Node.js. If it's missing, macOS will ask for your password once and this can take 1-3 minutes."
        case .copying:
            return "Copying StonkAgents into your Applications folder."
        case .configuring:
            return "Creating keys, starting services, and connecting StonkAgents. This step can take a few minutes."
        }
    }

    private func migrateExisting() throws {
        // Stop running daemon
        for processName in Self.daemonProcessNames {
            let killTask = Process()
            killTask.executableURL = URL(fileURLWithPath: "/usr/bin/killall")
            killTask.arguments = [processName]
            try? killTask.run()
            killTask.waitUntilExit()
        }

        // Unload and remove old LaunchAgent plists (com.stonkagents.*)
        let fm = FileManager.default
        let home = fm.homeDirectoryForCurrentUser
        let launchAgentsDir = home.appendingPathComponent("Library/LaunchAgents")

        if let files = try? fm.contentsOfDirectory(atPath: launchAgentsDir.path) {
            for file in files where file.hasSuffix(".plist")
                && Self.launchAgentPrefixes.contains(where: { file.hasPrefix($0) }) {
                let plistPath = launchAgentsDir.appendingPathComponent(file).path
                let unload = Process()
                unload.executableURL = URL(fileURLWithPath: "/bin/launchctl")
                unload.arguments = ["unload", plistPath]
                try? unload.run()
                unload.waitUntilExit()
                try? fm.removeItem(atPath: plistPath)
            }
        }

        // Remove old app from /Applications/
        for component in components {
            let destPath = component.destinationURL.path
            if fm.fileExists(atPath: destPath) {
                try fm.removeItem(atPath: destPath)
            }
        }
    }

    private func copyPayload() throws {
        let fm = FileManager.default

        for component in components {
            // Find payload in installer bundle's Resources
            guard let bundleURL = Bundle.main.url(forResource: "payload", withExtension: nil)?
                .appendingPathComponent(component.bundleName) else {
                // Fallback: look directly in Resources
                guard let fallbackURL = Bundle.main.url(forResource: component.bundleName, withExtension: nil) else {
                    throw InstallerError.payloadNotFound(component.bundleName)
                }
                try fm.copyItem(at: fallbackURL, to: component.destinationURL)
                continue
            }

            if fm.fileExists(atPath: component.destinationURL.path) {
                try fm.removeItem(at: component.destinationURL)
            }

            try fm.copyItem(at: bundleURL, to: component.destinationURL)
        }
    }

    private func runLauncherInstallWorkflow() throws {
        for component in components where component.launchAfter {
            let launcherURL = component.destinationURL
                .appendingPathComponent("Contents")
                .appendingPathComponent("MacOS")
                .appendingPathComponent("stonkagents-launcher")
            guard FileManager.default.isExecutableFile(atPath: launcherURL.path) else {
                throw InstallerError.launcherInstallFailed("stonkagents-launcher not found at \(launcherURL.path)")
            }

            let statusFile = FileManager.default.temporaryDirectory
                .appendingPathComponent("stonkagents-install-\(UUID().uuidString).status")
            FileManager.default.createFile(atPath: statusFile.path, contents: nil)

            defer { try? FileManager.default.removeItem(at: statusFile) }

            let process = Process()
            process.executableURL = URL(fileURLWithPath: launcherURL.path)
            process.arguments = ["--install-mode", "--status-file", statusFile.path]

            let stdoutPipe = Pipe()
            let stderrPipe = Pipe()
            process.standardOutput = stdoutPipe
            process.standardError = stderrPipe

            do {
                try process.run()
            } catch {
                throw InstallerError.launcherInstallFailed("Failed to start launcher: \(error.localizedDescription)")
            }

            var lastStatusLineSeen: String?
            while process.isRunning {
                if let line = latestStatusLine(from: statusFile), line != lastStatusLineSeen {
                    lastStatusLineSeen = line
                    if let detail = detailFromStatusLine(line) {
                        stepDetail = detail
                    }
                }
                RunLoop.current.run(until: Date().addingTimeInterval(0.2))
            }
            process.waitUntilExit()

            let stdoutData = stdoutPipe.fileHandleForReading.readDataToEndOfFile()
            let stderrData = stderrPipe.fileHandleForReading.readDataToEndOfFile()
            let result = (
                status: process.terminationStatus,
                stdout: String(decoding: stdoutData, as: UTF8.self),
                stderr: String(decoding: stderrData, as: UTF8.self)
            )

            if result.status != 0 {
                let detail = lastStatusMessage(from: statusFile)
                    ?? {
                        let stderr = result.stderr.trimmingCharacters(in: .whitespacesAndNewlines)
                        if !stderr.isEmpty { return stderr }
                        return result.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
                    }()
                throw InstallerError.launcherInstallFailed(detail.isEmpty ? "Unknown launcher failure" : detail)
            }
        }
    }

    private static let requiredNodeVersion = NodeSemver(major: 22, minor: 12, patch: 0)

    private func ensureCompatibleNode() throws {
        if let runtime = findBestNodeRuntime(), runtime.version >= Self.requiredNodeVersion {
            stepDetail = "Node.js \(format(version: runtime.version)) is already installed. No password prompt is needed."
            return
        }

        stepDetail = "Node.js is required for StonkAgents. macOS will ask for your password once, and installation can take 1-3 minutes."
        guard let nodePkg = Bundle.main.url(forResource: "Node", withExtension: "pkg") else {
            throw InstallerError.nodeIncompatible("Node.js v22.12+ not found and bundled Node.pkg is missing.")
        }

        try installBundledNodePackage(nodePkg)

        guard let runtime = findBestNodeRuntime(), runtime.version >= Self.requiredNodeVersion else {
            throw InstallerError.nodeInstallFailed("Node install completed but Node v22.12+ is still not detected.")
        }
        stepDetail = "Node.js \(format(version: runtime.version)) installed."
    }

    private func installBundledNodePackage(_ pkgURL: URL) throws {
        let escapedPkgPath = pkgURL.path.replacingOccurrences(of: "\"", with: "\\\"")
        stepDetail = "macOS is requesting your password to install Node.js (required for StonkAgents). This may take 1-3 minutes."
        let script = """
        do shell script "/usr/sbin/installer -pkg " & quoted form of "\(escapedPkgPath)" & " -target /" with administrator privileges
        """
        let result = try runProcess(executable: "/usr/bin/osascript", arguments: ["-e", script])
        if result.status != 0 {
            let message = result.stderr.trimmingCharacters(in: .whitespacesAndNewlines)
            throw InstallerError.nodeInstallFailed(message.isEmpty ? "macOS authorization was cancelled or installer failed." : message)
        }
    }

    private func findBestNodeRuntime() -> NodeRuntime? {
        var candidates = Set<String>()
        let envPath = ProcessInfo.processInfo.environment["PATH"] ?? ""
        for dir in envPath.split(separator: ":") {
            candidates.insert(String(dir) + "/node")
        }

        let home = FileManager.default.homeDirectoryForCurrentUser.path
        let commonCandidates = [
            "/opt/homebrew/bin/node",
            "/usr/local/bin/node",
            "\(home)/.volta/bin/node",
            "\(home)/.asdf/shims/node"
        ]
        for candidate in commonCandidates {
            candidates.insert(candidate)
        }

        let nvmRoot = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".nvm")
            .appendingPathComponent("versions")
            .appendingPathComponent("node")
        if let entries = try? FileManager.default.contentsOfDirectory(
            at: nvmRoot,
            includingPropertiesForKeys: nil,
            options: [.skipsHiddenFiles]
        ) {
            for entry in entries {
                let nodePath = entry.appendingPathComponent("bin").appendingPathComponent("node").path
                candidates.insert(nodePath)
            }
        }

        var best: NodeRuntime?
        for candidate in candidates {
            guard FileManager.default.isExecutableFile(atPath: candidate) else { continue }
            guard let version = parseNodeVersion(from: candidate) else { continue }
            if let existing = best {
                if existing.version < version {
                    best = NodeRuntime(path: candidate, version: version)
                }
            } else {
                best = NodeRuntime(path: candidate, version: version)
            }
        }
        return best
    }

    private func parseNodeVersion(from binaryPath: String) -> NodeSemver? {
        guard let result = try? runProcess(executable: binaryPath, arguments: ["-v"]),
              result.status == 0 else {
            return nil
        }
        let raw = result.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
        guard raw.hasPrefix("v") else { return nil }
        let parts = raw.dropFirst().split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count >= 3,
              let major = Int(parts[0]),
              let minor = Int(parts[1]),
              let patch = Int(parts[2]) else {
            return nil
        }
        return NodeSemver(major: major, minor: minor, patch: patch)
    }

    private func format(version: NodeSemver) -> String {
        "v\(version.major).\(version.minor).\(version.patch)"
    }

    private func runProcess(executable: String, arguments: [String]) throws -> (status: Int32, stdout: String, stderr: String) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments

        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        process.standardOutput = stdoutPipe
        process.standardError = stderrPipe

        do {
            try process.run()
        } catch {
            throw InstallerError.copyFailed("Failed to run \(executable): \(error.localizedDescription)")
        }
        process.waitUntilExit()

        let stdoutData = stdoutPipe.fileHandleForReading.readDataToEndOfFile()
        let stderrData = stderrPipe.fileHandleForReading.readDataToEndOfFile()
        let stdout = String(decoding: stdoutData, as: UTF8.self)
        let stderr = String(decoding: stderrData, as: UTF8.self)
        return (process.terminationStatus, stdout, stderr)
    }

    private func lastStatusMessage(from statusFile: URL) -> String? {
        guard let text = try? String(contentsOf: statusFile),
              let line = text
                .split(separator: "\n")
                .map(String.init)
                .last else {
            return nil
        }
        let fields = line.split(separator: "\t", omittingEmptySubsequences: false)
        if fields.count >= 3 {
            return String(fields[2])
        }
        return line
    }

    private func latestStatusLine(from statusFile: URL) -> String? {
        guard let text = try? String(contentsOf: statusFile) else {
            return nil
        }
        return text
            .split(separator: "\n")
            .map(String.init)
            .last
    }

    private static let statusPrefix = "StonkAgents:"

    private func detailFromStatusLine(_ line: String) -> String? {
        let fields = line.split(separator: "\t", omittingEmptySubsequences: false)
        guard fields.count >= 3 else {
            return line
        }

        let phase = String(fields[1])
        let rawMessage = String(fields[2])
        switch phase {
        case "install":
            return "Preparing folders and files."
        case "config":
            return "Creating secure keys and local settings."
        case "services":
            return "Starting background services. macOS may ask to allow background activity."
        case "daemon":
            return "Starting StonkAgents service."
        case "peer-key":
            return "Connecting this Mac to your StonkAgents account."
        case "node":
            return "Checking Node.js for StonkAgents commands. If install is needed, this can take 1-3 minutes."
        case "cli":
            return "Installing StonkAgents command tools. This can take 1-2 minutes."
        case "onboard":
            if rawMessage.hasPrefix("StonkAgents:") {
                return rawMessage
            }
            return "Finalizing your account setup."
        case "gateway":
            if rawMessage.hasPrefix("StonkAgents:") {
                return rawMessage
            }
            return "Starting StonkAgents gateway."
        case "complete":
            return "All setup steps are complete."
        case "error":
            return rawMessage
        default:
            return rawMessage
        }
    }
}
