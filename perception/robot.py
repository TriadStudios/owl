import collections
import http.client
import io
import json
import os
import re
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid
import wave
from datetime import datetime
from pathlib import Path

import cv2
import numpy as np
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from reachy_mini.vision.face_detector import FaceDetector
from reachy_mini.utils import create_head_pose
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
SCRIBE_URL = "https://api.elevenlabs.io/v1/speech-to-text"
SCRIBE_MODEL = "scribe_v1"
SCRIBE_TIMEOUT_S = 2.0
MAX_KEYTERMS = 40
KEYTERM = re.compile(r"[A-Za-z0-9][A-Za-z0-9 ]{0,39}")
BASE_KEYTERMS = ["port", "pull", "unplug", "uplink", "cable", *(f"port {n}" for n in ("one", "two", "three", "four", "five", "six", "seven", "eight"))]
ELEVENLABS_URL = "https://api.elevenlabs.io/v1/text-to-speech/{voice}/stream?output_format=pcm_16000"
ELEVENLABS_MODEL = "eleven_flash_v2_5"
ELEVENLABS_DEFAULT_VOICE = "21m00Tcm4TlvDq8ikWAM"
PCM_CHUNK_BYTES = 3200
MOTION_SIZE = (160, 90)
MOTION_PIXEL = 25
MOTION_FRACTION = 0.004
MOTION_SAME_FRAME = 0.5
MOTION_FRESH_S = 1.0
MOTION_HITS = 3
MOTION_WINDOW_S = 1.0
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
FACE_SEARCH_Y = 0.3
FACE_SEARCH_STEPS = 2
DAEMON_PORT = 8000
DOA_EVERY_S = 0.1
DOA_TIMEOUT_S = 2.0
VOICE_QUIET_AFTER_SAY_S = 1.0
VOICE_MAX_YAW_DEG = 55
VOICE_PITCH_DEG = -10
FACE_SEARCH_WAIT_S = 0.5
FACE_SEARCH_SIDE = 0.5
FACE_SEARCH_TOP = 0.25
DEBUG_DIR = segment.ROOT / "data" / "debug" / "robot"
DEBUG_FPS = 10
DEBUG_SIZE = (640, 360)
DEBUG_PREROLL_S = 3
DEBUG_GRID_MAX = 300

app = FastAPI()
state: dict[str, object] = {
    "mini": None, "error": None, "moving_at": 0.0, "face_at": 0.0, "face": None, "face_box": None, "centroid": [0.5, 0.5],
    "frame_at": 0.0, "head_busy_until": 0.0, "tracking": False, "look": None, "activity": "idle",
    "voice_at": 0.0, "voice_angle": float(np.pi / 2), "said_until": 0.0,
}
audio: collections.deque[tuple[float, np.ndarray]] = collections.deque()
audio_lock = threading.Lock()
head_lock = threading.Lock()
voice_lock = threading.Lock()
debug: dict[str, object] = {
    "on": False, "dir": None, "writer": None, "events": None, "clips": 0, "next_at": 0.0,
    "preroll": collections.deque(maxlen=DEBUG_PREROLL_S * DEBUG_FPS), "grid": collections.deque(), "grid_session": [],
}
debug_lock = threading.Lock()


def debug_start() -> None:
    if not debug["on"]:
        return
    with debug_lock:
        if debug["dir"] is not None:
            return
        folder = DEBUG_DIR / datetime.now().strftime("%Y%m%dT%H%M%S")
        folder.mkdir(parents=True, exist_ok=True)
        writer = cv2.VideoWriter(str(folder / "camera.mp4"), cv2.VideoWriter_fourcc(*"mp4v"), DEBUG_FPS, DEBUG_SIZE)
        for image in debug["preroll"]:
            writer.write(image)
        debug["preroll"].clear()
        debug["grid_session"] = list(debug["grid"])
        debug["grid"].clear()
        debug.update(dir=folder, writer=writer, events=(folder / "events.jsonl").open("a", encoding="utf-8"), clips=0)
    print(f"debug recording {folder}", flush=True)


