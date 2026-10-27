import hashlib
import json
import threading
from datetime import datetime, timezone

import cv2
import numpy as np
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from starlette.concurrency import run_in_threadpool

import capture
import leds
import segment

MAX_IMAGE_BYTES = 20 * 2**20
MAX_BURST_S = 30
MAX_RECORD_S = 300
RECORD_START_TIMEOUT_S = 20
CAMERA_ATTEMPTS = 3
POLYGON_EPSILON_PX = 1.5
FRAMES = segment.ROOT / "data" / "frames"

app = FastAPI()
analyze_lock = threading.Lock()
camera_lock = threading.Lock()
recording: dict[str, object] = {}


def to_frame(x: float, y: float, rect: list[int]) -> list[float]:
    return [round(x / 2 + rect[0], 1), round(y / 2 + rect[1], 1)]


def describe(image_bytes: bytes) -> dict[str, object]:
    with analyze_lock:
        _, results, rect, _ = segment.analyze(image_bytes, segment.load_env())
    row = segment.ports(results)
    cables = results[segment.CABLE_PROMPT]
    ports = []
    for number, port in enumerate(row, start=1):
        x0, y0, x1, y1 = port["box"]
        bgr = segment.PORT_PALETTE[(number - 1) % len(segment.PORT_PALETTE)] if port["occupied"] else segment.EMPTY_GRAY
        entry = {
            "num": number,
            "occupied": port["occupied"],
            "box": to_frame(x0, y0, rect) + to_frame(x1, y1, rect),
            "color": "#{2:02x}{1:02x}{0:02x}".format(*bgr),
            "confidence": port["score"],
        }
        if port["cable"] is not None:
            contours, _ = cv2.findContours(segment.decode_rle(cables[port["cable"]]["mask_rle"]), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
            outline = cv2.approxPolyDP(max(contours, key=cv2.contourArea), POLYGON_EPSILON_PX, True)
            entry["cable_polygon"] = [to_frame(float(x), float(y), rect) for x, y in outline.reshape(-1, 2)]
        ports.append(entry)
    row_top, row_bottom = row[0]["row_band"]
    return {"ports": ports, "row": [row_top / 2 + rect[1], row_bottom / 2 + rect[1]], "frame_hash": hashlib.sha256(image_bytes).hexdigest()}


def led_boxes(result: dict[str, object]) -> list[list[float]]:
    top, bottom = result["row"]
    return [[port["box"][0], top, port["box"][2], bottom] for port in result["ports"]]


@app.get("/health")
def health() -> dict[str, object]:
    env = segment.load_env()
    spend = json.loads(segment.SPEND_PATH.read_text()) if segment.SPEND_PATH.exists() else {}
    return {
        "backend": segment.backend(env),
        "billed_today": spend.get(datetime.now(timezone.utc).date().isoformat(), 0),
        "daily_cap": int(env.get("OWL_SAM_DAILY_CAP", "2000")),
    }


def describe_upload(image_bytes: bytes) -> dict[str, object]:
    result = describe(image_bytes)
    image = cv2.imdecode(np.frombuffer(image_bytes, np.uint8), cv2.IMREAD_COLOR)
    result["leds"] = leds.leds([image], led_boxes(result))
    return result


def describe_camera(burst: float) -> dict[str, object]:
    with camera_lock, capture.connect(segment.load_env()) as mini:
        capture.home(mini)
        for attempt in range(1, CAMERA_ATTEMPTS + 1):
            ok, encoded = cv2.imencode(".png", next(capture.frames(mini, 0)))
            if not ok:
                raise ValueError("failed to encode the camera frame")
            try:
                result = describe(encoded.tobytes())
                break
            except ValueError as error:
                if attempt == CAMERA_ATTEMPTS:
                    raise ValueError(f"{CAMERA_ATTEMPTS} camera frames rejected, last: {error}") from error
        FRAMES.mkdir(parents=True, exist_ok=True)
        (FRAMES / f"{result['frame_hash']}.png").write_bytes(encoded.tobytes())
        result["leds"] = leds.leds(capture.frames(mini, burst), led_boxes(result))
    return result


def record(regions: list[list[int]], ready: threading.Event, stop: threading.Event, state: dict[str, object]) -> None:
    changes = [0.0] * len(regions)
    state["changes"] = changes
    try:
        with camera_lock, capture.connect(segment.load_env()) as mini:
            capture.home(mini)
            base = None
            for frame in capture.frames(mini, MAX_RECORD_S):
                gray = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY).astype(np.int16)
                if base is None:
                    base = gray
                    ready.set()
                for i, (x0, y0, x1, y1) in enumerate(regions):
                    changes[i] = max(changes[i], float(np.abs(gray[y0:y1, x0:x1] - base[y0:y1, x0:x1]).mean()))
                if stop.is_set():
                    return
    except Exception as error:
        state["error"] = str(error)
    finally:
        ready.set()


