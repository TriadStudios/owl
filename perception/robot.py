import collections
import json
import os
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import wave
from pathlib import Path

import cv2
import numpy as np
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from reachy_mini.vision.face_detector import FaceDetector
from reachy_mini.vision.face_tracking import Tracker
from starlette.concurrency import run_in_threadpool

import capture
import segment

STT_MODEL = "openai/whisper-small.en"
AUDIO_RATE = 16000
AUDIO_KEEP_S = 30.0
VAD_FRAME_S = 0.03
SPEECH_RMS = 0.02
SPEECH_OVER_FLOOR = 3.0
SPEECH_START_FRAMES = 3
SPEECH_END_S = 0.7
WAIT_FOR_SPEECH_S = 6.0
MAX_UTTERANCE_S = 8.0
PLAYBACK_TAIL_S = 0.4
MAX_SAY_CHARS = 500
ELEVENLABS_URL = "https://api.elevenlabs.io/v1/text-to-speech/{voice}/stream?output_format=pcm_16000"
ELEVENLABS_MODEL = "eleven_flash_v2_5"
ELEVENLABS_DEFAULT_VOICE = "21m00Tcm4TlvDq8ikWAM"
PCM_CHUNK_BYTES = 3200
MOTION_SIZE = (160, 90)
MOTION_PIXEL = 25
MOTION_FRACTION = 0.01
PRESENCE_FRESH_S = 1.0
FACE_WIDTH = 640
FACE_EVERY_S = 0.2
TRACK_DEADBAND = (0.08, 0.10)
TRACK_EVERY_S = 0.5
TRACK_MOVE_S = 0.4
TURN_S = 1.0
SETTLE_S = 0.3
ANTENNA_RAD = 0.35
ANTENNA_EVERY_S = 0.45

app = FastAPI()
state: dict[str, object] = {"mini": None, "error": None, "moving_at": 0.0, "face_at": 0.0, "face": None, "centroid": [0.5, 0.5], "frame_at": 0.0, "head_busy_until": 0.0, "tracking": False}
audio: collections.deque[tuple[float, np.ndarray]] = collections.deque()
audio_lock = threading.Lock()
head_lock = threading.Lock()
voice_lock = threading.Lock()


def mini():
    if state["error"]:
        raise RuntimeError(state["error"])
    if state["mini"] is None:
        raise RuntimeError("bridge not connected")
    return state["mini"]


def move_head(fn, seconds: float) -> None:
    with head_lock:
        state["head_busy_until"] = time.monotonic() + seconds + SETTLE_S
        fn()


def watch_camera(m) -> None:
    detector, tracker = FaceDetector(), Tracker()
    previous, next_face, next_track = None, 0.0, 0.0
    try:
        for frame in capture.frames(m, float("inf")):
            now = time.monotonic()
            height, width = frame.shape[:2]
            state["frame_size"], state["frame_at"] = (width, height), now
            gray = cv2.cvtColor(cv2.resize(frame, MOTION_SIZE, interpolation=cv2.INTER_AREA), cv2.COLOR_BGR2GRAY).astype(np.int16)
            if previous is not None and now > state["head_busy_until"]:
                changed = np.abs(gray - previous) > MOTION_PIXEL
                if changed.mean() > MOTION_FRACTION:
                    ys, xs = np.nonzero(changed)
                    state["centroid"] = [float(xs.mean() + 0.5) / MOTION_SIZE[0], float(ys.mean() + 0.5) / MOTION_SIZE[1]]
                    state["moving_at"] = now
            previous = gray
            if now >= next_face:
                next_face = now + FACE_EVERY_S
                scale = FACE_WIDTH / width
                small = cv2.resize(frame, None, fx=scale, fy=scale, interpolation=cv2.INTER_AREA)
                face = tracker.select(detector.detect(small), small.shape[1], small.shape[0])
                if face is not None:
                    state["face"], state["face_at"] = [face.nose[0] / small.shape[1], face.nose[1] / small.shape[0]], now
            if state["tracking"] and now >= next_track and now - state["face_at"] < FACE_EVERY_S * 2 and now > state["head_busy_until"]:
                x, y = state["face"]
                if abs(x - 0.5) > TRACK_DEADBAND[0] or abs(y - 0.5) > TRACK_DEADBAND[1]:
                    next_track = now + TRACK_EVERY_S
                    look(m, x, y, TRACK_MOVE_S)
            time.sleep(0.03)
    except Exception as error:
        state["error"] = f"camera watcher stopped: {error}"