def debug_grid(gray: np.ndarray, fraction: float) -> None:
    if not debug["on"]:
        return
    with debug_lock:
        entry = (time.time(), gray.astype(np.uint8), fraction)
        if debug["dir"] is not None:
            if len(debug["grid_session"]) < DEBUG_GRID_MAX:
                debug["grid_session"].append(entry)
            return
        debug["grid"].append(entry)
        while debug["grid"] and debug["grid"][0][0] < entry[0] - DEBUG_PREROLL_S:
            debug["grid"].popleft()


def debug_stop() -> None:
    with debug_lock:
        if debug["dir"] is None:
            return
        grid = debug["grid_session"]
        if grid:
            np.savez_compressed(debug["dir"] / "motion.npz", t=np.array([g[0] for g in grid]), frames=np.stack([g[1] for g in grid]), fraction=np.array([g[2] for g in grid]))
        debug["grid_session"] = []
        debug["writer"].release()
        debug["events"].close()
        print(f"debug saved {debug['dir']}", flush=True)
        debug.update(dir=None, writer=None, events=None)


def debug_event(name: str, **fields) -> None:
    with debug_lock:
        if debug["events"] is not None:
            debug["events"].write(json.dumps({"t": datetime.now().isoformat(timespec="milliseconds"), "event": name, **fields}) + "\n")
            debug["events"].flush()


def debug_clip(samples: np.ndarray) -> str | None:
    with debug_lock:
        if debug["dir"] is None:
            return None
        debug["clips"] += 1
        path = debug["dir"] / f"heard-{debug['clips']}.wav"
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(AUDIO_RATE)
        w.writeframes((np.clip(samples, -1, 1) * 32767).astype("<i2").tobytes())
    return path.name


def debug_frame(frame: np.ndarray, now: float) -> None:
    if not debug["on"] or now < debug["next_at"]:
        return
    debug["next_at"] = now + 1 / DEBUG_FPS
    image = cv2.resize(frame, DEBUG_SIZE, interpolation=cv2.INTER_AREA)
    w, h = DEBUG_SIZE
    face = now - state["face_at"] < PRESENCE_FRESH_S
    moving = now - state["moving_at"] < MOTION_FRESH_S
    if state["tracking"]:
        dx, dy = TRACK_DEADBAND
        cv2.rectangle(image, (int((0.5 - dx) * w), int((0.5 - dy) * h)), (int((0.5 + dx) * w), int((0.5 + dy) * h)), (255, 255, 0), 1)
    if face and state["face_box"]:
        bx, by, bw, bh = state["face_box"]
        cv2.rectangle(image, (int(bx * w), int(by * h)), (int((bx + bw) * w), int((by + bh) * h)), (0, 255, 0), 2)
        cv2.circle(image, (int(state["face"][0] * w), int(state["face"][1] * h)), 4, (0, 255, 0), -1)
    if moving:
        cx, cy = int(state["centroid"][0] * w), int(state["centroid"][1] * h)
        cv2.drawMarker(image, (cx, cy), (0, 165, 255), cv2.MARKER_CROSS, 18, 2)
    if state["look"] and now - state["look"][2] < 1.0:
        cv2.circle(image, (int(state["look"][0] * w), int(state["look"][1] * h)), 10, (0, 0, 255), 2)
    voice = f"{state['voice_angle']:.2f} rad" if now - state["voice_at"] < PRESENCE_FRESH_S else "no"
    lines = [
        datetime.now().strftime("%H:%M:%S.%f")[:-3],
        f"{state['activity']}",
        f"face {'yes' if face else 'no'}  motion {'yes' if moving else 'no'}  voice {voice}  tracking {'on' if state['tracking'] else 'off'}  head {'moving' if now < state['head_busy_until'] else 'still'}",
    ]
    for i, line in enumerate(lines):
        cv2.putText(image, line[:90], (8, 18 + 18 * i), cv2.FONT_HERSHEY_SIMPLEX, 0.45, (0, 0, 0), 3, cv2.LINE_AA)
        cv2.putText(image, line[:90], (8, 18 + 18 * i), cv2.FONT_HERSHEY_SIMPLEX, 0.45, (255, 255, 255), 1, cv2.LINE_AA)
    with debug_lock:
        if debug["writer"] is not None:
            debug["writer"].write(image)
        else:
            debug["preroll"].append(image)


