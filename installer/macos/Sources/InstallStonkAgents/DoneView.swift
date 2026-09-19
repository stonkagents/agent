import AppKit
import SwiftUI

struct DoneView: View {
    let onClose: () -> Void
    @State private var glowIntensity: Double = 2.0

    // Portal URL. Override at launch time with STONKAGENTS_PORTAL_URL.
    private var portalURL: URL {
        let env = ProcessInfo.processInfo.environment
        if let raw = env["STONKAGENTS_PORTAL_URL"],
           let url = URL(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)),
           !raw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return url
        }
        return URL(string: "https://stonkagents.com")!
    }

    var body: some View {
        VStack(spacing: 18) {
            Spacer()

            AnimatedLogo(glowIntensity: glowIntensity)

            Text("StonkAgents is ready")
                .font(.system(.title2, design: .monospaced))
                .fontWeight(.bold)
                .foregroundColor(Color(red: 0.91, green: 0.91, blue: 0.91))

            VStack(spacing: 10) {
                Text("Your agent is live on this Mac.")
                    .foregroundColor(Color(red: 0.75, green: 0.75, blue: 0.75))

                VStack(alignment: .leading, spacing: 4) {
                    Text("Next in the StonkAgents portal:")
                        .foregroundColor(Color(red: 0.75, green: 0.75, blue: 0.75))
                        .padding(.bottom, 2)
                    Text("→ Launch your agent token")
                    Text("→ Discover other agents")
                    Text("→ Share files P2P")
                }
                .foregroundColor(Color(red: 0.55, green: 0.55, blue: 0.55))
            }
            .font(.system(.caption, design: .monospaced))
            .multilineTextAlignment(.center)

            Spacer()

            VStack(spacing: 8) {
                Button(action: {
                    NSWorkspace.shared.open(portalURL)
                    onClose()
                }) {
                    Text("Open the StonkAgents portal  →")
                        .font(.system(.body, design: .monospaced))
                        .fontWeight(.semibold)
                        .foregroundColor(Color(red: 0.05, green: 0.07, blue: 0.10))
                        .padding(.horizontal, 28)
                        .padding(.vertical, 10)
                        .background(Color(red: 0.26, green: 0.91, blue: 0.60))
                        .cornerRadius(6)
                }
                .buttonStyle(.plain)

                Button(action: onClose) {
                    Text("Close")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundColor(Color(red: 0.55, green: 0.55, blue: 0.55))
                        .padding(.horizontal, 24)
                        .padding(.vertical, 6)
                }
                .buttonStyle(.plain)
            }

            Spacer().frame(height: 16)
        }
        .onAppear {
            withAnimation(.easeOut(duration: 1.0).delay(0.5)) {
                glowIntensity = 1.2
            }
        }
    }
}
