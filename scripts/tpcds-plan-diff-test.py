#!/usr/bin/env python3
"""tpcds-plan-diff-test.py -- tests for the join-method election channel in
scripts/tpcds-plan-diff.py (M0145-0022).

The channel exists because M0145-0012 and M0145-0018 both produced plans whose
VALUES were byte-identical at SF0.25 while the shape and the clock regressed —
a class no value gate can see. These tests protect the two things that make it
trustworthy: the node-name matching (where "Parallel Hash Join" and "Nested
Loop Left Join" are the shapes a naive regex gets wrong) and the direction
call, since flagging the wrong direction would train a reader to ignore it.

Usage: python3 scripts/tpcds-plan-diff-test.py [-v]
"""

import os
import subprocess
import sys
import tempfile
import unittest

TOOL = os.path.join(os.path.dirname(os.path.abspath(__file__)), "tpcds-plan-diff.py")


def write_plans(tmp, name, blocks, header="===== Q%d ====="):
    """blocks: {qid: [plan line, ...]} -> a capture file the tool can parse.

    `header` selects which of the harness's TWO capture formats to write —
    the sweep's `===== Qn =====` or jointree-parity-capture.sh's `=== Qn`.
    """
    path = os.path.join(tmp, name)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write("# goopg: deadbeef test capture\n")
        for qid in sorted(blocks):
            # `===== Q1 =====`, not `=== Q1`: a capture written with the
            # wrong header parses as ZERO blocks and every assertion about
            # absence then passes VACUOUSLY. This repo has been bitten by that
            # exact off-by-two before (an awk range using `=== Q5`), and the
            # first draft of this file reproduced it.
            fh.write((header % qid) + "\n")
            for line in blocks[qid]:
                fh.write(line + "\n")
    return path


def run(old, new):
    p = subprocess.run([sys.executable, TOOL, old, new],
                       capture_output=True, text=True)
    return p.stdout


class JoinMethodElectionTest(unittest.TestCase):
    def diff(self, old_blocks, new_blocks):
        with tempfile.TemporaryDirectory() as tmp:
            return run(write_plans(tmp, "old.txt", old_blocks),
                       write_plans(tmp, "new.txt", new_blocks))

    def test_identical_plans_emit_no_election_line(self):
        b = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        out = self.diff(b, b)
        self.assertNotIn("JOIN-METHOD-ELECTION", out)

    def test_move_into_nested_loop_is_flagged_as_a_suspect(self):
        old = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        new = {1: ["  ->  Nested Loop  (cost=1..9 rows=3 width=4)"]}
        out = self.diff(old, new)
        self.assertIn("into-nestloop=1", out)
        self.assertIn("suspects=Q1", out)
        self.assertIn("hash-1", out)
        self.assertIn("nestloop+1", out)

    def test_move_out_of_nested_loop_is_reported_but_not_a_suspect(self):
        old = {1: ["  ->  Nested Loop  (cost=1..9 rows=3 width=4)"]}
        new = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        out = self.diff(old, new)
        self.assertIn("moved=1", out)
        self.assertIn("into-nestloop=0", out)
        self.assertNotIn("suspects=", out)

    def test_parallel_hash_join_counts_once_as_hash(self):
        # The subtlety a naive regex gets wrong: "Parallel Hash Join" contains
        # "Hash Join", so an unordered alternation would count it twice, and a
        # bare `Hash` match would also catch the "Parallel Hash" build node.
        old = {1: ["  ->  Nested Loop  (cost=1..9 rows=3 width=4)"]}
        new = {1: [
            "  ->  Parallel Hash Join  (cost=1..2 rows=3 width=4)",
            "        ->  Parallel Hash  (cost=1..1 rows=3 width=4)",
        ]}
        out = self.diff(old, new)
        self.assertIn("hash+1", out)
        self.assertNotIn("hash+2", out)

    def test_outer_join_spellings_count_as_their_family(self):
        old = {1: ["  ->  Merge Left Join  (cost=1..2 rows=3 width=4)"]}
        new = {1: ["  ->  Nested Loop Left Join  (cost=1..9 rows=3 width=4)"]}
        out = self.diff(old, new)
        self.assertIn("merge-1", out)
        self.assertIn("nestloop+1", out)
        self.assertIn("suspects=Q1", out)

    def test_report_only_never_changes_exit_status(self):
        old = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        new = {1: ["  ->  Nested Loop  (cost=1..9 rows=3 width=4)"]}
        with tempfile.TemporaryDirectory() as tmp:
            o = write_plans(tmp, "old.txt", old)
            n = write_plans(tmp, "new.txt", new)
            p = subprocess.run([sys.executable, TOOL, o, n],
                               capture_output=True, text=True)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_a_changed_plan_with_no_join_move_is_not_listed(self):
        # Only join-method MOVES are reported: a cost-only change is shape
        # movement the PLAN-SHAPE line already covers, and repeating it here
        # would bury the signal this channel exists for.
        old = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        new = {1: ["  ->  Hash Join  (cost=1..5 rows=3 width=4)"]}
        out = self.diff(old, new)
        self.assertIn("moved=0", out)
        self.assertNotIn("# join-method: Q1", out)