def look(m, x: float, y: float, seconds: float) -> None:
    width, height = state.get("frame_size", (1280, 720))
    u, v = int(min(max(x, 0.02), 0.98) * width), int(min(max(y, 0.02), 0.98) * height)
    move_head(lambda: m.look_at_image(u, v, duration=seconds), seconds)


def watch_audio(m) -> None:
    try:
        while True:
            sample = m.media.get_audio_sample()
            if sample is None:
                time.sleep(0.005)
                continue
            now = time.monotonic()
            with audio_lock:
                audio.append((now, sample.reshape(len(sample), -1).mean(axis=1).astype(np.float32)))
                while audio and audio[0][0] < now - AUDIO_KEEP_S:
                    audio.popleft()
    except Exception as error:
        state["error"] = f"audio watcher stopped: {error}"


def audio_since(start: float) -> np.ndarray:
    with audio_lock:
        chunks = [chunk for at, chunk in audio if at >= start]
    return np.concatenate(chunks) if chunks else np.zeros(0, np.float32)


def speech_model():
    if "stt" not in state:
        import torch
        from transformers import pipeline

        state["stt"] = pipeline("automatic-speech-recognition", model=STT_MODEL, dtype=torch.float16, device="cuda")
    return state["stt"]


def frame_rms(samples: np.ndarray) -> np.ndarray:
    n = int(VAD_FRAME_S * AUDIO_RATE)
    usable = len(samples) // n * n
    return np.sqrt((samples[:usable].reshape(-1, n) ** 2).mean(axis=1)) if usable else np.zeros(0)


def listen() -> dict[str, object]:
    with voice_lock:
        mini()
        start = time.monotonic()
        floor = float(np.percentile(frame_rms(audio_since(start - 3.0)), 20)) if audio else 0.0
        threshold = max(SPEECH_RMS, SPEECH_OVER_FLOOR * floor)
        began, text = None, ""
        while True:
            time.sleep(VAD_FRAME_S * 3)
            rms = frame_rms(audio_since(start))
            loud = rms > threshold
            elapsed = len(rms) * VAD_FRAME_S
            if began is None:
                runs = np.convolve(loud, np.ones(SPEECH_START_FRAMES), "valid") >= SPEECH_START_FRAMES
                if runs.any():
                    began = int(np.argmax(runs))
                elif time.monotonic() - start > WAIT_FOR_SPEECH_S:
                    break
                continue
            quiet = len(loud) - 1 - int(np.flatnonzero(loud).max())
            if quiet * VAD_FRAME_S >= SPEECH_END_S or (len(loud) - began) * VAD_FRAME_S >= MAX_UTTERANCE_S:
                break
        samples = audio_since(start)
        if began is not None:
            clip = samples[max(0, int((began * VAD_FRAME_S - 0.3) * AUDIO_RATE)):]
            text = speech_model()({"raw": clip, "sampling_rate": AUDIO_RATE}, generate_kwargs={"max_new_tokens": 48})["text"].strip()
        print(f"heard {text!r} after {time.monotonic() - start:.1f} s (threshold {threshold:.3f}, floor {floor:.3f}, speech {'yes' if began is not None else 'no'})", flush=True)
    return {"text": text}


