import Foundation

struct OverlayPolygon: Decodable {
    let port: Int
    let color: String
    let points: [[Double]]
}

struct OverlayLabel: Decodable {
    let port: Int
    let text: String
    let verdict: String
}

struct Overlay: Decodable {
    var polygons: [OverlayPolygon] = []
    var labels: [OverlayLabel] = []
}

enum ServerMessage: Decodable {
    case overlay(Overlay)
    case say(String)
    case unknown

    private enum Keys: String, CodingKey { case type, polygons, labels, text }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        switch try c.decode(String.self, forKey: .type) {
        case "overlay":
            self = .overlay(Overlay(
                polygons: try c.decodeIfPresent([OverlayPolygon].self, forKey: .polygons) ?? [],
                labels: try c.decodeIfPresent([OverlayLabel].self, forKey: .labels) ?? []
            ))
        case "say":
            self = .say(try c.decode(String.self, forKey: .text))
        default:
            self = .unknown
        }
    }
}

struct FrameMessage: Encodable {
    let type = "frame"
    let ts: Int64
    let jpeg_b64: String
}

struct UtteranceMessage: Encodable {
    let type = "utterance"
    let text: String
}
