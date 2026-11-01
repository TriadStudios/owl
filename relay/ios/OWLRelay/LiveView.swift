import RealityKit
import SwiftUI

struct LiveView: View {
    @EnvironmentObject var model: AppModel
    @State private var selected: Int?

    var body: some View {
        ZStack {
            ARViewContainer(view: model.scene.view)
                .ignoresSafeArea()
                .onTapGesture(coordinateSpace: .local) { selected = model.scene.port(at: $0) }
            PinnedTags(scene: model.scene, selected: selected, ports: model.ports?.ports ?? [], speech: model.toasts.last?.text)
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
                ForEach(model.scene.reachyAt == nil ? model.toasts : []) { toast in
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
        .task {
            while !Task.isCancelled {
                await model.refresh()
                try? await Task.sleep(for: .seconds(15))
            }
        }
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
    let selected: Int?
    let ports: [Port]
    let speech: String?

    var body: some View {
        GeometryReader { geo in
            canvas
            if let speech, let at = scene.reachyAt {
                Text(speech)
                    .font(.callout)
                    .foregroundStyle(.black)
                    .padding(10)
                    .background(.white, in: RoundedRectangle(cornerRadius: 14))
                    .frame(maxWidth: 240)
                    .fixedSize(horizontal: false, vertical: true)
                    .position(x: min(max(at.x, 136), geo.size.width - 136), y: at.y - 30)
            }
            if let tag = scene.labels.first(where: { $0.label.port == selected }) {
                VStack {
                    PortCard(tag: tag, port: ports.first { $0.num == tag.label.port })
                    Spacer(minLength: 0)
                }
                .frame(width: PortCard.width, height: 360)
                .position(x: min(max(tag.at.x, PortCard.width / 2 + 16), geo.size.width - PortCard.width / 2 - 16), y: tag.at.y + 8 + 180)
            }
        }
        .allowsHitTesting(false)
    }

    private var canvas: some View {
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
                let text = Text("\(tag.label.port)")
                    .font(.caption.bold())
                    .foregroundStyle(.white)
                let resolved = ctx.resolve(text)
                let box = resolved.measure(in: size)
                let rect = CGRect(x: tag.at.x - box.width / 2 - 4, y: tag.at.y - box.height - 8, width: box.width + 8, height: box.height + 4)
                ctx.fill(Path(roundedRect: rect, cornerRadius: 4), with: .color(Color(hex: tag.color)))
                ctx.draw(resolved, at: CGPoint(x: rect.midX, y: rect.midY))
            }
        }
    }
}

struct PortCard: View {
    static let width: CGFloat = 260

    let tag: ScreenLabel
    let port: Port?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Port \(tag.label.port)").font(.headline)
                Spacer()
                Text(tag.label.verdict.title)
                    .font(.caption.bold())
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(Color(hex: tag.color), in: Capsule())
            }
            if let port {
                if !port.downstream.isEmpty {
                    Text(port.downstream).font(.subheadline).foregroundStyle(.secondary)
                }
                ForEach(port.devices.prefix(4), id: \.mac) { device in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Circle().fill(device.online ? Verdict.safe.color : Verdict.unknown.color).frame(width: 8, height: 8)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(device.hostname ?? device.ip ?? device.mac).font(.callout)
                            let detail = [device.vendor, device.hostname == nil ? nil : device.ip].compactMap { $0 }
                            if !detail.isEmpty {
                                Text(detail.joined(separator: " · ")).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
                if port.behind > min(port.devices.count, 4) {
                    Text("\(port.behind) devices behind this port").font(.caption).foregroundStyle(.secondary)
                } else if port.devices.isEmpty {
                    Text("No devices seen").font(.caption).foregroundStyle(.secondary)
                }
            } else {
                Text("Loading port details…").font(.caption).foregroundStyle(.secondary)
            }
        }
        .foregroundStyle(.white)
        .padding(12)
        .background(.black.opacity(0.8), in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(Color(hex: tag.color), lineWidth: 2))
    }
}
