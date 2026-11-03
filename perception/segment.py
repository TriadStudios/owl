import base64
import contextlib
import functools
import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from pathlib import Path

import cv2
import numpy as np
from meta_sam_parser import CompletedOutcome, decode_mask_to_raster, image_segmentation_format

ROOT = Path(__file__).resolve().parent.parent
CACHE = ROOT / "data" / "sam-cache"
SPEND_PATH = CACHE / "spend.json"
SPEND_LOCK_TIMEOUT_S = 30
ROI_PATH = ROOT / "data" / "port-roi.json"
ROI_PAD_X, ROI_PAD_UP, ROI_PAD_DOWN = 2, 3, 4
ROI_PITCH = 1.2
ROI_MIN_BOXES = 4
META_API_URL = "https://api.meta.ai/v1/responses"
LOCAL_MODEL = "facebook/sam3"
LOCAL_LONG_SIDE = 1280
SOCKET_PROMPTS = ("ethernet port", "network port")
CABLE_PROMPT = "cable"
PROMPTS = (*SOCKET_PROMPTS, CABLE_PROMPT)
HAND_PROMPT = "hand"
HAND_MIN_SCORE = 0.4
TIP_ABOVE = 0.5
TIP_BELOW = 1.5
TIP_BAND = 0.5
REACH_STEP = 4
PLUG_TOP_SLACK = 0.25
HANDHELD_CROP_WIDTH = 800
HANDHELD_PLACE_SCALE = 0.5
COPPER_PORTS = 8
PORT_PALETTE = (
    (75, 25, 230), (75, 180, 60), (25, 225, 255), (200, 130, 0), (48, 130, 245), (180, 30, 145),
    (240, 240, 70), (230, 50, 240), (60, 245, 210), (190, 190, 250), (128, 128, 0), (255, 190, 220),
)
EMPTY_GRAY = (150, 150, 150)
LABEL_BACKGROUND = (32, 32, 32)

Instance = dict[str, object]


def encode_rle(mask: np.ndarray) -> dict[str, object]:
    flat = mask.ravel(order="F")
    edges = np.flatnonzero(flat[1:] != flat[:-1]) + 1
    counts = np.diff(np.concatenate(([0], edges, [flat.size]))).tolist()
    if flat[0]:
        counts.insert(0, 0)
    return {"size": list(mask.shape), "counts": counts}


def decode_rle(rle: dict[str, object]) -> np.ndarray:
    height, width = rle["size"]
    flat = np.repeat(np.arange(len(rle["counts"])) % 2, rle["counts"]).astype(np.uint8)
    return flat.reshape(width, height).T.copy()


def iou(a: list[float], b: list[float]) -> float:
    inter = max(0.0, min(a[2], b[2]) - max(a[0], b[0])) * max(0.0, min(a[3], b[3]) - max(a[1], b[1]))
    union = (a[2] - a[0]) * (a[3] - a[1]) + (b[2] - b[0]) * (b[3] - b[1]) - inter
    return inter / union if union > 0 else 0.0


def read_spend() -> dict[str, int]:
    return json.loads(SPEND_PATH.read_text()) if SPEND_PATH.exists() else {}


@contextlib.contextmanager
def spend_lock():
    lock = SPEND_PATH.with_suffix(".lock")
    give_up = time.monotonic() + SPEND_LOCK_TIMEOUT_S
    while True:
        try:
            fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY)
            break
        except (FileExistsError, PermissionError):
            if time.monotonic() > give_up:
                raise RuntimeError(f"{lock} held for {SPEND_LOCK_TIMEOUT_S} s; delete it if no OWL process is billing")
            time.sleep(0.01)
    try:
        yield
    finally:
        os.close(fd)
        os.unlink(lock)


def add_spend(today: str) -> None:
    with spend_lock():
        spend = read_spend()
        spend[today] = spend.get(today, 0) + 1
        tmp = SPEND_PATH.with_suffix(".tmp")
        tmp.write_text(json.dumps(spend))
        os.replace(tmp, SPEND_PATH)


