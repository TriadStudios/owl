import CryptoKit
import Foundation

enum Pinning {
    static func handle(_ challenge: URLAuthenticationChallenge, pin: String?, completion: (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first,
              let pin else {
            completion(.cancelAuthenticationChallenge, nil)
            return
        }
        let hex = SHA256.hash(data: SecCertificateCopyData(leaf) as Data).map { String(format: "%02x", $0) }.joined()
        if hex == pin {
            completion(.useCredential, URLCredential(trust: trust))
        } else {
            completion(.cancelAuthenticationChallenge, nil)
        }
    }
}

final class PinnedDelegate: NSObject, URLSessionDelegate {
    let pin: String

    init(pin: String) {
        self.pin = pin
    }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        Pinning.handle(challenge, pin: pin, completion: completionHandler)
    }
}

enum APIError: LocalizedError {
    case notPaired
    case unauthorized
    case server(Int, String?)
    case pinMismatch

    var errorDescription: String? {
        switch self {
        case .notPaired: "No closet paired."
        case .unauthorized: "The closet rejected this phone's token. Re-pair to continue."
        case .server(let code, let message): message.map { "Server: \($0)" } ?? "Server returned HTTP \(code)."
        case .pinMismatch: "The closet's certificate doesn't match the pairing. Re-pair to continue."
        }
    }
}

enum API {
    private static let fraction = try! NSRegularExpression(pattern: #"(\.\d{3})\d+"#)

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let raw = try dec.singleValueContainer().decode(String.self)
            let s = fraction.stringByReplacingMatches(in: raw, range: NSRange(raw.startIndex..., in: raw), withTemplate: "$1")
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = f.date(from: s) { return date }
            f.formatOptions = [.withInternetDateTime]
            if let date = f.date(from: s) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "bad date \(raw)"))
        }
        return d
    }()

    private static var sessions: [String: URLSession] = [:]
    private static let lock = NSLock()

    private static func session(pin: String) -> URLSession {
        lock.withLock {
            if let s = sessions[pin] { return s }
            let config = URLSessionConfiguration.ephemeral
            config.timeoutIntervalForRequest = 10
            let s = URLSession(configuration: config, delegate: PinnedDelegate(pin: pin), delegateQueue: nil)
            sessions[pin] = s
            return s
        }
    }

    static func get<T: Decodable>(_ path: String, from pairing: Pairing?) async throws -> T {
        try decoder.decode(T.self, from: try await data(path, from: pairing))
    }

    static func post<T: Decodable>(_ path: String, _ body: [String: String], to pairing: Pairing?) async throws -> T {
        try decoder.decode(T.self, from: try await data(path, from: pairing, body: try JSONEncoder().encode(body)))
    }

    static func data(_ path: String, from pairing: Pairing?, body: Data? = nil) async throws -> Data {
        guard let pairing else { throw APIError.notPaired }
        guard let base = pairing.apiBase, let url = URL(string: path, relativeTo: base) else { throw URLError(.badURL) }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(pairing.token)", forHTTPHeaderField: "Authorization")
        if let body {
            request.httpMethod = "POST"
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session(pin: pairing.pin).data(for: request)
        } catch let error as URLError where !Task.isCancelled && (error.code == .cancelled || error.code == .serverCertificateUntrusted) {
            throw APIError.pinMismatch
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        switch status {
        case 200: return data
        case 401: throw APIError.unauthorized
        default:
            let message = (try? JSONDecoder().decode([String: String].self, from: data))?["error"]
            throw APIError.server(status, message)
        }
    }
}
