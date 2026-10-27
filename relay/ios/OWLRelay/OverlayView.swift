import SwiftUI

struct OverlayView: View {
    let overlay: Overlay

    var body: some View {
        Canvas { ctx, size in
            for polygon in overlay.polygons {
                let points = polygon.points.compactMap { p in
                    p.count == 2 ? CGPoint(x: p[0] * size.width, y: p[1] * size.height) : nil
                }
                guard points.count > 2 else { continue }
                var path = Path()
                path.addLines(points)
                path.closeSubpath()
                let color = Color(hex: polygon.color)
                ctx.fill(path, with: .color(color.opacity(0.25)))
                ctx.stroke(path, with: .color(color), lineWidth: 2)
            }
            for label in overlay.labels {
                guard let polygon = overlay.polygons.first(where: { $0.port == label.port }) else { continue }
                let pts = polygon.points.filter { $0.count == 2 }
                guard !pts.isEmpty else { continue }
                let x = pts.map { $0[0] }.reduce(0, +) / Double(pts.count) * size.width
                let y = pts.map { $0[1] }.min()! * size.height
                let text = Text("\(label.text) · \(label.verdict.title)")
                    .font(.caption.bold())
                    .foregroundStyle(.white)
                let resolved = ctx.resolve(text)
                let box = resolved.measure(in: size)
                let rect = CGRect(x: x - box.width / 2 - 4, y: y - box.height - 8, width: box.width + 8, height: box.height + 4)
                ctx.fill(Path(roundedRect: rect, cornerRadius: 4), with: .color(Color(hex: polygon.color)))
                ctx.draw(resolved, at: CGPoint(x: rect.midX, y: rect.midY))
            }
        }
        .allowsHitTesting(false)
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
