import Foundation

enum Verdict: String, Decodable, Hashable {
    case safe, caution, do_not_touch, unknown

    init(from decoder: Decoder) throws {
        self = Verdict(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }

    var title: String {
        switch self {
        case .safe: "Safe"
        case .caution: "Caution"
        case .do_not_touch: "Do not touch"
        case .unknown: "Unknown"
        }
    }

    var rank: Int {
        switch self {
        case .do_not_touch: 3
        case .caution: 2
        case .unknown: 1
        case .safe: 0
        }
    }
}

enum Severity: String, Decodable, Hashable {
    case critical, warning, info

    init(from decoder: Decoder) throws {
        self = Severity(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .info
    }
}

struct Closet: Decodable, Hashable {
    let name: String
}

struct SwitchInfo: Decodable, Hashable {
    let name: String
    let model: String
    let ports_total: Int
    let ports_in_use: Int
    let last_poll: Date?
}

struct Device: Decodable, Hashable {
    let name: String?
    let vendor: String?
    let ip: String?
}

struct BehindHost: Decodable, Hashable {
    let mac: String
    let vendor: String?
    let ip: String?
}

struct Port: Decodable, Hashable, Identifiable {
    let num: Int
    let name: String
    let color: String
    let verdict: Verdict
    let verdict_inputs: [String]
    let device: Device?
    let behind: [BehindHost]
    let liveness: String
    let criticality: String
    let sources: [String: String]
    let last_verified: Date?
    let evidence_url: String?

    var id: Int { num }
}

struct PortsResponse: Decodable {
    let closet: Closet
    let switchInfo: SwitchInfo
    let ports: [Port]

    private enum CodingKeys: String, CodingKey {
        case closet, ports
        case switchInfo = "switch"
    }
}

struct Finding: Decodable, Hashable, Identifiable {
    let id: String
    let severity: Severity
    let port: Int?
    let title: String
    let detail: String
    let ts: Date
}

struct FindingsResponse: Decodable {
    let findings: [Finding]
}
