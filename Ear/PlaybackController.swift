import AVFoundation
import Combine
import MediaPlayer
import UIKit

final class PlaybackController: NSObject, ObservableObject {
    @Published private(set) var statusText = "Ready"
    @Published private(set) var startButtonEnabled = true
    @Published private(set) var isLive = false
    @Published private(set) var isMuted = false

    private let streamURL = URL(string: "http://100.64.0.1:8080/stream.m3u8")!
    private let session = AVAudioSession.sharedInstance()
    private let defaults = UserDefaults.standard

    private var player: AVPlayer?
    private var item: AVPlayerItem?
    private var playerStatusObservation: NSKeyValueObservation?
    private var itemStatusObservation: NSKeyValueObservation?
    private var keepUpObservation: NSKeyValueObservation?
    private var timeObserver: Any?
    private var retryWorkItem: DispatchWorkItem?
    private var stallWorkItem: DispatchWorkItem?
    private var routeRecoveryWorkItem: DispatchWorkItem?

    private var wantsPlayback = false
    private var isInterrupted = false
    private var retryAttempt = 0
    private var generation = UUID()
    private var lastObservedMediaTime: Double?
    private var lastProgressUptime: TimeInterval = 0
    private var healthySinceUptime: TimeInterval?
    private var lastHeartbeatWriteUptime: TimeInterval = 0
    private var hasStartedPlayingCurrentItem = false

    private lazy var logURL: URL = {
        FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("EarPlayback.log")
    }()

    override init() {
        super.init()
        prepareLog()
        registerNotifications()
        configureRemoteCommands()
        updateReadyStatus()
        log("APP_LAUNCHED")
    }

    deinit {
        NotificationCenter.default.removeObserver(self)
        removePlayer()
        let commands = MPRemoteCommandCenter.shared()
        commands.playCommand.removeTarget(self)
        commands.pauseCommand.removeTarget(self)
    }

    func start() {
        runOnMain { [weak self] in
            guard let self else { return }
            self.wantsPlayback = true
            self.isInterrupted = false
            self.startButtonEnabled = false
            self.retryAttempt = 0
            self.log("START_REQUESTED")
            self.rebuildPlayer(reason: "user start")
        }
    }

    func toggleMute() {
        runOnMain { [weak self] in
            guard let self else { return }
            self.isMuted.toggle()
            self.player?.isMuted = self.isMuted
            self.log("MUTE_CHANGED muted=\(self.isMuted)")
        }
    }

    private func rebuildPlayer(reason: String) {
        dispatchPrecondition(condition: .onQueue(.main))
        guard wantsPlayback, !isInterrupted else { return }

        routeRecoveryWorkItem?.cancel()
        routeRecoveryWorkItem = nil
        cancelRetry()
        cancelStallWatchdog()
        removePlayer()

        do {
            try activateAudioSession()
        } catch {
            log("SESSION_ACTIVATION_FAILED reason=\(quoted(error.localizedDescription))")
            scheduleRetry(reason: "audio session activation failed")
            return
        }

        generation = UUID()
        let thisGeneration = generation
        let newItem = AVPlayerItem(url: streamURL)
        newItem.preferredForwardBufferDuration = 0
        let newPlayer = AVPlayer(playerItem: newItem)
        newPlayer.automaticallyWaitsToMinimizeStalling = true
        newPlayer.isMuted = isMuted

        item = newItem
        player = newPlayer
        observe(newPlayer, item: newItem, generation: thisGeneration)
        installTimeObserver(on: newPlayer, generation: thisGeneration)
        updateNowPlaying(rate: 0)
        setStatus("Connecting")
        log("PLAYER_CREATED reason=\(quoted(reason))")
    }

