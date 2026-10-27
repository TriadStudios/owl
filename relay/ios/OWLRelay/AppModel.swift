import AVFoundation
import Foundation

final class AppModel: ObservableObject {
    @Published var connected = false
    @Published var pairing: Pairing? = PairingStore.load()
    @Published var overlay = Overlay()
    @Published var sentFPS = 0
    @Published var talking = false
    @Published var fps: Int {
        didSet {
            camera.fps = fps
            UserDefaults.standard.set(camera.fps, forKey: "fps")
        }
    }

    let camera = PhoneCamera()
    let frames: FrameSource
    private let relay = RelayClient()
    private let speechIn: SpeechIn = AppleSpeechIn()
    private let speechOut: SpeechOut = AppleSpeechOut()
    private var frameCount = 0
    private var timer: Timer?

    init() {
        let saved = UserDefaults.standard.integer(forKey: "fps")
        fps = saved == 0 ? 5 : saved
        frames = camera
        camera.fps = fps

        relay.onConnected = { [weak self] up in
            DispatchQueue.main.async { self?.connected = up }
        }
        relay.onMessage = { [weak self] msg in
            DispatchQueue.main.async {
                switch msg {
                case .overlay(let o): self?.overlay = o
                case .say(let text): self?.speechOut.say(text)
                case .unknown: break
                }
            }
        }
        frames.onFrame = { [weak self] jpeg in
            guard let self else { return }
            self.relay.sendFrame(jpeg)
            DispatchQueue.main.async { self.frameCount += 1 }
        }
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self else { return }
            self.sentFPS = self.frameCount
            self.frameCount = 0
        }
    }

    func start() {
        try? AVAudioSession.sharedInstance().setCategory(.playAndRecord, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try? AVAudioSession.sharedInstance().setActive(true)
        AppleSpeechIn.requestAccess()
        AVCaptureDevice.requestAccess(for: .video) { granted in
            if granted { self.frames.start() }
        }
        if let pairing { relay.connect(pairing) }
    }

    func pausePreview() { frames.stop() }
    func resumePreview() { frames.start() }

    func pair(with code: String) -> Bool {
        guard let data = code.data(using: .utf8),
              let p = try? JSONDecoder().decode(Pairing.self, from: data),
              p.relayURL?.scheme == "wss", !p.token.isEmpty, p.pin.count == 64 else { return false }
        PairingStore.save(p)
        pairing = p
        relay.connect(p)
        return true
    }

    func unpair() {
        PairingStore.clear()
        pairing = nil
        overlay = Overlay()
        relay.disconnect()
    }

    func beginTalk() {
        guard !talking else { return }
        talking = true
        speechIn.start { [weak self] text in
            self?.relay.sendUtterance(text)
        }
    }

    func endTalk() {
        guard talking else { return }
        talking = false
        speechIn.stop()
    }
}
