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

    init(session: ARSession) {
        view = ARView(frame: .zero, cameraMode: .ar, automaticallyConfigureSession: false)
        view.session = session
        updates = view.scene.subscribe(to: SceneEvents.Update.self) { [weak self] _ in
            self?.project()
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
        project()
    }

    func clear() {
        pins = []
        labelPins = []
        project()
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
