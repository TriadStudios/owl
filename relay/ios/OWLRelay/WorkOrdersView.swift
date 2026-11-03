import SwiftUI

struct WorkOrdersView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        List {
            if model.techName.isEmpty {
                Section {
                    Label("Set your name in Settings so orders assigned to you show up here.", systemImage: "person.crop.circle.badge.exclamationmark")
                        .font(.callout)
                }
            }
            section("In progress", model.orders.filter { $0.status == "accepted" })
            section("Assigned to you", model.orders.filter { $0.status == "assigned" && $0.isMine(model.techName) })
            section("Open", model.orders.filter { $0.status == "open" })
            if model.orders.isEmpty {
                Text("No open work orders.").foregroundStyle(.secondary)
            }
        }
        .navigationTitle("Work orders")
        .navigationDestination(for: Int.self) { id in
            if let order = model.orders.first(where: { $0.id == id }) {
                WorkOrderDetail(order: order)
            } else {
                Text("This work order is closed.").foregroundStyle(.secondary)
            }
        }
        .refreshable { await model.refresh() }
        .task {
            while !Task.isCancelled {
                await model.refresh()
                try? await Task.sleep(for: .seconds(10))
            }
        }
    }

    @ViewBuilder
    private func section(_ title: String, _ orders: [WorkOrder]) -> some View {
        if !orders.isEmpty {
            Section(title) {
                ForEach(orders) { order in
                    NavigationLink(value: order.id) { WorkOrderRow(order: order) }
                }
            }
        }
    }
}

struct WorkOrderRow: View {
    let order: WorkOrder

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(order.name).font(.headline)
                if order.mismatch {
                    Text("TICKET PORT WRONG")
                        .font(.caption2.bold())
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Verdict.doNotTouch.color.opacity(0.25), in: Capsule())
                        .foregroundStyle(Verdict.doNotTouch.color)
                }
            }
            Text("\(order.device ?? "Port \(order.port)") · port \(order.port) · \(order.tech.isEmpty ? "unassigned" : order.tech)")
                .font(.subheadline)
                .foregroundStyle(.secondary)
        }
    }
}

struct WorkOrderDetail: View {
    @EnvironmentObject var model: AppModel
    let order: WorkOrder
    @State private var busy = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    Text("Port \(order.port)").font(.system(size: 40, weight: .bold))
                    if order.mismatch, let claimed = order.claimed_port {
                        Text("ticket said \(claimed)")
                            .font(.title3)
                            .strikethrough()
                            .foregroundStyle(Verdict.doNotTouch.color)
                    }
                }
                if let check = order.check {
                    Text(check)
                        .font(.body)
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background((order.mismatch ? Verdict.doNotTouch.color : Verdict.safe.color).opacity(0.18), in: RoundedRectangle(cornerRadius: 12))
                }
                if let note = order.note, !note.isEmpty {
                    Text(note)
                }
                Text(order.tech.isEmpty ? "Waiting for a tech · until \(order.end.formatted(date: .omitted, time: .shortened))"
                     : "\(order.tech) · \(order.status == "accepted" ? "in progress" : "assigned") · until \(order.end.formatted(date: .omitted, time: .shortened))")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                if order.status == "accepted" {
                    Label("Open Live and OWL will guide you to port \(order.port).", systemImage: "camera.viewfinder")
                        .font(.callout)
                    button("Mark done", role: .destructive) { await model.finish(order) }
                } else {
                    button("Accept work order", role: nil) { await model.accept(order) }
                    Text("Or say \"accept \(order.name)\" in Live, hands free.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .padding(16)
        }
        .navigationTitle(order.name)
        .navigationBarTitleDisplayMode(.inline)
    }

    private func button(_ title: String, role: ButtonRole?, action: @escaping () async -> Void) -> some View {
        Button(role: role) {
            busy = true
            Task {
                await action()
                busy = false
            }
        } label: {
            Text(title).font(.headline).frame(maxWidth: .infinity, minHeight: 50)
        }
        .buttonStyle(.borderedProminent)
        .disabled(busy)
    }
}
