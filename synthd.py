#!/usr/bin/env python3
"""Huginn synthesis daemon.

Watches $RAVEN_HOME/queue for *.txt (dropped by the Stop hook) and converts each
to a *.wav the writer muxes into the stream. Keeps the Kokoro model loaded so
synthesis is ~0.1s warm instead of ~2.7s cold per reply.

Design:
- Kokoro is the voice; on ANY synth error it falls back to macOS `say`, so a
  reply is never dropped to silence.
- Reads config.sh each pass (cheap) so voice/backend changes need no restart of
  logic, only re-source. Model is loaded once for the configured Kokoro model.
- Atomic: writes to <stamp>.wav.part then renames, so the writer never grabs a
  half-written file.
"""
import os
import subprocess
import time
from pathlib import Path

import ravenlog

SPEECH = Path(os.environ.get("RAVEN_HOME") or Path.home() / "code" / "experiments" / "raven")
QUEUE = SPEECH / "queue"
CONFIG = SPEECH / "config.sh"


def load_config() -> dict:
    cfg = {
        "VOICE_BACKEND": "kokoro",
        "KOKORO_VOICE": "af_heart",
        "KOKORO_MODEL": "prince-canuma/Kokoro-82M",
        "SAY_VOICE": "Samantha",
        "SUMMARIZE": "0",
        "SUMMARY_MODEL": "qwen3:1.7b",
    }
    try:
        for line in CONFIG.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            cfg[k.strip()] = v.strip().strip('"').strip("'")
    except FileNotFoundError:
        pass
    return cfg


def summarize(text: str, model: str) -> str:
    """One spoken sentence. Guarded: empty/failed -> original text.

    /no_think disables Qwen3's reasoning trace, which otherwise burns the whole
    budget inside <think> and can emit an empty summary.
    """
    prompt = (
        "/no_think Summarize the following in ONE short spoken sentence for a "
        "driver listening hands-free. No preamble, no markdown, just the "
        "sentence.\n\n" + text
    )
    try:
        r = subprocess.run(
            ["ollama", "run", model, prompt],
            capture_output=True, text=True, timeout=20,
        )
        out = r.stdout.strip()
        # strip any leaked <think>...</think>
        if "</think>" in out:
            out = out.split("</think>", 1)[1].strip()
        return out if out else text
    except Exception:
        return text


class Synth:
    def __init__(self):
        self.model = None
        self.model_id = None

    def kokoro(self, text: str, voice: str, model_id: str, out: Path) -> bool:
        import numpy as np
        import soundfile as sf
        from mlx_audio.tts.utils import load_model
        if self.model is None or self.model_id != model_id:
            self.model = load_model(model_id)
            self.model_id = model_id
        segs = [r.audio for r in self.model.generate(
            text=text, voice=voice, speed=1.0, verbose=False)]
        if not segs:
            return False
        # Explicit format: out is a ".wav.part" temp name and soundfile can't
        # infer WAV from that extension — it would raise and force say-fallback.
        sf.write(str(out), np.concatenate(segs), 24000, format="WAV")
        return True

    def say(self, text: str, voice: str, out: Path) -> bool:
        aiff = out.with_suffix(".aiff.part")
        subprocess.run(["say", "-v", voice, "-o", str(aiff), text],
                       check=True, timeout=30)
        os.replace(aiff, out.with_suffix(".aiff"))
        return True


def main():
    synth = Synth()
    # Warm the model at boot so the FIRST reply of a drive is ~0.1s, not ~5s.
    cfg = load_config()
    if cfg["VOICE_BACKEND"] == "kokoro":
        try:
            import tempfile
            tmp = Path(tempfile.gettempdir()) / "huginn_warm.wav.part"
            synth.kokoro("Ready.", cfg["KOKORO_VOICE"], cfg["KOKORO_MODEL"], tmp)
            tmp.unlink(missing_ok=True)
            print("[synthd] kokoro warmed", flush=True)
        except Exception as e:
            print(f"[synthd] warmup skipped: {e}", flush=True)

    while True:
        txts = sorted(QUEUE.glob("*.txt"))
        if not txts:
            time.sleep(0.2)
            continue
        cfg = load_config()
        f = txts[0]
        try:
            text = f.read_text().strip()
        except FileNotFoundError:
            continue
        if not text:
            f.unlink(missing_ok=True)
            continue

        if cfg["SUMMARIZE"] == "1":
            text = summarize(text, cfg["SUMMARY_MODEL"])

        stamp = f.stem
        wav_part = QUEUE / f"{stamp}.wav.part"
        wav = QUEUE / f"{stamp}.wav"
        done = False
        backend = "none"
        t0 = time.time()
        if cfg["VOICE_BACKEND"] == "kokoro":
            try:
                if synth.kokoro(text, cfg["KOKORO_VOICE"], cfg["KOKORO_MODEL"], wav_part):
                    os.replace(wav_part, wav)
                    done = True
                    backend = "kokoro"
            except Exception as e:
                print(f"[synthd] kokoro failed, say fallback: {e}", flush=True)
                ravenlog.log("synthd", "kokoro_fail", id=stamp, err=str(e)[:200])
        if not done:
            try:
                synth.say(text, cfg["SAY_VOICE"], wav)  # writes .aiff
                backend = "say"
                done = True
            except Exception as e:
                print(f"[synthd] say fallback failed: {e}", flush=True)
                ravenlog.log("synthd", "say_fail", id=stamp, err=str(e)[:200])
        ravenlog.log("synthd", "synth", id=stamp, backend=backend, ok=done,
                     ms=round((time.time() - t0) * 1000), chars=len(text))
        f.unlink(missing_ok=True)
        wav_part.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
