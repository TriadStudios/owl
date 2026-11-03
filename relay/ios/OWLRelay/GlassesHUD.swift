import Foundation
import MWDATDisplay

// Kept apart from SwiftUI files because the Display toolkit's Text and Image share SwiftUI's names.
struct HUD: Equatable {
    var order: String?
    var orderNote: String?
    var orderWrong = false
    var hand: String?
    var handDetail: String?
    var handDanger = false
    var said: String?

    var isEmpty: Bool { order == nil && hand == nil && said == nil }

    func view() -> FlexBox {
        FlexBox(direction: .column, spacing: 10, padding: EdgeInsets(all: 14)) {
            if let order {
                FlexBox(direction: .row, spacing: 8, crossAlignment: .center) {
                    Icon(name: orderWrong ? .exclamationTriangle : .crossBriefcase)
                    Text(order, style: .heading)
                }
                if let orderNote {
                    Text(orderNote, style: .meta, color: .secondary)
                }
            }
            if let hand {
                FlexBox(direction: .row, spacing: 8, crossAlignment: .center) {
                    Icon(name: handDanger ? .exclamationCircle : .checkmarkCircle)
                    Text(hand, style: .heading)
                }
                if let handDetail {
                    Text(handDetail, style: .body, color: .secondary)
                }
            }
            if let said {
                FlexBox(direction: .row, spacing: 8, crossAlignment: .start) {
                    Icon(name: .speechBubble, style: .outline)
                    Text(said, style: .body)
                }
            }
        }
        .background(.card)
    }
}
