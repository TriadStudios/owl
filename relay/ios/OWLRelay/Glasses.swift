import CoreImage
import CoreMedia
import MWDATCamera
import MWDATCore
import MWDATDisplay
import MWDATSpeech
import UIKit

@MainActor
final class Glasses: ObservableObject {
    @Published private(set) var registration: RegistrationState = .unavailable
    @Published private(set) var devices: [DeviceIdentifier] = []
    @Published private(set) var streaming = false
    @Published private(set) var frame: UIImage?
    @Published var problem: String?
    @Published fileprivate(set) var trace = Trace()
    @Published private(set) var listening = false
    @Published private(set) var sent: HUD?
    var onHeard: ((String) -> Void)?
    var onListening: ((Bool) -> Void)?
    private var display: Display?
    private var shown: HUD?

    struct Trace: Equatable {
        var session = "none"
        var stream = "none"
        var display = "none"
        var speech = "none"
        var framesIn = 0
        var framesSent = 0
        var lastSentKB = 0

        var line: String {
            "session \(session) · stream \(stream) · display \(display) · mic \(speech) · in \(framesIn) · sent \(framesSent)\(lastSentKB > 0 ? " (\(lastSentKB) KB)" : "")"
        }
    }

    func show(_ hud: HUD) {
        guard let display, hud != shown else { return }
        shown = hud
        Task {
            do {
                if hud.isEmpty {
                    try await display.clearDisplay()
                } else {
                    try await display.send(hud.view())
                }
                sent = hud.isEmpty ? nil : hud
            } catch {
                trace.display = "send failed: \(error)"
            }
        }
    }

    fileprivate func attach(_ display: Display?) {
        self.display = display
        shown = nil
        sent = nil
        trace.display = display == nil ? "none" : "on"
    }

    fileprivate func heard(_ text: String) {
        onHeard?(text)
    }

    fileprivate func speech(_ state: SpeechState) {
        trace.speech = "\(state)"
        let on = state == .started
        guard on != listening else { return }
        listening = on
        onListening?(on)
    }

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

    var details: [String] {
        guard let wearables else { return [] }
        return wearables.devices.compactMap(wearables.deviceForIdentifier).map { d in
            let battery = d.batteryLevel.map { " · battery \($0)%" } ?? ""
            return "\(d.nameOrId()): \(d.linkState) · \(d.compatibility()) · worn \(d.donState) · hinges \(d.hingeState)\(battery)"
        }
    }

