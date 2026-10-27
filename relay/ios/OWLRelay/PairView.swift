import SwiftUI

struct PairView: View {
    @EnvironmentObject var model: AppModel
    @State private var scanning = false

    var body: some View {
        VStack(spacing: 24) {
            Spacer()
            if let active = model.active {
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 56))
                    .foregroundStyle(Verdict.safe.color)
                Card {
                    Text(model.ports?.closet.name ?? active.displayName)
                        .font(.title2.bold())
                    Text(model.ports?.switchInfo.model ?? "Reaching \(active.host)…")
                        .font(.body)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                PrimaryButton(title: "Continue") { model.showPair = false }
                Button("Scan a different closet") { scanning = true }
                    .frame(minHeight: 44)
            } else {
                Image(systemName: "qrcode.viewfinder")
                    .font(.system(size: 64, weight: .light))
                    .foregroundStyle(Theme.accent)
                VStack(spacing: 8) {
                    Text("Not paired")
                        .font(.largeTitle.bold())
                    Text("Scan the pairing QR shown by the OWL server in your network closet.")
                        .font(.body)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                Spacer()
                PrimaryButton(title: "Scan to pair") { scanning = true }
                #if DEBUG
                Button("Preview with sample data") {
                    model.mock = true
                    model.showPair = false
                }
                .frame(minHeight: 44)
                #endif
            }
        }
        .padding(24)
        .sheet(isPresented: $scanning) { ScanSheet() }
        .task(id: model.activeID) { await model.refresh() }
    }
}
