import AVFoundation
import SwiftUI

enum Source: String, CaseIterable, Identifiable {
    case phone = "Phone"
    case glasses = "Glasses"

    var id: String { rawValue }
}

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
    @Published var orders: [WorkOrder] = []
    @Published var techName: String {
        didSet { UserDefaults.standard.set(techName, forKey: "techName") }
    }
    @Published var loadError: String?
    @Published var connected = false
    @Published var overlay = Overlay()
    @Published var toasts: [Toast] = []
    @Published var roundTrip: TimeInterval?
    @Published var readingSince: Date?
    @Published var lastPorts: Int?
    @Published var recording = false {
        didSet { recording ? recorder.start() : recorder.stop() }
    }
    @Published var talking = false
    @Published var hearing = false
    @Published var handsFree: Bool {
        didSet {
            UserDefaults.standard.set(handsFree, forKey: "handsFree")
            if handsFree {
                listen()
            } else if hearing {
                speechIn.cancel()
                hearing = false
            }
        }
    }
    @Published var fps: Int {
        didSet {
            camera.fps = fps
            glassesCamera.fps = fps
            UserDefaults.standard.set(camera.fps, forKey: "fps")
        }
    }
    @Published var source = Source.phone {
        didSet {
            guard source != oldValue else { return }
            (oldValue == .phone ? camera as FrameSource : glassesCamera).stop()
            overlay = Overlay()
            scene.clear()
            lastPorts = nil
            readingSince = nil
            sent = nil
            if live { startLive() }
        }
    }
    @Published var voice: Bool {
        didSet { UserDefaults.standard.set(voice, forKey: "voice") }
    }

    let camera = ARCamera()
    lazy var scene = ARScene(session: camera.session)
    let glasses = Glasses()
    lazy var glassesCamera = GlassesCamera(glasses: glasses)
    private let relay = RelayClient()
    private let speechIn: SpeechIn = AppleSpeechIn()
    private let speechOut: SpeechOut = AppleSpeechOut()
    private var started = false
    private var sent: (geometry: FrameGeometry?, at: Date, number: Int, jpeg: Data)?
    private var frameNumber = 0
    private let recorder = DebugRecorder()
    private var timeout: Task<Void, Never>?
    private var live = false
    private var speaking = 0
    private var awaitingPush = false
    private var lastListenFailure: String?
    private var lastSaid: String?

    private var activeSource: FrameSource {
        source == .phone ? camera : glassesCamera
    }

    var glassesAimed: OverlayLabel? {
        let center = CGPoint(x: 0.5, y: 0.5)
        var best: (port: Int, distance: CGFloat)?
        for polygon in overlay.polygons {
            let pts = polygon.points.compactMap { $0.count == 2 ? CGPoint(x: $0[0], y: $0[1]) : nil }
            guard pts.count > 2 else { continue }
            var path = Path()
            path.addLines(pts)
            path.closeSubpath()
            let mid = CGPoint(x: pts.map(\.x).reduce(0, +) / Double(pts.count), y: pts.map(\.y).reduce(0, +) / Double(pts.count))
            let distance = path.contains(center) ? 0 : hypot(mid.x - center.x, mid.y - center.y)
            if distance < 0.08, distance < (best?.distance ?? .infinity) { best = (polygon.port, distance) }
        }
        return best.flatMap { b in overlay.labels.first { $0.port == b.port } }
    }

    var active: Pairing? {
        pairings.first { $0.id == activeID }
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
        handsFree = UserDefaults.standard.object(forKey: "handsFree") as? Bool ?? true
        techName = UserDefaults.standard.string(forKey: "techName") ?? ""
        camera.fps = fps

        relay.onConnected = { [weak self] up in
            Task { @MainActor in
                self?.connected = up
                self?.readingSince = nil
                self?.activeSource.ready = true
            }
        }
        relay.onMessage = { [weak self] msg in
            Task { @MainActor in self?.handle(msg) }
        }
        camera.onFrame = { [weak self] jpeg, geometry in
            Task { @MainActor in self?.send(jpeg, geometry) }
        }
        glassesCamera.fps = fps
        glassesCamera.onFrame = { [weak self] jpeg, _ in
            Task { @MainActor in self?.send(jpeg, nil) }
        }
        // While the glasses' own mic transcribes, the phone stops listening so one sentence is never sent twice.
        glasses.onListening = { [weak self] on in
            guard let self else { return }
            if on {
                speechIn.cancel()
                hearing = false
            } else {
                listen()
            }
        }
        glasses.onHeard = { [weak self] text in
            guard let self, live, source == .glasses, speaking == 0 else { return }
            heard(text)
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
        frameNumber += 1
        sent = (geometry, Date(), frameNumber, jpeg)
        readingSince = sent?.at
        relay.sendFrame(jpeg)
        timeout?.cancel()
        timeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(60))
            if !Task.isCancelled { self?.activeSource.ready = true }
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
        async let w: Result<WorkOrdersResponse, Error> = fetch("/api/workorders", pairing)
        let (hr, pr, fr, wr) = await (h, p, f, w)
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
        if case .success(let v) = wr {
            orders = v.work_orders.filter(\.live)
            updateHUD()
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
        live = true
        listen()
        activeSource.ready = true
        if source == .glasses {
            glassesCamera.start()
            return
        }
        AVCaptureDevice.requestAccess(for: .video) { granted in
            if granted { Task { @MainActor in self.camera.start() } }
        }
    }

    func stopLive() {
        live = false
        activeSource.stop()
        speechIn.cancel()
        hearing = false
        awaitingPush = false
    }

    // Hands-free listening sends what was said after a pause and stops while OWL speaks, so the phone never answers its own voice.
    private func listen() {
        guard live, handsFree, !talking, !awaitingPush, speaking == 0, !hearing, !(source == .glasses && glasses.listening) else { return }
        hearing = true
        let failure = speechIn.start(endAfterPause: true) { [weak self] text in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.hearing = false
                if let text { self.heard(text) }
                self.listen()
            }
        }
        guard let failure else {
            lastListenFailure = nil
            return
        }
        hearing = false
        if failure != lastListenFailure { show("Can't listen: \(failure)") }
        lastListenFailure = failure
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(3))
            self?.listen()
        }
    }

    private func heard(_ text: String) {
        relay.sendUtterance(text, tech: techName)
        show("You: \(text)")
    }

    // The order this tech is working now; with no name set, whichever order is in progress.
    var order: WorkOrder? {
        orders.first { $0.status == "accepted" && (techName.isEmpty || $0.isMine(techName)) }
    }

    func accept(_ order: WorkOrder) async {
        do {
            let _: WorkOrder = try await API.post("/api/workorders/\(order.id)/accept", ["tech": techName], to: active)
        } catch {
            show("Couldn't accept \(order.name): \(error.localizedDescription)")
        }
        await refresh()
    }

    func finish(_ order: WorkOrder) async {
        do {
            _ = try await API.data("/api/workorders/\(order.id)/close", from: active, body: Data())
        } catch {
            show("Couldn't close \(order.name): \(error.localizedDescription)")
        }
        await refresh()
    }

    func beginTalk() {
        guard !talking else { return }
        speechIn.cancel()
        hearing = false
        awaitingPush = false
        talking = true
        let failure = speechIn.start(endAfterPause: false) { [weak self] text in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.awaitingPush = false
                if let text { self.heard(text) } else { self.show("Didn't catch that") }
                self.listen()
            }
        }
        if let failure {
            show("Can't listen: \(failure)")
        } else {
            awaitingPush = true
        }
    }

    func endTalk() {
        guard talking else { return }
        talking = false
        speechIn.stop()
        listen()
    }

    private func record(_ overlay: Overlay, _ sent: (geometry: FrameGeometry?, at: Date, number: Int, jpeg: Data)) {
        let n = String(format: "%04d", sent.number)
        recorder.write("\(n)-frame.jpg", sent.jpeg)
        if let geometry = sent.geometry {
            recorder.json("\(n)-geometry.json", geometry.debug)
            recorder.write("\(n)-depth.f32", geometry.depthData)
        }
        recorder.json("\(n)-overlay.json", ["overlay": overlay.debug, "fit": scene.lastFit, "round_trip_s": roundTrip ?? 0])
        for delay in [1, 4] {
            Task { [weak self] in
                try? await Task.sleep(for: .seconds(delay))
                guard let self, recorder.recording else { return }
                scene.capture { [recorder] jpeg, info in
                    if let jpeg { recorder.write("\(n)-view-\(delay)s.jpg", jpeg) }
                    recorder.json("\(n)-view-\(delay)s.json", info)
                }
            }
        }
    }

    private func handle(_ msg: ServerMessage) {
        switch msg {
        case .overlay(let o):
            let aimedBefore = glassesAimed
            let handBefore = overlay.touching
            overlay = o
            updateHUD()
            if let hand = o.touching, hand != handBefore, o.labels.first(where: { $0.port == hand })?.verdict == .doNotTouch {
                UINotificationFeedbackGenerator().notificationOccurred(.error)
            } else if o.touching == nil, source == .glasses, let aimed = glassesAimed, aimed.verdict == .doNotTouch, aimed.port != aimedBefore?.port {
                UINotificationFeedbackGenerator().notificationOccurred(.error)
            }
            if let sent {
                if let geometry = sent.geometry { scene.pin(o, from: geometry) }
                roundTrip = Date().timeIntervalSince(sent.at)
                if recorder.recording { record(o, sent) }
                self.sent = nil
            }
            timeout?.cancel()
            readingSince = nil
            let found = Set(o.polygons.map(\.port)).count
            let hadPorts = (lastPorts ?? 0) > 0
            lastPorts = found
            activeSource.ready = true
            if !hadPorts && found > 0 {
                UINotificationFeedbackGenerator().notificationOccurred(.success)
            }
        case .say(let text):
            // A spoken reply can be OWL accepting a work order by voice, so the list catches up right away.
            Task { await refresh() }
            if voice {
                if hearing {
                    speechIn.cancel()
                    hearing = false
                }
                speaking += 1
                speechOut.say(text) { [weak self] in
                    MainActor.assumeIsolated {
                        guard let self else { return }
                        self.speaking -= 1
                        self.listen()
                    }
                }
            }
            show(text)
            lastSaid = text
            updateHUD()
        case .unknown:
            break
        }
    }

    // The glasses screen shows the same three things as Live: the job, the port under the hand, and what OWL just said.
    private func updateHUD() {
        var hud = HUD()
        if let order {
            hud.order = "\(order.name) · PORT \(order.port)" + (order.mismatch ? " (not \(order.claimed_port ?? 0))" : "")
            hud.orderNote = order.note ?? order.device
            hud.orderWrong = order.mismatch
        }
        if let hand = overlay.touching, let label = overlay.labels.first(where: { $0.port == hand }) {
            hud.hand = "HAND ON PORT \(hand) · \(label.verdict.title.uppercased())"
            let detail = label.text.hasPrefix("\(hand) ") ? String(label.text.dropFirst("\(hand) ".count)) : ""
            hud.handDetail = detail.isEmpty ? nil : detail
            hud.handDanger = label.verdict == .doNotTouch
        }
        hud.said = lastSaid
        glasses.show(hud)
    }

    private func show(_ text: String) {
        let toast = Toast(text: text)
        toasts.append(toast)
        scene.speaking = true
        Task {
            try? await Task.sleep(for: .seconds(5))
            toasts.removeAll { $0.id == toast.id }
            scene.speaking = !toasts.isEmpty
        }
    }
}
