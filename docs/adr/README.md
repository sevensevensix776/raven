# Raven Architecture Decision Records

These records capture the architectural choices behind Raven. They describe why the current system is voice-out only, local-first, subscription-safe, and built around a continuous background audio path. For the chronological account, see [`../HISTORY.md`](../HISTORY.md); for side-by-side costs, see [`../TRADEOFFS.md`](../TRADEOFFS.md); for the operating system, see the [project README](../../README.md).

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-voice-out-only.md) | Voice-out only; keep Remote Control for voice-in | Accepted |
| [0002](0002-native-ios-app.md) | Native iOS app rather than a PWA | Accepted |
| [0003](0003-continuous-hls-comfort-noise.md) | Continuous HLS with a comfort-noise floor | Accepted |
| [0004](0004-local-kokoro.md) | Local Kokoro TTS rather than cloud ElevenLabs | Accepted |
| [0005](0005-claude-code-hooks.md) | Claude Code hooks on the Claude.ai subscription rather than the Agent SDK | Accepted |
| [0006](0006-non-mixable-playback.md) | Non-mixable `.playback` audio session | Accepted |
| [0007](0007-tailscale-transport.md) | Tailscale transport with no cloud relay | Accepted |
| [0008](0008-cli-ios-build.md) | CLI build, sign, install, and launch for iOS | Accepted |
| [0009](0009-go-orchestration-python-synthesis.md) | Go orchestration with Python retained for synthesis | Accepted |
| [0010](0010-latest-wins-interrupt.md) | Latest-wins interruption | Accepted; implementation deferred |
| [0011](0011-no-token-streaming.md) | No token-level streaming under the subscription constraint | Accepted |
| [0012](0012-uncapped-replies.md) | Uncapped spoken replies | Accepted |

## Status convention

- **Accepted** means the decision governs the system.
- **Accepted; implementation deferred** means the policy is chosen and its design is documented, but current production behavior has not yet changed.
- A future record should supersede, rather than silently rewrite, a decision whose underlying constraints change.
- [0013 — Target only the current phone's iOS (iOS 26)](0013-target-current-ios-only.md)
- [0014 — Channel names come from the local session title, not Remote Control](0014-channel-names-local-title.md)
