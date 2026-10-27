import SwiftUI

struct LiveView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            ZStack {
                CameraPreview(session: model.camera.session)
                OverlayView(overlay: model.overlay)
            }
            .aspectRatio(9.0 / 16.0, contentMode: .fit)
            VStack(spacing: 10) {
                Text(status)
                    .font(.footnote.monospaced())
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(.black.opacity(0.6), in: Capsule())
                if let label = model.bannerLabel {
                    HStack(spacing: 10) {
                        Image(systemName: label.verdict == .do_not_touch ? "hand.raised.fill" : "info.circle.fill")
                        Text("\(label.verdict.title.uppercased()) · \(label.text)")
                            .lineLimit(1)
                    }
                    .font(.headline)
                    .foregroundStyle(.black)
                    .padding(.horizontal, 16)
                    .frame(maxWidth: .infinity, minHeight: 48)
                    .background(label.verdict.color, in: RoundedRectangle(cornerRadius: 14))
                }
                ForEach(model.toasts) { toast in
                    Text(toast.text)
                        .font(.callout)
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(.black.opacity(0.75), in: RoundedRectangle(cornerRadius: 12))
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
                Spacer()
                Text(model.talking ? "Listening…" : "Hold to talk")
                    .font(.headline)
                    .foregroundStyle(.white)
                    .frame(maxWidth: .infinity, minHeight: 64)
                    .background(model.talking ? Verdict.do_not_touch.color : Theme.accent, in: RoundedRectangle(cornerRadius: 16))
                    .gesture(DragGesture(minimumDistance: 0)
                        .onChanged { _ in model.beginTalk() }
                        .onEnded { _ in model.endTalk() })
                    .accessibilityAddTraits(.isButton)
            }
            .padding(16)
            .animation(.default, value: model.toasts)
        }
        .onAppear { model.startLive() }
        .onDisappear { model.stopLive() }
    }

    private var status: String {
        let link = model.connected ? "connected" : "offline"
        let pair = model.active?.displayName ?? "unpaired"
        return "\(link) · \(pair) · \(model.sentFPS)/\(model.fps) fps"
    }
}
