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

# `blocked-excluding-matches` (AGENT.md plan-parity harness): the same tally
# with MATCH queries dropped. Differs from the raw roll-up by exactly one
# rendering tag -- Q13 is MATCH after N7 right-join canonicalisation yet
# still carries the verdict-neutral `rendering` tag, so the raw count
# overstates the queries `rendering` blocks by one.
EXPECTED_CATEGORIES_EXCL = dict(EXPECTED_CATEGORIES, rendering=7)

LINE_RE = re.compile(r"^(Q\S+)\s+(MATCH|SHAPE-DIFF|UNPARSED|MISSING-NODE|ERROR|TIMEOUT)"
                     r"\s+\[([^\]]*)\]")
# R2 (plan-parity-fix-take2): the rollup gained an `unparsed=` field between
# shapediff and missingnode — the tool declining to answer, split out of the
# MISSING-NODE verdict it used to be conflated with.
ROLLUP_RE = re.compile(r"PLAN-PARITY:\s+queries=(\d+)\s+match=(\d+)\s+"
                       r"shapediff=(\d+)\s+unparsed=(\d+)\s+"
                       r"missingnode=(\d+)\s+"
                       r"error=(\d+)\s+timeout=(\d+)")
CATS_RE = re.compile(r"^CATEGORIES:\s+(.*)$")
CATS_EXCL_RE = re.compile(r"^CATEGORIES-EXCL-MATCH:\s+(.*)$")


def run_tool(*args):
    return subprocess.run([sys.executable, TOOL] + list(args),
                          capture_output=True, text=True, cwd=ROOT)


def parse_report(out):
    per_query, rollup, cats, cats_excl = {}, None, None, None
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
                      "UNPARSED": int(m.group(4)),
                      "MISSING-NODE": int(m.group(5)), "ERROR": int(m.group(6)),
                      "TIMEOUT": int(m.group(7)),
                      "queries": int(m.group(1))}
        m = CATS_RE.search(line)
        if m:
            cats = dict((k, int(v)) for k, v in
                        re.findall(r"(\S+)=(\d+)", m.group(1)))
        m = CATS_EXCL_RE.search(line)
        if m:
            cats_excl = dict((k, int(v)) for k, v in
                             re.findall(r"(\S+)=(\d+)", m.group(1)))
    return per_query, rollup, cats, cats_excl


class ParityDiffTest(unittest.TestCase):
    def test_self_test_passes(self):
        proc = run_tool("--self-test")
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("17/17 passed", proc.stdout)

    def test_corpus_budget(self):
        """Pinned mismatch budget: any plan move fails here by design."""
        proc = run_tool(GOOPG_PLANS, PG_DIR)
        self.assertEqual(proc.returncode, 0, proc.stderr)  # report-only
        per_query, rollup, cats, cats_excl = parse_report(proc.stdout)
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
        self.assertEqual(cats_excl, EXPECTED_CATEGORIES_EXCL)

    def test_pg_single_file_mode_agrees(self):
        """PG side as one === sections file matches the fixture-dir mode."""
        if not os.path.exists(PG_SINGLE):
            self.skipTest("paired PG capture absent")
        proc = run_tool(GOOPG_PLANS, PG_SINGLE)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        per_query, _, _, _ = parse_report(proc.stdout)
        for key, (verdict, _) in EXPECTED.items():
            self.assertIn(key, per_query)
            self.assertEqual(per_query[key][0], verdict,
                             "mode disagreement on %s" % key)

    def run_on_fixtures(self, sections):
        """Run the tool over an ad-hoc corpus: {key: (goopg_lines, pg_lines)}."""
        with tempfile.TemporaryDirectory() as tmp:
            goopg = os.path.join(tmp, "goopg.txt")
            pgdir = os.path.join(tmp, "pg")
            os.mkdir(pgdir)
            with open(goopg, "w") as fh:
                for key, (glines, _) in sections.items():
                    fh.write("=== %s\n" % key)
                    fh.write("".join(l + "\n" for l in glines))
            for key, (_, plines) in sections.items():
                with open(os.path.join(pgdir, "%s.txt" % key), "w") as fh:
                    fh.write("".join(l + "\n" for l in plines))
            proc = run_tool(goopg, pgdir)
            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            return parse_report(proc.stdout)

    # A MATCH carrying a category tag: Sort Key signatures differ, which N6
    # declares verdict-neutral `rendering`. The raw roll-up counts it; the
    # `blocked-excluding-matches` roll-up must not.
    RENDERING_MATCH = (
        [
            "Sort  (cost=10.00..10.10 rows=7 width=60)",
            "  Sort Key: a.x",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
        ],
        [
            "Sort  (cost=10.00..10.10 rows=7 width=60)",
            "  Sort Key: a.y",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
        ],
    )
    # A SHAPE-DIFF: swapped hash-join children => join-order.
    JOIN_ORDER_DIFF = (
        [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (a.x = b.x)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
        ],
        [
            "Hash Join  (cost=10.00..20.00 rows=5 width=10)",
            "  Hash Cond: (b.x = a.x)",
            "  ->  Seq Scan on b  (cost=0.00..5.00 rows=5 width=5)",
            "  ->  Seq Scan on a  (cost=0.00..5.00 rows=5 width=5)",
        ],
    )

    def test_categories_excl_match_drops_tagged_match(self):
        """A tagged MATCH separates the two roll-ups (the whole point)."""
        per_query, rollup, cats, cats_excl = self.run_on_fixtures({
            "Q1": self.RENDERING_MATCH,
            "Q2": self.JOIN_ORDER_DIFF,
        })
        self.assertEqual(per_query["Q1"], ("MATCH", ("rendering",)))
        self.assertEqual(per_query["Q2"], ("SHAPE-DIFF", ("join-order",)))
        self.assertEqual(rollup["MATCH"], 1)
        self.assertIsNotNone(cats_excl)
        self.assertNotEqual(cats, cats_excl)
        self.assertEqual(cats["rendering"], 1)
        self.assertEqual(cats_excl["rendering"], 0)
        # Non-MATCH tags are untouched, and both lines carry every category
        # in the same key order.
        self.assertEqual(cats["join-order"], 1)
        self.assertEqual(cats_excl["join-order"], 1)
        self.assertEqual(list(cats), list(cats_excl))

    def test_categories_excl_match_equals_raw_without_matches(self):
        """No MATCH query => the two roll-ups are identical."""
        _, rollup, cats, cats_excl = self.run_on_fixtures({
            "Q1": self.JOIN_ORDER_DIFF,
        })
        self.assertEqual(rollup["MATCH"], 0)
        self.assertEqual(cats, cats_excl)
        self.assertEqual(cats["join-order"], 1)

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
            per_query, rollup, _, _ = parse_report(proc.stdout)
            self.assertEqual(per_query["Q1"][0], "MATCH")
            self.assertEqual(per_query["Q2"][0], "ERROR")
            self.assertEqual(rollup["ERROR"], 1)

    def test_unavailable_input_is_report_only(self):
        proc = run_tool(os.path.join(ROOT, "no-such-file.txt"), PG_DIR)
        self.assertEqual(proc.returncode, 0)
        self.assertIn("unavailable", proc.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
