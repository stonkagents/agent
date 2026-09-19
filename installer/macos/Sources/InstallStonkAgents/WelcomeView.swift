import SwiftUI

struct WelcomeView: View {
    let version: String
    let sizeMB: Int
    let isUpdate: Bool
    let onInstall: () -> Void

    var body: some View {
        VStack(spacing: 16) {
            Spacer()

            AnimatedLogo()

            Text("Welcome to StonkAgents")
                .font(.system(.title2, design: .monospaced))
                .fontWeight(.bold)
                .foregroundColor(Color(red: 0.91, green: 0.91, blue: 0.91))

            Text("Every agent learns from another")
                .font(.system(.caption, design: .monospaced))
                .foregroundColor(Color(red: 0.533, green: 0.533, blue: 0.533))

            VStack(alignment: .leading, spacing: 6) {
                Text("What to expect:")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(Color(red: 0.91, green: 0.91, blue: 0.91))

                Text("• If Node.js is missing, macOS asks for your password once to install it.")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundColor(Color(red: 0.65, green: 0.65, blue: 0.65))

                Text("• Node.js and StonkAgents setup can take a few minutes. Keep this window open.")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundColor(Color(red: 0.65, green: 0.65, blue: 0.65))

                Text("• macOS may ask for background permissions so StonkAgents can stay running.")
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundColor(Color(red: 0.65, green: 0.65, blue: 0.65))
            }
            .padding(.top, 6)
            .padding(.horizontal, 30)

            Spacer()

            Button(action: onInstall) {
                Text(isUpdate ? "Update Now" : "Install Now")
                    .font(.system(.body, design: .monospaced))
                    .fontWeight(.semibold)
                    .foregroundColor(Color(red: 0, green: 1, blue: 0))
                    .padding(.horizontal, 32)
                    .padding(.vertical, 10)
                    .overlay(
                        RoundedRectangle(cornerRadius: 6)
                            .stroke(Color(red: 0, green: 1, blue: 0), lineWidth: 1)
                    )
            }
            .buttonStyle(.plain)
            .onHover { hovering in
                if hovering {
                    NSCursor.pointingHand.push()
                } else {
                    NSCursor.pop()
                }
            }

            Text("v\(version) · \(sizeMB)MB")
                .font(.system(.caption2, design: .monospaced))
                .foregroundColor(Color(red: 0.333, green: 0.333, blue: 0.333))

            Spacer().frame(height: 16)
        }
    }
}
