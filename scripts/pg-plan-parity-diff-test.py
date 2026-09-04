#!/usr/bin/env python3
"""pg-plan-parity-diff-test.py -- tests for scripts/pg-plan-parity-diff.py.

P0-06 gate: unit checks over recorded plan pairs (via --self-test) plus the
pinned TPC-H mismatch budget. Report-only instrument: the tool always exits
0; THIS test fails when the roll-up moves, forcing a human to re-verify the
budget before re-pinning.

Corpus: goopg analysis/leftdeep-joins/a01ii-cut3-paired.plans.txt
(=== QN sections) vs PG bench/tpch/plans-pg/QN.txt fixtures.

Budget provenance (pinned 2026-09-05, tool reviewed query-by-query):
- MATCH (5): Q1/Q6/Q14/Q15a-VIEWBODY identical after normalisation; Q13
  identical after right-join canonicalisation + Hash stripping (rendering
  notes on key text only).
- MISSING-NODE (2): Q5/Q8 -- PG-only Materialize over the 1-row region
  scan; goopg's EXPLAIN renderer has no Materialize arm.
- SHAPE-DIFF (15): remainder, categories verified per query (phases live
  here: join-order/method for Phase 3, aggregation/sort-strategy for
  Phase 4, parameterisation for decorrelation/SubPlan work).
- parallelism=0: both captures are serial EXPLAIN without ANALYZE; the
  category is implemented and self-tested, just empty on this corpus.

Usage: python3 scripts/pg-plan-parity-diff-test.py [-v]
"""

import os
import re
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "scripts", "pg-plan-parity-diff.py")
GOOPG_PLANS = os.path.join(
    ROOT, "analysis", "leftdeep-joins", "a01ii-cut3-paired.plans.txt")
PG_DIR = os.path.join(ROOT, "bench", "tpch", "plans-pg")
PG_SINGLE = os.path.join(
    ROOT, "analysis", "leftdeep-joins", "a01ii-cut3-paired.pg.plans.txt")

# Pinned mismatch budget: query -> (verdict, categories).
EXPECTED = {
    "Q1": ("MATCH", ()),
    "Q2": ("SHAPE-DIFF", ("join-order", "join-method", "parameterisation",
                           "aggregation-strategy")),
    "Q3": ("SHAPE-DIFF", ("join-method", "scan-type", "rendering")),
    "Q4": ("SHAPE-DIFF", ("aggregation-strategy", "sort-strategy",
                           "qual-placement")),
    "Q5": ("MISSING-NODE", ("join-order", "join-method",
                             "aggregation-strategy", "sort-strategy",
                             "qual-placement", "rendering")),
    "Q6": ("MATCH", ()),
    "Q7": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type",
                           "aggregation-strategy", "sort-strategy",
                           "qual-placement")),
    "Q8": ("MISSING-NODE", ("join-order", "join-method", "scan-type",
                             "aggregation-strategy", "sort-strategy")),
    "Q9": ("SHAPE-DIFF", ("join-order", "join-method", "rendering")),
    "Q10": ("SHAPE-DIFF", ("join-order", "rendering")),
    "Q11": ("SHAPE-DIFF", ("parameterisation", "qual-placement",
                            "rendering")),
    "Q12": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type",
                            "aggregation-strategy", "sort-strategy")),
    "Q13": ("MATCH", ("rendering",)),
    "Q14": ("MATCH", ()),
    "Q15a-VIEWBODY": ("MATCH", ()),
    "Q16": ("SHAPE-DIFF", ("join-order", "join-method", "parameterisation",
                            "rendering")),
    "Q17": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type",
                            "parameterisation", "qual-placement")),
    "Q18": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type",
                            "rendering")),
    "Q19": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type",
                            "qual-placement")),
    "Q20": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type",
                            "parameterisation", "qual-placement")),
    "Q21": ("SHAPE-DIFF", ("join-order", "join-method",
                            "aggregation-strategy", "sort-strategy")),
    "Q22": ("SHAPE-DIFF", ("parameterisation", "aggregation-strategy",
                            "sort-strategy")),
}

EXPECTED_ROLLUP = {"MATCH": 5, "SHAPE-DIFF": 15, "MISSING-NODE": 2,
                   "ERROR": 0, "TIMEOUT": 0}

EXPECTED_CATEGORIES = {"join-order": 13, "join-method": 13, "scan-type": 8,
                       "parameterisation": 6, "aggregation-strategy": 8,
                       "sort-strategy": 7, "parallelism": 0,
                       "qual-placement": 7, "rendering": 8}

