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
    @Published private(set) var tracking = false
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
            let normal = if case .normal = view.session.currentFrame?.camera.trackingState { true } else { false }
            if normal != tracking { tracking = normal }
            animateReachy()
            project()
        }
    }

    func pin(_ overlay: Overlay, from geometry: FrameGeometry) {
        // Depth at polygon corners lands in port holes, on cables or on the table behind, so measure only near
        // each port box's middle and put everything on the switch face: a vertical plane through the port row.
        var centers: [(port: Int, point: SIMD3<Float>)] = []
        for polygon in overlay.polygons where !centers.contains(where: { $0.port == polygon.port }) {
            let pts = polygon.points.compactMap(Self.point)
            guard !pts.isEmpty else { continue }
            let mid = CGPoint(x: pts.map(\.x).reduce(0, +) / Double(pts.count), y: pts.map(\.y).reduce(0, +) / Double(pts.count))
            let depths = ([mid] + pts.map { CGPoint(x: (mid.x + $0.x) / 2, y: (mid.y + $0.y) / 2) }).compactMap(geometry.depthAt).sorted()
            guard !depths.isEmpty else { continue }
            let (origin, direction) = geometry.ray(mid)
            centers.append((polygon.port, origin + direction * depths[depths.count / 2]))
        }
        centers.sort { $0.port < $1.port }
        let eye = geometry.ray(CGPoint(x: 0.5, y: 0.5))
        let anchor = centers.isEmpty ? eye.0 + eye.1 * 0.5 : centers.map(\.point).reduce(.zero, +) / Float(centers.count)
        let toEye = eye.0 - anchor
        var normal = normalize(SIMD3(toEye.x, 0, toEye.z))
        if let first = centers.first, let last = centers.last, simd_length(last.point - first.point) > 0.03 {
            let row = last.point - first.point
            normal = normalize(simd_cross(SIMD3(row.x, 0, row.z), [0, 1, 0]))
        }
        func world(_ p: CGPoint) -> SIMD3<Float> {
            let (origin, direction) = geometry.ray(p)
            let facing = simd_dot(normal, direction)
            let t = abs(facing) < 1e-4 ? 0.5 : simd_dot(normal, anchor - origin) / facing
            return origin + direction * (t > 0 ? t : 0.5)
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
        if let first = labelPins.min(by: { $0.label.port < $1.label.port }), let last = labelPins.max(by: { $0.label.port < $1.label.port }) {
            let along = last.point - first.point
            let side = simd_length(along) > 0.01 ? normalize(SIMD3(along.x, 0, along.z)) : SIMD3<Float>(1, 0, 0)
            let target = last.point + side * 0.12 + [0, 0.02, 0]
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
