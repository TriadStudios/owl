import sys
import time
from datetime import datetime
from pathlib import Path

import cv2
from reachy_mini import ReachyMini

ROOT = Path(__file__).resolve().parent.parent


def main() -> None:
    env = dict(
        line.split("=", 1)
        for line in (ROOT / ".env.local").read_text().splitlines()
        if "=" in line
    )
    host = env.get("OWL_REACHY_HOST", "").strip()
    if not host:
        sys.exit("OWL_REACHY_HOST missing from .env.local")

    with ReachyMini(host=host, connection_mode="network") as mini:
        deadline = time.monotonic() + 10
        frame = mini.media.get_frame()
        while frame is None and time.monotonic() < deadline:
            time.sleep(0.1)
            frame = mini.media.get_frame()
        K, D = mini.media.camera.K, mini.media.camera.D
    if frame is None:
        sys.exit("no frame from the camera within 10 s")
    if K is None or D is None:
        sys.exit("camera calibration not available from the SDK")

    undistorted = cv2.undistort(frame, K, D)
    frames = ROOT / "data" / "frames"
    frames.mkdir(parents=True, exist_ok=True)
    out = frames / f"{datetime.now():%Y%m%dT%H%M%S}.png"
    for path in (out, ROOT / "testdata" / "switch-face.png"):
        if not cv2.imwrite(str(path), undistorted):
            sys.exit(f"failed to write {path}")
    print(out)


if __name__ == "__main__":
    main()
