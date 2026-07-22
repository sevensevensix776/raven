# Raven for iPhone

Raven is a native SwiftUI client for listening to Claude Code replies as a live background audio stream. It connects to the Raven service on the Mac over Tailscale, plays the continuous HLS feed through `AVPlayer`, shows the transcript of audio whose emission has begun on the Mac, and lets the driver follow or pin a Claude session. It is voice-out only: prompts still enter through Claude Code Remote Control and iOS dictation.

The app's display name is **Raven**. Its Xcode target and built product are **Ear**, and its default bundle identifier is `com.example.Ear` (override with `PRODUCT_BUNDLE_IDENTIFIER`).

## What the app does

- Plays `http://100.64.0.1:8080/stream.m3u8` near the live edge.
- Uses a non-mixable `.playback` audio session so Raven becomes Now Playing, follows the CarPlay/audio route, and remains eligible for background audio.
- Retries indefinitely after player failures, stalls, interruptions, route changes, and media-services resets.
- Polls `/channels` and `/transcript` with ETags while the app is foregrounded.
- Sends follow/pin changes to `POST /active`.
- Mutes locally with `AVPlayer.isMuted` without stopping the stream.
- Records player evidence in `Documents/EarPlayback.log` and uploads new log bytes to the Mac's `POST /log` endpoint.

## UI

The main view has three deliberately small surfaces:

1. **Header** — Raven artwork, product name, and the current `Following · <project>` or pinned-channel label.
2. **Transcript** — the most recent 50 utterances that the Mac writer began emitting, with project and relative time. It is a playback transcript, not a complete history of every Claude session.
3. **Transport** — **Start/LIVE**, **Mute**, and **Channels**.

The channel sheet offers:

- **Follow active session**, which switches when a new Claude prompt is submitted; and
- **Pin one session**, which keeps the Mac speech gate on a selected session.

The app has no in-app Stop button. System Now Playing and CarPlay expose Play/Pause. Mute is intentionally different from Pause: mute suppresses output while playback and the background HLS connection continue.

## Build, sign, install, and launch

The build is fully command-line driven; Xcode.app supplies the toolchain but the GUI is not part of the workflow. Signing uses your own App Store Connect API key and Apple Developer team (placeholders shown):

| Setting | Value |
|---|---|
| Key file | `/path/to/AuthKey_XXXXXXXXXX.p8` (kept outside the repo) |
| Key ID | `YOUR_KEY_ID` |
| Issuer ID | `YOUR_ISSUER_ID` |
| Development team | `YOUR_TEAM_ID` |
| Bundle ID | `com.example.Ear` |

### One-time setup

Create the two gitignored local files — your Tailscale address and your signing details. Neither ever enters the repository.

```bash
cd ios
cp raven-host.local.example raven-host.local   # -> <your-mac-tailscale-ip>:8080
cp build.local.sh.example  build.local.sh      # device id, bundle id, team, API key
xcrun devicectl list devices                   # to fill in RAVEN_DEVICE_ID
```

### Every build after that

Connect and unlock the iPhone, trust the Mac if prompted, then:

```bash
./build-install.sh
```

It builds a signed Release, verifies the signature, prints the host baked into the app, installs to the device, and launches it.

Using the script rather than a hand-typed `xcodebuild` matters for one specific reason: the invocation carries ~15 flags, and getting `PRODUCT_BUNDLE_IDENTIFIER` wrong installs a **second** app instead of updating the one on your phone. The script always passes the bundle id from `build.local.sh`, so the checked-in project can keep a generic default safely.

The `.p8` signing key is a credential: keep it outside this repository, and never copy it into a build artifact.

## Why playback survives in the background

Background survival is a chain of cooperating decisions:

- `Info.plist` declares the `audio` background mode.
- `PlaybackController` activates `AVAudioSession` as non-mixable `.playback`.
- `AVPlayer` consumes an HLS playlist with no end marker.
- The Mac continuously emits PCM, including a low comfort-noise floor between replies, so the live stream neither ends nor becomes inert digital silence.
- Now Playing metadata identifies Raven as a live audio stream, and the remote command center supplies Play/Pause controls.
- A 20-second stall watchdog and exponential retry loop rebuild the player when progress stops. Retry delay grows from 1 second to a 30-second ceiling and resets after 60 seconds of healthy playback.
- On item readiness and every rebuild, the player seeks approximately one second behind the HLS live edge before playing.

