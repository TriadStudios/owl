import os
import subprocess
import tempfile
import threading
import time
import wave
from pathlib import Path

import cv2
import numpy as np
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from starlette.concurrency import run_in_threadpool

import capture
import segment

STT_MODEL = "openai/whisper-small.en"
STT_RATE = 16000
LISTEN_S = 6.0
SILENCE_PEAK = 0.02
MAX_SAY_CHARS = 500
MOTION_SIZE = (160, 90)
MOTION_PIXEL = 25
MOTION_FRACTION = 0.01
MOTION_FRESH_S = 1.0
TURN_S = 1.0

app = FastAPI()
state: dict[str, object] = {"mini": None, "moving": False, "centroid": [0.5, 0.5], "at": 0.0, "error": None}
robot_lock = threading.Lock()


def watch_motion(mini) -> None:
    previous = None
    try:
        for frame in capture.frames(mini, float("inf")):
            gray = cv2.cvtColor(cv2.resize(frame, MOTION_SIZE, interpolation=cv2.INTER_AREA), cv2.COLOR_BGR2GRAY).astype(np.int16)
            if previous is not None:
                changed = np.abs(gray - previous) > MOTION_PIXEL
                moving = changed.mean() > MOTION_FRACTION
                if moving:
                    ys, xs = np.nonzero(changed)
                    state["centroid"] = [float(xs.mean() + 0.5) / MOTION_SIZE[0], float(ys.mean() + 0.5) / MOTION_SIZE[1]]
                state["moving"] = moving
                state["at"] = time.monotonic()
                state["frame_size"] = frame.shape[1::-1]
            previous = gray
            time.sleep(0.1)
    except Exception as error:
        state["error"] = f"motion watcher stopped: {error}"


def speech_model():
    if "stt" not in state:
        import torch
        from transformers import pipeline

        state["stt"] = pipeline("automatic-speech-recognition", model=STT_MODEL, dtype=torch.float16, device="cuda")
    return state["stt"]


def mini():
    if state["error"]:
        raise RuntimeError(state["error"])
    if state["mini"] is None:
        raise RuntimeError("bridge not connected")
    return state["mini"]


def turn(x: float, y: float) -> dict[str, object]:
    with robot_lock:
        m = mini()
        width, height = state.get("frame_size", (1280, 720))
        m.look_at_image(int(min(max(x, 0.02), 0.98) * width), int(min(max(y, 0.02), 0.98) * height), duration=TURN_S)
        time.sleep(TURN_S)
    return {"ok": True}


def home() -> dict[str, object]:
    with robot_lock:
        capture.home(mini())
    return {"ok": True}


def say(text: str) -> dict[str, object]:
    with robot_lock, tempfile.TemporaryDirectory() as tmp:
        wav = Path(tmp) / "owl-say.wav"
        script = (
            "Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; "
            "$s.SetOutputToWaveFile($env:OWL_WAV); $s.Speak([Console]::In.ReadToEnd()); $s.Dispose()"
        )
        subprocess.run(["powershell", "-NoProfile", "-Command", script], input=text, text=True, check=True, timeout=30, env={**os.environ, "OWL_WAV": str(wav)})
        with wave.open(str(wav)) as w:
            seconds = w.getnframes() / w.getframerate()
        mini().media.play_sound(str(wav))
        time.sleep(seconds + 0.3)
    return {"ok": True, "seconds": round(seconds, 1)}


def listen() -> dict[str, object]:
    with robot_lock:
        m = mini()
        rate = m.media.get_input_audio_samplerate()
        m.media.start_recording()
        chunks = []
        end = time.monotonic() + LISTEN_S
        try:
            while time.monotonic() < end:
                sample = m.media.get_audio_sample()
                if sample is not None:
                    chunks.append(sample.reshape(len(sample), -1).mean(axis=1))
                else:
                    time.sleep(0.01)
        finally:
            m.media.stop_recording()
    if not chunks or rate <= 0:
        raise RuntimeError("no audio from the robot microphone")
    audio = np.concatenate(chunks).astype(np.float32)
    audio16 = np.interp(np.arange(0, len(audio) * STT_RATE / rate) * rate / STT_RATE, np.arange(len(audio)), audio).astype(np.float32)
    peak = float(np.abs(audio).max())
    text = "" if peak < SILENCE_PEAK else speech_model()({"raw": audio16, "sampling_rate": STT_RATE}, generate_kwargs={"max_new_tokens": 48})["text"].strip()
    print(f"heard {text!r} ({len(audio) / rate:.1f} s, peak {peak:.3f})")
    return {"text": text}


async def run(fn, *args) -> dict[str, object]:
    try:
        return await run_in_threadpool(fn, *args)
    except (RuntimeError, subprocess.SubprocessError, OSError) as error:
        raise HTTPException(503, str(error)) from error


@app.get("/health")
def health() -> dict[str, object]:
    if state["error"] or state["mini"] is None:
        raise HTTPException(503, str(state["error"] or "bridge not connected"))
    return {"ok": True, "motion_age_s": round(time.monotonic() - state["at"], 1)}


@app.get("/motion")
def motion() -> dict[str, object]:
    if state["error"]:
        raise HTTPException(503, str(state["error"]))
    fresh = time.monotonic() - state["at"] < MOTION_FRESH_S
    return {"moving": bool(state["moving"]) and fresh, "centroid": state["centroid"]}


@app.post("/turn")
async def turn_to(request: Request) -> dict[str, object]:
    body = await request.json()
    x, y = body.get("x"), body.get("y")
    if not all(isinstance(v, (int, float)) and 0 <= v <= 1 for v in (x, y)):
        raise HTTPException(400, "want {x, y} in [0, 1] of the frame")
    return await run(turn, float(x), float(y))


@app.post("/home")
async def go_home() -> dict[str, object]:
    return await run(home)


@app.post("/say")
async def say_text(request: Request) -> dict[str, object]:
    text = (await request.json()).get("text")
    if not isinstance(text, str) or not text.strip() or len(text) > MAX_SAY_CHARS:
        raise HTTPException(400, f"want {{text}} of 1-{MAX_SAY_CHARS} characters")
    return await run(say, text.strip())


@app.post("/listen")
async def listen_once() -> dict[str, object]:
    return await run(listen)


def main() -> None:
    m = capture.connect(segment.load_env())
    m.__enter__()
    state["mini"] = m
    capture.home(m)
    speech_model()
    threading.Thread(target=watch_motion, args=(m,), daemon=True).start()
    uvicorn.run(app, host="127.0.0.1", port=8702)
