# ADR 0002: Use a Native iOS Playback App

## Status

Accepted.

## Context

The original full-duplex concept could not be implemented reliably as a PWA because WebKit mutes background microphone capture and web content cannot declare `UIBackgroundModes: audio` (WebKit bug 226620). Background playback itself has worked since iOS 15.4, so the final voice-out-only scope does not make web playback literally impossible.

Raven nevertheless needs more than foreground playback: a declared background-audio mode, direct control of the audio session, live-edge seeking, indefinite retry after stalls and route changes, Now Playing integration, and observable playback progress. A backgrounded browser tab offers less control over those behaviors.

## Decision

Build a native SwiftUI iPhone app. Use `AVPlayer` for the live HLS stream, `AVAudioSession` with the `audio` background mode, `MPRemoteCommandCenter` for Play/Pause, and explicit retry and stall recovery.

The internal target and product remain `Ear`; the user-facing display name is Raven.

## Consequences

- Raven can remain eligible for background playback with the screen locked.
- The app can seek near the live edge, rebuild the player on failure, observe advancing media time, and integrate with Now Playing and CarPlay controls.
- Development requires an Apple toolchain, signing identity, provisioning, installation, and iOS-specific maintenance.
- Distribution is less convenient than opening a URL.
- Foreground API polling still stops when the SwiftUI scene is inactive; only the HLS audio path is expected to continue in the background.

## Alternatives considered

- **PWA.** Simpler to distribute, and background playback is possible, but it cannot support the original background-capture requirement and provides less reliable control over Raven's playback and recovery lifecycle.
- **Retain full-duplex native v1.** Rejected by [ADR 0001](0001-voice-out-only.md); native code does not make unnecessary microphone ownership desirable.
- **Fork an existing Claude Code mobile wrapper.** Rejected because Raven needs a narrow player, not a second Claude Code client, and wrapper maintenance carries the release-cadence risk observed in Omnara.

See the iPhone repository at `ios/` and the [project history](../HISTORY.md).
