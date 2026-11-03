import json
import unittest
from pathlib import Path

import cv2
import numpy as np

import leds
import segment

TESTDATA = Path(__file__).resolve().parents[2] / "testdata"


class FixtureFrame(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.sam = json.loads((TESTDATA / "sam-switch-face.json").read_text())
        results = dict(cls.sam["results"])
        # The fixture was captured with the earlier "ethernet cable" prompt.
        results[segment.CABLE_PROMPT] = results.pop("ethernet cable")
        cls.row = segment.ports(results)

    def test_numbers_eight_ports_with_first_three_plugged(self) -> None:
        self.assertEqual([port["occupied"] for port in self.row], [True] * 3 + [False] * 5)
        centers = [(port["box"][0] + port["box"][2]) / 2 for port in self.row]
        self.assertEqual(centers, sorted(centers))

    def test_each_plugged_port_owns_a_distinct_cable(self) -> None:
        cables = [port["cable"] for port in self.row if port["occupied"]]
        self.assertNotIn(None, cables)
        self.assertEqual(len(set(cables)), len(cables))
        self.assertTrue(all(port["cable"] is None for port in self.row if not port["occupied"]))

    def test_link_leds_lit_only_on_plugged_ports(self) -> None:
        frame = cv2.imread(str(TESTDATA / "switch-face.png"))
        x0, y0 = self.sam["rect"][:2]
        boxes = [[p["box"][0] / 2 + x0, p["row_band"][0] / 2 + y0, p["box"][2] / 2 + x0, p["row_band"][1] / 2 + y0] for p in self.row]
        self.assertEqual([led["link_on"] for led in leds.leds([frame], boxes)], [True] * 3 + [False] * 5)


    def test_hand_from_the_side_is_placed_by_its_fingertips_not_by_the_wrist_below_them(self) -> None:
        x0, y0, x1, y1 = (int(v) for v in self.row[2]["box"])
        width = 2 * int(self.row[-1]["box"][2])
        hand = np.zeros((4 * y1, width), np.uint8)
        hand[y0 - 20 :, int(self.row[5]["box"][0]) :] = 1
        hand[y0 - 20 : y0, x0:width] = 1
        hand[y1 : y1 + 6, x1 - 6 : width] = 1
        self.assertEqual(segment.touching(self.row, [{"score": 0.9, "mask_rle": segment.encode_rle(hand)}]), 3)

    def test_pinch_is_placed_between_the_lowest_fingertips_not_by_the_knuckles_beside_them(self) -> None:
        x0, y0, x1, y1 = (int(v) for v in self.row[4]["box"])
        n1 = int(self.row[5]["box"][2])
        hand = np.zeros((2 * y1, 2 * int(self.row[-1]["box"][2])), np.uint8)
        hand[: (y0 + y1) // 2, x0:n1] = 1
        hand[:y1, x0 - 3 : x0 + 3] = 1
        hand[:y1, x1 - 3 : x1 + 3] = 1
        self.assertEqual(segment.touching(self.row, [{"score": 0.9, "mask_rle": segment.encode_rle(hand)}]), 5)

    def test_hand_above_the_switch_touches_nothing(self) -> None:
        y0, y1 = self.row[4]["box"][1::2]
        hand = np.zeros((2 * int(y1), 2 * int(self.row[-1]["box"][2])), np.uint8)
        hand[: int(y0 - 2 * (y1 - y0)), :] = 1
        self.assertIsNone(segment.touching(self.row, [{"score": 0.9, "mask_rle": segment.encode_rle(hand)}]))


class AdjacentPlugsFrame(unittest.TestCase):
    def test_wide_plug_end_is_one_port_when_sfp_boxes_shrink_the_socket_median(self) -> None:
        sam = json.loads((TESTDATA / "sam-switch-face-2-split.json").read_text())
        row = segment.ports(sam["results"])
        self.assertEqual("".join("X" if port["occupied"] else "." for port in row), "XXX...X.")

    def test_socket_box_in_the_gap_between_neighbouring_plugs_is_not_a_port(self) -> None:
        sam = json.loads((TESTDATA / "sam-switch-face-2-gap.json").read_text())
        row = segment.ports(sam["results"])
        self.assertEqual("".join("X" if port["occupied"] else "." for port in row), "XXX...X.")


class CrossingCableFrame(unittest.TestCase):
    def test_flat_cable_crossing_the_row_from_port_3_is_not_read_as_plugs_in_ports_1_and_2(self) -> None:
        sam = json.loads((TESTDATA / "sam-phone-crossing-cable.json").read_text())
        row = segment.ports(sam["results"])
        self.assertEqual("".join("X" if port["occupied"] else "." for port in row), "X.X.X.X.")

    def test_socket_half_hidden_by_a_crossing_cable_is_one_port_not_two(self) -> None:
        sam = json.loads((TESTDATA / "sam-phone-crossing-cable-2.json").read_text())
        row = segment.ports(sam["results"])
        self.assertEqual("".join("X" if port["occupied"] else "." for port in row), "X.X.X.X.")


if __name__ == "__main__":
    unittest.main()
