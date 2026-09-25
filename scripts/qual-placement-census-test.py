#!/usr/bin/env python3
"""qual-placement-census-test.py -- tests for scripts/qual-placement-census.py
(M0137-0010).

Unlike scripts/pg-plan-parity-diff-test.py's sibling, the tool under test is
NOT report-only: it is meant to be a real gate (K91, AGENT.md §"Plan-parity
harness" -- "a harness must fail loudly"), so these tests assert its exit
code directly rather than only parsing its stdout.

Usage: python3 scripts/qual-placement-census-test.py [-v]
"""

import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "scripts", "qual-placement-census.py")


def run_tool(*args):
    return subprocess.run([sys.executable, TOOL] + list(args),
                          capture_output=True, text=True, cwd=ROOT)


def write(tmp, name, lines):
    path = os.path.join(tmp, name)
    with open(path, "w") as fh:
        fh.write("\n".join(lines) + "\n")
    return path


class QualPlacementCensusTest(unittest.TestCase):
    def test_self_test_passes(self):
        proc = run_tool("--self-test")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("5/5 passed", proc.stdout)

    def test_identical_arms_exit_zero(self):
        with tempfile.TemporaryDirectory() as tmp:
            body = [
                "=== Q1",
                "Seq Scan on lineitem  (cost=0.00..5.00 rows=100 width=550)",
                "  Filter: (l_shipdate < '1995-01-01')",
                "=== Q2",
                "Index Scan using i on t  (cost=0.00..1.00 rows=1 width=1)",
                "  Index Cond: (t.x = 1)",
            ]
            a = write(tmp, "a.txt", body)
            b = write(tmp, "b.txt", body)
            proc = run_tool(a, b)
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            self.assertIn("mismatch=0", proc.stdout)
            self.assertIn("Q1 OK filter=1 index_cond=0", proc.stdout)
            self.assertIn("Q2 OK filter=0 index_cond=1", proc.stdout)

    def test_lost_filter_line_is_a_nonzero_exit(self):
        """R56 Q78 shape: three Filter: lines vanish between arms. This is
        exactly the regression class that shipped past every values-green
        row-count/checksum sweep -- the gate must fail on it."""
        with tempfile.TemporaryDirectory() as tmp:
            a = write(tmp, "a.txt", [
                "=== Q78",
                "Hash Join  (cost=1.00..2.00 rows=1 width=1)",
                "  ->  Seq Scan on a  (cost=0.00..1.00 rows=1 width=1)",
                "        Filter: (a.x > 1)",
                "  ->  Seq Scan on b  (cost=0.00..1.00 rows=1 width=1)",
                "        Filter: (b.y > 1)",
                "        Filter: (b.z > 1)",
            ])
            b = write(tmp, "b.txt", [
                "=== Q78",
                "Hash Join  (cost=1.00..2.00 rows=1 width=1)",
                "  ->  Seq Scan on a  (cost=0.00..1.00 rows=1 width=1)",
                "  ->  Seq Scan on b  (cost=0.00..1.00 rows=1 width=1)",
            ])
            proc = run_tool(a, b)
            self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
            self.assertIn("Q78 MISMATCH filter 3->0", proc.stdout)
            self.assertIn("mismatch=1", proc.stdout)

    def test_index_cond_change_is_a_nonzero_exit(self):
        with tempfile.TemporaryDirectory() as tmp:
            a = write(tmp, "a.txt", [
                "=== Q1",
                "Index Scan using i on t  (cost=0.00..1.00 rows=1 width=1)",
                "  Index Cond: (t.x = 1)",
            ])
            b = write(tmp, "b.txt", [
                "=== Q1",
                "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)",
                "  Filter: (t.x = 1)",
            ])
            proc = run_tool(a, b)
            self.assertEqual(proc.returncode, 1)
            self.assertIn("Q1 MISMATCH", proc.stdout)

    def test_query_present_only_one_arm_is_a_nonzero_exit(self):
        with tempfile.TemporaryDirectory() as tmp:
            a = write(tmp, "a.txt", ["=== Q1", "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)"])
            b = write(tmp, "b.txt", ["=== Q2", "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)"])
            proc = run_tool(a, b)
            self.assertEqual(proc.returncode, 1)
            self.assertIn("Q1 ARM-ONLY present only in arm A", proc.stdout)
            self.assertIn("Q2 ARM-ONLY present only in arm B", proc.stdout)
            self.assertIn("mismatch=2", proc.stdout)

    def test_estimate_only_changes_do_not_trip_the_gate(self):
        """cost=/rows=/width= drift (an estimate change, not a qual move) is
        the K50 class the tool must stay blind to -- it counts cond LINES,
        not their contents or the node's estimate fields."""
        with tempfile.TemporaryDirectory() as tmp:
            a = write(tmp, "a.txt", [
                "=== Q1",
                "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)",
                "  Filter: (t.x = 1)",
            ])
            b = write(tmp, "b.txt", [
                "=== Q1",
                "Seq Scan on t  (cost=0.00..99.00 rows=999 width=44)",
                "  Filter: (t.x = 1)",
            ])
            proc = run_tool(a, b)
            self.assertEqual(proc.returncode, 0)
            self.assertIn("mismatch=0", proc.stdout)

    def test_unreadable_input_exits_two(self):
        proc = run_tool(os.path.join(ROOT, "no-such-file.txt"),
                        os.path.join(ROOT, "no-such-file-either.txt"))
        self.assertEqual(proc.returncode, 2)
        self.assertIn("unavailable", proc.stdout)

    def test_requires_two_arms(self):
        proc = run_tool()
        self.assertNotEqual(proc.returncode, 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