def mini():
    if state["error"]:
        raise RuntimeError(state["error"])
    if state["mini"] is None:
        raise RuntimeError("bridge not connected")
    return state["mini"]


def move_head(fn, seconds: float, busy: bool = True) -> None:
    with head_lock:
        if busy:
            state["head_busy_until"] = time.monotonic() + seconds + SETTLE_S
        try:
            fn()
        except TimeoutError as error:
            print(f"move skipped: {error}", flush=True)
            debug_event("move_timeout", seconds=seconds, err=str(error))


def watch_camera(m) -> None:
    detector, tracker = FaceDetector(), Tracker()
    history: collections.deque[np.ndarray] = collections.deque(maxlen=2)
    motion_hits: collections.deque[float] = collections.deque()
    next_face, next_track = 0.0, 0.0
    try:
        for frame in capture.frames(m, float("inf")):
            now = time.monotonic()
            height, width = frame.shape[:2]
            state["frame_size"], state["frame_at"] = (width, height), now
            gray = cv2.cvtColor(cv2.resize(frame, MOTION_SIZE, interpolation=cv2.INTER_AREA), cv2.COLOR_BGR2GRAY).astype(np.int16)
            if now <= state["head_busy_until"]:
                history.clear()
            elif not history or np.abs(gray - history[-1]).mean() >= MOTION_SAME_FRAME:
                fraction = 0.0
                if len(history) == 2:
                    moved = (np.abs(history[1] - history[0]) > MOTION_PIXEL) & (np.abs(gray - history[0]) > MOTION_PIXEL)
                    fraction = float(moved.mean())
                    if fraction > MOTION_FRACTION:
                        motion_hits.append(now)
                        while motion_hits and motion_hits[0] < now - MOTION_WINDOW_S:
                            motion_hits.popleft()
                        if len(motion_hits) >= MOTION_HITS:
                            ys, xs = np.nonzero(moved)
                            state["centroid"] = [float(xs.mean() + 0.5) / MOTION_SIZE[0], float(ys.mean() + 0.5) / MOTION_SIZE[1]]
                            state["moving_at"] = now
                debug_grid(gray, fraction)
                history.append(gray)
            if now >= next_face:
                next_face = now + FACE_EVERY_S
                scale = FACE_WIDTH / width
                small = cv2.resize(frame, None, fx=scale, fy=scale, interpolation=cv2.INTER_AREA)
                sw, sh = small.shape[1], small.shape[0]
                face = tracker.select(detector.detect(small), sw, sh)
                if face is not None:
                    bx, by, bw, bh = face.bbox
                    state["face"], state["face_box"], state["face_at"] = [face.nose[0] / sw, face.nose[1] / sh], [bx / sw, by / sh, bw / sw, bh / sh], now
            if state["tracking"] and now >= next_track and now - state["face_at"] < FACE_EVERY_S * 2 and now > state["head_busy_until"]:
                x, y = state["face"]
                if abs(x - 0.5) > TRACK_DEADBAND[0] or abs(y - 0.5) > TRACK_DEADBAND[1]:
                    next_track = now + TRACK_EVERY_S
                    look(m, x, y, TRACK_MOVE_S)
            debug_frame(frame, now)
            time.sleep(0.03)
    except Exception as error:
        state["error"] = f"camera watcher stopped: {error}"


def look(m, x: float, y: float, seconds: float) -> None:
    width, height = state.get("frame_size", (1280, 720))
    u, v = int(min(max(x, 0.02), 0.98) * width), int(min(max(y, 0.02), 0.98) * height)
    state["look"] = (x, y, time.monotonic())
    debug_event("look", x=round(x, 3), y=round(y, 3), seconds=seconds)
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