    private func activateAudioSession() throws {
        // Non-mixable by choice: Claude owns the car audio for the drive. This is what
        // makes us the Now Playing app (per WWDC19/501: one remote command + a
        // non-mixable session), which is what routes to CarPlay and lights up the
        // steering-wheel controls. Cost: starting Spotify takes the session from us.
        // Do NOT add .duckOthers here. Huginn owns the route while its live stream is
        // playing; ducking would suppress the driver's other audio for the whole drive.
        try session.setCategory(.playback, mode: .default, options: [])
        if #available(iOS 17.0, *) {
            // This product deliberately follows CarPlay onto the next route instead of pausing on disconnect.
            try session.setPrefersInterruptionOnRouteDisconnect(false)
        }
        try session.setActive(true)
        log("SESSION_ACTIVE route=\(quoted(routeDescription()))")
    }

    private func observe(_ observedPlayer: AVPlayer, item observedItem: AVPlayerItem, generation: UUID) {
        playerStatusObservation = observedPlayer.observe(\.timeControlStatus, options: [.initial, .new]) { [weak self, weak observedPlayer] _, _ in
            self?.runOnMain {
                guard let self, let observedPlayer,
                      self.generation == generation, self.player === observedPlayer else { return }
                self.handleTimeControlStatus(observedPlayer)
            }
        }

        itemStatusObservation = observedItem.observe(\.status, options: [.initial, .new]) { [weak self, weak observedItem] _, _ in
            self?.runOnMain {
                guard let self, let observedItem,
                      self.generation == generation, self.item === observedItem else { return }
                self.handleItemStatus(observedItem)
            }
        }

        keepUpObservation = observedItem.observe(\.isPlaybackLikelyToKeepUp, options: [.initial, .new]) { [weak self, weak observedItem] _, _ in
            self?.runOnMain {
                guard let self, let observedItem,
                      self.generation == generation, self.item === observedItem else { return }
                if observedItem.isPlaybackLikelyToKeepUp {
                    self.log("LIKELY_TO_KEEP_UP true")
                } else if observedItem.status == .readyToPlay && self.wantsPlayback {
                    self.log("LIKELY_TO_KEEP_UP false")
                    self.armStallWatchdog(reason: "buffer not keeping up")
                }
            }
        }

        NotificationCenter.default.addObserver(
            self,
            selector: #selector(itemFailedToEnd(_:)),
            name: AVPlayerItem.failedToPlayToEndTimeNotification,
            object: observedItem
        )
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(itemStalled(_:)),
            name: AVPlayerItem.playbackStalledNotification,
            object: observedItem
        )
    }

    private func handleItemStatus(_ observedItem: AVPlayerItem) {
        switch observedItem.status {
        case .unknown:
            setStatus("Connecting")
        case .readyToPlay:
            log("ITEM_READY")
            armStallWatchdog(reason: "play did not start")
            playAtLiveEdge()
        case .failed:
            let detail = observedItem.error?.localizedDescription ?? "unknown error"
            log("ITEM_FAILED error=\(quoted(detail))")
            scheduleRetry(reason: "player item failed")
        @unknown default:
            log("ITEM_STATUS_UNKNOWN raw=\(observedItem.status.rawValue)")
            scheduleRetry(reason: "unknown player item status")
        }
    }

    private func playAtLiveEdge() {
        guard wantsPlayback, !isInterrupted, let player, let item else { return }

        let ranges = item.seekableTimeRanges.map(\.timeRangeValue).filter { $0.isValid && !$0.isEmpty }
        guard let range = ranges.last else {
            log("LIVE_EDGE range=unavailable action=play")
            player.play()
            return
        }

        let edge = CMTimeRangeGetEnd(range)
        let oneSecond = CMTime(seconds: 1, preferredTimescale: 600)
        let candidate = CMTimeSubtract(edge, oneSecond)
        let target = CMTimeCompare(candidate, range.start) >= 0 ? candidate : range.start
        log("LIVE_EDGE seek=\(formatTime(target.seconds)) edge=\(formatTime(edge.seconds))")
        player.seek(
            to: target,
            toleranceBefore: CMTime(seconds: 0.5, preferredTimescale: 600),
            toleranceAfter: CMTime(seconds: 0.5, preferredTimescale: 600)
        ) { [weak self, weak player] finished in
            self?.runOnMain {
                guard let self, let player, self.player === player, self.wantsPlayback, !self.isInterrupted else { return }
                self.log("LIVE_EDGE_SEEK_COMPLETED finished=\(finished)")
                player.play()
            }
        }
    }

    private func handleTimeControlStatus(_ observedPlayer: AVPlayer) {
        guard wantsPlayback else { return }

        switch observedPlayer.timeControlStatus {
        case .playing:
            isLive = true
            hasStartedPlayingCurrentItem = true
            cancelRetry()
            cancelStallWatchdog()
            if healthySinceUptime == nil {
                healthySinceUptime = ProcessInfo.processInfo.systemUptime
            }
            setStatus("Playing")
            updateNowPlaying(rate: 1)
            log("PLAYING_OBSERVED route=\(quoted(routeDescription()))")
        case .waitingToPlayAtSpecifiedRate:
            isLive = false
            healthySinceUptime = nil
            let reason = observedPlayer.reasonForWaitingToPlay?.rawValue ?? "unknown"
            setStatus("Waiting for stream")
            updateNowPlaying(rate: 0)
            log("WAITING_OBSERVED reason=\(quoted(reason))")
            armStallWatchdog(reason: "player waiting")
        case .paused:
            isLive = false
            healthySinceUptime = nil
            updateNowPlaying(rate: 0)
            if isInterrupted {
                setStatus("Interrupted")
            } else if item?.status == .readyToPlay && hasStartedPlayingCurrentItem {
                setStatus("Recovering")
                log("UNEXPECTED_PAUSE_OBSERVED")
                scheduleRetry(reason: "unexpected pause")
            } else if item?.status == .readyToPlay {
                setStatus("Starting")
                armStallWatchdog(reason: "play did not start")
            }
        @unknown default:
            healthySinceUptime = nil
            log("TIME_CONTROL_UNKNOWN")
            scheduleRetry(reason: "unknown time control state")
        }
    }

    private func installTimeObserver(on observedPlayer: AVPlayer, generation: UUID) {
        timeObserver = observedPlayer.addPeriodicTimeObserver(
            forInterval: CMTime(seconds: 15, preferredTimescale: 600),
            queue: .main
        ) { [weak self, weak observedPlayer] time in
            guard let self, let observedPlayer,
                  self.generation == generation, self.player === observedPlayer,
                  observedPlayer.timeControlStatus == .playing else { return }

            let seconds = time.seconds
            guard seconds.isFinite else { return }
            let uptime = ProcessInfo.processInfo.systemUptime

            if let previous = self.lastObservedMediaTime, seconds > previous + 1 {
                self.lastProgressUptime = uptime
                if uptime - self.lastHeartbeatWriteUptime >= 60 {
                    self.lastHeartbeatWriteUptime = uptime
                    let likely = self.item?.isPlaybackLikelyToKeepUp ?? false
                    self.writePlaybackProgress(mediaTime: seconds, likelyToKeepUp: likely)
                }
            }
            self.lastObservedMediaTime = seconds

            if let healthySince = self.healthySinceUptime, uptime - healthySince >= 60, self.retryAttempt != 0 {
                self.retryAttempt = 0
                self.log("RETRY_BACKOFF_RESET observed_playback_seconds=60")
            }
        }
    }

    private func writePlaybackProgress(mediaTime: Double, likelyToKeepUp: Bool) {
        let date = Date()
        defaults.set(date.timeIntervalSince1970, forKey: "lastPlaybackProgress")
        log(
            "PLAYBACK_PROGRESS media_time=\(formatTime(mediaTime)) " +
            "likely_to_keep_up=\(likelyToKeepUp) route=\(quoted(routeDescription()))"
        )
        setStatus("Playing • proved \(shortDate(date))")
    }

    private func armStallWatchdog(reason: String) {
        guard wantsPlayback, !isInterrupted, stallWorkItem == nil else { return }
        let generation = self.generation
        let progressAtArm = lastProgressUptime
        let work = DispatchWorkItem { [weak self] in
            guard let self, self.generation == generation, self.wantsPlayback, !self.isInterrupted else { return }
            self.stallWorkItem = nil
            if self.player?.timeControlStatus == .playing && self.lastProgressUptime > progressAtArm {
                return
            }
            self.log("STALL_WATCHDOG_FIRED reason=\(self.quoted(reason))")
            self.scheduleRetry(reason: reason)
        }
        stallWorkItem = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 20, execute: work)
    }

    private func scheduleRetry(reason: String) {
        guard wantsPlayback, !isInterrupted, retryWorkItem == nil else { return }
        cancelStallWatchdog()

        let delay = min(30.0, pow(2.0, Double(min(retryAttempt, 5))))
        retryAttempt += 1
        let work = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.retryWorkItem = nil
            self.rebuildPlayer(reason: "retry: \(reason)")
        }
        retryWorkItem = work
        player?.pause()
        setStatus("Retrying in \(Int(delay))s")
        log("RETRY_SCHEDULED delay=\(Int(delay)) attempt=\(retryAttempt) reason=\(quoted(reason))")
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: work)
    }

    private func cancelRetry() {
        retryWorkItem?.cancel()
        retryWorkItem = nil
    }

    private func cancelStallWatchdog() {
        stallWorkItem?.cancel()
        stallWorkItem = nil
    }

    private func removePlayer() {
        isLive = false
        playerStatusObservation?.invalidate()
        itemStatusObservation?.invalidate()
        keepUpObservation?.invalidate()
        playerStatusObservation = nil
        itemStatusObservation = nil
        keepUpObservation = nil

        if let timeObserver, let player {
            player.removeTimeObserver(timeObserver)
        }
        timeObserver = nil

        if let item {
            NotificationCenter.default.removeObserver(self, name: AVPlayerItem.failedToPlayToEndTimeNotification, object: item)
            NotificationCenter.default.removeObserver(self, name: AVPlayerItem.playbackStalledNotification, object: item)
        }

        player?.pause()
        player = nil
        item = nil
        lastObservedMediaTime = nil
        lastProgressUptime = 0
        healthySinceUptime = nil
        hasStartedPlayingCurrentItem = false
    }

    private func registerNotifications() {
        let center = NotificationCenter.default
        center.addObserver(self, selector: #selector(interruption(_:)), name: AVAudioSession.interruptionNotification, object: session)
        center.addObserver(self, selector: #selector(routeChanged(_:)), name: AVAudioSession.routeChangeNotification, object: session)
        center.addObserver(self, selector: #selector(mediaServicesReset(_:)), name: AVAudioSession.mediaServicesWereResetNotification, object: session)
        center.addObserver(self, selector: #selector(appBecameActive(_:)), name: UIApplication.didBecomeActiveNotification, object: nil)
    }

    @objc private func interruption(_ notification: Notification) {
        runOnMain { [weak self] in
            guard let self,
                  let rawType = notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt,
                  let type = AVAudioSession.InterruptionType(rawValue: rawType) else { return }

            switch type {
            case .began:
                self.isInterrupted = true
                self.cancelRetry()
                self.cancelStallWatchdog()
                self.setStatus("Interrupted")
                self.updateNowPlaying(rate: 0)
                self.log("INTERRUPTION_BEGAN")
            case .ended:
                let rawOptions = notification.userInfo?[AVAudioSessionInterruptionOptionKey] as? UInt
                let shouldResume = rawOptions.map {
                    AVAudioSession.InterruptionOptions(rawValue: $0).contains(.shouldResume)
                }
                self.isInterrupted = false
                self.log("INTERRUPTION_ENDED should_resume_hint=\(shouldResume.map(String.init) ?? "absent")")
                // User intent is "play forever"; a missing or false hint does not cancel that intent.
                if self.wantsPlayback {
                    self.rebuildPlayer(reason: "interruption ended")
                }
            @unknown default:
                self.log("INTERRUPTION_TYPE_UNKNOWN raw=\(rawType)")
            }
        }
    }

    @objc private func routeChanged(_ notification: Notification) {
        runOnMain { [weak self] in
            guard let self else { return }
            let rawReason = notification.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt
            let reason = rawReason.flatMap(AVAudioSession.RouteChangeReason.init(rawValue:))
            self.log("ROUTE_CHANGED reason=\(reason.map { String($0.rawValue) } ?? "unknown") route=\(self.quoted(self.routeDescription()))")

            guard self.wantsPlayback, !self.isInterrupted,
                  reason != .categoryChange else { return }
            self.routeRecoveryWorkItem?.cancel()
            let work = DispatchWorkItem { [weak self] in
                guard let self, self.wantsPlayback, !self.isInterrupted else { return }
                self.rebuildPlayer(reason: "route changed")
            }
            self.routeRecoveryWorkItem = work
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.75, execute: work)
        }
    }

    @objc private func mediaServicesReset(_ notification: Notification) {
        runOnMain { [weak self] in
            guard let self else { return }
            self.log("MEDIA_SERVICES_RESET")
            self.removePlayer()
            if self.wantsPlayback {
                self.rebuildPlayer(reason: "media services reset")
            }
        }
    }

    @objc private func itemFailedToEnd(_ notification: Notification) {
        runOnMain { [weak self] in
            guard let self, notification.object as? AVPlayerItem === self.item else { return }
            let error = notification.userInfo?[AVPlayerItemFailedToPlayToEndTimeErrorKey] as? Error
            self.log("FAILED_TO_PLAY_TO_END error=\(self.quoted(error?.localizedDescription ?? "unknown"))")
            self.scheduleRetry(reason: "failed to play to end")
        }
    }

    @objc private func itemStalled(_ notification: Notification) {
        runOnMain { [weak self] in
            guard let self, notification.object as? AVPlayerItem === self.item else { return }
            self.log("PLAYBACK_STALLED_NOTIFICATION")
            self.armStallWatchdog(reason: "playback stalled")
        }
    }

    @objc private func appBecameActive(_ notification: Notification) {
        runOnMain { [weak self] in
            guard let self else { return }
            self.log("APP_BECAME_ACTIVE")
            if self.wantsPlayback,
               self.player?.timeControlStatus != .playing,
               !self.isInterrupted {
                self.rebuildPlayer(reason: "app became active while not playing")
            } else if !self.wantsPlayback {
                self.updateReadyStatus()
            }
        }
    }

    private func configureRemoteCommands() {
        let commands = MPRemoteCommandCenter.shared()
        commands.playCommand.isEnabled = true
        commands.pauseCommand.isEnabled = true

        commands.playCommand.addTarget(self, action: #selector(remotePlay(_:)))
        commands.pauseCommand.addTarget(self, action: #selector(remotePause(_:)))
    }

    @objc private func remotePlay(_ event: MPRemoteCommandEvent) -> MPRemoteCommandHandlerStatus {
        runOnMain { [weak self] in
            guard let self else { return }
            self.log("REMOTE_PLAY_REQUESTED")
            self.wantsPlayback = true
            self.startButtonEnabled = false
            self.isInterrupted = false
            self.rebuildPlayer(reason: "remote play")
        }
        return .success
    }

    @objc private func remotePause(_ event: MPRemoteCommandEvent) -> MPRemoteCommandHandlerStatus {
        runOnMain { [weak self] in
            guard let self else { return }
            self.log("REMOTE_PAUSE_REQUESTED")
            self.wantsPlayback = false
            self.startButtonEnabled = true
            self.cancelRetry()
            self.cancelStallWatchdog()
            self.player?.pause()
            self.setStatus("Paused")
            self.updateNowPlaying(rate: 0)
            do {
                try self.session.setActive(false, options: .notifyOthersOnDeactivation)
                self.log("SESSION_DEACTIVATION_CALL_RETURNED")
            } catch {
                self.log("SESSION_DEACTIVATION_FAILED error=\(self.quoted(error.localizedDescription))")
            }
        }
        return .success
    }

    private func updateNowPlaying(rate: Float) {
        MPNowPlayingInfoCenter.default().nowPlayingInfo = [
            MPMediaItemPropertyTitle: "Ear",
            MPMediaItemPropertyArtist: "Live from Mac",
            MPNowPlayingInfoPropertyIsLiveStream: true,
            MPNowPlayingInfoPropertyPlaybackRate: rate,
            MPNowPlayingInfoPropertyMediaType: MPNowPlayingInfoMediaType.audio.rawValue
        ]
    }

    private func prepareLog() {
        let manager = FileManager.default
        if !manager.fileExists(atPath: logURL.path) {
            manager.createFile(atPath: logURL.path, contents: nil)
        }
        do {
            try manager.setAttributes(
                [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                ofItemAtPath: logURL.path
            )
        } catch {
            statusText = "Log setup failed: \(error.localizedDescription)"
        }
    }

    private func log(_ message: String) {
        let line = "\(Self.iso8601.string(from: Date())) \(message)\n"
        guard let data = line.data(using: .utf8) else { return }
        do {
            let handle = try FileHandle(forWritingTo: logURL)
            try handle.seekToEnd()
            try handle.write(contentsOf: data)
            try handle.synchronize()
            try handle.close()
        } catch {
            statusText = "Log write failed: \(error.localizedDescription)"
        }
    }

    private func updateReadyStatus() {
        let timestamp = defaults.double(forKey: "lastPlaybackProgress")
        if timestamp > 0 {
            statusText = "Ready • last proved \(shortDate(Date(timeIntervalSince1970: timestamp)))"
        } else {
            statusText = "Ready • no playback proof yet"
        }
        startButtonEnabled = true
    }

    private func setStatus(_ value: String) {
        let timestamp = defaults.double(forKey: "lastPlaybackProgress")
        if timestamp > 0, value != "Playing" {
            statusText = "\(value) • last proved \(shortDate(Date(timeIntervalSince1970: timestamp)))"
        } else {
            statusText = value
        }
    }

    private func routeDescription() -> String {
        let outputs = session.currentRoute.outputs
        guard !outputs.isEmpty else { return "no output" }
        return outputs.map { "\($0.portType.rawValue):\($0.portName)" }.joined(separator: ",")
    }

    private func runOnMain(_ block: @escaping () -> Void) {
        if Thread.isMainThread {
            block()
        } else {
            DispatchQueue.main.async(execute: block)
        }
    }

    private func quoted(_ value: String) -> String {
        "\"\(value.replacingOccurrences(of: "\"", with: "'"))\""
    }

    private func formatTime(_ seconds: Double) -> String {
        guard seconds.isFinite else { return "nan" }
        return String(format: "%.3f", seconds)
    }

    private func shortDate(_ date: Date) -> String {
        Self.shortDate.string(from: date)
    }

    private static let iso8601 = ISO8601DateFormatter()
    private static let shortDate: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .none
        formatter.timeStyle = .medium
        return formatter
    }()
}
