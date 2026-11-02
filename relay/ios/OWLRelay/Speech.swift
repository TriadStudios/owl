import AVFoundation
import Speech

protocol SpeechIn: AnyObject {
    func start(onFinal: @escaping (String?) -> Void) -> String?
    func stop()
}

protocol SpeechOut: AnyObject {
    func say(_ text: String)
}

final class AppleSpeechIn: SpeechIn {
    private let recognizer = SFSpeechRecognizer()
    private let engine = AVAudioEngine()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?

    static func requestAccess() {
        SFSpeechRecognizer.requestAuthorization { _ in }
        AVAudioApplication.requestRecordPermission { _ in }
    }

    func start(onFinal: @escaping (String?) -> Void) -> String? {
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
        task = recognizer.recognitionTask(with: request) { [weak self] result, error in
            guard let self, error != nil || result?.isFinal == true else { return }
            let text = result?.bestTranscription.formattedString ?? ""
            DispatchQueue.main.async {
                guard self.task != nil else { return }
                self.task = nil
                onFinal(text.isEmpty ? nil : text)
            }
        }
        return nil
    }

    func stop() {
        guard engine.isRunning else { return }
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        request = nil
    }
}

final class AppleSpeechOut: SpeechOut {
    private let synth = AVSpeechSynthesizer()

    func say(_ text: String) {
        synth.speak(AVSpeechUtterance(string: text))
    }
}
