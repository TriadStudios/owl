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
    @Published var health: Health?
    @Published var healthAt = Date()
    @Published var ports: PortsResponse?
    @Published var findings: [Finding] = []
    @Published var loadError: String?
    @Published var connected = false
    @Published var overlay = Overlay()
    @Published var toasts: [Toast] = []
    @Published var roundTrip: TimeInterval?
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

    let camera = ARCamera()
    lazy var scene = ARScene(session: camera.session)
    private let relay = RelayClient()
    private let speechIn: SpeechIn = AppleSpeechIn()
    private let speechOut: SpeechOut = AppleSpeechOut()
    private var started = false
    private var sent: (geometry: FrameGeometry?, at: Date)?
    private var timeout: Task<Void, Never>?

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
        showPair = activeID == nil
        let savedFPS = UserDefaults.standard.integer(forKey: "fps")
        fps = savedFPS == 0 ? 5 : savedFPS
        voice = UserDefaults.standard.object(forKey: "voice") as? Bool ?? true
        camera.fps = fps

        relay.onConnected = { [weak self] up in
            Task { @MainActor in
                self?.connected = up
                self?.camera.ready = true
            }
        }
        relay.onMessage = { [weak self] msg in
            Task { @MainActor in self?.handle(msg) }
        }
        camera.onFrame = { [weak self] jpeg, geometry in
            Task { @MainActor in self?.send(jpeg, geometry) }
        }
    }

    func start() {
        guard !started else { return }
        started = true
        try? AVAudioSession.sharedInstance().setCategory(.playAndRecord, options: [.defaultToSpeaker, .allowBluetoothHFP])
        try? AVAudioSession.sharedInstance().setActive(true)
        AppleSpeechIn.requestAccess()
        if let active { relay.connect(active) }
    }

    // One frame in flight at a time, so each overlay is known to belong to the camera pose it was sent from.
    private func send(_ jpeg: Data, _ geometry: FrameGeometry?) {
        sent = (geometry, Date())
        relay.sendFrame(jpeg)
        timeout?.cancel()
        timeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(15))
            if !Task.isCancelled { self?.camera.ready = true }
        }
    }

    var lastPoll: Date? {
        health?.last_poll_age_s.map { healthAt.addingTimeInterval(-$0) }
    }

    func refresh() async {
        guard let pairing = active else { return }
        async let h: Result<Health, Error> = fetch("/api/health", pairing)
        async let p: Result<PortsResponse, Error> = fetch("/api/ports", pairing)
        async let f: Result<FindingsResponse, Error> = fetch("/api/findings", pairing)
        let (hr, pr, fr) = await (h, p, f)
        guard pairing.id == activeID else { return }
        var errors: [String] = []
        switch hr {
        case .success(let v):
            health = v
            healthAt = Date()
        case .failure(let e): errors.append(e.localizedDescription)
        }
        switch pr {
        case .success(let v): ports = v
        case .failure(let e): errors.append(e.localizedDescription)
        }
        switch fr {
        case .success(let v): findings = v.findings
        case .failure(let e): errors.append(e.localizedDescription)
        }
        var seen = Set<String>()
        loadError = errors.isEmpty ? nil : errors.filter { seen.insert($0).inserted }.joined(separator: "\n")
    }

    private nonisolated func fetch<T: Decodable>(_ path: String, _ pairing: Pairing) async -> Result<T, Error> {
        do { return .success(try await API.get(path, from: pairing)) } catch { return .failure(error) }
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
        health = nil
        ports = nil
        findings = []
        loadError = nil
        overlay = Overlay()
        scene.clear()
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
        scene.clear()
        health = nil
        ports = nil
        findings = []
        loadError = nil
        showPair = true
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
            let had = overlay.labels.contains { $0.verdict == .doNotTouch }
            overlay = o
            if let sent {
                if let geometry = sent.geometry { scene.pin(o, from: geometry) }
                roundTrip = Date().timeIntervalSince(sent.at)
                self.sent = nil
            }
            timeout?.cancel()
            camera.ready = true
            if !had && o.labels.contains(where: { $0.verdict == .doNotTouch }) {
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
