import Foundation

final class RelayClient: NSObject, URLSessionWebSocketDelegate {
    var onConnected: ((Bool) -> Void)?
    var onMessage: ((ServerMessage) -> Void)?

    private var pairing: Pairing?
    private var session: URLSession?
    private var task: URLSessionWebSocketTask?
    private var sendingFrame = false
    private let queue = DispatchQueue(label: "owl.relay")

    func connect(_ pairing: Pairing) {
        queue.async {
            self.pairing = pairing
            self.open()
        }
    }

    func disconnect() {
        queue.async {
            self.pairing = nil
            self.close()
        }
    }

    func sendFrame(_ jpeg: Data) {
        queue.async {
            guard let task = self.task, !self.sendingFrame else { return }
            let msg = FrameMessage(ts: Int64(Date().timeIntervalSince1970 * 1000), jpeg_b64: jpeg.base64EncodedString())
            guard let data = try? JSONEncoder().encode(msg), let text = String(data: data, encoding: .utf8) else { return }
            self.sendingFrame = true
            task.send(.string(text)) { _ in
                self.queue.async { self.sendingFrame = false }
            }
        }
    }

    func sendUtterance(_ text: String) {
        queue.async {
            guard let task = self.task,
                  let data = try? JSONEncoder().encode(UtteranceMessage(text: text)),
                  let text = String(data: data, encoding: .utf8) else { return }
            task.send(.string(text)) { _ in }
        }
    }

    private func open() {
        close()
        guard let pairing, let url = pairing.relayURL else { return }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(pairing.token)", forHTTPHeaderField: "Authorization")
        let session = URLSession(configuration: .ephemeral, delegate: self, delegateQueue: nil)
        let task = session.webSocketTask(with: request)
        self.session = session
        self.task = task
        task.resume()
        receive(task)
    }

    private func close() {
        task?.cancel(with: .goingAway, reason: nil)
        session?.invalidateAndCancel()
        task = nil
        session = nil
        sendingFrame = false
        onConnected?(false)
    }

    private func receive(_ task: URLSessionWebSocketTask) {
        task.receive { result in
            self.queue.async {
                guard task === self.task else { return }
                switch result {
                case .success(let message):
                    let data: Data?
                    switch message {
                    case .string(let s): data = s.data(using: .utf8)
                    case .data(let d): data = d
                    @unknown default: data = nil
                    }
                    if let data, let msg = try? JSONDecoder().decode(ServerMessage.self, from: data) {
                        self.onMessage?(msg)
                    }
                    self.receive(task)
                case .failure:
                    self.close()
                    self.queue.asyncAfter(deadline: .now() + 2) {
                        if self.pairing != nil && self.task == nil { self.open() }
                    }
                }
            }
        }
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        onConnected?(true)
    }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        Pinning.handle(challenge, pin: queue.sync { pairing?.pin }, completion: completionHandler)
    }
}
