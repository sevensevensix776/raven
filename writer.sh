#!/bin/bash
# Emits raw PCM forever: comfort noise, interrupted by queued speech.
#
# Comfort noise (not silence) is load-bearing: car head units and BT sinks
# mute on digital silence and reactivate late, chopping the first word.
# a=0.002 measured at ~-64.8dB, survives AAC@32k without being audible.
#
# Listener gating via .heartbeat: a live HLS client polls the playlist every
# ~2s. No poll in 10s => nobody listening => hold the queue instead of
# broadcasting into the void. Turns a broadcast into a queue, zero JS on phone.
cd ~/speech || exit 1
HB=hls/.heartbeat

while true; do
  live=0
  if [ -f "$HB" ]; then
    age=$(( $(date +%s) - $(stat -f %m "$HB") ))
    [ "$age" -le 10 ] && live=1
  fi

  # Drop stale replies — don't read 20-minute-old news on reconnect.
  find queue \( -name '*.txt' -o -name '*.aiff' \) -mmin +10 -delete 2>/dev/null

  f=$(ls -1 queue/*.txt queue/*.aiff 2>/dev/null | head -1)

  if [ "$live" = "1" ] && [ -n "$f" ]; then
    # Synthesis lives here, not in the hook: the hook has a 2s budget and
    # `say` on a long reply blows it. Here it can take as long as it needs.
    if [ "${f##*.}" = "txt" ]; then
      a=$(mktemp -t spk)
      if say -o "$a.aiff" "$(cat "$f")" 2>/dev/null; then
        ffmpeg -loglevel quiet -i "$a.aiff" -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
      fi
      rm -f "$a" "$a.aiff"
    else
      ffmpeg -loglevel quiet -i "$f" -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
    fi
    rm -f "$f"
  else
    ffmpeg -loglevel quiet -f lavfi -i "anoisesrc=r=24000:a=0.002:d=0.25" \
           -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
  fi
done
