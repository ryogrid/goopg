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


def write_plans(tmp, name, blocks):
    """blocks: {qid: [plan line, ...]} -> a capture file the tool can parse."""
    path = os.path.join(tmp, name)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write("# goopg: deadbeef test capture\n")
        for qid in sorted(blocks):
            # `===== Q1 =====`, not `=== Q1`: a capture written with the
            # wrong header parses as ZERO blocks and every assertion about
            # absence then passes VACUOUSLY. This repo has been bitten by that
            # exact off-by-two before (an awk range using `=== Q5`), and the
            # first draft of this file reproduced it.
            fh.write("===== Q%d =====\n" % qid)
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


if __name__ == "__main__":
    unittest.main(argv=[sys.argv[0]] + sys.argv[1:], verbosity=2)
