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

private struct TranscriptLineKey: Hashable {
    let sessionID: String
    let text: String

    init(_ line: SpokenLine) {
        sessionID = line.sessionID
        text = line.text
    }
}

struct RootView: View {
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var playback = PlaybackController()
    @StateObject private var api = HuginnAPI()
    @State private var showingChannels = false
    @State private var nearBottom = true

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
        let lines = displayedTranscript

        return ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 18) {
                    if lines.isEmpty {
                        ContentUnavailableView(
                            "Nothing spoken yet",
                            systemImage: "text.bubble",
                            description: Text("Replies will appear here as Raven speaks them.")
                        )
                        .foregroundStyle(.white.opacity(0.55))
                        .frame(maxWidth: .infinity)
                        .padding(.top, 80)
                    } else {
                        ForEach(Array(lines.enumerated()), id: \.element.id) { index, line in
                            VStack(alignment: .leading, spacing: 10) {
                                if index == 0 || lines[index - 1].sessionID != line.sessionID {
                                    SessionDivider(project: line.project)
                                }

                                TranscriptRow(line: line)
                                    .opacity(line.isCatchup ? 0.72 : 1)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
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
            // Track how far the last row is from the viewport bottom, so we only
            // auto-scroll when the user is already near the bottom (never yank
            // them while they're reading history) and can show a jump button.
            .onScrollGeometryChange(for: CGFloat.self) { geo in
                geo.contentSize.height - geo.contentOffset.y - geo.containerSize.height
            } action: { _, distanceFromBottom in
                nearBottom = distanceFromBottom < 120
            }
            .overlay(alignment: .bottomTrailing) {
                if !nearBottom && !lines.isEmpty {
                    Button {
                        scrollToBottom(proxy, lines)
                    } label: {
                        Image(systemName: "chevron.down")
                            .font(.system(size: 18, weight: .bold))
                            .foregroundStyle(Palette.bgBottom)
                            .frame(width: 44, height: 44)
                            .background(Palette.gold)
                            .clipShape(Circle())
                            .shadow(color: .black.opacity(0.4), radius: 6, y: 2)
                    }
                    .padding(.trailing, 26)
                    .padding(.bottom, 16)
                    .transition(.scale.combined(with: .opacity))
                    .accessibilityLabel("Scroll to latest")
                }
            }
            .onChange(of: lines.last?.id) { _, id in
                guard let id, nearBottom else { return } // don't interrupt reading
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo(id, anchor: .bottom)
                }
            }
            .onChange(of: showingChannels) { _, showing in
                // Jump to latest whenever the sheet closes (i.e. on connect/open).
                if !showing { scrollToBottom(proxy, lines) }
            }
            .task(id: api.selectedSessionID) {
                // On opening / switching a session, land at the newest line.
                scrollToBottom(proxy, lines)
            }
        }
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy, _ lines: [SpokenLine]) {
        guard let last = lines.last?.id else { return }
        withAnimation(.easeOut(duration: 0.25)) {
            proxy.scrollTo(last, anchor: .bottom)
        }
        nearBottom = true
    }

    private var displayedTranscript: [SpokenLine] {
        let transcriptKeys = Set(api.transcript.map(TranscriptLineKey.init))
        var catchupKeys: Set<TranscriptLineKey> = []
        let uniqueCatchup = api.catchup.filter { line in
            let key = TranscriptLineKey(line)
            return !transcriptKeys.contains(key) && catchupKeys.insert(key).inserted
        }

        return (uniqueCatchup + api.transcript).sorted { lhs, rhs in
            if lhs.spokenAtEpoch == rhs.spokenAtEpoch {
                return lhs.id < rhs.id
            }
            return lhs.spokenAtEpoch < rhs.spokenAtEpoch
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
        if line.isUser {
            // Your turn — right-aligned, dimmed. Shown on screen, never spoken.
            VStack(alignment: .trailing, spacing: 5) {
                HStack(spacing: 6) {
                    Spacer()
                    Text("You")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.white.opacity(0.5))
                    Text(Date(timeIntervalSince1970: line.spokenAtEpoch), style: .relative)
                        .font(.caption2)
                        .foregroundStyle(.white.opacity(0.3))
                }
                Text(line.text)
                    .font(.body)
                    .foregroundStyle(.white.opacity(0.62))
                    .multilineTextAlignment(.trailing)
                    .frame(maxWidth: .infinity, alignment: .trailing)
                    .textSelection(.enabled)
            }
            .padding(.leading, 44)
            .padding(.bottom, 2)
        } else {
            // Claude's reply — left, gold header, bright.
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
                TranscriptMarkdown(source: line.display ?? line.text)
                    .foregroundStyle(.white.opacity(0.94))
                    .textSelection(.enabled)
            }
            .padding(.trailing, 44)
            .padding(.bottom, 2)
        }
    }
}

private struct TranscriptMarkdown: View {
    let source: String

