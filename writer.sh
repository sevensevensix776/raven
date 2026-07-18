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
  find queue \( -name '*.txt' -o -name '*.aiff' -o -name '*.wav' \) -mmin +10 -delete 2>/dev/null

  # synthd (warm Kokoro) turns queued *.txt into ready *.wav/*.aiff. Consume the
  # ready audio, oldest first (timestamp-named). Fall back to say-ing a *.txt
  # ourselves ONLY if it has waited >5s — meaning synthd is down. Never silent.
  f=$(ls -1 queue/*.wav queue/*.aiff 2>/dev/null | head -1)
  if [ -z "$f" ]; then
    t=$(ls -1 queue/*.txt 2>/dev/null | head -1)
    if [ -n "$t" ]; then
      age=$(( $(date +%s) - $(stat -f %m "$t") ))
      [ "$age" -ge 5 ] && f="$t"
    fi
  fi

  if [ "$live" = "1" ] && [ -n "$f" ]; then
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
    rm -f "$f"
  else
    ffmpeg -loglevel quiet -f lavfi -i "anoisesrc=r=24000:a=0.002:d=0.25" \
           -f s16le -ar 24000 -ac 1 -acodec pcm_s16le -
  fi
done
