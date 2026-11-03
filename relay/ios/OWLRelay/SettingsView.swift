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
            Section("You") {
                TextField("Your name, as work orders assign it", text: $model.techName)
                    .textInputAutocapitalization(.words)
                    .autocorrectionDisabled()
                    .frame(minHeight: 44)
            }
            GlassesSection(glasses: model.glasses)
            Section("Live") {
                Stepper("\(model.fps) fps", value: $model.fps, in: 1...ARCamera.maxFPS)
                    .frame(minHeight: 44)
                Toggle("Speak replies", isOn: $model.voice)
                Toggle("Listen without holding", isOn: $model.handsFree)
                    .frame(minHeight: 44)
                Button("Delete debug recordings", role: .destructive) {
                    model.recording = false
                    DebugRecorder.clear()
                }
                .frame(minHeight: 44)
            }
        }
        .navigationTitle("Settings")
        .sheet(isPresented: $scanning) { ScanSheet() }
    }
}

struct GlassesSection: View {
    @ObservedObject var glasses: Glasses

    var body: some View {
        Section {
            LabeledContent("Status", value: status)
            TimelineView(.periodic(from: .now, by: 2)) { _ in
                ForEach(glasses.details, id: \.self) { Text($0).font(.footnote).foregroundStyle(.secondary) }
            }
            if glasses.registered {
                Button("Disconnect glasses", role: .destructive) { Task { await glasses.disconnect() } }
                    .frame(minHeight: 44)
            } else {
                Button("Connect glasses") { Task { await glasses.connect() } }
                    .frame(minHeight: 44)
                    .disabled(glasses.wearables == nil || glasses.registration == .registering)
            }
            if let problem = glasses.problem {
                Text(problem).font(.footnote).foregroundStyle(Verdict.caution.color)
            }
        } header: {
            Text("Meta glasses")
        } footer: {
            Text("Connecting opens the Meta AI app. Developer Mode must be on there: Settings, App Info, tap the version five times.")
        }
    }

    private var status: String {
        switch glasses.registration {
        case .registered: glasses.devices.isEmpty ? "Connected, no glasses nearby" : "Connected"
        case .registering: "Waiting for Meta AI…"
        case .available: "Not connected"
        case .unavailable: "Meta AI not available"
        @unknown default: "Unknown"
        }
    }
}