def meta_api(image_bytes: bytes, height: int, width: int, env: dict[str, str], prompts: tuple[str, ...]) -> dict[str, list[Instance]]:
    key = env.get("OWL_META_API_KEY", "").strip()
    if not key:
        raise RuntimeError("OWL_META_API_KEY missing from .env.local")
    cap = int(env.get("OWL_SAM_DAILY_CAP", "2000"))

    CACHE.mkdir(parents=True, exist_ok=True)
    mime = "image/jpeg" if image_bytes[:2] == b"\xff\xd8" else "image/png"
    data_url = f"data:{mime};base64," + base64.b64encode(image_bytes).decode()

    def fetch(prompt: str) -> tuple[str, bool]:
        cache_path = CACHE / f"{hashlib.sha256(image_bytes + prompt.encode()).hexdigest()}.json"
        if cache_path.exists():
            return json.loads(cache_path.read_text())["output_text"], False
        today = datetime.now(timezone.utc).date().isoformat()
        with spend_lock():
            spent = read_spend().get(today, 0)
        if spent >= cap:
            raise RuntimeError(f"SAM daily cap reached: {spent}/{cap} images billed on {today} UTC; raise OWL_SAM_DAILY_CAP to continue")

        body = {
            "model": "sam-3.1",
            "input": [{
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": prompt}, {"type": "input_image", "image_url": data_url}],
            }],
            "stream": False,
            "metadata": {"include_confidence": "true"},
        }
        request = urllib.request.Request(
            META_API_URL,
            data=json.dumps(body).encode(),
            headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                reply = json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"Meta SAM API returned {error.code} for '{prompt}': {error.read().decode(errors='replace')[:500]}")

        add_spend(today)
        if reply.get("status") != "completed":
            raise RuntimeError(f"Meta SAM API response for '{prompt}' not completed: {json.dumps(reply)[:500]}")
        text = "".join(
            part.get("text", "")
            for item in reply.get("output", [])
            for part in item.get("content", [])
            if part.get("type") == "output_text"
        )
        cache_path.write_text(json.dumps({"prompt": prompt, "output_text": text}))
        return text, True

    # Each prompt is its own API round trip, so asking them together cuts a pass to its slowest prompt.
    with ThreadPoolExecutor(max_workers=len(prompts)) as pool:
        fetched = dict(zip(prompts, pool.map(fetch, prompts)))
    raw = {prompt: text for prompt, (text, _) in fetched.items()}
    billed = sum(paid for _, paid in fetched.values())
    print(f"billed {billed} images this run, {len(prompts) - billed} prompts from cache")

    results: dict[str, list[Instance]] = {}
    for prompt in prompts:
        parser = image_segmentation_format().create_parser()
        parser.push(raw[prompt])
        parsed = parser.finish(CompletedOutcome()).result
        for diagnostic in parsed.diagnostics:
            print(f"warning: '{prompt}' line {diagnostic.line}: {diagnostic.message}", file=sys.stderr)
        boxes = {record.object_id: record for record in parsed.records if record.kind == "box"}
        results[prompt] = []
        for record in parsed.records:
            if record.kind != "mask" or record.object_id not in boxes:
                continue
            left, top = max(int(record.bounds.left), 0), max(int(record.bounds.top), 0)
            right, bottom = min(int(round(record.bounds.right)), width), min(int(round(record.bounds.bottom)), height)
            if right <= left or bottom <= top:
                continue
            raster = np.frombuffer(decode_mask_to_raster(record.mask), np.uint8).reshape(record.mask.height, record.mask.width)
            mask = np.zeros((height, width), np.uint8)
            mask[top:bottom, left:right] = cv2.resize(raster, (right - left, bottom - top), interpolation=cv2.INTER_NEAREST)
            box = boxes[record.object_id]
            results[prompt].append({
                "mask_rle": encode_rle(mask),
                "box": [box.left, box.top, box.right, box.bottom],
                "score": box.confidence,
            })
    return results


@functools.cache
def local_model():
    import torch
    from transformers import Sam3Model, Sam3Processor

    processor = Sam3Processor.from_pretrained(LOCAL_MODEL)
    model = Sam3Model.from_pretrained(LOCAL_MODEL, dtype=torch.float16).to("cuda").eval()
    return processor, model


