import Foundation
import simd

final class DebugRecorder {
    static var root: URL { URL.documentsDirectory.appending(path: "debug") }

    private let queue = DispatchQueue(label: "owl.debug")
    private var folder: URL?

    var recording: Bool { folder != nil }

    func start() {
        let stamp = ISO8601DateFormatter().string(from: Date()).replacingOccurrences(of: ":", with: "-")
        let url = Self.root.appending(path: stamp)
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        folder = url
    }

    func stop() {
        folder = nil
    }

    func write(_ name: String, _ data: Data) {
        guard let folder else { return }
        queue.async { try? data.write(to: folder.appending(path: name)) }
    }

    func json(_ name: String, _ value: Any) {
        guard let data = try? JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys]) else { return }
        write(name, data)
    }

    static func clear() {
        try? FileManager.default.removeItem(at: root)
    }

    static func matrix(_ m: simd_float4x4) -> [[Double]] {
        (0..<4).map { c in (0..<4).map { r in Double(m[c][r]) } }
    }

    static func vector(_ v: SIMD3<Float>) -> [Double] {
        [Double(v.x), Double(v.y), Double(v.z)]
    }
}

extension FrameGeometry {
    var debug: [String: Any] {
        [
            "transform": DebugRecorder.matrix(transform),
            "intrinsics": (0..<3).map { c in (0..<3).map { r in Double(intrinsics[c][r]) } },
            "image_size": [imageSize.width, imageSize.height],
            "to_image": [toImage.a, toImage.b, toImage.c, toImage.d, toImage.tx, toImage.ty],
            "depth_size": [depthWidth, depthHeight],
        ]
    }

    var depthData: Data {
        depth.withUnsafeBufferPointer { Data(buffer: $0) }
    }
}

extension Overlay {
    var debug: [String: Any] {
        [
            "polygons": polygons.map { ["port": $0.port, "color": $0.color, "points": $0.points] },
            "labels": labels.map { ["port": $0.port, "text": $0.text, "verdict": $0.verdict.rawValue] },
        ]
    }
}
