# Raven rollback & checkpoint

How to get back to a known-good state fast if a change breaks the live pipeline.

## The checkpoint

Tag: **`checkpoint-pre-live-narration`** — the last verified state before the
`raven tail` process changed who owns speaking.

Everything lives in **one repo**: `~/code/experiments/raven` (runtime at the root,
plus `cli/`, `ios/`, `docs/`), pushed to a private GitHub remote with the tag on
it. If the machine is lost you can re-clone and check the tag out; the rollback
below is fully local and needs no network.

> The tag predates both the monorepo merge and the history rewrite that scrubbed
> the Tailscale IP, so its commit SHA is not the original one — the tag itself
> still resolves and still marks the pre-live-narration state.

## Known-good baseline (what "healthy" looks like)

Restore target. After any rollback, `raven diagnose` should match this shape:

- **5 long-lived processes:** `raven write`, `ffmpeg -re` (the HLS mux),
  `raven serve`, `synthd.py` (the venv Python), and `raven tail` (live narration).
- **`raven diagnose` → `VERDICT: HEALTHY`**, writer + synthd `OK`, Kokoro
  synthesizing, `kokoro->say fallbacks: 0`.
- **HLS advancing:** new `hls/stream*.ts` segments appear every ~1 s.
- **serve reachable:** `curl http://$RAVEN_BIND/health` → `200`. `serve` binds
  whatever `RAVEN_BIND` is set to (your Tailscale address, from the gitignored
  `config.local.sh`) — **not** loopback, so a request to `127.0.0.1` returning
  `000` is expected, not a fault.

## Level 0 — the kill switch (instant, no rebuild)

Live narration is a separate process (`.tail.pid`) behind `LIVE_NARRATION` in
`config.sh`. It is currently **on**. The Stop-hook speech path was never removed —
only demoted to a safety net while the tailer is alive — so turning the flag off
restores speak-on-Stop immediately, with no git and no rebuild:

```bash
# 1. Turn it off.
#    edit config.sh  ->  LIVE_NARRATION=0
# 2. Kill the tailer; do NOT restart it.
kill "$(cat ~/code/experiments/raven/.tail.pid)" 2>/dev/null
rm -f ~/code/experiments/raven/.tail.pid
# 3. The Stop hook resumes owning speech. Verify:
~/.local/bin/raven diagnose        # expect VERDICT: HEALTHY
```

**The design contract that makes Level 0 valid** (any future change must honor it,
or Level 0 is a lie): the tailer is additive and flag-gated; `LIVE_NARRATION=0`
reproduces Stop-only delivery exactly; the tailer owns its own pidfile and never
shares the Stop hook's producer role while the flag is off.

## Level 1 — full code rollback (git)

If something corrupted more than the tailer (state files, hook, writer, serve),
go back to the tagged code and rebuild:

```bash
# Stop the live pipeline.
~/code/experiments/raven/stop.sh

# Roll back — one repo; cli/ and ios/ come with it.
git -C ~/code/experiments/raven checkout checkpoint-pre-live-narration

# Rebuild + reinstall the binary from the tagged source.
# (Atomic + ad-hoc signed; never `cp` over the live executable.)
cd ~/code/experiments/raven/cli && ./install.sh

# Bring the pipeline back up and verify against the baseline above.
~/code/experiments/raven/start.sh
~/.local/bin/raven diagnose
```

Return to the tip afterward with `git -C ~/code/experiments/raven checkout master`.
The rollback leaves a detached HEAD — expected and non-destructive; nothing is
committed during a rollback.

> Rolling back **past** live narration? Delete its durable state so a stale format
> can't confuse older code:
> `rm -rf ~/code/experiments/raven/tail-state ~/code/experiments/raven/.tail.pid`

## What never rolls back

`config.local.sh` and `ios/raven-host.local` hold your machine's Tailscale
address and are gitignored — no checkout touches them, so `serve` keeps binding
the right address across any rollback. If they go missing, copy the `.example`
files next to them and fill in your address.