class CaptureFormatTest(unittest.TestCase):
    """Both capture formats must parse, and neither may fail vacuously.

    Until the tool accepted `=== Qn`, pointing it at a jointree capture
    printed `queries=0 same=0 changed=0` — a pass indistinguishable from
    "the plans agree". Two loops hand-rolled their own diff because of it and
    were then bitten by psql-path noise this tool already normalises.
    """

    def test_jointree_capture_header_parses(self):
        blocks = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        with tempfile.TemporaryDirectory() as tmp:
            o = write_plans(tmp, "old.txt", blocks, header="=== Q%d")
            n = write_plans(tmp, "new.txt", blocks, header="=== Q%d")
            out = run(o, n)
        self.assertIn("queries=1", out)
        self.assertNotIn("queries=0", out)

    def test_sweep_capture_header_still_parses(self):
        blocks = {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]}
        with tempfile.TemporaryDirectory() as tmp:
            o = write_plans(tmp, "old.txt", blocks)
            n = write_plans(tmp, "new.txt", blocks)
            out = run(o, n)
        self.assertIn("queries=1", out)

    def test_sub_labelled_block_is_its_own_query_not_its_neighbours(self):
        """M0145-0021b: the TPC-H capture writes `=== Q15a-VIEWBODY` and NO
        plain `=== Q15` block at all.

        Before the fold that header matched nothing, so `cur` stayed on the
        PRECEDING query and Q15a's plan was appended to Q14's block — measured
        on a real capture: 21 ids compared instead of 22, Q15 absent, Q14
        carrying a plan that is not Q14's. Both assertions are that failure.
        """
        with tempfile.TemporaryDirectory() as tmp:
            def capture(name, q15a):
                path = os.path.join(tmp, name)
                with open(path, "w", encoding="utf-8") as fh:
                    fh.write("# goopg: deadbeef test capture\n")
                    fh.write("=== Q14\n  ->  Hash Join  (cost=1..2 rows=3 width=4)\n")
                    fh.write("=== Q15a-VIEWBODY\n" + q15a + "\n")
                return path
            body = "  ->  Seq Scan on lineitem  (cost=1..2 rows=3 width=4)"
            out = run(capture("old.txt", body), capture("new.txt", body))
        self.assertIn("queries=2", out)     # Q14 AND Q15, not one merged block
        self.assertIn("changed=0", out)

    def test_a_sub_block_change_is_attributed_to_its_own_id(self):
        # Folding must not swallow the sub-block's content, and must not
        # misattribute it: the change below is Q15's, never Q14's.
        with tempfile.TemporaryDirectory() as tmp:
            def capture(name, q15a):
                path = os.path.join(tmp, name)
                with open(path, "w", encoding="utf-8") as fh:
                    fh.write("# goopg: deadbeef test capture\n")
                    fh.write("=== Q14\n  ->  Hash Join  (cost=1..2 rows=3 width=4)\n")
                    fh.write("=== Q15a-VIEWBODY\n" + q15a + "\n")
                return path
            out = run(capture("old.txt", "  ->  Seq Scan on lineitem  (cost=1..2 rows=3 width=4)"),
                      capture("new.txt", "  ->  Index Scan on lineitem  (cost=1..2 rows=3 width=4)"))
        self.assertIn("changed (1): Q15", out)

    def test_zero_block_capture_is_fatal_not_a_clean_result(self):
        # The safety property: a capture the tool cannot parse must never read
        # as "no differences".
        with tempfile.TemporaryDirectory() as tmp:
            good = write_plans(tmp, "good.txt", {1: ["  ->  Hash Join  (cost=1..2 rows=3 width=4)"]})
            bad = os.path.join(tmp, "bad.txt")
            with open(bad, "w", encoding="utf-8") as fh:
                fh.write("# goopg: a capture with no query blocks at all\n")
            p = subprocess.run([sys.executable, TOOL, good, bad],
                               capture_output=True, text=True)
            self.assertEqual(p.returncode, 2, p.stdout)
            self.assertIn("parsed 0 query blocks", p.stdout)
            self.assertNotIn("PLAN-SHAPE", p.stdout)


if __name__ == "__main__":
    unittest.main(argv=[sys.argv[0]] + sys.argv[1:], verbosity=2)
