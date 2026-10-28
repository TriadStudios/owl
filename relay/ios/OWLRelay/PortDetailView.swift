import SwiftUI

struct PortDetailView: View {
    @EnvironmentObject var model: AppModel
    let port: Port

    private var current: Port {
        model.ports?.ports.first { $0.num == port.num } ?? port
    }

    var body: some View {
        let port = current
        List {
            Section {
                HStack(spacing: 16) {
                    PortChip(num: port.num, verdict: port.verdict, size: 56)
                    VStack(alignment: .leading, spacing: 6) {
                        Text(port.deviceName ?? "Port \(port.num)").font(.title2.bold())
                        Text(port.verdict.title.uppercased())
                            .font(.caption.weight(.bold))
                            .padding(.horizontal, 10)
                            .padding(.vertical, 4)
                            .foregroundStyle(.black)
                            .background(port.verdict.color, in: Capsule())
                    }
                }
                .padding(.vertical, 6)
                if let reason = port.unknown_reason {
                    Label(reason, systemImage: "questionmark.circle")
                        .foregroundStyle(.secondary)
                }
            }
            Section("Verdict inputs") {
                if port.inputs.isEmpty {
                    Text("None").foregroundStyle(.secondary)
                }
                ForEach(Array(port.inputs.enumerated()), id: \.offset) { _, input in
                    Text(input).font(.callout.monospaced()).textSelection(.enabled)
                }
            }
            Section("Device") {
                LabeledContent("Device", value: port.deviceName ?? "—")
                LabeledContent("Vendor", value: port.primary?.vendor ?? "—")
                LabeledContent("IP", value: port.primary?.ip ?? "—")
                if let neighbor = port.neighbor {
                    LabeledContent("LLDP neighbor", value: neighbor)
                }
                if port.behind > 0 {
                    DisclosureGroup("\(port.behind) behind") {
                        ForEach(port.devices, id: \.self) { d in
                            HStack {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(d.hostname ?? d.mac).font(d.hostname == nil ? .body.monospaced() : .body)
                                    Text([d.hostname == nil ? nil : d.mac, d.vendor, d.ip].compactMap { $0 }.joined(separator: " · "))
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                                Spacer()
                                Circle().fill(d.online ? Verdict.safe.color : Verdict.unknown.color).frame(width: 8, height: 8)
                            }
                            .frame(minHeight: 44)
                        }
                        if port.devices.count < port.behind {
                            Text("\(port.behind - port.devices.count) more MACs without a known identity")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .frame(minHeight: 44)
                }
            }
            Section("Status") {
                LabeledContent("Link", value: port.switchState.capitalized)
                LabeledContent("Camera", value: port.camera.capitalized)
                LabeledContent("Liveness", value: (port.liveness ?? "unknown").capitalized)
                LabeledContent("Traffic", value: "\(ByteCountFormatter.string(fromByteCount: Int64(clamping: port.delta_bytes), countStyle: .binary)) / \(Int(port.window_s)) s")
                LabeledContent("Criticality", value: port.criticality.isEmpty ? "None" : port.criticality.joined(separator: ", "))
                LabeledContent("Last verified") {
                    if port.status == "consistent", let polled = model.ports?.polled_at {
                        Text(polled, format: .relative(presentation: .named))
                    } else {
                        Text(port.status == "unknown" ? "Not verified" : port.status.replacingOccurrences(of: "-", with: " ").capitalized)
                    }
                }
            }
            Section("Sources") {
                Text(port.sources.isEmpty ? "None" : port.sources.joined(separator: ", "))
                    .font(.callout.monospaced())
            }
            Section("Evidence") {
                EvidenceImage(hash: model.ports?.frame_hash, pairing: model.active)
            }
        }
        .navigationTitle("Port \(port.num)")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.refresh() }
    }
}

struct EvidenceImage: View {
    let hash: String?
    let pairing: Pairing?
    @State private var image: UIImage?
    @State private var failed: String?

    var body: some View {
        Group {
            if let image {
                Image(uiImage: image).resizable().scaledToFit()
                    .clipShape(RoundedRectangle(cornerRadius: 10))
            } else {
                Label(hash == nil ? "No camera frame yet" : failed ?? "Loading…", systemImage: "photo")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 120)
            }
        }
        .task(id: hash) {
            image = nil
            failed = nil
            guard let hash else { return }
            do {
                let data = try await API.data("/api/evidence/\(hash)", from: pairing)
                if let img = UIImage(data: data) { image = img } else { failed = "Evidence isn't an image" }
            } catch {
                failed = error.localizedDescription
            }
        }
    }
}
