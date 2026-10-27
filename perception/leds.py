from collections.abc import Iterable

import cv2
import numpy as np

GREEN = ((31, 40, 170), (90, 255, 255))
AMBER = ((5, 80, 170), (30, 255, 255))
LIT_FRACTION = 0.02
LINK_FRAMES = 0.5
BAND_UP, BAND_DOWN, BAND_SIDE = 0.45, 0.2, 0.1


def band(box: list[float], height: int, width: int) -> tuple[int, int, int, int]:
    x0, y0, x1, _ = box
    w = x1 - x0
    return (
        max(int(y0 - BAND_UP * w), 0),
        min(int(y0 + BAND_DOWN * w) + 1, height),
        max(int(x0 - BAND_SIDE * w), 0),
        min(int(x1 + BAND_SIDE * w) + 1, width),
    )


def leds(frames: Iterable[np.ndarray], boxes: list[list[float]]) -> list[dict[str, object]]:
    lit: list[list[float]] = []
    for frame in frames:
        hsv = cv2.cvtColor(frame, cv2.COLOR_BGR2HSV)
        row = []
        for box in boxes:
            top, bottom, left, right = band(box, *frame.shape[:2])
            region = hsv[top:bottom, left:right]
            mask = cv2.inRange(region, *GREEN) | cv2.inRange(region, *AMBER)
            row.append(np.count_nonzero(mask) / max(mask.size, 1))
        lit.append(row)
    if not lit:
        raise ValueError("no frames for LED detection")
    fractions = np.array(lit)
    peak = fractions.max(axis=0)
    energy = np.divide(fractions.std(axis=0), peak, out=np.zeros_like(peak), where=peak > 0)
    return [
        {
            "num": number,
            "link_on": bool(np.mean(fractions[:, number - 1] >= LIT_FRACTION) >= LINK_FRAMES),
            "activity_energy": round(float(energy[number - 1]), 4),
        }
        for number in range(1, len(boxes) + 1)
    ]
