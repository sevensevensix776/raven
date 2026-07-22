# ADR 0009: Port Orchestration to Go and Keep Synthesis in Python

## Status

Accepted.

## Context

The original hook grew into roughly 150 lines of Bash with Python heredocs. Quoting, `sed`, subprocess startup, and installed-versus-repository drift made changes fragile. Claude Code gives command hooks a two-second timeout, and a typical Python startup costs roughly 50–100 ms.

Asif already uses a single Go binary convention with `hermes`. Raven's hook, server, PCM writer, and diagnosis command are deterministic orchestration code. Speech synthesis is different: Kokoro is loaded through `mlx-audio`, which has no useful Go binding.

## Decision

Implement `raven hook`, `raven serve`, `raven write`, and `raven diagnose` in one dependency-free static Go binary. Keep `synthd.py` in Python as the warm Kokoro/`mlx-audio` boundary.

Port one component at a time and require behavior parity before live replacement:

- hook state was parity-tested across five cases;
- Python and Go server responses and side effects matched;
- Bash and Go writers produced byte-identical PCM in the parity harness; and
- diagnostics were compared line-for-line after normalizing presentation and time drift.

## Consequences

- The hook starts in roughly 1 ms and comfortably fits its two-second budget.
- One binary replaces Bash quoting, heredoc, and installed-source ambiguity for orchestration.
- The Go module has no third-party dependencies.
- Raven remains intentionally polyglot because Python is the practical ML runtime.
- Bash/Python predecessors remain rollback paths and parity fixtures rather than active architectural owners.
- State formats and local file protocols remain compatible, which constrains some cleanup but makes cutover reversible.
- The Mac binary must be installed atomically and codesigned; see [ADR 0008](0008-cli-ios-build.md).

## Alternatives considered

- **Keep Bash/Python orchestration.** Rejected because it was already producing quoting friction, slower startup, and installed-source drift.
- **Port synthesis to Go too.** Rejected because Kokoro/`mlx-audio` has no useful Go binding.
- **Rewrite from a new specification without parity.** Rejected because the live file and state contracts were already working; parity was safer than reinterpretation.
- **Use a framework or third-party Go dependencies.** Unnecessary for the small local command surface.

See `../../cli/README.md` and the [current runtime component map](../../README.md#runtime-components).
