import SwiftUI

@main
struct EarApp: App {
    var body: some Scene {
        WindowGroup { RootView() }
    }
}

private enum Palette {
    static let gold = Color(red: 0.91, green: 0.72, blue: 0.24)
    static let bgTop = Color(red: 0.09, green: 0.11, blue: 0.16)
    static let bgBottom = Color(red: 0.03, green: 0.04, blue: 0.06)
    static let panel = Color.white.opacity(0.055)
}

struct RootView: View {
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var playback = PlaybackController()
    @StateObject private var api = HuginnAPI()
    @State private var showingChannels = false

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [Palette.bgTop, Palette.bgBottom],
                startPoint: .top,
                endPoint: .bottom
            )
            .ignoresSafeArea()

            VStack(spacing: 0) {
                header
                transcript
                statusLine
                transport
            }
        }
        .preferredColorScheme(.dark)
        .sheet(isPresented: $showingChannels) {
            ChannelPicker(api: api)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
        }
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            await api.refreshChannels()
            await api.refreshTranscript()
            await api.uploadLog()
            var tick = 0
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard !Task.isCancelled else { return }
                await api.refreshTranscript()
                tick += 1
                if tick.isMultiple(of: 2) {
                    await api.refreshChannels()
                }
                if tick.isMultiple(of: 6) {  // ~30s: ship new playback-log bytes
                    await api.uploadLog()
                }
            }
        }
    }

    private var header: some View {
        HStack(spacing: 12) {
            Image("Raven")
                .resizable()
                .scaledToFit()
                .frame(width: 44, height: 44)
                .clipShape(RoundedRectangle(cornerRadius: 11, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: 11, style: .continuous)
                        .strokeBorder(Palette.gold.opacity(0.28), lineWidth: 1)
                )

            VStack(alignment: .leading, spacing: 2) {
                Text("Raven")
                    .font(.title2.weight(.semibold))
                    .foregroundStyle(.white)
                Text(api.channelLabel)
                    .font(.subheadline.weight(.medium))
                    .foregroundStyle(Palette.gold)
                    .lineLimit(1)
            }

            Spacer()
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 12)
    }

    private var transcript: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 18) {
                    if api.transcript.isEmpty {
                        ContentUnavailableView(
                            "Nothing spoken yet",
                            systemImage: "text.bubble",
                            description: Text("Replies will appear here as Raven speaks them.")
                        )
                        .foregroundStyle(.white.opacity(0.55))
                        .frame(maxWidth: .infinity)
                        .padding(.top, 80)
                    } else {
                        ForEach(api.transcript) { line in
                            TranscriptRow(line: line)
                                .id(line.id)
                        }
                    }
                }
                .padding(.horizontal, 18)
                .padding(.vertical, 16)
            }
            .background(Palette.panel)
            .clipShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
            .padding(.horizontal, 12)
            .onChange(of: api.transcript.last?.id) { _, id in
                guard let id else { return }
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo(id, anchor: .bottom)
                }
            }
        }
    }

    private var statusLine: some View {
        HStack(spacing: 6) {
            if let error = api.errorText {
                Text(error)
            } else {
                Text(playback.statusText)
            }
        }
        .font(.caption2.monospaced())
        .foregroundStyle(.white.opacity(0.38))
        .lineLimit(1)
        .padding(.horizontal, 18)
        .padding(.vertical, 8)
    }

    private var transport: some View {
        HStack(spacing: 14) {
            Button(action: playback.start) {
                HStack(spacing: 9) {
                    Circle()
                        .fill(playback.isLive ? Color.green : Palette.gold)
                        .frame(width: 12, height: 12)
                    Text(playback.isLive ? "LIVE" : (playback.startButtonEnabled ? "START" : "CONNECTING"))
                        .font(.headline.weight(.bold))
                }
                .foregroundStyle(.white)
                .frame(maxWidth: .infinity, minHeight: 58)
                .background(Palette.panel)
                .clipShape(Capsule())
                .overlay(Capsule().strokeBorder(Palette.gold.opacity(0.35)))
            }
            .buttonStyle(.plain)
            .disabled(!playback.startButtonEnabled)
            .accessibilityLabel(playback.isLive ? "Stream live" : "Start stream")

            TransportButton(
                symbol: playback.isMuted ? "speaker.slash.fill" : "speaker.wave.2.fill",
                label: playback.isMuted ? "Unmute" : "Mute",
                selected: playback.isMuted,
                action: playback.toggleMute
            )

            TransportButton(
                symbol: "list.bullet",
                label: "Channels",
                selected: showingChannels
            ) {
                showingChannels = true
            }
        }
        .padding(.horizontal, 18)
        .padding(.bottom, 10)
    }
}

private struct TranscriptRow: View {
    let line: SpokenLine

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack {
                Text(line.project.isEmpty ? "Claude" : line.project)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(Palette.gold)
                Spacer()
                Text(Date(timeIntervalSince1970: line.spokenAtEpoch), style: .relative)
                    .font(.caption2)
                    .foregroundStyle(.white.opacity(0.35))
            }
            Text(line.text)
                .font(.title3.weight(.regular))
                .foregroundStyle(.white.opacity(0.94))
                .lineSpacing(5)
                .textSelection(.enabled)
        }
        .padding(.bottom, 2)
    }
}

private struct TransportButton: View {
    let symbol: String
    let label: String
    var selected = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 22, weight: .semibold))
                .foregroundStyle(selected ? Palette.bgBottom : Palette.gold)
                .frame(width: 58, height: 58)
                .background(selected ? Palette.gold : Palette.panel)
                .clipShape(Circle())
                .overlay(Circle().strokeBorder(Palette.gold.opacity(0.35)))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
    }
}

private struct ChannelPicker: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject var api: HuginnAPI

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Button {
                        Task {
                            await api.followActiveSession()
                            dismiss()
                        }
                    } label: {
                        ChannelLabel(
                            title: "Follow active session",
                            subtitle: "Switch when you submit a prompt",
                            selected: api.selectionMode == "follow"
                        )
                    }
                }

                Section("Pin one session") {
                    ForEach(api.channels) { channel in
                        Button {
                            Task {
                                await api.pin(channel)
                                dismiss()
                            }
                        } label: {
                            ChannelLabel(
                                title: channel.project.isEmpty ? "Unknown project" : channel.project,
                                subtitle: channel.lastLine.isEmpty ? "No recent text" : channel.lastLine,
                                detail: Date(timeIntervalSince1970: channel.lastActiveEpoch),
                                selected: api.selectionMode == "pinned" && api.selectedSessionID == channel.sessionID
                            )
                        }
                    }
                }
            }
            .navigationTitle("Channels")
            .navigationBarTitleDisplayMode(.inline)
            .task { await api.refreshChannels() }   // fetch fresh the instant the sheet opens
            .refreshable { await api.refreshChannels() }
            .overlay {
                if api.channels.isEmpty {
                    ContentUnavailableView(
                        "No active sessions",
                        systemImage: "bubble.left.and.bubble.right",
                        description: Text("Talk to a Claude Code session and it will appear here.")
                    )
                }
            }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .preferredColorScheme(.dark)
    }
}

private struct ChannelLabel: View {
    let title: String
    let subtitle: String
    var detail: Date?
    let selected: Bool

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                .font(.title2)
                .foregroundStyle(selected ? Palette.gold : .secondary)
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(title)
                        .font(.headline)
                        .foregroundStyle(.primary)
                    Spacer()
                    if let detail {
                        Text(detail, style: .relative)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Text(subtitle)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
        }
        .contentShape(Rectangle())
        .padding(.vertical, 5)
    }
}
