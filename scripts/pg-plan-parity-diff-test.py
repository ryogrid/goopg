#!/usr/bin/env python3
"""pg-plan-parity-diff-test.py -- tests for scripts/pg-plan-parity-diff.py.

P0-06 gate: unit checks over recorded plan pairs (via --self-test) plus the
pinned TPC-H mismatch budget. Report-only instrument: the tool always exits
0; THIS test fails when the roll-up moves, forcing a human to re-verify the
budget before re-pinning.

Corpus: goopg analysis/leftdeep-joins/a01ii-cut3-paired.plans.txt
(=== QN sections) vs PG bench/tpch/plans-pg/QN.txt fixtures.

Budget provenance:
- Re-pinned 2026-09-24: the PG fixtures were re-captured under the current
  measurement convention — postgresql.conf work_mem = 512MB (superseding
  the old capture-time SET work_mem='64MB' pin) and the canonical parallel
  mode (max_parallel_workers_per_gather=4; the previous fixtures predate
  it and hold serial PG plans). The goopg side is still the historical
  a01ii-cut3 paired capture, so this budget deliberately mixes eras: it is
  a movement tripwire, not a parity scoreboard.
- Now: MATCH (1) = Q13 (right-join canonicalisation + Hash stripping);
  SHAPE-DIFF (21); parallelism=19 (the PG side now plans Gather paths).
- Previous pin (2026-09-05, serial-era PG fixtures): MATCH (5)
  Q1/Q6/Q13/Q14/Q15a-VIEWBODY, MISSING-NODE (2) Q5/Q8 (PG-only
  Materialize over the 1-row region scan), SHAPE-DIFF (15),
  parallelism=0.

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

# Pinned mismatch budget: query -> (verdict, categories).
# Re-pinned 2026-09-24 for the work_mem=512MB / parallel-mode fixture
# re-capture -- see "Budget provenance" in the module docstring.
EXPECTED = {
    "Q1": ("SHAPE-DIFF", ("join-order", "aggregation-strategy", "sort-strategy", "parallelism")),
    "Q2": ("SHAPE-DIFF", ("join-order", "join-method", "parameterisation", "aggregation-strategy", "parallelism")),
    "Q3": ("SHAPE-DIFF", ("join-order", "join-method", "parallelism", "rendering")),
    "Q4": ("SHAPE-DIFF", ("join-order", "aggregation-strategy", "sort-strategy", "parallelism")),
    "Q5": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type", "aggregation-strategy", "sort-strategy", "parallelism", "rendering")),
    "Q6": ("SHAPE-DIFF", ("join-order", "aggregation-strategy", "parallelism")),
    "Q7": ("SHAPE-DIFF", ("join-order", "join-method", "aggregation-strategy", "sort-strategy", "parallelism", "qual-placement")),
    "Q8": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type", "aggregation-strategy", "sort-strategy", "parallelism")),
    "Q9": ("SHAPE-DIFF", ("join-order", "join-method", "aggregation-strategy", "parallelism", "rendering")),
    "Q10": ("SHAPE-DIFF", ("join-order", "parallelism", "rendering")),
    "Q11": ("SHAPE-DIFF", ("parameterisation", "qual-placement", "rendering")),
    "Q12": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type", "aggregation-strategy", "sort-strategy", "parallelism")),
    "Q13": ("MATCH", ("rendering",)),
    "Q14": ("SHAPE-DIFF", ("join-order", "scan-type", "aggregation-strategy", "parallelism")),
    "Q15a-VIEWBODY": ("SHAPE-DIFF", ("join-order", "aggregation-strategy", "sort-strategy", "parallelism")),
    "Q16": ("SHAPE-DIFF", ("join-order", "parameterisation", "sort-strategy", "parallelism", "rendering")),
    "Q17": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type", "parameterisation", "parallelism", "qual-placement")),
    "Q18": ("SHAPE-DIFF", ("join-order", "join-method", "aggregation-strategy", "parallelism", "qual-placement", "rendering")),
    "Q19": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type", "aggregation-strategy", "parallelism")),
    "Q20": ("SHAPE-DIFF", ("join-order", "join-method", "scan-type", "parameterisation", "qual-placement")),
    "Q21": ("SHAPE-DIFF", ("join-order", "join-method", "aggregation-strategy", "sort-strategy", "parallelism", "qual-placement")),
    "Q22": ("SHAPE-DIFF", ("join-order", "parameterisation", "aggregation-strategy", "sort-strategy", "parallelism")),
}

EXPECTED_ROLLUP = {"MATCH": 1, "SHAPE-DIFF": 21, "MISSING-NODE": 0,
                   "ERROR": 0, "TIMEOUT": 0}

EXPECTED_CATEGORIES = {"join-order": 20, "join-method": 12, "scan-type": 7,
                       "parameterisation": 6, "aggregation-strategy": 15,
                       "sort-strategy": 10, "parallelism": 19,
                       "qual-placement": 6, "rendering": 8}

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
        # The single-file input is synthesised from the current fixture dir:
        # each QN.txt already carries its `=== QN` header, so concatenating
        # them yields a valid sections file. The historical
        # a01ii-cut3-paired.pg.plans.txt is an era-pinned artifact and must
        # NOT be overwritten just to feed this test.
        if not os.path.isdir(PG_DIR):
            self.skipTest("PG fixture dir absent")
        with tempfile.TemporaryDirectory() as tmp:
            single = os.path.join(tmp, "pg.plans.txt")
            with open(single, "w") as fh:
                for name in sorted(os.listdir(PG_DIR)):
                    if not name.endswith(".txt"):
                        continue
                    with open(os.path.join(PG_DIR, name)) as src:
                        fh.write(src.read())
                    fh.write("\n")
            proc = run_tool(GOOPG_PLANS, single)
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
