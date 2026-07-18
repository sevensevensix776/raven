# Raven summarization — scope and design

This document defines the unfinished summarization layer in Raven: compress long Claude replies into short, speakable audio for a driver without changing short replies or risking silence. The mechanism already exists in `synthd.py`, but it is globally disabled and has not been tuned on real drives. This is a rollout plan and target behavior, not a claim that the current summarizer is production-ready.

## Status

**Built, guarded, and OFF.** `config.sh` currently sets:

```bash
SUMMARIZE=0
SUMMARY_MODEL=qwen3:1.7b
```

When `SUMMARIZE=1`, every queued reply is sent through Ollama before synthesis. There is currently no length threshold: short and long replies are treated alike. The summary pass is also not represented in transcript metadata, and its elapsed time is not included in the `synthd/synth` latency metric.

## Goal

Make long replies useful when heard once, hands-free:

- preserve the decision, outcome, blocking issue, and next action;
- remove code, repetition, supporting detail, and screen-oriented structure;
- make the result natural to hear while attention is divided;
- never turn a valid reply into silence; and
- leave concise replies verbatim so Raven does not paraphrase unnecessarily.

This feature does not summarize the user's prompt, create voice-in, or replace the full Claude reply in Claude Code.

## Existing design

The current synthesis path is:

```text
queue/<stamp>.txt
→ summarize(text, SUMMARY_MODEL) when SUMMARIZE=1
→ Kokoro or say fallback
→ queue/<stamp>.wav or .aiff
```

`summarize()` runs:

```text
ollama run qwen3:1.7b "/no_think Summarize …"
```

The prompt requests one short spoken sentence with no preamble or Markdown. The subprocess has a 20-second timeout. Any exception returns the original text. If Qwen leaks a reasoning block, the code strips everything through `</think>` and uses the remaining answer.

### Known trap: an empty answer after `<think>`

Qwen3 can spend its output budget inside `<think>…</think>` and emit no summary after the closing tag. A naïve implementation then passes an empty string to speech synthesis and silently loses the reply.

Two protections are load-bearing:

1. Prefix the request with `/no_think` so the model does not enter the reasoning trace for this small transformation.
2. After stripping any leaked `<think>` block, treat an empty result as failure and return the original reply.

The fallback must remain fail-open: an imperfectly long spoken reply is better than no reply.

## Target behavior

Raven should use a length gate, then choose one of two explicit delivery modes.

| Reply class | Behavior | Transcript mode |
|---|---|---|
| Short enough to hear comfortably | Speak the cleaned Claude reply verbatim. | `verbatim` |
| Long enough to burden a driver | Summarize, validate a non-empty result, then speak the summary. | `summary` |
| Summary timeout, process error, or empty output | Speak the original cleaned reply. | `verbatim_fallback` |

The decision belongs in `synthd.py`, after the hook's speech cleanup and before either synthesis backend. Kokoro and `say` must receive the same chosen spoken text.

## Thresholds to tune

None of these values has been validated yet. They should be configurable and measured rather than buried in the prompt.

| Threshold | Initial experiment | What to observe |
|---|---:|---|
| Minimum length for summarization | 800–1,200 cleaned characters | Whether verbatim replies below the gate still feel too long, and whether summaries above it discard needed detail. |
| Spoken compression target | About 40–80 words, usually 2–4 sentences | Comprehension at driving speed. The current one-sentence prompt may be too lossy for decisions with multiple parts. |
| Maximum summary expansion | Summary must be materially shorter than the original | If the model returns an equal or longer answer, use the original or retry only if latency permits. |
| Summary timeout | Current value: 20 seconds | A local-model stall must not delay the audio path indefinitely. The operational target should be far below the HLS latency budget. |

The numeric ranges above are starting hypotheses, not accepted product defaults. The first tuning question is experiential: “At what point did I wish Raven would compress this?” Character count is a practical gate, but listening duration and semantic density are the real product measures.

Suggested configuration additions, once implementation work begins:

```bash
SUMMARY_MIN_CHARS=1000
SUMMARY_TARGET_WORDS=60
SUMMARY_TIMEOUT_SECONDS=20
```

`SUMMARY_MIN_CHARS=0` should mean “summarize every non-empty reply” only for controlled testing. The production default should preserve short replies verbatim.

## Transcript contract

The visible transcript should lead with what Raven actually spoke while preserving the authored reply for inspection. Today the hook writes the original cleaned reply into `.caption.json`, and `synthd` changes only its local synthesis text; enabling summarization today would therefore show the original while the phone hears the summary.

Before rollout, extend the caption/transcript record so the distinction is explicit:

```json
{
  "text": "The text Raven actually spoke.",
  "original_text": "The complete cleaned Claude reply.",
  "delivery_mode": "summary",
  "summary_model": "qwen3:1.7b"
}
```

Contract:

- `text` is always the spoken text and remains the field the current iPhone UI renders.
- `original_text` is present only when `text` differs from the cleaned source.
- `delivery_mode` is `verbatim`, `summary`, or `verbatim_fallback`.
- `summary_model` is present only when a summary was attempted successfully.
- The utterance `id`, session, project, and spoken timestamp remain unchanged.

`synthd` should update caption metadata atomically before publishing the ready audio file. The audio file remains the queue commit for “synthesis complete”; the transcript is still appended only when the writer starts emission.

## Instrumentation

Add a dedicated structured event around the summary pass rather than hiding it inside synthesis:

```text
comp=synthd event=summarize id=<stamp>
attempted=true ok=true fallback=false
input_chars=<n> output_chars=<n> ms=<n> model=qwen3:1.7b
```

Do not log reply bodies. Diagnosis needs latency, compression ratio, fallback count, and empty-result count—not the content itself.

## Rollout plan

1. **Drive on raw replies first.** Keep `SUMMARIZE=0` and `MAX_SPOKEN_CHARS=0`. Establish where long replies become irritating, which details matter, and whether interruption changes the need for compression.
2. **Add the length gate and transcript contract.** Do not enable summarization globally until the phone can distinguish spoken summary from original text.
3. **Replay a fixed corpus.** Include terse answers, multi-step implementations, errors, code-heavy replies, and several-minute responses. Verify short replies remain byte-for-byte equivalent after cleanup.
4. **Enable for deliberate test drives.** Start with a conservative high threshold. Review both comprehension and the structured compression/latency events after each drive.
5. **Tune target and threshold independently.** Lower the length gate only after the summary shape is trustworthy. Increase from one sentence to a few short sentences if decisions are being flattened.
6. **Promote only after fallback is boring.** Empty output, Ollama unavailable, timeout, or malformed reasoning must reliably produce the original reply without operator action.

## Limits

- The current implementation summarizes every reply when enabled; verbatim-short versus summarize-long still requires code.
- `qwen3:1.7b` is fast and local, but a small model may omit qualifications or merge distinct actions. This needs listening evaluation, not only text review.
- The present prompt targets one sentence. That is a useful smoke test, not a proven compression target.
- Summary latency happens before synthesis and currently has no dedicated event or diagnosis line.
- Summarization reduces duration; it does not reduce the underlying 4–8 second HLS delivery latency.
- The original Claude reply remains authoritative. A driving summary is a lossy listening aid.

