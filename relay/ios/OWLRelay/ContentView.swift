import SwiftUI

struct ContentView: View {
    @StateObject private var model = AppModel()
    @State private var showSettings = false

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            ZStack {
                CameraPreview(session: model.camera.session)
                OverlayView(overlay: model.overlay)
            }
            .aspectRatio(9.0 / 16.0, contentMode: .fit)
            VStack {
                HStack {
                    Text(status)
                        .font(.footnote.monospaced())
                        .padding(8)
                        .background(.black.opacity(0.6), in: RoundedRectangle(cornerRadius: 8))
                        .foregroundStyle(.white)
                    Spacer()
                    Button { showSettings = true } label: {
                        Image(systemName: "gearshape.fill")
                            .font(.title2)
                            .padding(8)
                            .background(.black.opacity(0.6), in: Circle())
                            .foregroundStyle(.white)
                    }
                }
                .padding()
                Spacer()
                Text(model.talking ? "Listening…" : "Hold to talk")
                    .font(.headline)
                    .foregroundStyle(.white)
                    .frame(width: 180, height: 64)
                    .background(model.talking ? Color.red : Color.blue, in: Capsule())
                    .gesture(DragGesture(minimumDistance: 0)
                        .onChanged { _ in model.beginTalk() }
                        .onEnded { _ in model.endTalk() })
                    .padding(.bottom, 32)
            }
        }
        .onAppear { model.start() }
        .sheet(isPresented: $showSettings, onDismiss: model.resumePreview) {
            SettingsView(model: model)
                .onAppear(perform: model.pausePreview)
        }
    }

    private var status: String {
        "\(model.connected ? "connected" : "offline") · \(model.pairing == nil ? "unpaired" : "paired") · \(model.sentFPS)/\(model.fps) fps"
    }
}
