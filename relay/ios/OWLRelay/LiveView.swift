import RealityKit
import SwiftUI

struct LiveView: View {
    @EnvironmentObject var model: AppModel
    @State private var selected: Int?

    var body: some View {
        ZStack {
            if model.source == .phone {
                ARViewContainer(view: model.scene.view)
                    .ignoresSafeArea()
                    .onTapGesture(coordinateSpace: .local) { selected = model.scene.port(at: $0) }
                PinnedTags(scene: model.scene, selected: selected, ports: model.ports?.ports ?? [], speech: model.toasts.last?.text)
                    .ignoresSafeArea()
            } else {
                GlassesFeed(glasses: model.glasses, overlay: model.overlay)
            }
            VStack(spacing: 10) {
                LiveStatus(scene: model.scene, glasses: model.glasses)
                Picker("Camera", selection: $model.source) {
                    ForEach(Source.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented)
                if model.source == .phone {
                    PhoneAim(scene: model.scene)
                } else {
                    AimBanner(label: model.glassesAimed, hasTags: !model.overlay.labels.isEmpty, hint: "Look straight at a port")
                }
                ForEach(model.source == .glasses || model.scene.reachyAt == nil ? model.toasts : []) { toast in
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
}

struct PhoneAim: View {
    @ObservedObject var scene: ARScene

    var body: some View {
        AimBanner(label: scene.aimed, hasTags: !scene.labels.isEmpty, hint: "Aim the dot at a port")
    }
}

struct AimBanner: View {
    let label: OverlayLabel?
    let hasTags: Bool
    let hint: String

    var body: some View {
        if let label {
            HStack(spacing: 10) {
                Image(systemName: label.verdict == .doNotTouch ? "hand.raised.fill" : "info.circle.fill")
                VStack(alignment: .leading, spacing: 1) {
                    Text("PORT \(label.port) · \(label.verdict.title.uppercased())").font(.headline)
                    let detail = label.text.hasPrefix("\(label.port) ") ? String(label.text.dropFirst("\(label.port) ".count)) : label.text
                    if detail != "\(label.port)" && !detail.isEmpty {
                        Text(detail).font(.subheadline).lineLimit(1)
                    }
                }
                Spacer(minLength: 0)
            }
            .foregroundStyle(.black)
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: 48)
            .background(label.verdict.color, in: RoundedRectangle(cornerRadius: 14))
        } else if hasTags {
            Text(hint)
                .font(.subheadline.bold())
                .foregroundStyle(.white)
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(.black.opacity(0.6), in: Capsule())
        }
    }
}

struct LiveStatus: View {
    @EnvironmentObject var model: AppModel
    @ObservedObject var scene: ARScene
    @ObservedObject var glasses: Glasses

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            HStack(alignment: .top, spacing: 12) {
                icon
                    .frame(width: 22, height: 22)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title(at: context.date)).font(.subheadline.bold())
                    Text(hint).font(.caption).foregroundStyle(.white.opacity(0.75))
                    if model.source == .glasses {
                        Text(glasses.trace.line).font(.caption2.monospaced()).foregroundStyle(.white.opacity(0.6))
                    }
                }
                Spacer(minLength: 0)
                Button { model.recording.toggle() } label: {
                    Image(systemName: model.recording ? "record.circle.fill" : "record.circle")
                        .font(.title2)
                        .foregroundStyle(model.recording ? .red : .white)
                        .frame(width: 44, height: 44)
                }
                .accessibilityLabel(model.recording ? "Stop debug recording" : "Start debug recording")
            }
            .foregroundStyle(.white)
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.black.opacity(0.65), in: RoundedRectangle(cornerRadius: 14))
        }
    }

    @ViewBuilder private var icon: some View {
        if !model.connected {
            Image(systemName: "wifi.exclamationmark")
        } else if model.readingSince != nil {
            ProgressView().tint(.white)
        } else {
            Image(systemName: "viewfinder")
        }
    }

    private func title(at now: Date) -> String {
        if !model.connected { return "Can't reach the OWL server" }
        if model.source == .glasses {
            if !glasses.registered { return "Glasses not connected" }
            if !glasses.streaming { return "Starting the glasses camera…" }
        } else if !scene.tracking {
            return "Starting the camera…"
        }
        if let since = model.readingSince {
            return "Reading the switch… \(Int(now.timeIntervalSince(since))) s"
        }
        return "Looking for the switch"
    }

    private var hint: String {
        if !model.connected { return "Join the closet Wi-Fi to reach \(model.active?.displayName ?? "the server")" }
        if model.source == .glasses {
            if !glasses.registered { return glasses.problem ?? "Connect them in Settings, then come back here." }
            if !glasses.streaming { return glasses.problem ?? "Put the glasses on and keep them connected in Meta AI." }
        } else if !scene.tracking {
            return "Move the phone slowly so AR can find its place"
        }
        let trip = model.roundTrip.map { String(format: " in %.0f s", $0) } ?? ""
        switch model.lastPorts {
        case nil: return "Hold steady with all 8 ports in view. The first look takes about 20 s."
        case 0?: return "Last look found no ports\(trip). Hold steady, square on, all 8 ports in view."
        case let n?:
            let next = model.source == .glasses ? "Look at a port to check it." : "Tap a port for its devices."
            return "Last look found \(n) port\(n == 1 ? "" : "s")\(trip). \(next)"
        }
    }
}

