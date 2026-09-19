import SwiftUI

struct AnimatedLogo: View {
    var glowIntensity: Double = 1.0
    @State private var glowOpacity: Double = 0.3

    var body: some View {
        ZStack {
            // Glow layer
            crabShape
                .fill(Color(red: 0, green: 1, blue: 0).opacity(glowOpacity * glowIntensity))
                .blur(radius: 20)

            // Main logo
            crabShape
                .fill(Color(red: 1, green: 0.3, blue: 0.3))

            // Sunglasses overlay
            sunglassesShape
                .fill(Color(red: 0, green: 1, blue: 0))
        }
        .frame(width: 120, height: 120)
        .onAppear {
            withAnimation(.easeInOut(duration: 2.0).repeatForever(autoreverses: true)) {
                glowOpacity = 0.8
            }
        }
    }

    private var crabShape: some Shape {
        CrabPath()
    }

    private var sunglassesShape: some Shape {
        SunglassesPath()
    }
}

struct CrabPath: Shape {
    func path(in rect: CGRect) -> Path {
        var path = Path()
        let s = min(rect.width, rect.height) / 120.0
        let ox = (rect.width - 120 * s) / 2
        let oy = (rect.height - 120 * s) / 2

        // Head dome
        path.move(to: CGPoint(x: ox + 20*s, y: oy + 58*s))
        path.addCurve(
            to: CGPoint(x: ox + 60*s, y: oy + 32*s),
            control1: CGPoint(x: ox + 20*s, y: oy + 40*s),
            control2: CGPoint(x: ox + 32*s, y: oy + 32*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 100*s, y: oy + 58*s),
            control1: CGPoint(x: ox + 88*s, y: oy + 32*s),
            control2: CGPoint(x: ox + 100*s, y: oy + 40*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 60*s, y: oy + 90*s),
            control1: CGPoint(x: ox + 100*s, y: oy + 78*s),
            control2: CGPoint(x: ox + 88*s, y: oy + 90*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 20*s, y: oy + 58*s),
            control1: CGPoint(x: ox + 32*s, y: oy + 90*s),
            control2: CGPoint(x: ox + 20*s, y: oy + 78*s)
        )

        // Left claw
        path.move(to: CGPoint(x: ox + 24*s, y: oy + 50*s))
        path.addCurve(
            to: CGPoint(x: ox + 10*s, y: oy + 30*s),
            control1: CGPoint(x: ox + 20*s, y: oy + 42*s),
            control2: CGPoint(x: ox + 14*s, y: oy + 36*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 16*s, y: oy + 20*s),
            control1: CGPoint(x: ox + 8*s, y: oy + 26*s),
            control2: CGPoint(x: ox + 10*s, y: oy + 20*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 22*s, y: oy + 28*s),
            control1: CGPoint(x: ox + 20*s, y: oy + 20*s),
            control2: CGPoint(x: ox + 22*s, y: oy + 24*s)
        )
        path.addLine(to: CGPoint(x: ox + 30*s, y: oy + 42*s))
        path.closeSubpath()

        // Right claw
        path.move(to: CGPoint(x: ox + 96*s, y: oy + 50*s))
        path.addCurve(
            to: CGPoint(x: ox + 110*s, y: oy + 30*s),
            control1: CGPoint(x: ox + 100*s, y: oy + 42*s),
            control2: CGPoint(x: ox + 106*s, y: oy + 36*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 104*s, y: oy + 20*s),
            control1: CGPoint(x: ox + 112*s, y: oy + 26*s),
            control2: CGPoint(x: ox + 110*s, y: oy + 20*s)
        )
        path.addCurve(
            to: CGPoint(x: ox + 98*s, y: oy + 28*s),
            control1: CGPoint(x: ox + 100*s, y: oy + 20*s),
            control2: CGPoint(x: ox + 98*s, y: oy + 24*s)
        )
        path.addLine(to: CGPoint(x: ox + 90*s, y: oy + 42*s))
        path.closeSubpath()

        return path
    }
}

struct SunglassesPath: Shape {
    func path(in rect: CGRect) -> Path {
        var path = Path()
        let s = min(rect.width, rect.height) / 120.0
        let ox = (rect.width - 120 * s) / 2
        let oy = (rect.height - 120 * s) / 2

        // Main bar
        path.addRect(CGRect(x: ox + 24*s, y: oy + 48*s, width: 72*s, height: 6*s))
        // Left ear
        path.addRect(CGRect(x: ox + 20*s, y: oy + 48*s, width: 6*s, height: 6*s))
        // Right ear
        path.addRect(CGRect(x: ox + 94*s, y: oy + 48*s, width: 6*s, height: 6*s))
        // Left lens frame
        path.addRect(CGRect(x: ox + 28*s, y: oy + 54*s, width: 24*s, height: 6*s))
        path.addRect(CGRect(x: ox + 28*s, y: oy + 54*s, width: 6*s, height: 18*s))
        path.addRect(CGRect(x: ox + 46*s, y: oy + 54*s, width: 6*s, height: 18*s))
        path.addRect(CGRect(x: ox + 28*s, y: oy + 66*s, width: 24*s, height: 6*s))
        // Right lens frame
        path.addRect(CGRect(x: ox + 68*s, y: oy + 54*s, width: 24*s, height: 6*s))
        path.addRect(CGRect(x: ox + 68*s, y: oy + 54*s, width: 6*s, height: 18*s))
        path.addRect(CGRect(x: ox + 86*s, y: oy + 54*s, width: 6*s, height: 18*s))
        path.addRect(CGRect(x: ox + 68*s, y: oy + 66*s, width: 24*s, height: 6*s))
        // Bridge
        path.addRect(CGRect(x: ox + 52*s, y: oy + 54*s, width: 16*s, height: 6*s))

        return path
    }
}

