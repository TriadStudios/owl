import ARKit
import Combine
import RealityKit
import SwiftUI

struct ScreenShape {
    let port: Int
    let color: String
    let points: [CGPoint]
}

struct ScreenLabel {
    let label: OverlayLabel
    let color: String
    let at: CGPoint
}

@MainActor
final class ARScene: ObservableObject {
    @Published private(set) var shapes: [ScreenShape] = []
    @Published private(set) var labels: [ScreenLabel] = []
    @Published private(set) var reachyAt: CGPoint?
    var speaking = false

    let view: ARView

    private struct Pin {
        let port: Int
        let color: String
        let points: [SIMD3<Float>]
    }

    private struct LabelPin {
        let label: OverlayLabel
        let color: String
        let point: SIMD3<Float>
    }

    private var pins: [Pin] = []
    private var labelPins: [LabelPin] = []
    private var updates: Cancellable?
    private let reachy = Reachy()
    private let reachyAnchor = AnchorEntity(world: .zero)
    private var placed = false
    private var time: Float = 0

    init(session: ARSession) {
        view = ARView(frame: .zero, cameraMode: .ar, automaticallyConfigureSession: false)
        view.session = session
        reachy.root.isEnabled = false
        reachyAnchor.addChild(reachy.root)
        view.scene.addAnchor(reachyAnchor)
        updates = view.scene.subscribe(to: SceneEvents.Update.self) { [weak self] event in
            guard let self else { return }
            time += Float(event.deltaTime)
            animateReachy()
            project()
        }
    }

    func pin(_ overlay: Overlay, from geometry: FrameGeometry) {
        let all = overlay.polygons.flatMap(\.points).compactMap(Self.point)
        let depths = all.compactMap(geometry.depthAt).sorted()
        let fallback = depths.isEmpty ? 0.5 : depths[depths.count / 2]
        func world(_ p: CGPoint) -> SIMD3<Float> {
            let (origin, direction) = geometry.ray(p)
            return origin + direction * (geometry.depthAt(p) ?? fallback)
        }
        pins = overlay.polygons.map { polygon in
            Pin(port: polygon.port, color: polygon.color, points: polygon.points.compactMap(Self.point).map(world))
        }
        labelPins = overlay.labels.compactMap { label in
            guard let polygon = overlay.polygons.first(where: { $0.port == label.port }) else { return nil }
            let pts = polygon.points.compactMap(Self.point)
            guard !pts.isEmpty else { return nil }
            let top = CGPoint(x: pts.map(\.x).reduce(0, +) / Double(pts.count), y: pts.map(\.y).min()!)
            return LabelPin(label: label, color: polygon.color, point: world(top))
        }
        if !labelPins.isEmpty {
            let center = labelPins.map(\.point).reduce(.zero, +) / Float(labelPins.count)
            let toward = view.cameraTransform.translation - center
            let target = center + normalize(SIMD3(toward.x, 0, toward.z)) * 0.1 + [0, 0.1, 0]
            if !placed || distance(target, reachyAnchor.position) > 0.15 {
                reachyAnchor.move(to: Transform(translation: target), relativeTo: nil, duration: placed ? 0.8 : 0)
                placed = true
            }
        }
        project()
    }

    private func animateReachy() {
        let camera = view.cameraTransform.translation
        if !placed, case .normal = view.session.currentFrame?.camera.trackingState {
            let back = view.cameraTransform.matrix.columns.2
            reachyAnchor.position = camera - normalize(SIMD3(back.x, 0, back.z)) * 0.6 + [0, -0.25, 0]
            placed = true
        }
        guard placed else { return }
        reachy.root.isEnabled = true
        reachy.animate(time: time, facing: camera, speaking: speaking)
        reachyAt = view.project(reachy.head)
    }

    func clear() {
        pins = []
        labelPins = []
        project()
    }

    func port(at p: CGPoint) -> Int? {
        let hit = shapes.last { shape in
            var path = Path()
            path.addLines(shape.points)
            path.closeSubpath()
            return path.contains(p)
        }
        if let hit { return hit.port }
        let near = labels.min { hypot($0.at.x - p.x, $0.at.y - p.y) < hypot($1.at.x - p.x, $1.at.y - p.y) }
        return near.flatMap { hypot($0.at.x - p.x, $0.at.y - p.y) < 44 ? $0.label.port : nil }
    }

    private func project() {
        let shapes = pins.compactMap { pin -> ScreenShape? in
            let points = pin.points.compactMap { view.project($0) }
            return points.count == pin.points.count && points.count > 2 ? ScreenShape(port: pin.port, color: pin.color, points: points) : nil
        }
        let labels = labelPins.compactMap { pin in
            view.project(pin.point).map { ScreenLabel(label: pin.label, color: pin.color, at: $0) }
        }
        if !(shapes.isEmpty && self.shapes.isEmpty) { self.shapes = shapes }
        if !(labels.isEmpty && self.labels.isEmpty) { self.labels = labels }
    }

    private static func point(_ p: [Double]) -> CGPoint? {
        p.count == 2 ? CGPoint(x: p[0], y: p[1]) : nil
    }
}
