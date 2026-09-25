#!/usr/bin/env python3
"""seam-decline-census-test.py -- tests for scripts/seam-decline-census.py
(M0137-0014).

Usage: python3 scripts/seam-decline-census-test.py [-v]
"""

import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "scripts", "seam-decline-census.py")


def run_tool(*args):
    return subprocess.run([sys.executable, TOOL] + list(args),
                          capture_output=True, text=True, cwd=ROOT)


def write(tmp, name, lines):
    path = os.path.join(tmp, name)
    with open(path, "w") as fh:
        fh.write("\n".join(lines) + "\n")
    return path


class SeamDeclineCensusTest(unittest.TestCase):
    def test_self_test_passes(self):
        proc = run_tool("--self-test")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("4/4 passed", proc.stdout)

    def test_single_log_single_class(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = write(tmp, "trace.log", [
                "DPTRACE seam-decline reason=leaf-count nrels=11 nleaves=1",
                "DPTRACE seam-decline reason=leaf-count nrels=6 nleaves=1",
            ])
            proc = run_tool("--timeout", "10m", log)
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            self.assertIn("SEAM-DECLINE-CENSUS: timeout=10m logs=1 classes=1 declines=2",
                          proc.stdout)
            self.assertIn("     2 reason=leaf-count", proc.stdout)

    def test_multiple_logs_aggregate_and_sort_by_reason(self):
        with tempfile.TemporaryDirectory() as tmp:
            a = write(tmp, "a.log", [
                "DPTRACE seam-decline reason=outer-spine nrels=4 nleaves=4",
            ])
            b = write(tmp, "b.log", [
                "DPTRACE seam-decline reason=lateral nrels=3 nleaves=3",
                "DPTRACE seam-decline reason=outer-spine nrels=2 nleaves=2",
            ])
            proc = run_tool("--timeout", "10m", a, b)
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            self.assertIn("logs=2 classes=2 declines=3", proc.stdout)
            lateral_line = proc.stdout.index("reason=lateral")
            spine_line = proc.stdout.index("reason=outer-spine")
            self.assertLess(lateral_line, spine_line,
                            "expected alphabetical class order (lateral before outer-spine)")

    def test_non_decline_lines_are_ignored(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = write(tmp, "trace.log", [
                "DPTRACE top=t1+t2 pairs=3 declined=0 costs=3 status=ok",
                "some unrelated server log line",
            ])
            proc = run_tool("--timeout", "10m", log)
            self.assertEqual(proc.returncode, 0)
            self.assertIn("classes=0 declines=0", proc.stdout)

    def test_missing_timeout_exits_two(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = write(tmp, "trace.log", [
                "DPTRACE seam-decline reason=leaf-count nrels=1 nleaves=1",
            ])
            proc = run_tool(log)
            self.assertEqual(proc.returncode, 2, proc.stdout + proc.stderr)
            self.assertIn("unavailable", proc.stdout)
            self.assertIn("--timeout", proc.stdout)

    def test_unreadable_log_exits_two(self):
        proc = run_tool("--timeout", "10m",
                        os.path.join(ROOT, "no-such-seam-decline-log.txt"))
        self.assertEqual(proc.returncode, 2, proc.stdout + proc.stderr)
        self.assertIn("unavailable", proc.stdout)

    def test_requires_at_least_one_log(self):
        proc = run_tool("--timeout", "10m")
        self.assertNotEqual(proc.returncode, 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
