import Foundation
import Security

struct Pairing: Codable, Equatable, Identifiable {
    let url: String
    let token: String
    let cert_sha256: String
    var name: String?

    var id: String { url }

    private var base: String {
        url.hasSuffix("/") ? String(url.dropLast()) : url
    }

    var relayURL: URL? {
        URL(string: base + "/relay")
    }

    var apiBase: URL? {
        guard var c = URLComponents(string: base) else { return nil }
        c.scheme = c.scheme == "ws" ? "http" : "https"
        c.path = ""
        return c.url
    }

    var pin: String {
        cert_sha256.lowercased().filter { $0.isHexDigit }
    }

    var host: String {
        URL(string: url)?.host ?? url
    }

    var displayName: String {
        name ?? host
    }
}

enum PairingStore {
    private static let listKey = "pairings"
    private static let activeKey = "activePairing"
    private static let service = "com.triadstudios.owlrelay"

    private struct Public: Codable {
        let url: String
        let cert_sha256: String
        let name: String?
    }

    static var activeURL: String? {
        get { UserDefaults.standard.string(forKey: activeKey) }
        set { UserDefaults.standard.set(newValue, forKey: activeKey) }
    }

    static func loadAll() -> [Pairing] {
        publics().compactMap { pub in
            readToken(pub.url).map { Pairing(url: pub.url, token: $0, cert_sha256: pub.cert_sha256, name: pub.name) }
        }
    }

    static func save(_ pairing: Pairing) {
        writeToken(pairing.token, for: pairing.url)
        var list = publics()
        let existing = list.first { $0.url == pairing.url }
        list.removeAll { $0.url == pairing.url }
        list.append(Public(url: pairing.url, cert_sha256: pairing.cert_sha256, name: pairing.name ?? existing?.name))
        UserDefaults.standard.set(try? JSONEncoder().encode(list), forKey: listKey)
    }

    static func remove(_ pairing: Pairing) {
        SecItemDelete(query(pairing.url) as CFDictionary)
        let list = publics().filter { $0.url != pairing.url }
        UserDefaults.standard.set(try? JSONEncoder().encode(list), forKey: listKey)
        if activeURL == pairing.url { activeURL = nil }
    }

    private static func publics() -> [Public] {
        guard let data = UserDefaults.standard.data(forKey: listKey) else { return [] }
        return (try? JSONDecoder().decode([Public].self, from: data)) ?? []
    }

    private static func query(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }

    private static func readToken(_ account: String) -> String? {
        var q = query(account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess,
              let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private static func writeToken(_ token: String, for account: String) {
        SecItemDelete(query(account) as CFDictionary)
        var q = query(account)
        q[kSecValueData as String] = Data(token.utf8)
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(q as CFDictionary, nil)
    }
}
