# Summarization (built, guarded, off)

Raven can compress long replies into short spoken audio before synthesis. The
mechanism exists in `synthd.py` and is **disabled by default**. It has never been
tuned on a real drive, so treat this as a working experiment, not a feature.

The roadmap entry for why it matters is [`FUTURE_WORK.md`](FUTURE_WORK.md) §3.

## What ships today

```bash
SUMMARIZE=0            # 1 sends every queued reply through Ollama first
SUMMARY_MODEL=qwen3:1.7b
```

With `SUMMARIZE=1` the path becomes:

```text
queue/<stamp>.txt → summarize(text, SUMMARY_MODEL) → Kokoro or say → .wav
```

`summarize()` shells out to `ollama run qwen3:1.7b "/no_think Summarize …"`,
asking for one short spoken sentence with no preamble or Markdown, under a
20-second timeout. **Any exception returns the original text** — the fallback is
deliberately fail-open, because an over-long spoken reply beats silence.

There is no length threshold: short and long replies are treated alike. That
alone makes the current behaviour wrong to enable globally.

### Load-bearing trap: the empty answer after `</think>`

Qwen3 can spend its entire output budget inside `<think>…</think>` and emit
nothing after the closing tag. A naive implementation then hands an empty string
to the synthesizer and **silently loses the reply**. Two protections matter:

1. Prefix the request with `/no_think` so the model skips the reasoning trace for
   what is a small transformation.
2. After stripping any leaked `<think>` block, treat an empty result as a
   *failure* and return the original reply.

If you change this code, keep both. Either one alone can produce silence.

## What must be built before turning it on

**A length gate.** Speak short replies verbatim; only summarize once a reply is
long enough to burden a driver. A starting hypothesis is 800–1,200 cleaned
characters and a 40–80 word target, but these are guesses — the real measures are
listening duration and semantic density, not character count. Reject a "summary"
that is not materially shorter than the original.

**A transcript contract.** Today the hook writes the original cleaned reply into
`.caption.json` and `synthd` changes only its local synthesis text — so enabling
summarization right now would show one thing on screen while the phone speaks
another. The record needs to distinguish them:

```json
{
  "text": "The text Raven actually spoke.",
  "original_text": "The complete cleaned Claude reply.",
  "delivery_mode": "summary",
  "summary_model": "qwen3:1.7b"
}
```

`text` stays the field the app renders; `original_text` appears only when the two
differ; `delivery_mode` is `verbatim`, `summary`, or `verbatim_fallback`.

**Instrumentation.** A dedicated `synthd/summarize` event carrying
`attempted`, `ok`, `fallback`, `input_chars`, `output_chars`, `ms`, and `model`.
Summary latency currently hides inside the synthesis metric. Never log reply
bodies — diagnosis needs the compression ratio, latency, and empty-result count,
not the content.

## Limits

- A 1.7B model may omit qualifications or merge distinct actions. This needs
  listening evaluation, not text review.
- Summarization reduces spoken *duration*; it does nothing for the HLS delivery
  latency, and it is a different lever from streaming synthesis (which reduces
  time-to-first-word).
- The original Claude reply remains authoritative. A driving summary is a lossy
  listening aid.