def local(rgb: np.ndarray, prompts: tuple[str, ...]) -> dict[str, list[Instance]]:
    import torch

    height, width = rgb.shape[:2]
    scale = min(1.0, LOCAL_LONG_SIDE / max(height, width))
    small = cv2.resize(rgb, None, fx=scale, fy=scale, interpolation=cv2.INTER_AREA) if scale < 1 else rgb
    processor, model = local_model()
    results: dict[str, list[Instance]] = {}
    for prompt in prompts:
        inputs = processor(images=small, text=prompt, return_tensors="pt").to("cuda", torch.float16)
        with torch.inference_mode():
            outputs = model(**inputs)
        result = processor.post_process_instance_segmentation(outputs, target_sizes=[(height, width)])[0]
        results[prompt] = [
            {"mask_rle": encode_rle(mask), "box": box, "score": score}
            for mask, box, score in zip(
                result["masks"].cpu().numpy().astype(np.uint8),
                result["boxes"].float().cpu().tolist(),
                result["scores"].float().cpu().tolist(),
            )
        ]
    print(f"peak VRAM {torch.cuda.max_memory_allocated() / 2**30:.2f} GiB")
    return results


def ports(results: dict[str, list[Instance]]) -> list[dict[str, object]]:
    row = port_row(results)
    if len(row) != COPPER_PORTS:
        raise ValueError(f"{len(row)} port/plug boxes in the port row, the layout has {COPPER_PORTS} copper ports")
    return row


def touching(row: list[dict[str, object]], hands: list[Instance]) -> int | None:
    top = float(np.median([p["box"][1] for p in row]))
    bottom = float(np.median([p["box"][3] for p in row]))
    height = bottom - top
    centers = [(p["box"][0] + p["box"][2]) / 2 for p in row]
    pitch = float(np.median(np.diff(centers)))
    best, best_gap = None, float("inf")
    for hand in hands:
        if (hand["score"] or 0.0) < HAND_MIN_SCORE:
            continue
        mask = decode_rle(hand["mask_rle"]) > 0
        if not mask.any():
            continue
        # A pinch holds the plug between the fingertips, the parts of the hand farthest from where it enters the picture; the knuckles and palm beside them cover the neighbours. The hand can come in from above or from the side, so the lowest point is not always a fingertip.
        reach = reach_from_edge(mask[::REACH_STEP, ::REACH_STEP])
        ys, xs = np.nonzero(reach > reach.max() - TIP_BAND * height / REACH_STEP)
        ys, xs = ys * REACH_STEP, xs * REACH_STEP
        tip_y = float(ys.mean())
        if not top - TIP_ABOVE * height <= tip_y <= bottom + TIP_BELOW * height:
            continue
        tip_x = float(xs.min() + xs.max()) / 2
        nearest = min(range(len(row)), key=lambda i: abs(centers[i] - tip_x))
        gap = abs(tip_y - (top + bottom) / 2)
        if abs(centers[nearest] - tip_x) <= pitch / 2 and gap < best_gap:
            best, best_gap = nearest + 1, gap
    return best


def reach_from_edge(mask: np.ndarray) -> np.ndarray:
    inside = mask.astype(np.uint8)
    front = np.zeros_like(inside)
    for edge in (np.s_[0, :], np.s_[-1, :], np.s_[:, 0], np.s_[:, -1]):
        front[edge] |= inside[edge]
    if not front.any():
        first = np.nonzero(inside)[0].min()
        front[first] = inside[first]
    reach = np.where(front > 0, 0, -1).astype(np.int32)
    step, kernel = 0, np.ones((3, 3), np.uint8)
    while front.any():
        step += 1
        front = cv2.dilate(front, kernel) & inside & (reach < 0)
        reach[front > 0] = step
    return reach


def same_port(a: list[float], b: list[float]) -> bool:
    return min(a[2], b[2]) - max(a[0], b[0]) >= 0.5 * min(a[2] - a[0], b[2] - b[0])


