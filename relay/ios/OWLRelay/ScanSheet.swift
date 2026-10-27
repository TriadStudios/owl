import SwiftUI

struct ScanSheet: View {
    @EnvironmentObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var error: String?
    @State private var attempt = 0

    var body: some View {
        ZStack(alignment: .bottom) {
            QRScanner { code in
                if model.pair(with: code) {
                    dismiss()
                } else {
                    error = "That QR isn't an OWL pairing code."
                    attempt += 1
                }
            }
            .id(attempt)
            .ignoresSafeArea()
            VStack(spacing: 12) {
                Text(error ?? "Point at the pairing QR on the OWL server.")
                    .font(.callout)
                    .foregroundStyle(error == nil ? .white : Verdict.do_not_touch.color)
                    .multilineTextAlignment(.center)
                Button("Cancel") { dismiss() }
                    .font(.headline)
                    .frame(maxWidth: .infinity, minHeight: 52)
                    .background(.black.opacity(0.6), in: RoundedRectangle(cornerRadius: 14))
            }
            .padding(20)
            .background(.black.opacity(0.5))
        }
    }
}
