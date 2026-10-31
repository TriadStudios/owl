import SwiftUI

enum Theme {
    static let accent = Color(hex: "#0A84FF")
    static let card = Color(white: 0.11)
    static let palette = ["#5E5CE6", "#BF5AF2", "#64D2FF", "#FF7AB2", "#AC8E68", "#66D4CF", "#A7A3FF", "#D1D1D6", "#DA70D6"]

    static func portColor(_ num: Int) -> String {
        palette[((num - 1) % palette.count + palette.count) % palette.count]
    }
}

extension Verdict {
    var color: Color {
        switch self {
        case .safe: Color(hex: "#30D158")
        case .caution: Color(hex: "#FF9F0A")
        case .doNotTouch: Color(hex: "#FF453A")
        case .unknown: Color(hex: "#8E8E93")
        }
    }
}

extension Severity {
    var color: Color {
        switch self {
        case .critical: Verdict.doNotTouch.color
        case .warning: Verdict.caution.color
        case .info: Theme.accent
        }
    }
}

struct PortChip: View {
    let num: Int
    let verdict: Verdict
    var size: CGFloat = 48

    var body: some View {
        Text("\(num)")
            .font(.system(size: size * 0.38, weight: .semibold).monospacedDigit())
            .foregroundStyle(.black)
            .frame(width: size, height: size)
            .background(Color(hex: Theme.portColor(num)), in: Circle())
            .padding(4)
            .overlay(Circle().strokeBorder(verdict.color, lineWidth: 3))
    }
}

struct Card<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) { content }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 16))
    }
}

struct PrimaryButton: View {
    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(.headline)
                .frame(maxWidth: .infinity, minHeight: 56)
        }
        .buttonStyle(.borderedProminent)
        .buttonBorderShape(.roundedRectangle(radius: 14))
    }
}

extension Color {
    init(hex: String) {
        let s = hex.trimmingCharacters(in: CharacterSet(charactersIn: "#"))
        let v = UInt32(s, radix: 16) ?? 0xFFFFFF
        self.init(
            red: Double((v >> 16) & 0xFF) / 255,
            green: Double((v >> 8) & 0xFF) / 255,
            blue: Double(v & 0xFF) / 255
        )
    }
}
