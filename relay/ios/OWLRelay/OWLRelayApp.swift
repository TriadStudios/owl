import SwiftUI

@main
struct OWLRelayApp: App {
    @StateObject private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(model)
                .tint(Theme.accent)
                .preferredColorScheme(.dark)
                .onAppear { model.start() }
        }
    }
}

struct RootView: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        if model.showPair {
            PairView()
        } else {
            TabView {
                NavigationStack { DashboardView() }
                    .tabItem { Label("Closet", systemImage: "square.grid.3x2") }
                LiveView()
                    .tabItem { Label("Live", systemImage: "camera.viewfinder") }
                NavigationStack { SettingsView() }
                    .tabItem { Label("Settings", systemImage: "gearshape") }
            }
        }
    }
}
