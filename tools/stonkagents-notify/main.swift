/// stonkagents-notify — macOS system notification for StonkAgents
///
/// Usage:
///   stonkagents-notify <title> <message>
///   stonkagents-notify <title> <subtitle> <message>
///
/// Build:
///   swiftc -O -o stonkagents-notify main.swift
///
/// Displays a native macOS notification via osascript (AppleScript).
/// Uses "display notification" which works for CLI tools without a bundle.
///
/// Feature: F-025 (Auto-Update System)
/// Story: US-025-07 (macOS Install Sequence)

import Foundation

// MARK: - Argument Parsing

let args = CommandLine.arguments.dropFirst()  // skip executable name

let title: String
let subtitle: String?
let body: String

switch args.count {
case 2:
    title    = args[args.startIndex]
    subtitle = nil
    body     = args[args.index(after: args.startIndex)]
case 3:
    title    = args[args.startIndex]
    subtitle = args[args.index(args.startIndex, offsetBy: 1)]
    body     = args[args.index(args.startIndex, offsetBy: 2)]
default:
    fputs("Usage: stonkagents-notify <title> <message>\n", stderr)
    fputs("       stonkagents-notify <title> <subtitle> <message>\n", stderr)
    exit(1)
}

// MARK: - AppleScript Notification

/// Escapes a string for safe embedding in AppleScript.
func escapeAppleScript(_ s: String) -> String {
    return s
        .replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: "\"", with: "\\\"")
}

var script = "display notification \"\(escapeAppleScript(body))\" with title \"\(escapeAppleScript(title))\""

if let sub = subtitle {
    script += " subtitle \"\(escapeAppleScript(sub))\""
}

script += " sound name \"default\""

let proc = Process()
proc.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
proc.arguments = ["-e", script]

let errPipe = Pipe()
proc.standardError = errPipe

do {
    try proc.run()
    proc.waitUntilExit()

    if proc.terminationStatus != 0 {
        let errData = errPipe.fileHandleForReading.readDataToEndOfFile()
        let errStr = String(data: errData, encoding: .utf8) ?? "unknown error"
        fputs("osascript failed: \(errStr)\n", stderr)
        exit(1)
    }
} catch {
    fputs("Failed to run osascript: \(error.localizedDescription)\n", stderr)
    exit(1)
}