def port_row(results: dict[str, list[Instance]]) -> list[dict[str, object]]:
    sockets: list[Instance] = []
    for instance in sorted(
        (instance for prompt in SOCKET_PROMPTS for instance in results[prompt]),
        key=lambda instance: -(instance["score"] or 0.0),
    ):
        if all(iou(instance["box"], kept["box"]) < 0.5 for kept in sockets):
            sockets.append(instance)
    if not sockets:
        raise ValueError("no socket boxes found")

    socket_width = float(np.median([s["box"][2] - s["box"][0] for s in sockets]))
    sockets = [s for s in sockets if 0.6 * socket_width <= s["box"][2] - s["box"][0] <= 1.6 * socket_width]
    socket_height = float(np.median([s["box"][3] - s["box"][1] for s in sockets]))
    centers_y = np.array([(s["box"][1] + s["box"][3]) / 2 for s in sockets])
    aligned = [np.abs(centers_y - y) <= socket_height / 2 for y in centers_y]
    row_y = float(np.median(centers_y[max(aligned, key=np.count_nonzero)]))
    top, bottom = int(round(row_y - socket_height / 2)), int(round(row_y + socket_height / 2))

    candidates: list[dict[str, object]] = []
    for s, y in zip(sockets, centers_y):
        # A cable over part of a socket can give it a second, smaller box beside the whole one; sockets come by score, so the better box wins.
        if abs(y - row_y) <= socket_height / 2 and not any(same_port(s["box"], c["box"]) for c in candidates):
            candidates.append({"box": s["box"], "occupied": False, "cable": None, "score": s["score"]})
    # Small boxes off the row (the SFP cages' LEDs) pull the all-socket median down and the console port and merged double boxes in the row push it up; the row's lower quartile tracks the real sockets.
    socket_width = float(np.percentile([c["box"][2] - c["box"][0] for c in candidates], 25))
    for cable_index, cable in enumerate(results[CABLE_PROMPT]):
        mask = decode_rle(cable["mask_rle"])
        rows = np.flatnonzero(mask.any(axis=1))
        if rows.size == 0 or rows[0] >= bottom + 2 * socket_height:
            continue
        # SAM's cable mask often stops short of the plug (a black plug on the black face can end over a socket height below the row), so read the plug end from where the mask starts, not only inside the row band.
        plug_top = max(int(rows[0]), top, 0)
        strip = mask[plug_top:plug_top + bottom - top]
        count, labels, stats, _ = cv2.connectedComponentsWithStats(strip)
        for label, (x, y, w, h, area) in enumerate(stats[1:count], start=1):
            if area < 0.15 * socket_height * socket_width:
                continue
            if round(w / socket_width) > 1:
                # A cable leaving its plug sideways crosses the face of the row and reads as more plugs; plug ends start at the top of the strip, the crossing cable enters it lower down.
                tops = (labels[:, x:x + w] == label).argmax(axis=0)
                high = np.flatnonzero(tops <= tops.min() + PLUG_TOP_SLACK * socket_height)
                x, w = x + int(high[0]), int(high[-1] - high[0]) + 1
            plugs = max(1, round(w / socket_width))
            for i in range(plugs):
                box = [x + i * w / plugs, plug_top + y, x + (i + 1) * w / plugs, plug_top + y + h]
                candidates = [c for c in candidates if not same_port(c["box"], box)]
                candidates.append({"box": box, "occupied": True, "cable": cable_index, "score": cable["score"]})

    # The gap between two neighbouring plugs can read as a socket that overlaps neither plug by half; no port fits between neighbours.
    plugs = sorted(((c["box"][0] + c["box"][2]) / 2, c["box"][2] - c["box"][0]) for c in candidates if c["occupied"])
    between = [(a, b) for (a, wa), (b, wb) in zip(plugs, plugs[1:]) if b - a < wa + wb]
    candidates = [c for c in candidates if c["occupied"] or not any(a < (c["box"][0] + c["box"][2]) / 2 < b for a, b in between)]

    candidates.sort(key=lambda c: c["box"][0] + c["box"][2])
    runs: list[list[dict[str, object]]] = []
    for candidate in candidates:
        center = (candidate["box"][0] + candidate["box"][2]) / 2
        if runs and center - (runs[-1][-1]["box"][0] + runs[-1][-1]["box"][2]) / 2 <= 2.5 * socket_width:
            runs[-1].append(candidate)
        else:
            runs.append([candidate])
    row = max(runs, key=len, default=[])
    for port in row:
        port["row_band"] = [top, bottom]
    return row


