import Foundation
import Security

struct Pairing: Codable, Equatable {
    let url: String
    let token: String
    let cert_sha256: String

    var relayURL: URL? {
        URL(string: url.hasSuffix("/") ? url + "relay" : url + "/relay")
    }

    var pin: String {
        cert_sha256.lowercased().filter { $0.isHexDigit }
    }
}

enum PairingStore {
    private static let defaultsKey = "pairing"
    private static let service = "com.triadstudios.owlrelay"
    private static let account = "token"

    private struct Public: Codable {
        let url: String
        let cert_sha256: String
    }

    static func load() -> Pairing? {
        guard let data = UserDefaults.standard.data(forKey: defaultsKey),
              let pub = try? JSONDecoder().decode(Public.self, from: data),
              let token = readToken() else { return nil }
        return Pairing(url: pub.url, token: token, cert_sha256: pub.cert_sha256)
    }

    static func save(_ pairing: Pairing) {
        writeToken(pairing.token)
        let data = try? JSONEncoder().encode(Public(url: pairing.url, cert_sha256: pairing.cert_sha256))
        UserDefaults.standard.set(data, forKey: defaultsKey)
    }

    static func clear() {
        SecItemDelete(query as CFDictionary)
        UserDefaults.standard.removeObject(forKey: defaultsKey)
    }

    private static var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }

    private static func readToken() -> String? {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess,
              let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private static func writeToken(_ token: String) {
        SecItemDelete(query as CFDictionary)
        var q = query
        q[kSecValueData as String] = Data(token.utf8)
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(q as CFDictionary, nil)
    }
}