def wav_bytes(samples: np.ndarray) -> bytes:
    buf = io.BytesIO()
    with wave.open(buf, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(AUDIO_RATE)
        w.writeframes((np.clip(samples, -1, 1) * 32767).astype("<i2").tobytes())
    return buf.getvalue()


def scribe(clip: np.ndarray, key: str, keyterms: list[str]) -> str:
    boundary = uuid.uuid4().hex
    fields = [("model_id", SCRIBE_MODEL), ("language_code", "en"), ("tag_audio_events", "false")] + [("keyterms", k) for k in keyterms]
    body = b"".join(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode() for name, value in fields)
    body += f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="heard.wav"\r\nContent-Type: audio/wav\r\n\r\n'.encode() + wav_bytes(clip) + f"\r\n--{boundary}--\r\n".encode()
    request = urllib.request.Request(SCRIBE_URL, data=body, headers={"xi-api-key": key, "Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        with urllib.request.urlopen(request, timeout=SCRIBE_TIMEOUT_S) as response:
            return str(json.load(response).get("text", "")).strip()
    except urllib.error.HTTPError as error:
        raise RuntimeError(f"Scribe returned {error.code}: {error.read().decode(errors='replace')[:200]}") from error


def transcribe(clip: np.ndarray, keyterms: list[str]) -> tuple[str, str]:
    key = segment.load_env().get("ELEVENLABS_API_KEY", "").strip()
    if key:
        try:
            return scribe(clip, key, BASE_KEYTERMS + keyterms), "scribe"
        except (OSError, ValueError, RuntimeError) as error:
            print(f"scribe failed, using whisper: {error}", flush=True)
            debug_event("scribe_fallback", err=str(error)[:200])
    return speech_model()({"raw": clip, "sampling_rate": AUDIO_RATE}, generate_kwargs={"max_new_tokens": 48})["text"].strip(), "whisper"


def listen(keyterms: list[str]) -> dict[str, object]:
    with voice_lock:
        mini()
        state["activity"] = "listening"
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
        stt, stt_s = "", 0.0
        if began is not None:
            clip = samples[max(0, int((began * VAD_FRAME_S - 0.3) * AUDIO_RATE)):]
            stt_start = time.monotonic()
            text, stt = transcribe(clip, keyterms)
            stt_s = time.monotonic() - stt_start
        took = time.monotonic() - start
        print(f"heard {text!r} via {stt or '-'} in {stt_s:.2f} s, {took:.1f} s total (threshold {threshold:.3f}, floor {floor:.3f})", flush=True)
        state["activity"] = f"heard: {text}"
        debug_event("listen", text=text, stt=stt, stt_s=round(stt_s, 2), took_s=round(took, 2), threshold=round(threshold, 4), floor=round(floor, 4), speech_at_s=None if began is None else round(began * VAD_FRAME_S, 2), clip=debug_clip(samples))
    return {"text": text, "stt": stt}


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
        state["activity"] = f"saying: {text}"
        requested = time.monotonic()

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
                angles = [side * ANTENNA_RAD, -side * ANTENNA_RAD]
                move_head(lambda: m.goto_target(antennas=angles, duration=ANTENNA_EVERY_S * 0.9), ANTENNA_EVERY_S, busy=False)
                side = -side
            else:
                time.sleep(ANTENNA_EVERY_S)
        move_head(lambda: m.goto_target(antennas=[0.0, 0.0], duration=0.3), 0.3, busy=False)
        state["activity"], state["said_until"] = "idle", time.monotonic() + VOICE_QUIET_AFTER_SAY_S
        voice = "elevenlabs" if key else "windows"
        debug_event("say", text=text, voice=voice, seconds=round(seconds, 2), first_audio_ms=round(((first[0] if first else time.monotonic()) - requested) * 1000))
    return {"ok": True, "seconds": round(seconds, 1), "voice": voice}


def face_seen(within: float) -> bool:
    return time.monotonic() - state["face_at"] < within


def voice_heard(within: float) -> bool:
    return time.monotonic() - state["voice_at"] < within


def watch_voice(host: str) -> None:
    conn = http.client.HTTPConnection(host, DAEMON_PORT, timeout=DOA_TIMEOUT_S)
    failures = 0
    while True:
        time.sleep(DOA_EVERY_S)
        try:
            conn.request("GET", "/api/state/doa")
            doa = json.loads(conn.getresponse().read())
        except (OSError, ValueError, http.client.HTTPException) as error:
            conn.close()
            failures += 1
            if failures in (1, 10, 100) or failures % 1000 == 0:
                print(f"voice direction unavailable ({failures} failures): {error}", flush=True)
            continue
        now = time.monotonic()
        if doa and doa.get("speech_detected") and not state["activity"].startswith("saying") and now > state["said_until"]:
            state["voice_at"], state["voice_angle"] = now, float(doa["angle"])


def turn(x: float, y: float) -> dict[str, object]:
    m = mini()
    debug_start()
    state["activity"] = "turning"
    how = "face"
    if face_seen(PRESENCE_FRESH_S):
        x, y = state["face"]
    elif voice_heard(PRESENCE_FRESH_S):
        x, how = 0.5, "voice"
    else:
        y, how = FACE_SEARCH_Y, "motion x, looking up"
    if how == "voice":
        yaw = float(np.clip(np.degrees(np.pi / 2 - state["voice_angle"]), -VOICE_MAX_YAW_DEG, VOICE_MAX_YAW_DEG))
        debug_event("turn", how=how, doa_rad=round(state["voice_angle"], 3), yaw_deg=round(yaw, 1), pitch_deg=VOICE_PITCH_DEG)
        pose = create_head_pose(yaw=yaw, pitch=VOICE_PITCH_DEG)
        move_head(lambda: m.goto_target(head=pose, duration=TURN_S), TURN_S)
    else:
        debug_event("turn", target=[round(x, 3), round(y, 3)], how=how)
        look(m, x, y, TURN_S)
    time.sleep(TURN_S + SETTLE_S)
    for step in range(FACE_SEARCH_STEPS):
        time.sleep(FACE_SEARCH_WAIT_S)
        if face_seen(FACE_SEARCH_WAIT_S + SETTLE_S):
            break
        debug_event("search", step=step + 1)
        look(m, 0.5 + (x - 0.5) * FACE_SEARCH_SIDE, FACE_SEARCH_TOP, TURN_S)
        time.sleep(TURN_S + SETTLE_S)
    found = face_seen(1.5)
    if found:
        look(m, *state["face"], TURN_S / 2)
    state["tracking"], state["activity"] = True, "idle"
    debug_event("face" if found else "no_face")
    return {"ok": True, "face": found}


def home() -> dict[str, object]:
    state["tracking"], state["activity"] = False, "going home"
    m = mini()
    debug_event("home")
    move_head(lambda: capture.home(m), capture.HOME_MOVE_S * capture.HOME_PASSES)
    state["activity"] = "idle"
    debug_stop()
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
    return {"ok": True, "frame_age_s": round(time.monotonic() - state["frame_at"], 1), "debug": debug["on"], "voice": "elevenlabs" if segment.load_env().get("ELEVENLABS_API_KEY", "").strip() else "windows"}


@app.get("/motion")
def motion() -> dict[str, object]:
    if state["error"]:
        raise HTTPException(503, str(state["error"]))
    now = time.monotonic()
    face = now - state["face_at"] < PRESENCE_FRESH_S
    moving = now - state["moving_at"] < MOTION_FRESH_S
    voice = now - state["voice_at"] < PRESENCE_FRESH_S
    return {"moving": face or moving or voice, "face": face, "voice": voice, "voice_angle": round(state["voice_angle"], 3), "centroid": state["face"] if face else state["centroid"]}


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
async def listen_once(request: Request) -> dict[str, object]:
    body = await request.body()
    keyterms = json.loads(body).get("keyterms", []) if body else []
    if not isinstance(keyterms, list) or len(keyterms) > MAX_KEYTERMS or not all(isinstance(k, str) and KEYTERM.fullmatch(k) for k in keyterms):
        raise HTTPException(400, f"keyterms: at most {MAX_KEYTERMS} strings of letters, digits and spaces, 1-40 long")
    return await run(listen, keyterms)


def main() -> None:
    debug["on"] = segment.load_env().get("OWL_ROBOT_DEBUG", "").strip() == "1"
    m = capture.connect(segment.load_env())
    m.__enter__()
    state["mini"] = m
    capture.home(m)
    speech_model()
    threading.Thread(target=watch_audio, args=(m,), daemon=True).start()
    threading.Thread(target=watch_camera, args=(m,), daemon=True).start()
    threading.Thread(target=watch_voice, args=(segment.load_env().get("OWL_REACHY_HOST", "").strip(),), daemon=True).start()
    uvicorn.run(app, host="127.0.0.1", port=8702)
