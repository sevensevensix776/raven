# Security

## Threat model, stated plainly

**Raven's HTTP API has no authentication.** Not a weak token — none. Every
endpoint (`/channels`, `/transcript`, `/catchup`, `/active`, `/health`, `/log`)
answers any request that reaches the port. Access control is delegated entirely
to the network layer: the server is expected to bind a Tailscale address, and
Tailscale is expected to be the only way to reach it.

This is a deliberate trade for a single-user tool on a private tailnet. It also
means the following are true, and you should decide whether you accept them:

| If someone can reach the port | They can |
| --- | --- |
| Any device on your tailnet | Read `/transcript` — everything Claude said in the selected session, including code, file paths, and anything Claude quoted from your repositories |
| Any device on your tailnet | `POST /active` to change which session is being spoken, or pin a different one |
| Any device on your tailnet | Play the live HLS stream and hear the narration |
| Anyone on your LAN, if you rebind | Everything above — `RAVEN_BIND=0.0.0.0:8080` removes the only control there is |

**Do not expose Raven to the public internet, and do not port-forward to it.**
There is nothing behind the network boundary to stop an attacker.

## What the transcript contains

Raven synthesizes assistant replies from the selected Claude Code session. If
Claude reads a secret aloud — an API key echoed in output, a `.env` value, a
customer name — that text passes through the queue, is written to
`spoken.jsonl`, and is served by `/transcript`. Raven applies no redaction. Treat
`$RAVEN_HOME/logs/` and `spoken.jsonl` as sensitive as the sessions themselves.

`raven diagnose` prints operational metrics, not reply text, and is safe to
paste into an issue. `spoken.jsonl` and `logs/phone.jsonl` are not.

## Transport

Traffic between the Mac and the phone is plain HTTP. The iOS app therefore sets
`NSAllowsArbitraryLoads`. Confidentiality comes from Tailscale's WireGuard
encryption, not from TLS — which is why the tailnet assumption above is
load-bearing rather than advisory.

## Secrets and the repository

These are gitignored and must never be committed:

- `config.local.sh` — your tailnet address and local overrides
- `ios/raven-host.local`, `ios/build.local.sh` — host address and signing identity
- `logs/`, `queue/`, `spoken.jsonl`, `channels.json`, `selection.json` — session content

Your App Store Connect `.p8` key belongs **outside the repository entirely**, not
merely gitignored.

If you fork this and publish, check rendered images as well as text. A diagram
PNG in this repository leaked a tailnet address through a stale render after the
source had been scrubbed — `git filter-repo --replace-text` rewrites text, not
pixels.

## Reporting a vulnerability

Open a GitHub issue for anything non-sensitive. For a finding you would rather
not disclose publicly, use GitHub's private vulnerability reporting on this
repository. This is a personal project maintained in spare time — expect a
best-effort response, not an SLA.
