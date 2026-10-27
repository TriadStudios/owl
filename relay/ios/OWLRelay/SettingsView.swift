import SwiftUI

struct SettingsView: View {
    @EnvironmentObject var model: AppModel
    @State private var scanning = false

    var body: some View {
        Form {
            Section("Closets") {
                if model.pairings.isEmpty {
                    Text("No paired closets").foregroundStyle(.secondary)
                }
                ForEach(model.pairings) { pairing in
                    Button {
                        model.activate(pairing)
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(pairing.displayName).foregroundStyle(.primary)
                                Text(pairing.host).font(.caption).foregroundStyle(.secondary)
                            }
                            Spacer()
                            if pairing.id == model.activeID {
                                Image(systemName: "checkmark").foregroundStyle(Theme.accent)
                            }
                        }
                        .frame(minHeight: 44)
                    }
                    .swipeActions {
                        Button("Unpair", role: .destructive) { model.unpair(pairing) }
                    }
                }
                Button(model.active == nil ? "Pair a closet" : "Re-pair or add a closet") { scanning = true }
                    .frame(minHeight: 44)
                if let active = model.active {
                    Button("Unpair \(active.displayName)", role: .destructive) { model.unpair(active) }
                        .frame(minHeight: 44)
                }
            }
            Section("Live") {
                Stepper("\(model.fps) fps", value: $model.fps, in: 1...PhoneCamera.maxFPS)
                    .frame(minHeight: 44)
                Toggle("Speak replies", isOn: $model.voice)
                    .frame(minHeight: 44)
            }
            #if DEBUG
            Section("Developer") {
                Toggle("Use sample data", isOn: $model.mock)
                    .frame(minHeight: 44)
            }
            #endif
        }
        .navigationTitle("Settings")
        .sheet(isPresented: $scanning) { ScanSheet() }
    }
}
