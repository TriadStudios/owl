import Foundation

enum Verdict: String, Decodable, Hashable {
    case safe
    case caution
    case doNotTouch = "do-not-touch"
    case unknown

    init(from decoder: Decoder) throws {
        self = Verdict(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }

    var title: String {
        switch self {
        case .safe: "Safe"
        case .caution: "Caution"
        case .doNotTouch: "Do not touch"
        case .unknown: "Unknown"
        }
    }
}

enum Severity: String, Decodable, Hashable {
    case critical, warning, info

    init(from decoder: Decoder) throws {
        self = Severity(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .info
    }
}

struct Health: Decodable, Equatable {
    let switch_reachable: Bool
    let perception_up: Bool
    let robot_attached: Bool
    let relay_connected: Int
    let last_poll_age_s: Double?
    let last_frame_age_s: Double?
}

struct PortDevice: Decodable, Hashable {
    let mac: String
    let ip: String?
    let hostname: String?
    let vendor: String?
    let online: Bool
    let sources: [String]
}

struct Port: Decodable, Hashable, Identifiable {
    let num: Int
    let camera: String
    let switchState: String
    let status: String
    let sources: [String]
    let verdict: Verdict
    let unknown_reason: String?
    let downstream: String
    let behind: Int
    let neighbor: String?
    let liveness: String?
    let delta_bytes: UInt64
    let window_s: Double
    let criticality: [String]
    let devices: [PortDevice]
    let inputs: [String]

    var id: Int { num }

    var isUp: Bool { switchState == "up" }

    var onSwitch: Bool { switchState != "unknown" }

    private enum CodingKeys: String, CodingKey {
        case num, camera, status, sources, verdict, unknown_reason, downstream, behind, neighbor, liveness, delta_bytes, window_s, criticality, devices, inputs
        case switchState = "switch"
    }

    var primary: PortDevice? { devices.first }

    var deviceName: String? {
        primary?.hostname ?? neighbor ?? (downstream.isEmpty ? nil : downstream)
    }
}

struct PortsResponse: Decodable {
    let polled_at: Date
    let frame_hash: String?
    let ports: [Port]

    var portsTotal: Int { ports.filter(\.onSwitch).count }

    var portsInUse: Int { ports.filter(\.isUp).count }
}

struct Finding: Decodable, Hashable, Identifiable {
    let id: String
    let severity: Severity
    let port: Int?
    let title: String
    let detail: String?
    let ts: Date?

    private enum Keys: String, CodingKey { case id, severity, port, title, detail, ts }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        title = try c.decode(String.self, forKey: .title)
        id = (try? c.decodeIfPresent(String.self, forKey: .id)) ?? title
        severity = (try? c.decodeIfPresent(Severity.self, forKey: .severity)) ?? .info
        port = try? c.decodeIfPresent(Int.self, forKey: .port)
        detail = try? c.decodeIfPresent(String.self, forKey: .detail)
        ts = try? c.decodeIfPresent(Date.self, forKey: .ts)
    }
}

struct FindingsResponse: Decodable {
    let findings: [Finding]

    private struct Lenient: Decodable {
        let value: Finding?
        init(from decoder: Decoder) throws { value = try? Finding(from: decoder) }
    }

    private enum Keys: String, CodingKey { case findings }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        findings = (try c.decodeIfPresent([Lenient].self, forKey: .findings) ?? []).compactMap(\.value)
    }
}
