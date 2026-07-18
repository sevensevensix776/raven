# Huginn voice config — sourced by synthd and writer.
# Change a value, then: ~/code/experiments/raven/stop.sh && ~/code/experiments/raven/start.sh

# Voice backend: kokoro | say
VOICE_BACKEND=kokoro

# Kokoro voice (af_heart is the warm default; alternates: am_michael, bf_emma, am_puck)
KOKORO_VOICE=af_heart
KOKORO_MODEL=prince-canuma/Kokoro-82M

# Fallback voice if Kokoro fails mid-synth (never go silent)
SAY_VOICE=Samantha

# Summarize replies before speaking: 1=on 0=off. Off until separately verified.
SUMMARIZE=0
SUMMARY_MODEL=qwen3:1.7b

# Between replies: noise = proven low floor that keeps the app alive;
# silence = true digital silence (kills the static) — GATED on a device test.
IDLE_FLOOR=noise

# Max characters spoken per reply. 700 was clipping real replies mid-sentence.
# 2500 ~= up to ~3 min of speech. For very long replies, turn SUMMARIZE on.
MAX_SPOKEN_CHARS=0   # 0 = no cap (speak the whole reply)

# Drop idle sessions from the channel picker after this many hours (backstop for
# abrupt closes; clean quits are removed instantly via the SessionEnd hook).
CHANNEL_TTL_HOURS=6
