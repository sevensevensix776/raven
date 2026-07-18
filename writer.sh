#!/bin/bash
# Emits raw PCM forever: an idle floor between replies, real speech when queued.
#
# IDLE_FLOOR (config.sh):
#   noise   -> continuous low pink floor. PROVEN to keep the backgrounded app
#              alive and to stop car head units muting + chopping the first word.
#   silence -> true digital silence between replies (kills the audible static),
#              with a short pink pre-roll ONLY before each clip to wake the amp.
#              EXPERIMENTAL: relies on the native app surviving silence — the
#              exact thing that killed v1. Device-test before trusting it.
#
# Listener gating via .heartbeat: an HLS client polls the playlist ~every 2s.
# No poll in 10s => nobody listening => hold the queue instead of broadcasting.
cd ~/speech || exit 1
[ -f config.sh ] && . ./config.sh
IDLE_FLOOR="${IDLE_FLOOR:-noise}"
HB=hls/.heartbeat

idle_pcm() {
  if [ "$IDLE_FLOOR" = "silence" ]; then
    ffmpeg -loglevel quiet -f lavfi -i "anullsrc=r=24000:cl=mono:d=0.25" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
  else
    ffmpeg -loglevel quiet -f lavfi -i "anoisesrc=r=24000:c=pink:a=0.002:d=0.25" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
  fi
}

while true; do
  live=0
  if [ -f "$HB" ]; then
    age=$(( $(date +%s) - $(stat -f %m "$HB") ))
    [ "$age" -le 10 ] && live=1
  fi

  # Drop stale replies — don't read old news on reconnect.
  find queue \( -name '*.txt' -o -name '*.aiff' -o -name '*.wav' -o -name '*.caption.json' \) \
    -mmin +10 -delete 2>/dev/null

  # synthd (warm Kokoro) turns queued *.txt into ready *.wav. Consume ready audio
  # oldest first; say-synthesize a *.txt ourselves ONLY if it waited >5s (synthd
  # down). Never silent.
  f=$(ls -1 queue/*.wav queue/*.aiff 2>/dev/null | head -1)
  if [ -z "$f" ]; then
    t=$(ls -1 queue/*.txt 2>/dev/null | head -1)
    if [ -n "$t" ]; then
      age=$(( $(date +%s) - $(stat -f %m "$t") ))
      [ "$age" -ge 5 ] && f="$t"
    fi
  fi

  if [ "$live" = "1" ] && [ -n "$f" ]; then
    stem="${f%.*}"
    metadata="$stem.caption.json"

    # Pink pre-roll wakes the car amp so the first word isn't clipped. Needed
    # when idle is silence; harmless (brief, quiet) when idle is noise.
    ffmpeg -loglevel quiet -f lavfi -i "anoisesrc=r=24000:c=pink:a=0.002:d=0.35" \
      -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -

    # Record the utterance to the transcript the moment we start emitting it.
    python3 transcript_add.py "$metadata" 2>/dev/null
    python3 ravenlog.py writer emit id="$(basename "$stem")" 2>/dev/null

    if [ "${f##*.}" = "txt" ]; then
      # Fallback path: synthd isn't running. `say` inline so we still speak.
      a=$(mktemp -t spk)
      if say -o "$a.aiff" "$(cat "$f")" 2>/dev/null; then
        ffmpeg -loglevel quiet -i "$a.aiff" -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
      fi
      rm -f "$a" "$a.aiff"
    else
      ffmpeg -loglevel quiet -i "$f" -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
    fi
    rm -f "$f" "$metadata"
  else
    idle_pcm
  fi
done