The audio session deliberately does not mix or duck. Raven owns the car route while playing; starting another audio app can interrupt or take the session. When the interruption ends, Raven rebuilds the player if playback is still wanted.

## Playback evidence

`PlaybackController` appends timestamped lifecycle, audio-session, route, retry, stall, and player events to:

```text
Documents/EarPlayback.log
```

While the player is `.playing`, a periodic observer checks that media time advances. It writes `PLAYBACK_PROGRESS` at most once per minute and persists the last proof time for the status line. This proves that `AVPlayer` advanced through media; it does not prove that the car speakers were audible.

The file uses `completeUntilFirstUserAuthentication` protection, so it remains available after the phone has been unlocked once following boot. File sharing and opening documents in place are enabled in `Info.plist`.

### Pull the log to the Mac

With the same device and bundle variables used for installation:

```bash
cd ~/code/experiments/raven/ios

xcrun devicectl device copy from \
  --device "$RAVEN_DEVICE_ID" \
  --domain-type appDataContainer \
  --domain-identifier com.example.Ear \
  --source Documents/EarPlayback.log \
  --destination ./EarPlayback.log

tail -100 ./EarPlayback.log
```

Because `UIFileSharingEnabled` is on, the same file is also visible on the phone under **Files → On My iPhone → Raven** after the device is unlocked.

## Phone-log upload

When the app becomes active, and then about every 30 seconds while it remains active, `HuginnAPI.uploadLog()` reads bytes after a persisted local offset and posts new lines to:

```http
POST http://100.64.0.1:8080/log
Content-Type: application/json

{"device":"iphone","lines":["2026-… PLAYING_OBSERVED …"]}
```

The app sends at most the newest 500 lines in one request. On HTTP 200 it advances the persisted byte offset; failures are silent and retried at the next foreground upload opportunity. The Mac appends the lines to `~/code/experiments/raven/logs/phone.jsonl` and records a structured `phone/log_upload` event in `~/code/experiments/raven/logs/events.jsonl`. `python3 ~/code/experiments/raven/diagnose.py` uses that evidence alongside the Mac pipeline state.

Uploads are diagnostic only. They do not keep the audio session alive and do not control playback.

## Network contract

The service base URL comes from the `RAVEN_HOST` build setting → `Info.plist` `RavenHost` → `RavenConfig.host` (in `HuginnAPI.swift`), set locally in the gitignored `raven-host.local`. `Info.plist` relaxes App Transport Security (`NSAllowsArbitraryLoads`) to allow the plain-HTTP tailnet stream — Tailscale encrypts the transport, and there is no public HTTP endpoint. The phone must be able to reach that Tailscale IP; the app has no in-app host setting.

| Endpoint | App behavior |
|---|---|
| `GET /stream.m3u8` | Continuous background HLS playback. Playlist polling also tells the Mac that a listener is live. |
| `GET /channels` | Foreground refresh at launch, approximately every 10 seconds, on pull-to-refresh, and after a selection change. Uses `If-None-Match`. |
| `GET /transcript?limit=50` | Foreground refresh at launch and approximately every 5 seconds. Uses `If-None-Match`. |
| `POST /active` | Sends `follow` or a known pinned `session_id`. |
| `POST /log` | Uploads new playback-log lines while foregrounded. |

## Limits

- **Audio continues in the background; API polling does not.** Transcript, channel, and phone-log refresh tasks run only while the SwiftUI scene is active.
- **Latency is expected.** The Mac's two-second HLS segments and `AVPlayer` buffering put playback roughly 4–8 seconds behind the Claude hook.
- **The host is fixed.** Changing the Mac's tailnet IP requires a code and `Info.plist` update plus a rebuild.
- **HTTP has no app-layer authentication.** Reachability is limited by the Tailscale network boundary.
- **Mute is local.** It does not pause the player, stop HLS requests, or prevent the Mac from treating the phone as a live listener.
- **The app shows emitted transcript, not guaranteed acoustic delivery.** A row appears when the Mac starts writing audio. `EarPlayback.log` proves player progress, not speaker output or complete comprehension.
- **Raven currently exposes no Skip control.** Latest-wins and manual Skip are designed on the Mac side but are not implemented in this app.
- **Portrait iPhone only.** The target requires iOS 17, arm64, and iPhone device family; there is no iPad or Mac Catalyst target.
