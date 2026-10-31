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
                if model.health != nil || model.ports != nil {
                    SwitchCard(health: model.health, ports: model.ports, lastPoll: model.lastPoll)
                }
                if let ports = model.ports {
                    PortStrip(ports: ports.ports)
                    FindingsList(findings: model.findings, ports: ports.ports)
                } else if model.loadError == nil {
                    ProgressView().frame(maxWidth: .infinity, minHeight: 200)
                }
            }
            .padding(16)
        }
        .refreshable { await model.refresh() }
        .navigationTitle(model.active?.displayName ?? "Closet")
        .navigationDestination(for: Port.self) { PortDetailView(port: $0) }
        .task(id: model.activeID) { if model.ports == nil { await model.refresh() } }
    }
}

struct SwitchCard: View {
    let health: Health?
    let ports: PortsResponse?
    let lastPoll: Date?

    var body: some View {
        Card {
            HStack {
                Text("Switch").font(.title2.bold())
                Spacer()
                if let health {
                    StatusDot(on: health.switch_reachable, label: health.switch_reachable ? "Reachable" : "Unreachable")
                }
            }
            if let ports {
                HStack(alignment: .firstTextBaseline) {
                    Text("\(ports.portsInUse)")
                        .font(.system(size: 34, weight: .semibold).monospacedDigit())
                    Text("of \(ports.portsTotal) ports in use")
                        .foregroundStyle(.secondary)
                }
            }
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                Text(pollText(now: ctx.date))
                    .font(.footnote.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            if let health {
                HStack(spacing: 16) {
                    StatusDot(on: health.perception_up, label: "Camera AI")
                    StatusDot(on: health.robot_attached, label: "Robot")
                }
                .font(.footnote)
            }
        }
    }

    private func pollText(now: Date) -> String {
        guard let lastPoll else { return "No switch poll yet" }
        let s = max(0, Int(now.timeIntervalSince(lastPoll)))
        return s < 60 ? "Last poll \(s) s ago" : "Last poll \(s / 60) min \(s % 60) s ago"
    }
}

struct StatusDot: View {
    let on: Bool
    let label: String

    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(on ? Verdict.safe.color : Verdict.unknown.color).frame(width: 8, height: 8)
            Text(label).foregroundStyle(.secondary)
        }
    }
}

struct PortStrip: View {
    let ports: [Port]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Ports").font(.headline)
            if ports.isEmpty {
                Card { Text("The switch reported no ports.").foregroundStyle(.secondary) }
            }
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(ports) { port in
                        NavigationLink(value: port) {
                            VStack(spacing: 4) {
                                PortChip(num: port.num, verdict: port.verdict)
                                Text(port.isUp ? "up" : port.switchState)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Port \(port.num), \(port.verdict.title)")
                    }
                }
                .padding(.vertical, 2)
            }
            HStack(spacing: 14) {
                ForEach([Verdict.safe, .caution, .doNotTouch, .unknown], id: \.self) { v in
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
                if let detail = finding.detail {
                    Text(detail).font(.subheadline).foregroundStyle(.secondary)
                }
                if let ts = finding.ts {
                    Text(ts, format: .relative(presentation: .named))
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
            }
            Spacer(minLength: 0)
            if let port {
                PortChip(num: port.num, verdict: port.verdict, size: 32)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, minHeight: 56, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 16))
        .contentShape(Rectangle())
    }
}
