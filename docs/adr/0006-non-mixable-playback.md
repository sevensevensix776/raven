# ADR 0006: Use a Non-Mixable Playback Audio Session

## Status

Accepted.

## Context

Raven is used through the car audio route. It needs to become the active Now Playing app so CarPlay and steering-wheel Play/Pause controls operate on the live stream. V1 used ducking in a way that quieted music for an entire drive.

Mixing Raven with other media would make route ownership and remote-command behavior less deterministic. Ducking is particularly ill-suited to an endless HLS stream because the session remains active even when Raven is emitting only its idle floor.

## Decision

Configure `AVAudioSession` as non-mixable `.playback` with no `.mixWithOthers` and no `.duckOthers`. While Raven is playing, Claude owns the car audio route. Publish Now Playing metadata and Play/Pause remote commands.

## Consequences

- Raven is the Now Playing app and can use CarPlay and steering-wheel controls.
- Other audio is not left permanently ducked beneath Raven's endless stream.
- Starting Spotify or another media app can interrupt or take Raven's audio session.
- Raven must rebuild or resume its player after interruptions and route changes when playback is still wanted.
- The user cannot expect continuous mixed music underneath Claude narration.

## Alternatives considered

- **`.duckOthers`.** Rejected because the endless stream can suppress other audio for the whole drive; v1 demonstrated this failure.
- **`.mixWithOthers`.** Rejected because Raven would not reliably own Now Playing and the car controls.
- **Deactivate the session during idle gaps.** Rejected because idle continuity is load-bearing for background survival and first-word delivery; see [ADR 0003](0003-continuous-hls-comfort-noise.md).

See `PlaybackController.swift` in `ios/` and [`../TRADEOFFS.md`](../TRADEOFFS.md).
