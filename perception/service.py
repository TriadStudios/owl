import hashlib
import itertools
import json
import queue
import threading
import time
from datetime import datetime, timezone

import cv2
import numpy as np
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import StreamingResponse
from starlette.concurrency import run_in_threadpool

import capture
import leds
import segment
import track

MAX_IMAGE_BYTES = 20 * 2**20
MAX_BURST_S = 30
MAX_RECORD_S = 300
RECORD_START_TIMEOUT_S = 20
CAMERA_ATTEMPTS = 3
MAX_CAPTURE_FRAMES = 10
CAPTURE_GRACE_S = 5
MAX_TRACK_FPS = 15
TRACK_BACKLOG = 64
POLYGON_EPSILON_PX = 1.5
HOLD_INTERVAL_S = 1.0
HOLD_SETTLE_S = 0.4
FRAMES = segment.ROOT / "data" / "frames"
DEBUG = segment.ROOT / "data" / "debug"
ALIGN_SCALE = 0.5
ALIGN_CRITERIA = (cv2.TERM_CRITERIA_EPS | cv2.TERM_CRITERIA_COUNT, 100, 1e-5)

app = FastAPI()
analyze_lock = threading.Lock()
camera_lock = threading.Lock()
recording: dict[str, object] = {}
reference: dict[str, np.ndarray] = {}


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


def debug_frame(kind: str, frame: np.ndarray, rects: list[list[float]], labels: list[str], lit: list[bool]) -> None:
    image = frame.copy()
    for rect, label, on in zip(rects, labels, lit):
        x0, y0, x1, y1 = (int(round(v)) for v in rect)
        color = (0, 255, 0) if on else (0, 0, 255)
        cv2.rectangle(image, (x0, y0), (x1, y1), color, 1)
        cv2.putText(image, label, (x0, max(y0 - 4, 10)), cv2.FONT_HERSHEY_SIMPLEX, 0.35, color, 1, cv2.LINE_AA)
    DEBUG.mkdir(parents=True, exist_ok=True)
    path = DEBUG / f"{datetime.now():%Y%m%dT%H%M%S%f}-{kind}.png"
    cv2.imwrite(str(path), image)
    print(f"debug frame {path.name}")


def small_gray(frame: np.ndarray) -> np.ndarray:
    return cv2.resize(cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY), None, fx=ALIGN_SCALE, fy=ALIGN_SCALE, interpolation=cv2.INTER_AREA).astype(np.float32)


def align(frame: np.ndarray, rects: list[list[float]]) -> list[list[float]]:
    # The head lands a little differently every session, so move the boxes from the segmented frame onto this one.
    if "gray" not in reference:
        return rects
    ref, cur = reference["gray"], small_gray(frame)
    (dx, dy), _ = cv2.phaseCorrelate(ref, cur, cv2.createHanningWindow(ref.shape[::-1], cv2.CV_32F))
    warp = np.array([[1, 0, dx], [0, 1, dy]], np.float32)
    try:
        _, warp = cv2.findTransformECC(ref, cur, warp, cv2.MOTION_EUCLIDEAN, ALIGN_CRITERIA, None, 5)
    except cv2.error:
        pass
    warp[:, 2] /= ALIGN_SCALE
    print(f"aligned to the segmented frame: shift {warp[0, 2]:.1f},{warp[1, 2]:.1f} px, rotation {np.degrees(np.arctan2(warp[1, 0], warp[0, 0])):.2f} deg")
    moved = []
    for x0, y0, x1, y1 in rects:
        cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
        nx, ny = warp @ np.array([cx, cy, 1.0])
        moved.append([max(x0 + nx - cx, 0.0), max(y0 + ny - cy, 0.0), x1 + nx - cx, y1 + ny - cy])
    return moved


def watch(kind: str, frames, boxes: list[list[float]]) -> list[dict[str, object]]:
    first = next(frames)
    boxes = align(first, boxes)
    result = leds.leds(itertools.chain([first], frames), boxes)
    height, width = first.shape[:2]
    bands = [[left, top, right, bottom] for top, bottom, left, right in (leds.band(box, height, width) for box in boxes)]
    debug_frame(kind, first, bands, [f"{r['num']} {'on' if r['link_on'] else 'off'} {r['activity_energy']}" for r in result], [r["link_on"] for r in result])
    return result


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
            frame = next(capture.frames(mini, 0))
            ok, encoded = cv2.imencode(".png", frame)
            if not ok:
                raise ValueError("failed to encode the camera frame")
            try:
                result = describe(encoded.tobytes())
                break
            except ValueError as error:
                debug_frame("segment-rejected", frame, [], [], [])
                if attempt == CAMERA_ATTEMPTS:
                    raise ValueError(f"{CAMERA_ATTEMPTS} camera frames rejected, last: {error}") from error
        reference["gray"] = small_gray(frame)
        FRAMES.mkdir(parents=True, exist_ok=True)
        (FRAMES / f"{result['frame_hash']}.png").write_bytes(encoded.tobytes())
        result["leds"] = watch("segment-leds", capture.frames(mini, burst), led_boxes(result))
    return result


