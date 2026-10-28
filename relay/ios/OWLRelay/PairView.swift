import SwiftUI

struct PairView: View {
    @EnvironmentObject var model: AppModel
    @State private var scanning = false

    var body: some View {
        VStack(spacing: 24) {
            Spacer()
            if let active = model.active {
                Image(systemName: model.health == nil ? "antenna.radiowaves.left.and.right" : "checkmark.circle.fill")
                    .font(.system(size: 56))
                    .foregroundStyle(model.health == nil ? Theme.accent : Verdict.safe.color)
                Card {
                    Text(active.displayName).font(.title2.bold())
                    Text(summary(active))
                        .font(.body)
                        .foregroundStyle(.secondary)
                    if let error = model.loadError {
                        Text(error).font(.footnote).foregroundStyle(Verdict.caution.color)
                    }
                }
                Spacer()
                PrimaryButton(title: "Continue") { model.showPair = false }
                    .disabled(model.health == nil)
                Button("Scan a different closet") { scanning = true }
                    .frame(minHeight: 44)
            } else {
                Image(systemName: "qrcode.viewfinder")
                    .font(.system(size: 64, weight: .light))
                    .foregroundStyle(Theme.accent)
                VStack(spacing: 8) {
                    Text("Not paired")
                        .font(.largeTitle.bold())
                    Text("On the closet box, run owl pair and scan the QR it prints.")
                        .font(.body)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                Spacer()
                PrimaryButton(title: "Scan to pair") { scanning = true }
            }
        }
        .padding(24)
        .sheet(isPresented: $scanning) { ScanSheet() }
        .task(id: model.activeID) { await model.refresh() }
    }

    private func summary(_ active: Pairing) -> String {
        guard let health = model.health else { return "Reaching \(active.host)…" }
        var parts = [health.switch_reachable ? "Switch reachable" : "Switch unreachable"]
        if let ports = model.ports { parts.append("\(ports.portsInUse) of \(ports.portsTotal) ports in use") }
        return parts.joined(separator: " · ")
    }
}
