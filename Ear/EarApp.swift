import SwiftUI

@main
struct EarApp: App {
    @StateObject private var playback = PlaybackController()

    var body: some Scene {
        WindowGroup {
            VStack(spacing: 20) {
                Button("Start") {
                    playback.start()
                }
                .buttonStyle(.borderedProminent)
                .disabled(!playback.startButtonEnabled)

                Text(playback.statusText)
                    .font(.footnote.monospaced())
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
            }
            .padding(24)
        }
    }
}