struct GlassesFeed: View {
    @ObservedObject var glasses: Glasses
    let overlay: Overlay

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            if let frame = glasses.frame {
                Image(uiImage: frame)
                    .resizable()
                    .scaledToFit()
                    .overlay { FlatTags(overlay: overlay) }
            } else {
                Image(systemName: "eyeglasses")
                    .font(.system(size: 64))
                    .foregroundStyle(.white.opacity(0.4))
            }
        }
    }
}

struct FlatTags: View {
    let overlay: Overlay

    var body: some View {
        Canvas { ctx, size in
            var tagged = Set<Int>()
            for polygon in overlay.polygons {
                let pts = polygon.points.compactMap { $0.count == 2 ? CGPoint(x: $0[0] * size.width, y: $0[1] * size.height) : nil }
                guard pts.count > 2 else { continue }
                var path = Path()
                path.addLines(pts)
                path.closeSubpath()
                let color = Color(hex: polygon.color)
                ctx.fill(path, with: .color(color.opacity(0.25)))
                ctx.stroke(path, with: .color(color), lineWidth: 2)
                guard tagged.insert(polygon.port).inserted else { continue }
                let resolved = ctx.resolve(Text("\(polygon.port)").font(.caption.bold()).foregroundStyle(.white))
                let box = resolved.measure(in: size)
                let top = CGPoint(x: pts.map(\.x).reduce(0, +) / Double(pts.count), y: pts.map(\.y).min()!)
                let rect = CGRect(x: top.x - box.width / 2 - 4, y: top.y - box.height - 8, width: box.width + 8, height: box.height + 4)
                ctx.fill(Path(roundedRect: rect, cornerRadius: 4), with: .color(color))
                ctx.draw(resolved, at: CGPoint(x: rect.midX, y: rect.midY))
            }
            let dot = CGRect(x: size.width / 2 - 11, y: size.height / 2 - 11, width: 22, height: 22)
            ctx.stroke(Path(ellipseIn: dot), with: .color(.white), lineWidth: 3)
        }
        .allowsHitTesting(false)
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
            Circle()
                .strokeBorder(scene.aimed.map { $0.verdict.color } ?? .white, lineWidth: 3)
                .background(Circle().fill(.black.opacity(0.25)))
                .frame(width: 22, height: 22)
                .position(x: geo.size.width / 2, y: geo.size.height / 2)
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