def elevenlabs(text: str, key: str, voice: str, play) -> None:
    request = urllib.request.Request(
        ELEVENLABS_URL.format(voice=voice),
        data=json.dumps({"text": text, "model_id": ELEVENLABS_MODEL}).encode(),
        headers={"xi-api-key": key, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            rest = b""
            while chunk := response.read(PCM_CHUNK_BYTES):
                data = rest + chunk
                usable = len(data) // 2 * 2
                rest = data[usable:]
                play(np.frombuffer(data[:usable], "<i2").astype(np.float32) / 32768)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"ElevenLabs returned {error.code}: {error.read().decode(errors='replace')[:300]}") from error


def windows_voice(text: str, play) -> None:
    with tempfile.TemporaryDirectory() as tmp:
        wav = Path(tmp) / "owl-say.wav"
        script = (
            "Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; "
            "$s.SetOutputToWaveFile($env:OWL_WAV); $s.Speak([Console]::In.ReadToEnd()); $s.Dispose()"
        )
        subprocess.run(["powershell", "-NoProfile", "-Command", script], input=text, text=True, check=True, timeout=30, env={**os.environ, "OWL_WAV": str(wav)})
        with wave.open(str(wav)) as w:
            rate, pcm = w.getframerate(), np.frombuffer(w.readframes(w.getnframes()), "<i2").reshape(-1, w.getnchannels()).mean(axis=1)
    play(np.interp(np.arange(0, len(pcm) * AUDIO_RATE / rate) * rate / AUDIO_RATE, np.arange(len(pcm)), pcm).astype(np.float32) / 32768)


def say(text: str) -> dict[str, object]:
    with voice_lock:
        m = mini()
        env = segment.load_env()
        key = env.get("ELEVENLABS_API_KEY", "").strip()
        pushed, first = [0], []

        def play(samples: np.ndarray) -> None:
            if not first:
                first.append(time.monotonic())
            m.media.push_audio_sample(samples)
            pushed[0] += len(samples)

        if key:
            elevenlabs(text, key, env.get("OWL_ELEVENLABS_VOICE", ELEVENLABS_DEFAULT_VOICE).strip(), play)
        else:
            windows_voice(text, play)
        seconds = pushed[0] / AUDIO_RATE
        end = (first[0] if first else time.monotonic()) + seconds + PLAYBACK_TAIL_S
        side = 1
        while time.monotonic() < end:
            if time.monotonic() > state["head_busy_until"]:
                m.goto_target(antennas=[side * ANTENNA_RAD, -side * ANTENNA_RAD], duration=ANTENNA_EVERY_S * 0.9)
                side = -side
            time.sleep(ANTENNA_EVERY_S)
        m.goto_target(antennas=[0.0, 0.0], duration=0.3)
    return {"ok": True, "seconds": round(seconds, 1), "voice": "elevenlabs" if key else "windows"}


def turn(x: float, y: float) -> dict[str, object]:
    m = mini()
    if time.monotonic() - state["face_at"] < PRESENCE_FRESH_S:
        x, y = state["face"]
    look(m, x, y, TURN_S)
    time.sleep(TURN_S)
    state["tracking"] = True
    return {"ok": True, "target": "face" if [x, y] == state["face"] else "motion"}


def home() -> dict[str, object]:
    state["tracking"] = False
    m = mini()
    move_head(lambda: capture.home(m), capture.HOME_MOVE_S * capture.HOME_PASSES)
    return {"ok": True}


async def run(fn, *args) -> dict[str, object]:
    try:
        return await run_in_threadpool(fn, *args)
    except (RuntimeError, subprocess.SubprocessError, OSError) as error:
        raise HTTPException(503, str(error)) from error


@app.get("/health")
def health() -> dict[str, object]:
    if state["error"] or state["mini"] is None:
        raise HTTPException(503, str(state["error"] or "bridge not connected"))
    return {"ok": True, "frame_age_s": round(time.monotonic() - state["frame_at"], 1), "voice": "elevenlabs" if segment.load_env().get("ELEVENLABS_API_KEY", "").strip() else "windows"}


@app.get("/motion")
def motion() -> dict[str, object]:
    if state["error"]:
        raise HTTPException(503, str(state["error"]))
    now = time.monotonic()
    face = now - state["face_at"] < PRESENCE_FRESH_S
    moving = now - state["moving_at"] < PRESENCE_FRESH_S
    return {"moving": face or moving, "face": face, "centroid": state["face"] if face else state["centroid"]}


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
    threading.Thread(target=watch_audio, args=(m,), daemon=True).start()
    threading.Thread(target=watch_camera, args=(m,), daemon=True).start()
    uvicorn.run(app, host="127.0.0.1", port=8702)
