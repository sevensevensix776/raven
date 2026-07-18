#!/bin/bash
# Build and install the raven binary SAFELY.
#
# Do NOT `cp raven ~/.local/bin/raven` directly: on macOS, overwriting a binary
# that running processes have mmap'd (raven serve / raven write are long-lived)
# invalidates its code signature, and every NEW exec — including `raven hook` on
# each Claude turn — gets SIGKILLed (exit 137). That silently kills narration.
#
# The safe path: build, write to a temp name on the same filesystem, ad-hoc
# codesign it, then atomically rename into place. Running processes keep their
# old inode; new execs get a validly-signed binary.
set -euo pipefail
cd "$(dirname "$0")"

DEST="${1:-$HOME/.local/bin/raven}"
mkdir -p "$(dirname "$DEST")"

go build -o raven .
tmp="$(dirname "$DEST")/.raven.new.$$"
cp raven "$tmp"
codesign -s - -f "$tmp" >/dev/null 2>&1 || true   # ad-hoc sign; harmless if unsupported
mv -f "$tmp" "$DEST"                               # atomic on the same filesystem
echo "installed: $DEST"
"$DEST" 2>&1 | head -1 || true
