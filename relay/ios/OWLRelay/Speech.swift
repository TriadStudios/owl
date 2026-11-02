import AVFoundation
import Speech

protocol SpeechIn: AnyObject {
    func start(endAfterPause: Bool, onFinal: @escaping (String?) -> Void) -> String?
    func stop()
    func cancel()
}

protocol SpeechOut: AnyObject {
    func say(_ text: String, done: @escaping () -> Void)
}

final class AppleSpeechIn: SpeechIn {
    private let recognizer = SFSpeechRecognizer()
    private let engine = AVAudioEngine()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var pause: DispatchWorkItem?
    private let pauseAfter: TimeInterval = 1.2
    private var onFinal: ((String?) -> Void)?
    private var listening = 0

    init() {
        // Starting the camera or a Bluetooth route reconfigures audio and stops the engine without ending the recognition, which would leave the mic deaf until Live is reopened.
        NotificationCenter.default.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
            guard let self else { return }
            self.finish(nil, of: self.listening)
        }
    }

    static func requestAccess() {
        SFSpeechRecognizer.requestAuthorization { _ in }
        AVAudioApplication.requestRecordPermission { _ in }
    }

    func start(endAfterPause: Bool, onFinal: @escaping (String?) -> Void) -> String? {
        guard task == nil else { return "still finishing the last question" }
        guard SFSpeechRecognizer.authorizationStatus() == .authorized else { return "speech recognition is off for OWL Relay in Settings" }
        guard AVAudioApplication.shared.recordPermission == .granted else { return "the microphone is off for OWL Relay in Settings" }
        guard let recognizer, recognizer.isAvailable else { return "speech recognition is unavailable right now" }
        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0 else { return "no microphone input" }
        let request = SFSpeechAudioBufferRecognitionRequest()
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
            request.append(buffer)
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            return "the microphone didn't start: \(error.localizedDescription)"
        }
        self.request = request
        self.onFinal = onFinal
        listening += 1
        let current = listening
        task = recognizer.recognitionTask(with: request) { [weak self] result, error in
            guard let self else { return }
            if endAfterPause, error == nil, result?.isFinal == false {
                DispatchQueue.main.async { self.endAfterPause() }
                return
            }
            guard error != nil || result?.isFinal == true else { return }
            let text = result?.bestTranscription.formattedString ?? ""
            DispatchQueue.main.async { self.finish(text.isEmpty ? nil : text, of: current) }
        }
        return nil
    }

    // A cancelled task still calls back, so only the listen that is current may finish.
    private func finish(_ text: String?, of current: Int) {
        guard current == listening, let task, let onFinal else { return }
        self.task = nil
        self.onFinal = nil
        stop()
        task.cancel()
        onFinal(text)
    }

    private func endAfterPause() {
        pause?.cancel()
        let item = DispatchWorkItem { [weak self] in self?.stop() }
        pause = item
        DispatchQueue.main.asyncAfter(deadline: .now() + pauseAfter, execute: item)
    }

    func stop() {
        pause?.cancel()
        guard request != nil else { return }
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        request = nil
    }

    func cancel() {
        let task = task
        self.task = nil
        onFinal = nil
        stop()
        task?.cancel()
    }
}

final class AppleSpeechOut: NSObject, SpeechOut, AVSpeechSynthesizerDelegate, @unchecked Sendable {
    private let synth = AVSpeechSynthesizer()
    private var done: [ObjectIdentifier: () -> Void] = [:]

    override init() {
        super.init()
        synth.delegate = self
    }

    func say(_ text: String, done: @escaping () -> Void) {
        let utterance = AVSpeechUtterance(string: text)
        self.done[ObjectIdentifier(utterance)] = done
        synth.speak(utterance)
    }

    func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didFinish utterance: AVSpeechUtterance) {
        finish(utterance)
    }

    func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didCancel utterance: AVSpeechUtterance) {
        finish(utterance)
    }

    private func finish(_ utterance: AVSpeechUtterance) {
        DispatchQueue.main.async {
            self.done.removeValue(forKey: ObjectIdentifier(utterance))?()
        }
    }
}