LINE_RE = re.compile(r"^(Q\S+)\s+(MATCH|SHAPE-DIFF|MISSING-NODE|ERROR|TIMEOUT)"
                     r"\s+\[([^\]]*)\]")
ROLLUP_RE = re.compile(r"PLAN-PARITY:\s+queries=(\d+)\s+match=(\d+)\s+"
                       r"shapediff=(\d+)\s+missingnode=(\d+)\s+"
                       r"error=(\d+)\s+timeout=(\d+)")
CATS_RE = re.compile(r"CATEGORIES:\s+(.*)$")


def run_tool(*args):
    return subprocess.run([sys.executable, TOOL] + list(args),
                          capture_output=True, text=True, cwd=ROOT)


def parse_report(out):
    per_query, rollup, cats = {}, None, None
    for line in out.splitlines():
        m = LINE_RE.match(line)
        if m:
            key, verdict, raw = m.group(1), m.group(2), m.group(3)
            got = tuple(c for c in raw.split(",") if c)
            per_query[key] = (verdict, got)
            continue
        m = ROLLUP_RE.search(line)
        if m:
            rollup = {"MATCH": int(m.group(2)), "SHAPE-DIFF": int(m.group(3)),
                      "MISSING-NODE": int(m.group(4)), "ERROR": int(m.group(5)),
                      "TIMEOUT": int(m.group(6)),
                      "queries": int(m.group(1))}
        m = CATS_RE.search(line)
        if m:
            cats = dict((k, int(v)) for k, v in
                        re.findall(r"(\S+)=(\d+)", m.group(1)))
    return per_query, rollup, cats


class ParityDiffTest(unittest.TestCase):
    def test_self_test_passes(self):
        proc = run_tool("--self-test")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("15/15 passed", proc.stdout)

    def test_corpus_budget(self):
        """Pinned mismatch budget: any plan move fails here by design."""
        proc = run_tool(GOOPG_PLANS, PG_DIR)
        self.assertEqual(proc.returncode, 0, proc.stderr)  # report-only
        per_query, rollup, cats = parse_report(proc.stdout)
        self.assertEqual(set(per_query), set(EXPECTED),
                         "query set moved: %s" %
                         (set(per_query) ^ set(EXPECTED)))
        for key, (verdict, categories) in EXPECTED.items():
            self.assertEqual(per_query[key],
                             (verdict, categories),
                             "budget moved on %s" % key)
        self.assertEqual({k: rollup[k] for k in EXPECTED_ROLLUP},
                         EXPECTED_ROLLUP)
        self.assertEqual(rollup["queries"], len(EXPECTED))
        self.assertEqual(cats, EXPECTED_CATEGORIES)

    def test_pg_single_file_mode_agrees(self):
        """PG side as one === sections file matches the fixture-dir mode."""
        if not os.path.exists(PG_SINGLE):
            self.skipTest("paired PG capture absent")
        proc = run_tool(GOOPG_PLANS, PG_SINGLE)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        per_query, _, _ = parse_report(proc.stdout)
        for key, (verdict, _) in EXPECTED.items():
            self.assertIn(key, per_query)
            self.assertEqual(per_query[key][0], verdict,
                             "mode disagreement on %s" % key)

    def test_missing_section_is_error_but_exit_zero(self):
        with tempfile.TemporaryDirectory() as tmp:
            goopg = os.path.join(tmp, "goopg.txt")
            with open(goopg, "w") as fh:
                fh.write("=== Q1\n"
                         "Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)\n")
            pgdir = os.path.join(tmp, "pg")
            os.mkdir(pgdir)
            with open(os.path.join(pgdir, "Q1.txt"), "w") as fh:
                fh.write("Seq Scan on a  (cost=0.00..1.00 rows=5 width=5)\n")
            with open(os.path.join(pgdir, "Q2.txt"), "w") as fh:
                fh.write("Seq Scan on b  (cost=0.00..1.00 rows=5 width=5)\n")
            proc = run_tool(goopg, pgdir)
            self.assertEqual(proc.returncode, 0)  # report-only, always 0
            per_query, rollup, _ = parse_report(proc.stdout)
            self.assertEqual(per_query["Q1"][0], "MATCH")
            self.assertEqual(per_query["Q2"][0], "ERROR")
            self.assertEqual(rollup["ERROR"], 1)

    def test_unavailable_input_is_report_only(self):
        proc = run_tool(os.path.join(ROOT, "no-such-file.txt"), PG_DIR)
        self.assertEqual(proc.returncode, 0)
        self.assertIn("unavailable", proc.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