def start_recording(regions: list[list[int]]) -> dict[str, object]:
    if recording.get("thread") is not None and recording["thread"].is_alive():
        raise ValueError("a motion recording is already running")
    ready, stop = threading.Event(), threading.Event()
    recording.clear()
    recording["stop"] = stop
    recording["thread"] = threading.Thread(target=record, args=(regions, ready, stop, recording), daemon=True)
    recording["thread"].start()
    if not ready.wait(RECORD_START_TIMEOUT_S):
        stop.set()
        raise RuntimeError(f"camera gave no frame within {RECORD_START_TIMEOUT_S} s")
    if "error" in recording:
        raise RuntimeError(recording["error"])
    return {"recording": True}


def stop_recording() -> dict[str, object]:
    thread = recording.get("thread")
    if thread is None:
        raise ValueError("no motion recording to stop")
    recording["stop"].set()
    thread.join(RECORD_START_TIMEOUT_S)
    if "error" in recording:
        raise RuntimeError(recording["error"])
    return {"changes": [round(c, 2) for c in recording["changes"]]}


def watch_leds(boxes: list[list[float]], seconds: float) -> dict[str, object]:
    with camera_lock, capture.connect(segment.load_env()) as mini:
        capture.home(mini)
        return {"leds": leds.leds(capture.frames(mini, seconds), boxes)}


async def run(fn, *args) -> dict[str, object]:
    try:
        return await run_in_threadpool(fn, *args)
    except ValueError as error:
        raise HTTPException(422, str(error)) from error
    except RuntimeError as error:
        raise HTTPException(503, str(error)) from error


@app.post("/segment")
async def segment_frame(request: Request, burst: float = 0) -> dict[str, object]:
    image_bytes = await request.body()
    if burst:
        if image_bytes or not 0 < burst <= MAX_BURST_S:
            raise HTTPException(400, f"burst takes no body and at most {MAX_BURST_S} s")
        return await run(describe_camera, burst)
    if not image_bytes or len(image_bytes) > MAX_IMAGE_BYTES:
        raise HTTPException(413 if image_bytes else 400, f"send one image of at most {MAX_IMAGE_BYTES} bytes as the request body")
    return await run(describe_upload, image_bytes)


@app.post("/motion/start")
async def motion_start(request: Request) -> dict[str, object]:
    regions = (await request.json()).get("regions")
    if not isinstance(regions, list) or not regions or not all(
        isinstance(r, list) and len(r) == 4 and all(isinstance(v, int) and v >= 0 for v in r) and r[0] < r[2] and r[1] < r[3] for r in regions
    ):
        raise HTTPException(400, "want {regions: [[x0, y0, x1, y1], ...] as non-negative ints}")
    return await run(start_recording, regions)


@app.post("/motion/stop")
async def motion_stop() -> dict[str, object]:
    return await run(stop_recording)


@app.post("/leds")
async def leds_burst(request: Request) -> dict[str, object]:
    body = await request.json()
    boxes, seconds = body.get("boxes"), body.get("seconds")
    if (
        not isinstance(boxes, list) or not boxes
        or not all(isinstance(b, list) and len(b) == 4 and all(isinstance(v, (int, float)) and v >= 0 for v in b) and b[0] < b[2] and b[1] < b[3] for b in boxes)
        or not isinstance(seconds, (int, float)) or not 0 < seconds <= MAX_BURST_S
    ):
        raise HTTPException(400, f"want {{boxes: [[x0, y0, x1, y1], ...] in frame pixels, seconds: (0, {MAX_BURST_S}]}}")
    return await run(watch_leds, boxes, float(seconds))


def main() -> None:
    uvicorn.run(app, host="127.0.0.1", port=8701)
