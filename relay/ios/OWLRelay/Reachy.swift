import RealityKit
import UIKit

final class Reachy {
    let root = Entity()
    private let body = Entity()
    private let antennas: [Entity]

    init() {
        let shell = SimpleMaterial(color: .white, roughness: 0.3, isMetallic: false)
        let dark = SimpleMaterial(color: UIColor(white: 0.12, alpha: 1), roughness: 0.2, isMetallic: false)
        let glow = UnlitMaterial(color: UIColor(red: 0.39, green: 0.82, blue: 1, alpha: 1))

        func part(_ mesh: MeshResource, _ material: RealityKit.Material, _ at: SIMD3<Float>) -> ModelEntity {
            let e = ModelEntity(mesh: mesh, materials: [material])
            e.position = at
            return e
        }

        body.addChild(part(.generateBox(size: [0.11, 0.09, 0.09], cornerRadius: 0.04), shell, [0, 0.045, 0]))
        body.addChild(part(.generateBox(size: [0.14, 0.085, 0.09], cornerRadius: 0.04), shell, [0, 0.14, 0]))
        body.addChild(part(.generateBox(size: [0.115, 0.05, 0.01], cornerRadius: 0.02), dark, [0, 0.14, 0.044]))
        for x: Float in [-0.027, 0.027] {
            body.addChild(part(.generateSphere(radius: 0.012), glow, [x, 0.142, 0.048]))
        }
        antennas = [-1, 1].map { (side: Float) in
            let pivot = Entity()
            pivot.position = [0.045 * side, 0.18, 0]
            pivot.addChild(part(.generateBox(size: [0.004, 0.07, 0.004]), dark, [0, 0.035, 0]))
            pivot.addChild(part(.generateSphere(radius: 0.008), dark, [0, 0.07, 0]))
            return pivot
        }
        antennas.forEach { body.addChild($0) }
        body.scale = SIMD3(repeating: 0.5)
        root.addChild(body)
    }

    var head: SIMD3<Float> {
        root.position(relativeTo: nil) + [0, 0.14, 0]
    }

    func animate(time t: Float, facing camera: SIMD3<Float>, speaking: Bool) {
        body.position.y = sin(t * 2) * 0.006
        let to = camera - root.position(relativeTo: nil)
        body.orientation = simd_quatf(angle: atan2(to.x, to.z), axis: [0, 1, 0])
        let sway = speaking ? sin(t * 14) * 0.35 : sin(t * 1.5) * 0.08
        for (i, antenna) in antennas.enumerated() {
            let side: Float = i == 0 ? 1 : -1
            antenna.orientation = simd_quatf(angle: side * 0.25 + sway, axis: [0, 0, 1])
        }
    }
}
