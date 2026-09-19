import SwiftUI

struct InstallerProgressView: View {
    @ObservedObject var installer: Installer

    var body: some View {
        VStack(spacing: 16) {
            Spacer()

            AnimatedLogo(glowIntensity: installer.progress)

            Text(installer.isExistingInstall ? "Updating StonkAgents..." : "Installing StonkAgents...")
                .font(.system(.title2, design: .monospaced))
                .fontWeight(.bold)
                .foregroundColor(Color(red: 0.91, green: 0.91, blue: 0.91))

            Text(installer.stepDetail)
                .font(.system(.caption, design: .monospaced))
                .foregroundColor(Color(red: 0.72, green: 0.72, blue: 0.72))
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)

            // Progress bar
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    RoundedRectangle(cornerRadius: 3)
                        .fill(Color(red: 0.094, green: 0.11, blue: 0.157))
                        .frame(height: 6)

                    RoundedRectangle(cornerRadius: 3)
                        .fill(Color(red: 0, green: 1, blue: 0))
                        .frame(width: geo.size.width * installer.progress, height: 6)
                        .shadow(color: Color(red: 0, green: 1, blue: 0).opacity(0.3), radius: 10)
                }
            }
            .frame(height: 6)
            .padding(.horizontal, 40)

            Text("\(Int(installer.progress * 100))%")
                .font(.system(.caption, design: .monospaced))
                .foregroundColor(Color(red: 0, green: 1, blue: 0))

            Spacer().frame(height: 8)

            // Step list
            VStack(alignment: .leading, spacing: 6) {
                ForEach(InstallStep.allCases) { step in
                    HStack(spacing: 8) {
                        if installer.completedSteps.contains(step) {
                            Image(systemName: "checkmark")
                                .font(.system(size: 10, weight: .bold))
                                .foregroundColor(Color(red: 0, green: 1, blue: 0))
                                .frame(width: 14)
                        } else if installer.currentStep == step {
                            ProgressView()
                                .scaleEffect(0.5)
                                .frame(width: 14)
                        } else {
                            Text(" ")
                                .frame(width: 14)
                        }

                        Text(step.rawValue)
                            .font(.system(.caption, design: .monospaced))
                            .foregroundColor(
                                installer.completedSteps.contains(step)
                                    ? Color(red: 0, green: 1, blue: 0)
                                    : installer.currentStep == step
                                        ? Color(red: 0.91, green: 0.91, blue: 0.91)
                                        : Color(red: 0.333, green: 0.333, blue: 0.333)
                            )
                    }
                }
            }

            Spacer()
        }
    }
}
