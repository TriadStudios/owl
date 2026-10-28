import collections
import hashlib
import json
import queue
import tempfile
import threading
import time
from pathlib import Path

import cv2
import numpy as np

import capture
import segment

HAND_PROMPT = "hand"
HAND_MIN_SCORE = 0.4
FOLLOW_MIN_SCORE = 0.5
FOLLOW_SEARCH = 1.0
REGION_DOWN = 1.0
CLIP_SECONDS = 15
CLIP_WIDTH = 640
EVIDENCE = segment.ROOT / "data" / "evidence"

state: dict[str, object] = {"frozen": False}
clip_lock = threading.Lock()
clip: collections.deque[tuple[float, np.ndarray]] = collections.deque()


def frozen() -> bool:
    return bool(state["frozen"])


def detect_hand(rgb: np.ndarray) -> tuple[list[float], float] | None:
    import torch

    processor, model = segment.local_model()
    inputs = processor(images=rgb, text=HAND_PROMPT, return_tensors="pt").to("cuda", torch.float16)
    with torch.inference_mode():
        outputs = model(**inputs)
    result = processor.post_process_instance_segmentation(outputs, target_sizes=[rgb.shape[:2]])[0]
    scored = list(zip(result["boxes"].float().cpu().tolist(), result["scores"].float().cpu().tolist()))
    best = max(scored, key=lambda bs: bs[1], default=None)
    return best if best and best[1] >= HAND_MIN_SCORE else None


def region(height: int, width: int) -> list[int]:
    x0, y0, x1, y1 = json.loads(segment.ROI_PATH.read_text())["rect"]
    return [x0, y0, x1, min(height, int(y1 + REGION_DOWN * (y1 - y0)))]


def follow(gray: np.ndarray, template: np.ndarray, box: list[float]) -> tuple[list[float], float] | None:
    h, w = template.shape
    x0, y0 = int(max(box[0] - FOLLOW_SEARCH * w, 0)), int(max(box[1] - FOLLOW_SEARCH * h, 0))
    x1, y1 = int(min(box[2] + FOLLOW_SEARCH * w, gray.shape[1])), int(min(box[3] + FOLLOW_SEARCH * h, gray.shape[0]))
    window = gray[y0:y1, x0:x1]
    if window.shape[0] < h or window.shape[1] < w:
        return None
    _, score, _, (mx, my) = cv2.minMaxLoc(cv2.matchTemplate(window, template, cv2.TM_CCOEFF_NORMED))
    if score < FOLLOW_MIN_SCORE:
        return None
    return [x0 + mx, y0 + my, x0 + mx + w, y0 + my + h], float(score)


def emit(lines: queue.Queue, line: str | None) -> None:
    while True:
        try:
            lines.put_nowait(line)
            return
        except queue.Full:
            try:
                lines.get_nowait()
            except queue.Empty:
                pass


def run(fps: float, row: list[float], lines: queue.Queue, stop: threading.Event, camera_lock: threading.Lock) -> None:
    latest: dict[str, object] = {"rgb": None, "gray": None, "seq": 0}
    found: dict[str, object] = {"seq": -1}
    lock = threading.Lock()

    def detector() -> None:
        while not stop.is_set():
            with lock:
                rgb, gray, seq = latest["rgb"], latest["gray"], latest["seq"]
            if rgb is None:
                time.sleep(0.02)
                continue
            hand = detect_hand(rgb)
            with lock:
                found.update(seq=seq, hand=hand, gray=gray)

    try:
        with camera_lock, capture.connect(segment.load_env()) as mini:
            capture.home(mini)
            threading.Thread(target=detector, daemon=True).start()
            box, template, used, next_at = None, None, -1, 0.0
            for frame in capture.frames(mini, float("inf")):
                if stop.is_set():
                    return
                now = time.monotonic()
                if now < next_at:
                    continue
                next_at = now + 1 / fps
                rx0, ry0, rx1, ry1 = region(*frame.shape[:2])
                crop = frame[ry0:ry1, rx0:rx1]
                gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
                with lock:
                    latest["seq"] = int(latest["seq"]) + 1
                    latest.update(rgb=cv2.cvtColor(crop, cv2.COLOR_BGR2RGB), gray=gray)
                    seen = dict(found)
                source, score = "", 0.0
                if seen["seq"] != used:
                    used = seen["seq"]
                    if seen["hand"] is not None:
                        box, score = seen["hand"]
                        x0, y0, x1, y1 = (int(round(v)) for v in box)
                        template = seen["gray"][y0:y1, x0:x1] if x1 - x0 > 4 and y1 - y0 > 4 else None
                        source = "sam3"
                        followed = follow(gray, template, box) if template is not None else None
                        if followed:
                            box = followed[0]
                    else:
                        box, template = None, None
                elif template is not None and box is not None:
                    followed = follow(gray, template, box)
                    box, score = followed if followed else (None, 0.0)
                    source = "follow" if followed else ""
                hand = [round(box[0] + rx0, 1), round(box[1] + ry0, 1), round(box[2] + rx0, 1), round(box[3] + ry0, 1)] if box is not None else None
                state["frozen"] = hand is not None and hand[1] < row[1] and hand[3] > row[0]
                with clip_lock:
                    scale = CLIP_WIDTH / frame.shape[1]
                    clip.append((time.time(), cv2.resize(frame, None, fx=scale, fy=scale, interpolation=cv2.INTER_AREA)))
                    while clip and clip[0][0] < time.time() - CLIP_SECONDS:
                        clip.popleft()
                emit(lines, json.dumps({"t": round(time.time(), 3), "hand": hand, "source": source, "score": round(score, 3), "frozen": state["frozen"]}) + "\n")
    except Exception as error:
        emit(lines, json.dumps({"error": str(error)}) + "\n")
    finally:
        state["frozen"] = False
        stop.set()
        emit(lines, None)


def save_clip() -> str:
    with clip_lock:
        frames = list(clip)
    if not frames:
        raise ValueError("no tracked frames to save")
    span = max(frames[-1][0] - frames[0][0], 1e-3)
    fps = max(1.0, (len(frames) - 1) / span)
    height, width = frames[0][1].shape[:2]
    EVIDENCE.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=EVIDENCE) as tmp:
        path = Path(tmp) / "clip.mp4"
        writer = cv2.VideoWriter(str(path), cv2.VideoWriter_fourcc(*"mp4v"), fps, (width, height))
        for _, image in frames:
            writer.write(image)
        writer.release()
        data = path.read_bytes()
        digest = hashlib.sha256(data).hexdigest()
        final = EVIDENCE / f"{digest}.mp4"
        if not final.exists():
            path.replace(final)
    return digest
