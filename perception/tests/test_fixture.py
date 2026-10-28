import json
import unittest
from pathlib import Path

import cv2

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


class AdjacentPlugsFrame(unittest.TestCase):
    def test_wide_plug_end_is_one_port_when_sfp_boxes_shrink_the_socket_median(self) -> None:
        sam = json.loads((TESTDATA / "sam-switch-face-2-split.json").read_text())
        row = segment.ports(sam["results"])
        self.assertEqual("".join("X" if port["occupied"] else "." for port in row), "XXX...X.")

    def test_socket_box_in_the_gap_between_neighbouring_plugs_is_not_a_port(self) -> None:
        sam = json.loads((TESTDATA / "sam-switch-face-2-gap.json").read_text())
        row = segment.ports(sam["results"])
        self.assertEqual("".join("X" if port["occupied"] else "." for port in row), "XXX...X.")


if __name__ == "__main__":
    unittest.main()
