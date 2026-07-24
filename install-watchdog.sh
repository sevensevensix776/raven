#!/bin/bash
# Installs the Raven watchdog as a LaunchAgent: starts the pipeline at login and
# re-checks it every 60s, so a reboot or a crash can't leave Raven silently dead.
#
#   ./install-watchdog.sh            install and load
#   ./install-watchdog.sh --uninstall
#
# The agent runs as your user (not root) because Raven needs your home dir, your
# Tailscale session, and — via `say` — the user audio context.

set -e

RAVEN_HOME="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LABEL="tv.raven.watchdog"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

if [ "$1" = "--uninstall" ]; then
  launchctl unload "$PLIST" 2>/dev/null || true
  rm -f "$PLIST"
  echo "uninstalled: $LABEL"
  exit 0
fi

mkdir -p "$HOME/Library/LaunchAgents" "$RAVEN_HOME/logs"

cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$LABEL</string>

    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>$RAVEN_HOME/watchdog.sh</string>
    </array>

    <key>WorkingDirectory</key>
    <string>$RAVEN_HOME</string>

    <!-- Start at login, then re-check on an interval. The script exits straight
         away when healthy, so this is nearly free. KeepAlive would be wrong here:
         start.sh daemonizes and exits, which launchd would read as a crash. -->
    <key>RunAtLoad</key>
    <true/>
    <key>StartInterval</key>
    <integer>60</integer>

    <!-- ffmpeg, python3 and the raven binary must resolve without a login shell. -->
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>RAVEN_HOME</key>
        <string>$RAVEN_HOME</string>
    </dict>

    <key>StandardOutPath</key>
    <string>$RAVEN_HOME/logs/watchdog.log</string>
    <key>StandardErrorPath</key>
    <string>$RAVEN_HOME/logs/watchdog.log</string>

    <key>ProcessType</key>
    <string>Background</string>
</dict>
</plist>
PLIST_EOF

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

echo "installed: $PLIST"
echo "  runs at login and every 60s"
echo "  logs:  $RAVEN_HOME/logs/watchdog.log  (+ watchdog events in events.jsonl)"
echo "  check: launchctl list | grep raven"
echo "  stop:  ./install-watchdog.sh --uninstall"
