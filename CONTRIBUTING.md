# Contributing

Raven is a personal project that solves one person's problem: hearing Claude
Code replies while driving. It is published because the architecture may be
useful to others, not because it is seeking to become a general-purpose product.
Set expectations accordingly — issues and PRs are welcome, but scope is guarded
and responses are best-effort.

## Before opening a PR

Raven is hard to test the way it is actually used, so the bar is: **prove the
pipeline still speaks.**

```bash
cd cli
go build -o raven . && go vet ./... && go test ./...
```

Then run it for real. Unit tests cover the logic; they cannot tell you that
audio reached a phone. Start the stack, send a Claude Code turn through it, and
confirm you hear it:

```bash
./start.sh
raven diagnose        # exits non-zero when the pipeline is not serving
```

If your change touches the hook, the tailer, or the writer, say in the PR what
you heard — not just that tests passed. Several real bugs in this project's
history passed every test: double-speak, a queue that drained silently, a
deploy that reported success while the old binary still served.

## What is likely to be accepted

- Pronunciation fixes in `internal/clean` — add the substitution **and** a case
  in `clean_test.go`. This is the most common useful contribution.
- Bug fixes with a failing test that now passes.
- Portability fixes, as long as macOS behaviour is unchanged.
- Documentation that corrects something wrong. Documentation that describes
  something unbuilt belongs in `docs/FUTURE_WORK.md`, not the README.

## What is unlikely to be accepted

- Voice-in, wake words, or microphone capture. This is a deliberate boundary,
  not an oversight — see [ADR 0001](docs/adr/0001-voice-out-only.md).
- Metered cloud TTS as the default path. Roughly zero marginal cost per reply is
  a product requirement ([ADR 0004](docs/adr/0004-local-kokoro.md)).
- Anything that adds a network listener without authentication, or that widens
  the existing bind beyond a tailnet. See [SECURITY.md](SECURITY.md).
- Third-party Go dependencies in `cli/`. `go.mod` declares only the module and
  the Go version, and that is worth keeping.

## Conventions

- Go code is `gofmt`-clean; run `go vet`.
- Architectural decisions get an ADR in `docs/adr/`. If a PR changes a decision
  an ADR records, amend the ADR in the same PR — a stale ADR is worse than none.
- Documentation states what is **shipped**. Proposals go to
  [`docs/FUTURE_WORK.md`](docs/FUTURE_WORK.md).

## Licence

Contributions are accepted under the [MIT Licence](LICENSE).
