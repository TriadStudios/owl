import AVFoundation
import CoreImage

protocol FrameSource: AnyObject {
    var fps: Int { get set }
    var onFrame: ((Data) -> Void)? { get set }
    func start()
    func stop()
}

final class PhoneCamera: NSObject, FrameSource, AVCaptureVideoDataOutputSampleBufferDelegate {
    static let maxFPS = 10

    let session = AVCaptureSession()
    var onFrame: ((Data) -> Void)?
    var fps: Int {
        get { lock.withLock { _fps } }
        set { lock.withLock { _fps = min(max(newValue, 1), Self.maxFPS) } }
    }

    private var _fps = 5
    private let lock = NSLock()
    private let queue = DispatchQueue(label: "owl.camera")
    private let context = CIContext()
    private var lastFrame: CFTimeInterval = 0
    private var configured = false

    func start() {
        queue.async {
            if !self.configured { self.configure() }
            if !self.session.isRunning { self.session.startRunning() }
        }
    }

    func stop() {
        queue.async {
            if self.session.isRunning { self.session.stopRunning() }
        }
    }

    private func configure() {
        configured = true
        session.beginConfiguration()
        session.sessionPreset = .hd1280x720
        if let device = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .back),
           let input = try? AVCaptureDeviceInput(device: device),
           session.canAddInput(input) {
            session.addInput(input)
        }
        let output = AVCaptureVideoDataOutput()
        output.alwaysDiscardsLateVideoFrames = true
        output.videoSettings = [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_32BGRA]
        output.setSampleBufferDelegate(self, queue: queue)
        if session.canAddOutput(output) { session.addOutput(output) }
        if let connection = output.connection(with: .video), connection.isVideoRotationAngleSupported(90) {
            connection.videoRotationAngle = 90
        }
        session.commitConfiguration()
    }

    func captureOutput(_ output: AVCaptureOutput, didOutput sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
        let now = CACurrentMediaTime()
        guard now - lastFrame >= 1.0 / Double(fps),
              let onFrame,
              let pixels = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        lastFrame = now
        let image = CIImage(cvPixelBuffer: pixels)
        guard let jpeg = context.jpegRepresentation(
            of: image,
            colorSpace: CGColorSpace(name: CGColorSpace.sRGB)!,
            options: [kCGImageDestinationLossyCompressionQuality as CIImageRepresentationOption: 0.6]
        ) else { return }
        onFrame(jpeg)
    }
}