def load_env() -> dict[str, str]:
    env_file = ROOT / ".env.local"
    env = dict(line.split("=", 1) for line in env_file.read_text().splitlines() if "=" in line) if env_file.exists() else {}
    env.update(os.environ)
    return env


def backend(env: dict[str, str]) -> str:
    name = env.get("OWL_SAM_BACKEND", "meta-api").strip()
    if name not in ("meta-api", "local"):
        raise ValueError(f"unknown OWL_SAM_BACKEND '{name}', expected meta-api or local")
    return name


def segment(image_bytes: bytes, image: np.ndarray, env: dict[str, str], prompts: tuple[str, ...] = PROMPTS) -> dict[str, list[Instance]]:
    if backend(env) == "local":
        return local(cv2.cvtColor(image, cv2.COLOR_BGR2RGB), prompts)
    return meta_api(image_bytes, image.shape[0], image.shape[1], env, prompts)


def place_row(image_bytes: bytes, image: np.ndarray, env: dict[str, str]) -> list[int]:
    height, width = image.shape[:2]
    try:
        full_row = port_row(segment(image_bytes, image, env))
    except ValueError as error:
        raise ValueError(f"cannot place the port-row crop: {error}") from error
    # From far away SAM misses some plugged ports at full frame; the crop is padded wide enough that a partial row still places it.
    if len(full_row) < ROI_MIN_BOXES:
        raise ValueError(f"cannot place the port-row crop: {len(full_row)} aligned boxes, need at least {ROI_MIN_BOXES}")
    return row_rect(full_row, width, height)


def row_rect(full_row: list[dict[str, object]], width: int, height: int) -> list[int]:
    box_width = float(np.median([p["box"][2] - p["box"][0] for p in full_row]))
    box_height = float(np.median([p["box"][3] - p["box"][1] for p in full_row]))
    pad_x = ROI_PAD_X + ROI_PITCH * max(0, COPPER_PORTS - len(full_row))
    return [
        max(int(min(p["box"][0] for p in full_row) - pad_x * box_width), 0),
        max(int(min(p["box"][1] for p in full_row) - ROI_PAD_UP * box_height), 0),
        min(int(max(p["box"][2] for p in full_row) + pad_x * box_width), width),
        min(int(max(p["box"][3] for p in full_row) + ROI_PAD_DOWN * box_height), height),
    ]


def place_row_small(image: np.ndarray, env: dict[str, str]) -> list[int]:
    # The whole-frame pass only has to find the row, and a half-size frame uploads and runs in a third of the time.
    small = cv2.resize(image, None, fx=HANDHELD_PLACE_SCALE, fy=HANDHELD_PLACE_SCALE, interpolation=cv2.INTER_AREA)
    ok, encoded = cv2.imencode(".jpg", small, [cv2.IMWRITE_JPEG_QUALITY, 90])
    if not ok:
        raise ValueError("failed to encode the frame for placing the port row")
    return [int(v / HANDHELD_PLACE_SCALE) for v in place_row(encoded.tobytes(), small, env)]


