import AVFoundation
import SwiftUI

struct Toast: Identifiable, Equatable {
    let id = UUID()
    let text: String
}

@MainActor
final class AppModel: ObservableObject {
    @Published var pairings: [Pairing]
    @Published var activeID: String?
    @Published var showPair: Bool
    @Published var mock: Bool {
        didSet {
            API.mock = mock
            ports = nil
            findings = []
            if !mock && active == nil { showPair = true }
            Task { await refresh() }
        }
    }
    @Published var ports: PortsResponse?
    @Published var findings: [Finding] = []
    @Published var loadError: String?
    @Published var connected = false
    @Published var overlay = Overlay()
    @Published var toasts: [Toast] = []
    @Published var sentFPS = 0
    @Published var talking = false
    @Published var fps: Int {
        didSet {
            camera.fps = fps
            UserDefaults.standard.set(camera.fps, forKey: "fps")
        }
    }
    @Published var voice: Bool {
        didSet { UserDefaults.standard.set(voice, forKey: "voice") }
    }

    let camera = PhoneCamera()
    private let relay = RelayClient()
    private let speechIn: SpeechIn = AppleSpeechIn()
    private let speechOut: SpeechOut = AppleSpeechOut()
    private var frameCount = 0
    private var started = false

    var active: Pairing? {
        pairings.first { $0.id == activeID }
    }

    var bannerLabel: OverlayLabel? {
        overlay.labels.max { $0.verdict.rank < $1.verdict.rank }
    }

    init() {
        let pairings = PairingStore.loadAll()
        let activeID = PairingStore.activeURL ?? pairings.first?.id
        self.pairings = pairings
        self.activeID = activeID
        mock = API.mock
        showPair = activeID == nil && !API.mock
        let savedFPS = UserDefaults.standard.integer(forKey: "fps")
        fps = savedFPS == 0 ? 5 : savedFPS
        voice = UserDefaults.standard.object(forKey: "voice") as? Bool ?? true
        camera.fps = fps

        relay.onConnected = { [weak self] up in
            Task { @MainActor in self?.connected = up }
        }
        relay.onMessage = { [weak self] msg in
            Task { @MainActor in self?.handle(msg) }
        }
        let relay = relay
        camera.onFrame = { [weak self] jpeg in
            relay.sendFrame(jpeg)
            Task { @MainActor in self?.frameCount += 1 }
        }
    }

    func start() {
        guard !started else { return }
        started = true
        try? AVAudioSession.sharedInstance().setCategory(.playAndRecord, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try? AVAudioSession.sharedInstance().setActive(true)
        AppleSpeechIn.requestAccess()
        if let active { relay.connect(active) }
        Task { [weak self] in
            while true {
                try? await Task.sleep(for: .seconds(1))
                guard let self else { return }
                self.sentFPS = self.frameCount
                self.frameCount = 0
            }
        }
    }

    func refresh() async {
        let pairing = active
        guard pairing != nil || mock else { return }
        do {
            async let p: PortsResponse = API.get("/api/ports", from: pairing)
            async let f: FindingsResponse = API.get("/api/findings", from: pairing)
            let (pr, fr) = try await (p, f)
            ports = pr
            findings = fr.findings
            loadError = nil
            if !mock, var pairing, pairing.name != pr.closet.name {
                pairing.name = pr.closet.name
                PairingStore.save(pairing)
                pairings = PairingStore.loadAll()
            }
        } catch {
            loadError = error.localizedDescription
        }
    }

    func pair(with code: String) -> Bool {
        guard let data = code.data(using: .utf8),
              let p = try? JSONDecoder().decode(Pairing.self, from: data),
              p.relayURL?.scheme == "wss", !p.token.isEmpty, p.pin.count == 64 else { return false }
        PairingStore.save(p)
        pairings = PairingStore.loadAll()
        activate(p)
        return true
    }

    func activate(_ pairing: Pairing) {
        activeID = pairing.id
        PairingStore.activeURL = pairing.id
        ports = nil
        findings = []
        overlay = Overlay()
        relay.connect(pairing)
        Task { await refresh() }
    }

    func unpair(_ pairing: Pairing) {
        PairingStore.remove(pairing)
        pairings = PairingStore.loadAll()
        guard activeID == pairing.id else { return }
        if let next = pairings.first {
            activate(next)
            return
        }
        activeID = nil
        relay.disconnect()
        overlay = Overlay()
        if !mock {
            ports = nil
            findings = []
            showPair = true
        }
    }

    func startLive() {
        AVCaptureDevice.requestAccess(for: .video) { granted in
            if granted { Task { @MainActor in self.camera.start() } }
        }
    }

    func stopLive() {
        camera.stop()
    }

    func beginTalk() {
        guard !talking else { return }
        talking = true
        let relay = relay
        speechIn.start { text in
            relay.sendUtterance(text)
        }
    }

    func endTalk() {
        guard talking else { return }
        talking = false
        speechIn.stop()
    }

    private func handle(_ msg: ServerMessage) {
        switch msg {
        case .overlay(let o):
            let had = overlay.labels.contains { $0.verdict == .do_not_touch }
            overlay = o
            if !had && o.labels.contains(where: { $0.verdict == .do_not_touch }) {
                UINotificationFeedbackGenerator().notificationOccurred(.error)
            }
        case .say(let text):
            if voice { speechOut.say(text) }
            let toast = Toast(text: text)
            toasts.append(toast)
            Task {
                try? await Task.sleep(for: .seconds(5))
                toasts.removeAll { $0.id == toast.id }
            }
        case .unknown:
            break
        }
    }
}
