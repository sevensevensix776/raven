# ADR 0014 — Channel names come from the local session title, not Remote Control

## Status

Accepted (2026-07-18).

## Context

Raven's channel list shows a name per Claude Code session. Asif names his
sessions in **Claude Code Remote Control** (the phone app) with short names like
`standby`, `Raven`, `Memory`, `Errors`. The natural expectation is that Raven's
channel list matches those.

It does not, and cannot cheaply. **The Remote Control session names are stored
server-side in the claude.ai backend**, keyed to the bridge/remote-control
session (`bridgeSessionId` like `cse_…`). They are **not present in any local
file** — verified by searching the session transcript JSONL, `bridge-pointer.json`
(which holds only `sessionId`/`environmentId`/`pid`), and all of `~/.claude`.

What *is* local is a **separate** title: the transcript's `customTitle`
(user-set, e.g. via `/title`) and `aiTitle` (auto-generated). These are a
different naming system from the Remote Control name — e.g. this project's
session showed local `customTitle="drive-voice-experiments"` while its Remote
Control name was `Raven`.

## Decision

Raven displays the **local session title**: the transcript's `customTitle` if
set, else `aiTitle`, else the project folder (`basename(cwd)`). It does **not**
attempt to fetch the Remote Control name. The name is re-read from the transcript
on each `/channels` request (server-side, cached by file mtime), so local title
changes appear automatically, including on idle sessions (see ADR 0013's sibling
fix in the hook/server).

## Consequences

- A channel's name may differ from the phone's Remote Control name. **Accepted.**
  The local title is still a real, stable identifier.
- No dependency on claude.ai's private Remote Control API — nothing to break when
  Claude Code updates.
- **Do not re-investigate this.** The Remote Control name is not locally
  available; matching it would require the undocumented backend API.

## Alternatives considered

- **Query claude.ai's Remote Control backend for the names.** Rejected:
  undocumented, requires claude.ai auth, and fragile across Claude Code updates —
  exactly the wrapper-maintenance trap ADR 0001's context and the Omnara
  precedent warn about.
- **In-app renaming in Raven (a local override stored on the Mac).** Considered
  and offered; declined for now. It would decouple naming from Remote Control and
  be fully reliable, but adds a second place to name sessions. Revisit only if the
  local title proves insufficient.
