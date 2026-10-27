import SwiftUI

struct SettingsView: View {
    @ObservedObject var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var scanning = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    if let pairing = model.pairing {
                        LabeledContent("URL", value: pairing.url)
                        LabeledContent("Cert", value: String(pairing.pin.prefix(16)) + "…")
                        Button("Unpair", role: .destructive) { model.unpair() }
                    } else {
                        Text("Not paired")
                    }
                    Button(model.pairing == nil ? "Scan pairing QR" : "Re-pair") { scanning = true }
                    if let error { Text(error).foregroundStyle(.red) }
                }
                Section("Camera") {
                    Stepper("\(model.fps) fps", value: $model.fps, in: 1...PhoneCamera.maxFPS)
                }
            }
            .navigationTitle("Settings")
            .toolbar { Button("Done") { dismiss() } }
            .sheet(isPresented: $scanning) {
                QRScanner { code in
                    scanning = false
                    error = model.pair(with: code) ? nil : "Invalid pairing code"
                }
                .ignoresSafeArea()
            }
        }
    }
}
