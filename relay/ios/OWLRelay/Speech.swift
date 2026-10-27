import AVFoundation
import Speech

protocol SpeechIn: AnyObject {
    func start(onFinal: @escaping (String) -> Void)
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

    func start(onFinal: @escaping (String) -> Void) {
        guard task == nil, let recognizer, recognizer.isAvailable else { return }
        let request = SFSpeechAudioBufferRecognitionRequest()
        let input = engine.inputNode
        input.installTap(onBus: 0, bufferSize: 1024, format: input.outputFormat(forBus: 0)) { buffer, _ in
            request.append(buffer)
        }
        engine.prepare()
        guard (try? engine.start()) != nil else {
            input.removeTap(onBus: 0)
            return
        }
        self.request = request
        task = recognizer.recognitionTask(with: request) { [weak self] result, error in
            guard let self else { return }
            if let result, result.isFinal {
                let text = result.bestTranscription.formattedString
                if !text.isEmpty { onFinal(text) }
            }
            if error != nil || result?.isFinal == true {
                DispatchQueue.main.async { self.task = nil }
            }
        }
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
