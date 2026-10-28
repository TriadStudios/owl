import json
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
HOME_POSE = ROOT / "data" / "home-pose.json"
HOME_MOVE_S = 1.0
HOME_PASSES = 4
HOME_TOLERANCE_DEG = 1.5
FIRST_FRAME_TIMEOUT_S = 10
FRAME_INTERVAL_S = 1 / 30


def connect(env: dict[str, str]) -> ReachyMini:
    host = env.get("OWL_REACHY_HOST", "").strip()
    if not host:
        raise RuntimeError("OWL_REACHY_HOST missing from .env.local")
    return ReachyMini(host=host, connection_mode="network")


def home(mini: ReachyMini) -> None:
    if not HOME_POSE.exists():
        return
    head = np.array(json.loads(HOME_POSE.read_text())["head"], dtype=float)
    if head.shape != (4, 4):
        raise RuntimeError(f"{HOME_POSE.name} must hold a 4x4 head pose")
    mini.enable_motors()
    # The head sags under load and keeps creeping while held, and a goto to the current target does not move it, so correct from the measured pose.
    target = head
    for _ in range(HOME_PASSES):
        mini.goto_target(target, duration=HOME_MOVE_S)
        time.sleep(HOME_MOVE_S + 0.3)
        actual = mini.get_current_head_pose()
        if np.degrees(np.arccos(np.clip((np.trace(actual[:3, :3].T @ head[:3, :3]) - 1) / 2, -1, 1))) < HOME_TOLERANCE_DEG:
            return
        target = target @ np.linalg.inv(actual) @ head


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
            home(mini)
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
