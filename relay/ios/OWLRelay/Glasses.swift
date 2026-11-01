import CoreImage
import CoreMedia
import MWDATCamera
import MWDATCore
import UIKit

@MainActor
final class Glasses: ObservableObject {
    @Published private(set) var registration: RegistrationState = .unavailable
    @Published private(set) var devices: [DeviceIdentifier] = []
    @Published private(set) var streaming = false
    @Published private(set) var frame: UIImage?
    @Published var problem: String?

    let wearables: WearablesInterface?

    init() {
        do {
            try Wearables.configure()
            wearables = Wearables.shared
        } catch {
            wearables = nil
            problem = "Meta glasses toolkit failed to start: \(error.localizedDescription)"
        }
        guard let wearables else { return }
        registration = wearables.registrationState
        devices = wearables.devices
        Task { [weak self] in
            for await state in wearables.registrationStateStream() { self?.registration = state }
        }
        Task { [weak self] in
            for await devices in wearables.devicesStream() { self?.devices = devices }
        }
    }

    var registered: Bool { registration == .registered }

    func connect() async {
        guard let wearables else { return }
        problem = nil
        do {
            try await wearables.startRegistration()
        } catch {
            problem = error.localizedDescription
        }
    }

    func disconnect() async {
        guard let wearables else { return }
        do {
            try await wearables.startUnregistration()
        } catch {
            problem = error.localizedDescription
        }
    }

    func handle(_ url: URL) async {
        guard let wearables else { return }
        do {
            _ = try await wearables.handleUrl(url)
        } catch {
            problem = error.localizedDescription
        }
    }

    fileprivate func show(_ image: UIImage?, streaming: Bool) {
        if let image { frame = image }
        if streaming != self.streaming { self.streaming = streaming }
    }
}

final class GlassesCamera: FrameSource {
    var onFrame: ((Data, FrameGeometry?) -> Void)?
    var fps: Int {
        get { lock.withLock { _fps } }
        set { lock.withLock { _fps = min(max(newValue, 1), ARCamera.maxFPS) } }
    }
    var ready: Bool {
        get { lock.withLock { _ready } }
        set { lock.withLock { _ready = newValue } }
    }

    private let glasses: Glasses
    private var _fps = 5
    private var _ready = true
    private var lastSent: CFTimeInterval = 0
    private var lastShown: CFTimeInterval = 0
    private let lock = NSLock()
    private let context = CIContext()
    private var session: DeviceSession?
    private var camera: Camera?
    private var tokens = ListenerTokenBag()

    init(glasses: Glasses) {
        self.glasses = glasses
    }

    func start() {
        Task { @MainActor in await self.open() }
    }

    func stop() {
        camera?.stop()
        session?.stop()
        camera = nil
        session = nil
        let old = tokens
        tokens = ListenerTokenBag()
        Task { await old.cancelAll() }
        Task { @MainActor in self.glasses.show(nil, streaming: false) }
    }

    @MainActor private func open() async {
        guard session == nil, let wearables = glasses.wearables else { return }
        guard glasses.registered else {
            glasses.problem = "Connect your glasses in Settings first."
            return
        }
        do {
            if try await wearables.checkPermissionStatus(.camera) != .granted,
               try await wearables.requestPermission(.camera) != .granted {
                glasses.problem = "Allow camera access for OWL Relay in the Meta AI app."
                return
            }
            let session = try wearables.createSession(deviceSelector: AutoDeviceSelector(wearables: wearables))
            self.session = session
            try session.start()
            for await state in session.stateStream() {
                if state == .started { break }
                if state == .stopped {
                    glasses.problem = "The glasses session stopped. Are the glasses on and connected in Meta AI?"
                    self.session = nil
                    return
                }
            }
            guard let camera = try session.addCamera(config: StreamConfiguration(videoCodec: .raw, resolution: .high, frameRate: 15)) else {
                glasses.problem = "The glasses camera is not available."
                return
            }
            self.camera = camera
            camera.stream.videoFramePublisher.listen { [weak self] frame in
                self?.receive(frame)
            }.store(in: tokens)
            camera.stream.statePublisher.listen { [weak self] state in
                guard let self else { return }
                Task { @MainActor in self.glasses.show(nil, streaming: state == .streaming) }
            }.store(in: tokens)
            camera.stream.errorPublisher.listen { [weak self] error in
                guard let self else { return }
                Task { @MainActor in self.glasses.problem = error.localizedDescription }
            }.store(in: tokens)
            camera.stream.start()
        } catch {
            glasses.problem = error.localizedDescription
            self.session?.stop()
            self.session = nil
        }
    }

    private func receive(_ frame: VideoFrame) {
        let now = CACurrentMediaTime()
        if now - lastShown >= 0.1 {
            lastShown = now
            let image = frame.makeUIImage()
            Task { @MainActor in self.glasses.show(image, streaming: true) }
        }
        guard now - lastSent >= 1.0 / Double(fps), ready, let onFrame,
              let pixels = CMSampleBufferGetImageBuffer(frame.sampleBuffer),
              let jpeg = context.jpegRepresentation(
                  of: CIImage(cvPixelBuffer: pixels),
                  colorSpace: CGColorSpace(name: CGColorSpace.sRGB)!,
                  options: [kCGImageDestinationLossyCompressionQuality as CIImageRepresentationOption: 0.85]
              ) else { return }
        lastSent = now
        ready = false
        onFrame(jpeg, nil)
    }
}
