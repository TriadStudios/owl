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

enum API {
    static var mock: Bool {
        get { UserDefaults.standard.bool(forKey: "OWLMock") }
        set { UserDefaults.standard.set(newValue, forKey: "OWLMock") }
    }

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = f.date(from: s) { return date }
            f.formatOptions = [.withInternetDateTime]
            if let date = f.date(from: s) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "bad date \(s)"))
        }
        return d
    }()

    static func get<T: Decodable>(_ path: String, from pairing: Pairing?) async throws -> T {
        if mock {
            let name = path.split(separator: "/").last.map(String.init) ?? path
            guard let url = Bundle.main.url(forResource: name, withExtension: "json") else { throw URLError(.fileDoesNotExist) }
            return try decoder.decode(T.self, from: Data(contentsOf: url))
        }
        return try decoder.decode(T.self, from: try await data(path, from: pairing))
    }

    static func data(_ path: String, from pairing: Pairing?) async throws -> Data {
        guard let pairing, let base = pairing.apiBase, let url = URL(string: path, relativeTo: base) else {
            throw URLError(.badURL)
        }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(pairing.token)", forHTTPHeaderField: "Authorization")
        let session = URLSession(configuration: .ephemeral, delegate: PinnedDelegate(pin: pairing.pin), delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        let (data, response) = try await session.data(for: request)
        guard (response as? HTTPURLResponse)?.statusCode == 200 else { throw URLError(.badServerResponse) }
        return data
    }
}
