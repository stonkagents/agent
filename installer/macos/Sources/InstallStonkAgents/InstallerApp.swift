import SwiftUI

enum InstallerScreen {
    case welcome
    case installing
    case done
    case error
}

@main
struct InstallerApp: App {
    @StateObject private var installer = Installer()
    @State private var currentScreen: InstallerScreen = .welcome

    var body: some Scene {
        WindowGroup {
            ZStack {
                Color(red: 0.031, green: 0.039, blue: 0.059)
                    .ignoresSafeArea()

                switch currentScreen {
                case .welcome:
                    WelcomeView(
                        version: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0.3.0",
                        sizeMB: ComponentManifest.totalSizeMB,
                        isUpdate: installer.isExistingInstall,
                        onInstall: {
                            currentScreen = .installing
                            Task {
                                await installer.install()
                                if installer.error != nil {
                                    currentScreen = .error
                                } else {
                                    currentScreen = .done
                                }
                            }
                        }
                    )

                case .installing:
                    InstallerProgressView(installer: installer)

                case .done:
                    DoneView(onClose: {
                        NSApplication.shared.terminate(nil)
                    })

                case .error:
                    VStack(spacing: 16) {
                        Spacer()
                        AnimatedLogo(glowIntensity: 0.3)
                        Text("Installation Failed")
                            .font(.system(.title2, design: .monospaced))
                            .fontWeight(.bold)
                            .foregroundColor(Color(red: 1, green: 0.3, blue: 0.3))
                        Text(installer.error?.localizedDescription ?? "Unknown error")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundColor(Color(red: 0.533, green: 0.533, blue: 0.533))
                            .multilineTextAlignment(.center)
                            .padding(.horizontal, 32)
                        Spacer()
                        Button("Close") {
                            NSApplication.shared.terminate(nil)
                        }
                        .buttonStyle(.plain)
                        .foregroundColor(Color(red: 0.91, green: 0.91, blue: 0.91))
                        Spacer().frame(height: 16)
                    }
                }
            }
            .frame(width: 500, height: 420)
            .task {
                installer.checkExistingInstall()
            }
        }
        .windowResizability(.contentSize)
        .windowStyle(.hiddenTitleBar)
    }
}
