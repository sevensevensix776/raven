# ADR 0004: Use Local Kokoro TTS

## Status

Accepted.

## Context

Expected use is roughly 60 hours per month. The evaluated ElevenLabs Conversational AI path was estimated at about $288–300 per month and carried a 100-conversations-per-30-days cap. Local Kokoro-82M is Apache-2.0 licensed, runs on the M4 Pro, and has approximately zero marginal usage cost.

The quality comparison was honest: Kokoro is prosodically flat, like a competent audiobook narrator, and was roughly a coin-flip preference against ElevenLabs Flash in a quiet room. It is adequate for narration, not companionship. V1 had already selected Kokoro with the `af_heart` voice.

## Decision

Use `prince-canuma/Kokoro-82M` locally with the `af_heart` voice as Raven's primary TTS backend. Keep the model warm in `synthd.py` through `mlx-audio`. Use macOS `say` only as a fail-open emergency backend when synthesis fails or the daemon is actually unavailable.

## Consequences

- Normal narration has approximately zero marginal provider cost.
- Reply text and synthesized audio remain on the Mac and tailnet.
- Voice prosody is flatter than the preferred cloud alternative.
- Python and the `mlx-audio` environment remain runtime dependencies at the ML boundary.
- A warm model makes short synthesis fast, but whole-reply generation can still take about 15 seconds for roughly 2,500 characters.
- The fallback must distinguish a dead daemon from a slow one; racing `say` against live Kokoro caused double-speak.

## Alternatives considered

- **ElevenLabs Conversational AI.** Rejected on sustained cost and the evaluated conversation cap.
- **ElevenLabs Flash.** Better or comparable on some listening trials, but still cloud-billed and not enough better to outweigh local economics and privacy.
- **macOS `say` as the primary voice.** Retained only as fallback; its robotic voice is less suitable for long technical narration.
- **Qwen summarization before every reply.** This changes duration, not voice quality, and remains disabled and untuned. See [`../SUMMARIZATION.md`](../SUMMARIZATION.md).

See [`../../synthd.py`](../../synthd.py) and the [tradeoff table](../TRADEOFFS.md).
