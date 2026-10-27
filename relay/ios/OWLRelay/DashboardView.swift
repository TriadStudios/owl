import SwiftUI

struct DashboardView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                if let error = model.loadError {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .font(.callout)
                        .foregroundStyle(Verdict.caution.color)
                }
                if let ports = model.ports {
                    SwitchCard(info: ports.switchInfo)
                    PortStrip(ports: ports.ports)
                    FindingsList(findings: model.findings, ports: ports.ports)
                } else if model.loadError == nil {
                    ProgressView().frame(maxWidth: .infinity, minHeight: 200)
                }
            }
            .padding(16)
        }
        .refreshable { await model.refresh() }
        .navigationTitle(model.ports?.closet.name ?? model.active?.displayName ?? "Closet")
        .navigationDestination(for: Port.self) { PortDetailView(port: $0) }
        .task { if model.ports == nil { await model.refresh() } }
    }
}

struct SwitchCard: View {
    let info: SwitchInfo

    var body: some View {
        Card {
            VStack(alignment: .leading, spacing: 4) {
                Text(info.model).font(.title2.bold())
                Text(info.name).font(.subheadline).foregroundStyle(.secondary)
            }
            HStack(alignment: .firstTextBaseline) {
                Text("\(info.ports_in_use)")
                    .font(.system(size: 34, weight: .semibold).monospacedDigit())
                Text("of \(info.ports_total) ports in use")
                    .foregroundStyle(.secondary)
            }
            if let poll = info.last_poll {
                Text("Last poll \(poll, format: .relative(presentation: .named))")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

struct PortStrip: View {
    let ports: [Port]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Ports").font(.headline)
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(ports) { port in
                        NavigationLink(value: port) {
                            PortChip(num: port.num, color: port.color, verdict: port.verdict)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Port \(port.num), \(port.verdict.title)")
                    }
                }
                .padding(.vertical, 2)
            }
            HStack(spacing: 14) {
                ForEach([Verdict.safe, .caution, .do_not_touch, .unknown], id: \.self) { v in
                    HStack(spacing: 4) {
                        Circle().strokeBorder(v.color, lineWidth: 2).frame(width: 10, height: 10)
                        Text(v.title)
                    }
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
    }
}

struct FindingsList: View {
    let findings: [Finding]
    let ports: [Port]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Findings").font(.headline)
            if findings.isEmpty {
                Card { Text("Nothing to report.").foregroundStyle(.secondary) }
            }
            ForEach(findings) { finding in
                if let port = ports.first(where: { $0.num == finding.port }) {
                    NavigationLink(value: port) { FindingRow(finding: finding, port: port) }
                        .buttonStyle(.plain)
                } else {
                    FindingRow(finding: finding, port: nil)
                }
            }
        }
    }
}

struct FindingRow: View {
    let finding: Finding
    let port: Port?

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Circle().fill(finding.severity.color).frame(width: 10, height: 10).padding(.top, 6)
            VStack(alignment: .leading, spacing: 4) {
                Text(finding.title).font(.body.weight(.semibold))
                Text(finding.detail).font(.subheadline).foregroundStyle(.secondary)
                Text(finding.ts, format: .relative(presentation: .named))
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
            Spacer(minLength: 0)
            if let port {
                PortChip(num: port.num, color: port.color, verdict: port.verdict, size: 32)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, minHeight: 56, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 16))
        .contentShape(Rectangle())
    }
}