def analyze(image_bytes: bytes, env: dict[str, str], handheld: bool = False, rect: list[int] | None = None) -> tuple[np.ndarray, dict[str, list[Instance]], list[int], float]:
    image = cv2.imdecode(np.frombuffer(image_bytes, np.uint8), cv2.IMREAD_COLOR)
    if image is None:
        raise ValueError("cannot decode image")
    height, width = image.shape[:2]

    if handheld:
        # A handheld camera never sees the ports in the same place twice, so the stored crop of the fixed camera does not apply.
        rect = rect or place_row_small(image, env)
    else:
        if not ROI_PATH.exists():
            rect = place_row(image_bytes, image, env)
            ROI_PATH.parent.mkdir(parents=True, exist_ok=True)
            ROI_PATH.write_text(json.dumps({"rect": rect, "frame_size": [height, width]}))
            print(f"port-row crop {rect} stored in {ROI_PATH.name}")
        roi = json.loads(ROI_PATH.read_text())
        if roi["frame_size"] != [height, width]:
            raise ValueError(f"{ROI_PATH.name} was made for {roi['frame_size']} frames, this one is {[height, width]}; delete it to recompute")
        rect = roi["rect"]
    x0, y0, x1, y1 = rect
    # The API's time grows with the image, and a handheld crop of a near switch is already sharp at a fraction of 2x.
    scale = min(2.0, HANDHELD_CROP_WIDTH / (x1 - x0)) if handheld else 2.0
    crop = cv2.resize(image[y0:y1, x0:x1], None, fx=scale, fy=scale, interpolation=cv2.INTER_CUBIC if scale > 1 else cv2.INTER_AREA)
    # Handheld frames go to the API every few seconds, and a JPEG of the crop uploads in a fraction of the PNG's time.
    ok, encoded = cv2.imencode(".jpg", crop, [cv2.IMWRITE_JPEG_QUALITY, 92]) if handheld else cv2.imencode(".png", crop)
    if not ok:
        raise ValueError("failed to encode the port-row crop")

    start = time.perf_counter()
    results = segment(encoded.tobytes(), crop, env, (*PROMPTS, HAND_PROMPT) if handheld else PROMPTS)
    return crop, results, rect, time.perf_counter() - start


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit("usage: segment.py <image>")
    image_path = Path(sys.argv[1])
    env = load_env()
    try:
        crop, results, rect, elapsed = analyze(image_path.read_bytes(), env)
    except (ValueError, RuntimeError) as error:
        sys.exit(f"{image_path.name}: {error}")

    overlay = crop.copy()
    print(f"crop {rect} at 2x, coordinates below are crop pixels")
    for prompt, instances in results.items():
        print(f"{prompt}: {len(instances)}")
        for instance in sorted(instances, key=lambda instance: instance["box"][0] + instance["box"][2]):
            x0, y0, x1, y1 = (round(v) for v in instance["box"])
            score = "n/a" if instance["score"] is None else f"{instance['score']:.3f}"
            print(f"      score {score}  box ({x0}, {y0}, {x1}, {y1})")

    try:
        row = ports(results)
    except ValueError as error:
        sys.exit(f"frame rejected: {error}")

    cables = results[CABLE_PROMPT]
    attributed = {port["cable"] for port in row}
    for index, cable in enumerate(cables):
        if index not in attributed:
            contours, _ = cv2.findContours(decode_rle(cable["mask_rle"]), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
            cv2.drawContours(overlay, contours, -1, EMPTY_GRAY, 1)

    print(f"ports: {len(row)}")
    for number, port in enumerate(row, start=1):
        x0, y0, x1, y1 = (round(v) for v in port["box"])
        hue = PORT_PALETTE[(number - 1) % len(PORT_PALETTE)] if port["occupied"] else EMPTY_GRAY
        cable = "" if port["cable"] is None else f"  cable {port['cable']}"
        print(f"  {number:2d}  {'occupied' if port['occupied'] else 'empty':8s}  box ({x0}, {y0}, {x1}, {y1}){cable}")
        if port["cable"] is not None:
            contours, _ = cv2.findContours(decode_rle(cables[port["cable"]]["mask_rle"]), cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
            cv2.drawContours(overlay, contours, -1, hue, 2)
        cv2.rectangle(overlay, (x0, y0), (x1, y1), hue, 2)
        (text_width, text_height), baseline = cv2.getTextSize(str(number), cv2.FONT_HERSHEY_SIMPLEX, 0.5, 1)
        label_top = max(y0 - text_height - baseline - 4, 0)
        cv2.rectangle(overlay, (x0, label_top), (x0 + text_width + 4, label_top + text_height + baseline + 4), LABEL_BACKGROUND, -1)
        cv2.putText(overlay, str(number), (x0 + 2, label_top + text_height + 2), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (255, 255, 255), 1, cv2.LINE_AA)

    out = image_path.with_suffix(".overlay.png")
    if not cv2.imwrite(str(out), overlay):
        sys.exit(f"failed to write {out}")
    print(f"overlay {out}")
    print(f"segment {elapsed:.2f} s ({backend(env)})")


if __name__ == "__main__":
    main()