def record(regions: list[list[int]], ready: threading.Event, stop: threading.Event, state: dict[str, object]) -> None:
    changes = [0.0] * len(regions)
    state["changes"] = changes
    try:
        with camera_lock, capture.connect(segment.load_env()) as mini:
            locked = capture.home(mini)
            base = None
            for frame in capture.frames(mini, MAX_RECORD_S):
                now = time.monotonic()
                if base is None:
                    regions = [[int(round(v)) for v in r] for r in align(frame, regions)]
                    debug_frame("touch-start", frame, regions, [f"r{i + 1}" for i in range(len(regions))], [True] * len(regions))
                    base, base_small = cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY).astype(np.int16), small_gray(frame)
                    window = cv2.createHanningWindow(base_small.shape[::-1], cv2.CV_32F)
                    next_hold, settled = now + HOLD_INTERVAL_S, now
                    ready.set()
                    continue
                if locked is not None and now >= next_hold:
                    target, moved = capture.hold(mini, *locked)
                    locked, next_hold = (locked[0], target), now + HOLD_INTERVAL_S
                    if moved:
                        settled = now + HOLD_SETTLE_S
                # Frames while the head is still settling from a hold correction are blurred, so they would score as motion everywhere.
                if now >= settled:
                    (dx, dy), _ = cv2.phaseCorrelate(base_small, small_gray(frame), window)
                    back = np.float32([[1, 0, -dx / ALIGN_SCALE], [0, 1, -dy / ALIGN_SCALE]])
                    gray = cv2.warpAffine(cv2.cvtColor(frame, cv2.COLOR_BGR2GRAY), back, base.shape[::-1]).astype(np.int16)
                    for i, (x0, y0, x1, y1) in enumerate(regions):
                        changes[i] = max(changes[i], float(np.abs(gray[y0:y1, x0:x1] - base[y0:y1, x0:x1]).mean()))
                if stop.is_set():
                    peak = max(changes)
                    debug_frame("touch-end", frame, regions, [f"r{i + 1} {c:.1f}" for i, c in enumerate(changes)], [c == peak for c in changes])
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


def capture_frames(count: int, spacing: float) -> dict[str, object]:
    hashes: list[str] = []
    with camera_lock, capture.connect(segment.load_env()) as mini:
        capture.home(mini)
        next_at = 0.0
        for frame in capture.frames(mini, count * spacing + CAPTURE_GRACE_S):
            now = time.monotonic()
            if now < next_at:
                continue
            ok, encoded = cv2.imencode(".png", frame)
            if not ok:
                raise ValueError("failed to encode the camera frame")
            data = encoded.tobytes()
            digest = hashlib.sha256(data).hexdigest()
            FRAMES.mkdir(parents=True, exist_ok=True)
            (FRAMES / f"{digest}.png").write_bytes(data)
            hashes.append(digest)
            next_at = now + spacing
            if len(hashes) == count:
                break
    if len(hashes) < count:
        raise RuntimeError(f"camera gave {len(hashes)} of {count} frames")
    return {"frames": hashes}


def watch_leds(boxes: list[list[float]], seconds: float) -> dict[str, object]:
    with camera_lock, capture.connect(segment.load_env()) as mini:
        capture.home(mini)
        return {"leds": watch("leds", capture.frames(mini, seconds), boxes)}


async def run(fn, *args) -> dict[str, object]:
    try:
        return await run_in_threadpool(fn, *args)
    except ValueError as error:
        raise HTTPException(422, str(error)) from error
    except RuntimeError as error:
        raise HTTPException(503, str(error)) from error


def refuse_while_frozen() -> None:
    if track.frozen():
        raise HTTPException(409, "attribution frozen: a hand is over the port row")


@app.get("/track")
def track_stream(row: str, fps: float = 10) -> StreamingResponse:
    try:
        band = [float(v) for v in row.split(",")]
    except ValueError:
        band = []
    if len(band) != 2 or band[0] >= band[1] or not 1 <= fps <= MAX_TRACK_FPS:
        raise HTTPException(400, f"want row=y0,y1 in frame pixels and fps in 1..{MAX_TRACK_FPS}")
    lines: queue.Queue = queue.Queue(maxsize=TRACK_BACKLOG)
    stop = threading.Event()
    threading.Thread(target=track.run, args=(fps, band, lines, stop, camera_lock), daemon=True).start()

    def stream():
        try:
            while (line := lines.get()) is not None:
                yield line
        finally:
            stop.set()

    return StreamingResponse(stream(), media_type="application/x-ndjson")


@app.post("/track/clip")
async def track_clip() -> dict[str, object]:
    return {"hash": await run(track.save_clip)}


@app.post("/segment")
async def segment_frame(request: Request, burst: float = 0) -> dict[str, object]:
    refuse_while_frozen()
    image_bytes = await request.body()
    if burst:
        if image_bytes or not 0 < burst <= MAX_BURST_S:
            raise HTTPException(400, f"burst takes no body and at most {MAX_BURST_S} s")
        return await run(describe_camera, burst)
    if not image_bytes or len(image_bytes) > MAX_IMAGE_BYTES:
        raise HTTPException(413 if image_bytes else 400, f"send one image of at most {MAX_IMAGE_BYTES} bytes as the request body")
    return await run(describe_upload, image_bytes)


@app.post("/capture")
async def capture_burst(frames: int = 5, spacing: float = 0.2) -> dict[str, object]:
    if not 1 <= frames <= MAX_CAPTURE_FRAMES or not 0 <= spacing <= 2:
        raise HTTPException(400, f"frames in 1..{MAX_CAPTURE_FRAMES}, spacing in 0..2 s")
    return await run(capture_frames, frames, spacing)


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
    refuse_while_frozen()
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
