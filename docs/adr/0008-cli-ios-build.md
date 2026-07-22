# ADR 0008: Build, Sign, Install, and Launch iOS from the CLI

## Status

Accepted.

## Context

Raven is a personal native iPhone client and needs repeatable on-device builds without making the Xcode GUI part of the operating procedure. Xcode still supplies the compiler and SDK. Signing uses an existing App Store Connect API key issued under a paid Apple Developer Program membership. Free personal Apple IDs cannot create App Store Connect API keys, so they could not support this workflow.

The project also exposed a separate macOS code-signing deployment trap: copying the live Go `raven` executable over itself while long-lived processes had it memory-mapped invalidated its Mach-O signature. New hook executions received SIGKILL, exit 137. Although that failure involved the Mac binary rather than the iOS app, it established the same operational rule: an install is not complete until the installed artifact remains valid for a new signed execution.

## Decision

Use a fully command-line iOS workflow:

- `xcodebuild` builds and provisions the Release device app using the existing API key and paid team.
- `codesign --verify` validates the built app.
- `xcrun devicectl` installs and launches it on the connected iPhone.
- Signing credentials remain outside the repository.

For the Mac Go binary, use `raven-go/install.sh`: build, copy to a same-filesystem temporary path, ad-hoc codesign, and atomically rename. Never overwrite the running destination in place.

## Consequences

- The complete build/install flow is scriptable and reproducible without opening the Xcode GUI.
- Xcode.app, a connected trusted device, the paid team, and the private API key remain prerequisites.
- Credentials must be managed outside source control.
- A free Apple developer identity is insufficient for this API-key workflow.
- Operators must distinguish building from verifying and installing; the exit-137 incident showed that a copied artifact can still be unusable.

## Alternatives considered

- **Use the Xcode GUI for signing and installation.** Rejected as a manual, less repeatable operating path.
- **Use a free personal Apple ID.** Not viable for the chosen API-key-based workflow because personal teams cannot create App Store Connect API keys.
- **Copy a newly built Mac binary directly over the live executable.** Rejected after it invalidated the signature and silently killed hooks.

See the iPhone build guide in `../../ios/README.md`, the Go installer in `../../cli/install.sh`, and the exit-137 account in [`../HISTORY.md`](../HISTORY.md).
