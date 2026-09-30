"""Regression tests for the coverage audit; executed by GitHub Actions."""

import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location(
    "go_coverage", Path(__file__).resolve().parents[1] / "go_coverage.py")
COVERAGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COVERAGE)


class CoverageAuditTest(unittest.TestCase):
    def test_duplicate_blocks_merge_execution_counts(self):
        report = COVERAGE.summarize(
            "mode: atomic\n"
            "shop/a.go:1.1,2.2 3 0\n"
            "shop/a.go:1.1,2.2 3 2\n"
            "shop/b.go:1.1,2.2 1 0\n")
        self.assertEqual((report["covered"], report["total"]), (3, 4))
        self.assertEqual(report["percent"], "75")
        self.assertFalse(report["target_met"])
        self.assertEqual(report["files"]["shop/b.go"]["uncovered_blocks"], ["1.1,2.2"])
        self.assertTrue(COVERAGE.meets_minimum(report, "75"))
        self.assertFalse(COVERAGE.meets_minimum(report, "75.01"))

    def test_rounded_100_is_not_full_coverage(self):
        report = COVERAGE.summarize(
            "mode: count\nshop/a.go:1.1,2.2 99999 1\nshop/b.go:1.1,2.2 1 0\n")
        self.assertFalse(COVERAGE.meets_minimum(report, "100"))
        self.assertFalse(report["target_met"])

    def test_real_full_coverage(self):
        report = COVERAGE.summarize("mode: set\nshop/a.go:1.1,2.2 4 1\n")
        self.assertTrue(report["target_met"])
        self.assertTrue(COVERAGE.meets_minimum(report, "100"))
        self.assertEqual(report["uncovered"], 0)

    def test_invalid_or_empty_profiles_fail_closed(self):
        for text in ["", "mode: atomic\n", "mode: invalid\n", "mode: set\nbad\n",
                     "mode: set\na.go:1.1,2.2 0 0\n",
                     "mode: set\na.go:1.1,2.2 1 0\na.go:1.1,2.2 2 1\n"]:
            with self.subTest(text=text), self.assertRaises(ValueError):
                COVERAGE.summarize(text)

    def test_invalid_thresholds_fail_closed(self):
        for minimum in ["-1", "101", "NaN", "Infinity"]:
            with self.subTest(minimum=minimum), self.assertRaises(ValueError):
                COVERAGE.meets_minimum({"covered": 1, "total": 1}, minimum)


if __name__ == "__main__":
    unittest.main()
