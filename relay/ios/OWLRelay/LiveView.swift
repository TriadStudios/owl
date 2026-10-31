import RealityKit
import SwiftUI

struct LiveView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ZStack {
            ARViewContainer(view: model.scene.view)
                .ignoresSafeArea()
            PinnedTags(scene: model.scene)
                .ignoresSafeArea()
            VStack(spacing: 10) {
                Text(status)
                    .font(.footnote.monospaced())
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(.black.opacity(0.6), in: Capsule())
                if let label = model.bannerLabel {
                    HStack(spacing: 10) {
                        Image(systemName: label.verdict == .doNotTouch ? "hand.raised.fill" : "info.circle.fill")
                        Text("\(label.verdict.title.uppercased()) · \(label.text)")
                            .lineLimit(1)
                    }
                    .font(.headline)
                    .foregroundStyle(.black)
                    .padding(.horizontal, 16)
                    .frame(maxWidth: .infinity, minHeight: 48)
                    .background(label.verdict.color, in: RoundedRectangle(cornerRadius: 14))
                }
                ForEach(model.toasts) { toast in
                    Text(toast.text)
                        .font(.callout)
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(.black.opacity(0.75), in: RoundedRectangle(cornerRadius: 12))
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
                Spacer()
                Text(model.talking ? "Listening…" : "Hold to talk")
                    .font(.headline)
                    .foregroundStyle(.white)
                    .frame(maxWidth: .infinity, minHeight: 64)
                    .background(model.talking ? Verdict.doNotTouch.color : Theme.accent, in: RoundedRectangle(cornerRadius: 16))
                    .gesture(DragGesture(minimumDistance: 0)
                        .onChanged { _ in model.beginTalk() }
                        .onEnded { _ in model.endTalk() })
                    .accessibilityAddTraits(.isButton)
            }
            .padding(16)
            .animation(.default, value: model.toasts)
        }
        .onAppear { model.startLive() }
        .onDisappear { model.stopLive() }
    }

    private var status: String {
        let link = model.connected ? "connected" : "offline"
        let pair = model.active?.displayName ?? "unpaired"
        let trip = model.roundTrip.map { String(format: "tags %.1f s", $0) } ?? "waiting for tags"
        return "\(link) · \(pair) · \(trip)"
    }
}

struct ARViewContainer: UIViewRepresentable {
    let view: ARView

    func makeUIView(context: Context) -> ARView { view }

    func updateUIView(_ view: ARView, context: Context) {}
}

struct PinnedTags: View {
    @ObservedObject var scene: ARScene

    var body: some View {
        Canvas { ctx, size in
            for shape in scene.shapes {
                var path = Path()
                path.addLines(shape.points)
                path.closeSubpath()
                let color = Color(hex: shape.color)
                ctx.fill(path, with: .color(color.opacity(0.25)))
                ctx.stroke(path, with: .color(color), lineWidth: 2)
            }
            for tag in scene.labels {
                let text = Text("\(tag.label.text) · \(tag.label.verdict.title)")
                    .font(.caption.bold())
                    .foregroundStyle(.white)
                let resolved = ctx.resolve(text)
                let box = resolved.measure(in: size)
                let rect = CGRect(x: tag.at.x - box.width / 2 - 4, y: tag.at.y - box.height - 8, width: box.width + 8, height: box.height + 4)
                ctx.fill(Path(roundedRect: rect, cornerRadius: 4), with: .color(Color(hex: tag.color)))
                ctx.draw(resolved, at: CGPoint(x: rect.midX, y: rect.midY))
            }
        }
        .allowsHitTesting(false)
    }
}
