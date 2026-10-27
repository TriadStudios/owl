import SwiftUI

struct PortDetailView: View {
    @EnvironmentObject var model: AppModel
    let port: Port

    var body: some View {
        List {
            Section {
                HStack(spacing: 16) {
                    PortChip(num: port.num, color: port.color, verdict: port.verdict, size: 56)
                    VStack(alignment: .leading, spacing: 6) {
                        Text(port.name).font(.title2.bold())
                        Text(port.verdict.title.uppercased())
                            .font(.caption.weight(.bold))
                            .padding(.horizontal, 10)
                            .padding(.vertical, 4)
                            .foregroundStyle(.black)
                            .background(port.verdict.color, in: Capsule())
                    }
                }
                .padding(.vertical, 6)
            }
            Section("Why") {
                ForEach(port.verdict_inputs, id: \.self) { Text($0) }
            }
            Section("Device") {
                LabeledContent("Device", value: port.device?.name ?? "—")
                LabeledContent("Vendor", value: port.device?.vendor ?? "—")
                LabeledContent("IP", value: port.device?.ip ?? "—")
                if !port.behind.isEmpty {
                    DisclosureGroup("\(port.behind.count) behind") {
                        ForEach(port.behind, id: \.self) { host in
                            VStack(alignment: .leading, spacing: 2) {
                                Text(host.mac).font(.body.monospaced())
                                Text([host.vendor, host.ip].compactMap { $0 }.joined(separator: " · "))
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
            }
            Section("Status") {
                LabeledContent("Liveness", value: port.liveness.capitalized)
                LabeledContent("Criticality", value: port.criticality.capitalized)
                LabeledContent("Last verified") {
                    if let date = port.last_verified {
                        Text(date, format: .relative(presentation: .named))
                    } else {
                        Text("Never")
                    }
                }
            }
            if !port.sources.isEmpty {
                Section("Sources") {
                    ForEach(port.sources.sorted { $0.key < $1.key }, id: \.key) { key, value in
                        LabeledContent(key == "ip" ? "IP" : key.capitalized, value: value.uppercased())
                    }
                }
            }
            Section("Evidence") {
                EvidenceImage(path: port.evidence_url, pairing: model.active)
            }
        }
        .navigationTitle("Port \(port.num)")
        .navigationBarTitleDisplayMode(.inline)
    }
}

struct EvidenceImage: View {
    let path: String?
    let pairing: Pairing?
    @State private var image: UIImage?

    var body: some View {
        Group {
            if let image {
                Image(uiImage: image).resizable().scaledToFit()
                    .clipShape(RoundedRectangle(cornerRadius: 10))
            } else {
                Label(path == nil ? "No evidence yet" : "Loading…", systemImage: "photo")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 120)
            }
        }
        .task(id: path) {
            guard let path, let data = try? await API.data(path, from: pairing) else { return }
            image = UIImage(data: data)
        }
    }
}
