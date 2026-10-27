import sys
import time
from collections.abc import Iterator
from datetime import datetime
from pathlib import Path

import cv2
import numpy as np
from reachy_mini import ReachyMini

import segment

ROOT = Path(__file__).resolve().parent.parent
FIRST_FRAME_TIMEOUT_S = 10
FRAME_INTERVAL_S = 1 / 30


def connect(env: dict[str, str]) -> ReachyMini:
    host = env.get("OWL_REACHY_HOST", "").strip()
    if not host:
        raise RuntimeError("OWL_REACHY_HOST missing from .env.local")
    return ReachyMini(host=host, connection_mode="network")


def frames(mini: ReachyMini, seconds: float) -> Iterator[np.ndarray]:
    K, D = mini.media.camera.K, mini.media.camera.D
    if K is None or D is None:
        raise RuntimeError("camera calibration not available from the SDK")
    give_up = time.monotonic() + FIRST_FRAME_TIMEOUT_S
    end = None
    while end is None or time.monotonic() < end:
        frame = mini.media.get_frame()
        if frame is None:
            if end is None and time.monotonic() > give_up:
                raise RuntimeError(f"no frame from the camera within {FIRST_FRAME_TIMEOUT_S} s")
        else:
            yield cv2.undistort(frame, K, D)
            if end is None:
                end = time.monotonic() + seconds
        time.sleep(FRAME_INTERVAL_S)


def main() -> None:
    try:
        with connect(segment.load_env()) as mini:
            undistorted = next(frames(mini, 0))
    except RuntimeError as error:
        sys.exit(str(error))

    out_dir = ROOT / "data" / "frames"
    out_dir.mkdir(parents=True, exist_ok=True)
    out = out_dir / f"{datetime.now():%Y%m%dT%H%M%S}.png"
    for path in (out, ROOT / "testdata" / "switch-face.png"):
        if not cv2.imwrite(str(path), undistorted):
            sys.exit(f"failed to write {path}")
    print(out)


if __name__ == "__main__":
    main()
