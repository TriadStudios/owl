import hashlib
import json
import threading
from datetime import datetime, timezone

import cv2
import uvicorn
from fastapi import FastAPI, HTTPException, Request
from starlette.concurrency import run_in_threadpool

import segment

MAX_IMAGE_BYTES = 20 * 2**20
POLYGON_EPSILON_PX = 1.5

app = FastAPI()
analyze_lock = threading.Lock()


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
    return {"ports": ports, "frame_hash": hashlib.sha256(image_bytes).hexdigest()}


@app.get("/health")
def health() -> dict[str, object]:
    env = segment.load_env()
    spend = json.loads(segment.SPEND_PATH.read_text()) if segment.SPEND_PATH.exists() else {}
    return {
        "backend": segment.backend(env),
        "billed_today": spend.get(datetime.now(timezone.utc).date().isoformat(), 0),
        "daily_cap": int(env.get("OWL_SAM_DAILY_CAP", "2000")),
    }


@app.post("/segment")
async def segment_frame(request: Request) -> dict[str, object]:
    image_bytes = await request.body()
    if not image_bytes or len(image_bytes) > MAX_IMAGE_BYTES:
        raise HTTPException(413 if image_bytes else 400, f"send one image of at most {MAX_IMAGE_BYTES} bytes as the request body")
    try:
        return await run_in_threadpool(describe, image_bytes)
    except ValueError as error:
        raise HTTPException(422, str(error)) from error
    except RuntimeError as error:
        raise HTTPException(503, str(error)) from error


def main() -> None:
    uvicorn.run(app, host="127.0.0.1", port=8701)
