import SwiftUI

// Draws the card last sent to the glasses, so a screen recording shows what the wearer sees.
struct GlassesMirror: View {
    @ObservedObject var glasses: Glasses

    var body: some View {
        if let hud = glasses.sent {
            VStack(alignment: .leading, spacing: 8) {
                Label("On your glasses", systemImage: "eyeglasses")
                    .font(.caption.bold())
                    .foregroundStyle(.white.opacity(0.6))
                if let order = hud.order {
                    Label(order, systemImage: hud.orderWrong ? "exclamationmark.triangle" : "briefcase")
                        .font(.headline)
                    if let note = hud.orderNote {
                        Text(note).font(.caption).foregroundStyle(.white.opacity(0.7))
                    }
                }
                if let hand = hud.hand {
                    Label(hand, systemImage: hud.handDanger ? "exclamationmark.circle" : "checkmark.circle")
                        .font(.headline)
                    if let detail = hud.handDetail {
                        Text(detail).font(.subheadline).foregroundStyle(.white.opacity(0.7))
                    }
                }
                if let said = hud.said {
                    Label(said, systemImage: "bubble.left").font(.subheadline)
                }
            }
            .foregroundStyle(.white)
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(white: 0.12).opacity(0.92), in: RoundedRectangle(cornerRadius: 16))
        }
    }
}