    fileprivate func issue(with id: DeviceIdentifier) -> String? {
        guard let device = wearables?.deviceForIdentifier(id) else { return "Meta AI lost track of the glasses. Try again." }
        switch device.compatibility() {
        case .deviceUpdateRequired: return "The glasses need a software update. Update them in the Meta AI app, then try again."
        case .sdkUpdateRequired: return "These glasses need a newer Meta toolkit than this app has (1.0.1)."
        default: break
        }
        if device.linkState != .connected {
            return "The glasses aren't linked right now (\(device.linkState)). Open Meta AI, wake the glasses and try again."
        }
        if device.hingeState == .closed { return "Unfold the glasses so the camera can start." }
        return nil
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
    private var speech: Speech?
    private var tokens = ListenerTokenBag()

    init(glasses: Glasses) {
        self.glasses = glasses
    }

    func start() {
        Task { @MainActor in await self.open() }
    }

    func stop() {
        speech?.stop()
        camera?.stop()
        session?.stop()
        speech = nil
        camera = nil
        session = nil
        Task { @MainActor in
            self.glasses.attach(nil)
            self.glasses.speech(.stopped)
        }
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
            glasses.problem = nil
            // The selector needs a moment after launch to pick up the glasses Meta AI is linked to.
            let selector = AutoDeviceSelector(wearables: wearables)
            var device: DeviceIdentifier?
            for _ in 0..<30 {
                device = selector.activeDevice ?? wearables.devices.first
                if device != nil { break }
                try? await Task.sleep(for: .milliseconds(500))
            }
            guard let device else {
                glasses.problem = "No glasses found. Turn them on, unfold them and check they're connected in the Meta AI app."
                return
            }
            if let issue = glasses.issue(with: device) {
                glasses.problem = issue
                return
            }
            let session = try wearables.createSession(deviceSelector: SpecificDeviceSelector(device: device))
            self.session = session
            var failure: DeviceSessionError?
            session.errorPublisher.listen { error in
                Task { @MainActor in failure = error }
            }.store(in: tokens)
            let states = session.stateStream()
            try session.start()
            var seen: [DeviceSessionState] = []
            for await state in states {
                glasses.trace.session = "\(state)"
                seen.append(state)
                if state == .started { break }
                // The stream can open on the session's previous state, so only a stop after it began starting is a failure.
                guard state == .stopped, seen.contains(.starting) || seen.count > 1 else { continue }
                try? await Task.sleep(for: .milliseconds(500))
                self.session = nil
                if failure == .datAppOnTheGlassesUpdateRequired {
                    glasses.problem = "The glasses need an update for third-party apps. Opening Meta AI to install it."
                    try? await wearables.openDATGlassesAppUpdate()
                } else {
                    let reason = failure.map { "\($0.localizedDescription) (\($0))" } ?? "no reason given"
                    glasses.problem = "The glasses session stopped: \(reason). States: \(seen.map { "\($0)" }.joined(separator: " → "))"
                }
                return
            }
            let later = session.stateStream()
            Task { @MainActor [weak self] in
                for await state in later {
                    guard let self, self.session != nil else { return }
                    self.glasses.trace.session = "\(state)"
                    guard state == .stopped else { continue }
                    try? await Task.sleep(for: .milliseconds(500))
                    let reason = failure.map { "\($0.localizedDescription) (\($0))" } ?? "no reason given"
                    self.glasses.problem = "The glasses session stopped after it started: \(reason). Camera stream was \(self.glasses.trace.stream), \(self.glasses.trace.framesIn) frames in."
                    self.stop()
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
                Task { @MainActor in
                    self.glasses.trace.stream = "\(state)"
                    self.glasses.show(nil, streaming: state == .streaming)
                }
            }.store(in: tokens)
            camera.stream.errorPublisher.listen { [weak self] error in
                guard let self else { return }
                Task { @MainActor in self.glasses.problem = "The glasses camera reported \(error): \(error.localizedDescription)" }
            }.store(in: tokens)
            camera.stream.start()
            await extras(session, wearables, device)
        } catch {
            glasses.problem = error.localizedDescription
            self.session?.stop()
            self.session = nil
        }
    }

    // The screen and the glasses' own mic are extras: either can be missing or refused and the camera still streams.
    @MainActor private func extras(_ session: DeviceSession, _ wearables: WearablesInterface, _ device: DeviceIdentifier) async {
        if wearables.deviceForIdentifier(device)?.supportsDisplay() == true {
            do {
                let display = try session.addDisplay()
                display.start()
                glasses.attach(display)
            } catch {
                glasses.trace.display = "failed: \(error)"
            }
        } else {
            glasses.trace.display = "no screen"
        }
        do {
            if try await wearables.checkPermissionStatus(.microphone) != .granted,
               try await wearables.requestPermission(.microphone) != .granted {
                glasses.trace.speech = "no permission"
                return
            }
            guard let speech = try session.addSpeech() else {
                glasses.trace.speech = "unavailable"
                return
            }
            speech.transcriptionPublisher.listen { [weak self] result in
                guard result.isFinal, !result.text.isEmpty else { return }
                Task { @MainActor in self?.glasses.heard(result.text) }
            }.store(in: tokens)
            speech.statePublisher.listen { [weak self] state in
                Task { @MainActor in self?.glasses.speech(state) }
            }.store(in: tokens)
            speech.errorPublisher.listen { [weak self] error in
                Task { @MainActor in self?.glasses.trace.speech = "error \(error)" }
            }.store(in: tokens)
            speech.start()
            self.speech = speech
        } catch {
            glasses.trace.speech = "failed: \(error)"
        }
    }

    private var received = 0
    private func receive(_ frame: VideoFrame) {
        let now = CACurrentMediaTime()
        received += 1
        if now - lastShown >= 0.1 {
            lastShown = now
            let image = frame.makeUIImage()
            let count = received
            Task { @MainActor in
                self.glasses.trace.framesIn = count
                self.glasses.show(image, streaming: true)
            }
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
        let size = jpeg.count / 1024
        Task { @MainActor in
            self.glasses.trace.framesSent += 1
            self.glasses.trace.lastSentKB = size
        }
        onFrame(jpeg, nil)
    }
}
