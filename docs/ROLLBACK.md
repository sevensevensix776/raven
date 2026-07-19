# Raven rollback & checkpoint

How to get back to a known-good state fast if a new feature breaks the live
pipeline. Written at the checkpoint taken **before building live narration**
(the `raven tail` process that changes who owns speaking).

## The checkpoint

Tag on all three repos: **`checkpoint-pre-live-narration`** (2026-07-18).

| Repo | Path | Tagged commit |
|---|---|---|
| raven (runtime home) | `~/code/experiments/raven` | `checkpoint-pre-live-narration` |
| raven-go (the binary) | `~/code/experiments/raven-go` | `cd4e5e3` |
| ear (iOS app) | `~/code/experiments/ear` | `d13af52` |

The installed binary at this checkpoint:

- `~/.local/bin/raven` sha256 `e994d2272cacd898a6ac2ebbd93e92707a2326bcada28424f1895704a2a4d04d`
- built from raven-go `cd4e5e3`.

> **These repos have no git remotes** — the tag is a *local* anchor and the git
> history is the only backup. Rollback below is fully local and needs no network.

## Known-good baseline (what "healthy" looks like)

Restore target. After any rollback, `raven diagnose` must match this shape:

- **4 long-lived processes:** `raven write`, `ffmpeg -re` (HLS mux), `raven serve`,
  `synthd.py` (the venv python).
- **`raven diagnose` → `VERDICT: HEALTHY`**, writer + synthd `OK`, Kokoro synth
  working, `kokoro->say fallbacks: 0`.
- **HLS advancing:** new `hls/stream*.ts` segments appear every ~2 s.
- **serve reachable on the tailnet:** `curl http://100.64.0.1:8080/health` → `200`
  (serve binds the Tailscale IP, *not* loopback — loopback returning `000` is
  expected, not a fault).

## Level 0 — the kill switch (instant, no rebuild)

Live narration ships **default-off** (`LIVE_NARRATION=0`) as a separate process
(`.tail.pid`). The existing Stop-hook speech path is untouched when the flag is
off. So the first line of defense needs no git and no rebuild:

```bash
# 1. Make sure the flag is off (or remove the line entirely).
#    edit ~/code/experiments/raven/config.sh  ->  LIVE_NARRATION=0
# 2. Kill the tailer if it's running; do NOT restart it.
kill "$(cat ~/code/experiments/raven/.tail.pid)" 2>/dev/null
rm -f ~/code/experiments/raven/.tail.pid
# 3. The Stop hook resumes owning speech immediately. Verify:
~/.local/bin/raven diagnose        # expect VERDICT: HEALTHY
```

This restores today's exact behavior (speak-on-Stop) because that path was never
removed — only demoted to a safety net while the tailer is alive.

**Design contract that makes Level 0 valid** (the feature MUST honor it, or Level 0
is a lie): the tailer is additive and flag-gated; `LIVE_NARRATION=0` reproduces
today's Stop-only delivery byte-for-byte; the tailer has its own pidfile and never
shares the Stop hook's producer role while the flag is off.

## Level 1 — full code rollback (git)

If the feature corrupted more than the tailer (state files, hook, writer, serve),
go back to the tagged code and rebuild:

```bash
# Stop the live pipeline.
~/code/experiments/raven/stop.sh

# Roll each repo back to the checkpoint.
git -C ~/code/experiments/raven    checkout checkpoint-pre-live-narration
git -C ~/code/experiments/raven-go checkout checkpoint-pre-live-narration
git -C ~/code/experiments/ear      checkout checkpoint-pre-live-narration   # only if the app changed

# Rebuild + reinstall the binary from the tagged source (atomic; safe while stopped).
cd ~/code/experiments/raven-go && ./install.sh

# Bring the pipeline back up from the runtime home.
~/code/experiments/raven/start.sh

# Verify against the baseline above.
~/.local/bin/raven diagnose
```

To return to the tip of development afterward: `git -C <repo> checkout master`
(this leaves a detached HEAD during rollback — that's expected and non-destructive;
nothing is committed during a rollback).

> If the feature also wrote **new** durable state files (e.g. `tail-state/`,
> stop-intent records), delete them after Level 1 so a stale format can't confuse
> the rolled-back code:
> `rm -rf ~/code/experiments/raven/tail-state ~/code/experiments/raven/.stop-intent*`
> (paths TBD when the feature lands — update this line then.)

## Why this is safe to build on

The consolidation that produced this checkpoint was verified end-to-end (live hook
→ gate → queue → Kokoro synth; HLS on the tailnet) and the full test suite is green
(`go test`, parity 5/5, serve/diagnose parity, writer PCM integration). Rolling back
to `checkpoint-pre-live-narration` returns to that verified state.
