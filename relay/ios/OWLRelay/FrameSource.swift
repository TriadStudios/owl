import ARKit
import CoreImage

protocol FrameSource: AnyObject {
    var fps: Int { get set }
    var ready: Bool { get set }
    var onFrame: ((Data, FrameGeometry?) -> Void)? { get set }
    func start()
    func stop()
}

struct FrameGeometry {
    let transform: simd_float4x4
    let intrinsics: simd_float3x3
    let imageSize: CGSize
    let toImage: CGAffineTransform
    let depth: [Float]
    let depthWidth: Int
    let depthHeight: Int

    func ray(_ p: CGPoint) -> (SIMD3<Float>, SIMD3<Float>) {
        let q = p.applying(toImage)
        let x = Float(q.x * imageSize.width), y = Float(q.y * imageSize.height)
        let k = intrinsics
        let camera = SIMD4<Float>((x - k[2][0]) / k[0][0], -(y - k[2][1]) / k[1][1], -1, 0)
        let world = transform * camera
        let origin = transform.columns.3
        return (SIMD3(origin.x, origin.y, origin.z), SIMD3(world.x, world.y, world.z))
    }

    func depthAt(_ p: CGPoint) -> Float? {
        guard depthWidth > 0 else { return nil }
        let q = p.applying(toImage)
        let x = min(max(Int(q.x * Double(depthWidth)), 0), depthWidth - 1)
        let y = min(max(Int(q.y * Double(depthHeight)), 0), depthHeight - 1)
        let d = depth[y * depthWidth + x]
        return d.isFinite && d > 0 ? d : nil
    }
}

final class ARCamera: NSObject, FrameSource, ARSessionDelegate {
    static let maxFPS = 10

    let session = ARSession()
    var onFrame: ((Data, FrameGeometry?) -> Void)?
    var fps: Int {
        get { lock.withLock { _fps } }
        set { lock.withLock { _fps = min(max(newValue, 1), Self.maxFPS) } }
    }
    var ready: Bool {
        get { lock.withLock { _ready } }
        set { lock.withLock { _ready = newValue } }
    }

    private var _fps = 5
    private var _ready = true
    private let lock = NSLock()
    private let queue = DispatchQueue(label: "owl.camera")
    private let context = CIContext()
    private var lastFrame: TimeInterval = 0
    private var orientation = CGImagePropertyOrientation.right
    private static let longSide: CGFloat = 1920
    private static let maxJPEG = 1_500_000

    override init() {
        super.init()
        session.delegate = self
        session.delegateQueue = queue
    }

    func start() {
        let config = ARWorldTrackingConfiguration()
        config.planeDetection = [.vertical, .horizontal]
        if ARWorldTrackingConfiguration.supportsFrameSemantics(.sceneDepth) {
            config.frameSemantics.insert(.sceneDepth)
        }
        session.run(config)
    }

    func stop() {
        session.pause()
    }

    func session(_ session: ARSession, didUpdate frame: ARFrame) {
        guard case .normal = frame.camera.trackingState,
              frame.timestamp - lastFrame >= 1.0 / Double(fps), ready, let onFrame else { return }
        lastFrame = frame.timestamp
        orientation = Self.upright(frame.camera.transform) ?? orientation
        let image = CIImage(cvPixelBuffer: frame.capturedImage).oriented(orientation)
        let scale = min(1, Self.longSide / max(image.extent.width, image.extent.height))
        let scaled = image.transformed(by: CGAffineTransform(scaleX: scale, y: scale))
        // The relay closes on messages over 2 MB and base64 adds a third, so step quality down until the JPEG fits.
        guard let jpeg = [0.85, 0.7, 0.55].lazy.compactMap({ quality in
            self.context.jpegRepresentation(
                of: scaled,
                colorSpace: CGColorSpace(name: CGColorSpace.sRGB)!,
                options: [kCGImageDestinationLossyCompressionQuality as CIImageRepresentationOption: quality]
            )
        }).first(where: { $0.count <= Self.maxJPEG }) else { return }
        ready = false
        let (depth, w, h) = Self.copy(frame.sceneDepth?.depthMap)
        onFrame(jpeg, FrameGeometry(
            transform: frame.camera.transform,
            intrinsics: frame.camera.intrinsics,
            imageSize: frame.camera.imageResolution,
            toImage: Self.toImage(orientation),
            depth: depth,
            depthWidth: w,
            depthHeight: h
        ))
    }

    // World up seen in the camera's native landscape image picks the rotation that makes the image upright.
    private static func upright(_ transform: simd_float4x4) -> CGImagePropertyOrientation? {
        let up = simd_transpose(transform) * SIMD4<Float>(0, 1, 0, 0)
        guard hypot(up.x, up.y) > 0.5 else { return nil }
        if abs(up.x) > abs(up.y) { return up.x < 0 ? .right : .left }
        return up.y > 0 ? .up : .down
    }

    private static func toImage(_ orientation: CGImagePropertyOrientation) -> CGAffineTransform {
        switch orientation {
        case .right: CGAffineTransform(a: 0, b: -1, c: 1, d: 0, tx: 0, ty: 1)
        case .left: CGAffineTransform(a: 0, b: 1, c: -1, d: 0, tx: 1, ty: 0)
        case .down: CGAffineTransform(a: -1, b: 0, c: 0, d: -1, tx: 1, ty: 1)
        default: .identity
        }
    }

    private static func copy(_ buffer: CVPixelBuffer?) -> ([Float], Int, Int) {
        guard let buffer, CVPixelBufferGetPixelFormatType(buffer) == kCVPixelFormatType_DepthFloat32 else { return ([], 0, 0) }
        CVPixelBufferLockBaseAddress(buffer, .readOnly)
        defer { CVPixelBufferUnlockBaseAddress(buffer, .readOnly) }
        let w = CVPixelBufferGetWidth(buffer), h = CVPixelBufferGetHeight(buffer)
        let row = CVPixelBufferGetBytesPerRow(buffer)
        guard let base = CVPixelBufferGetBaseAddress(buffer) else { return ([], 0, 0) }
        var out = [Float](repeating: 0, count: w * h)
        for y in 0..<h {
            let src = (base + y * row).assumingMemoryBound(to: Float.self)
            for x in 0..<w { out[y * w + x] = src[x] }
        }
        return (out, w, h)
    }
}