    private var blocks: [TranscriptMarkdownBlock] {
        TranscriptMarkdownBlock.parse(source)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .paragraph(let text):
                    Text(inlineMarkdown(text))
                        .font(.title3.weight(.regular))
                        .lineSpacing(5)

                case .heading(let level, let text):
                    Text(inlineMarkdown(text))
                        .font(headingFont(level).weight(.bold))
                        .lineSpacing(4)

                case .list(let items):
                    VStack(alignment: .leading, spacing: 7) {
                        ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                            HStack(alignment: .firstTextBaseline, spacing: 9) {
                                Text(item.marker)
                                    .font(.title3.monospacedDigit().weight(.semibold))
                                    .foregroundStyle(Palette.gold.opacity(0.9))
                                    .frame(minWidth: 22, alignment: .trailing)
                                Text(inlineMarkdown(item.text))
                                    .font(.title3.weight(.regular))
                                    .lineSpacing(5)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                        }
                    }

                case .code:
                    Text("[code]")
                        .font(.subheadline.monospaced().weight(.semibold))
                        .foregroundStyle(.white.opacity(0.58))
                        .padding(.horizontal, 10)
                        .padding(.vertical, 6)
                        .background(.white.opacity(0.08), in: RoundedRectangle(cornerRadius: 7))
                        .overlay(
                            RoundedRectangle(cornerRadius: 7)
                                .strokeBorder(.white.opacity(0.12), lineWidth: 0.5)
                        )
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func inlineMarkdown(_ text: String) -> AttributedString {
        var options = AttributedString.MarkdownParsingOptions()
        options.interpretedSyntax = .inlineOnlyPreservingWhitespace
        return (try? AttributedString(markdown: text, options: options)) ?? AttributedString(text)
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: .title2
        case 2: .title3
        default: .headline
        }
    }
}

private struct TranscriptMarkdownListItem {
    let marker: String
    let text: String

    static func parse(_ rawLine: String) -> Self? {
        let line = rawLine.drop(while: { $0.isWhitespace })
        if line.hasPrefix("- ") || line.hasPrefix("* ") {
            return Self(marker: "•", text: String(line.dropFirst(2)))
        }

        let digits = line.prefix(while: { $0.isNumber })
        guard !digits.isEmpty else { return nil }
        let remainder = line.dropFirst(digits.count)
        guard remainder.hasPrefix(". ") else { return nil }
        return Self(marker: "\(digits).", text: String(remainder.dropFirst(2)))
    }
}

private enum TranscriptMarkdownBlock {
    case paragraph(String)
    case heading(Int, String)
    case list([TranscriptMarkdownListItem])
    case code

    static func parse(_ source: String) -> [Self] {
        let lines = source.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        var blocks: [Self] = []
        var paragraphLines: [String] = []
        var listItems: [TranscriptMarkdownListItem] = []

        func flushParagraph() {
            guard !paragraphLines.isEmpty else { return }
            blocks.append(.paragraph(paragraphLines.joined(separator: "\n")))
            paragraphLines.removeAll(keepingCapacity: true)
        }

        func flushList() {
            guard !listItems.isEmpty else { return }
            blocks.append(.list(listItems))
            listItems.removeAll(keepingCapacity: true)
        }

        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty {
                flushParagraph()
                flushList()
                continue
            }
            if trimmed == "[code]" {
                flushParagraph()
                flushList()
                blocks.append(.code)
                continue
            }
            if let item = TranscriptMarkdownListItem.parse(line) {
                flushParagraph()
                listItems.append(item)
                continue
            }

            flushList()
            if let heading = heading(from: trimmed) {
                flushParagraph()
                blocks.append(.heading(heading.level, heading.text))
            } else {
                paragraphLines.append(line)
            }
        }

        flushParagraph()
        flushList()
        return blocks
    }

    private static func heading(from line: String) -> (level: Int, text: String)? {
        let level = line.prefix(while: { $0 == "#" }).count
        guard (1...6).contains(level) else { return nil }
        let remainder = line.dropFirst(level)
        guard remainder.hasPrefix(" ") else { return nil }
        return (level, String(remainder.dropFirst()))
    }
}

private struct SessionDivider: View {
    let project: String

    var body: some View {
        HStack(spacing: 10) {
            Rectangle()
                .fill(.white.opacity(0.18))
                .frame(height: 0.5)

            Text(project.isEmpty ? "Claude" : project)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.white.opacity(0.78))
                .lineLimit(1)
                .padding(.horizontal, 10)
                .padding(.vertical, 5)
                .background(Palette.gold.opacity(0.12), in: Capsule())
                .overlay(Capsule().strokeBorder(Palette.gold.opacity(0.24), lineWidth: 0.5))

            Rectangle()
                .fill(.white.opacity(0.18))
                .frame(height: 0.5)
        }
        .frame(maxWidth: .infinity)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("Session: \(project.isEmpty ? "Claude" : project)")
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
                                title: channel.displayName,
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
