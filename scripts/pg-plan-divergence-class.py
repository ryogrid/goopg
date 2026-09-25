#!/usr/bin/env python3
"""pg-plan-divergence-class.py — per-stage divergence report (M0145-0002).

Sibling of scripts/pg-plan-first-divergence.py: it runs the same census
(one mutually-exclusive first-divergence record per divergent query) and
then maps each record to the divergence class the record belongs to — the
six medium-level divergences of
docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/
plan-flow-medium-abstraction.md ("The six divergences"), which are also the
M0145 stage boundaries. The dual-pipeline transition measures progress
per-slice (AGENT.md G8), not only by the differ's 9-category counts: when
M0145-0003 lands sublink pull-up, the D1 bucket is what should drain.

Classes (precedence order — first match wins, ordered by the pipeline
stage that could change the record, earliest first):

  D2-unionall      Append/Parallel Append/MergeAppend/SetOp/HashSetOp in
                   the diverging pair — the appendrel citizenship gap
                   (M0145-0004; pull_up_simple_union_all analogue).
  D1-sublink       SubPlan/InitPlan/Subquery Scan or a Semi/Anti join
                   detail in the diverging pair — M0145-0003.
  D6-cte           CTE Scan / WorkTable Scan / CTE <name> header /
                   Recursive Union in the diverging pair — the inline_cte
                   gap. Deferred: M0145 keeps the CTE boundary intact by
                   design.

  D3-partialpath   Gather/Gather Merge, a Parallel/Partial/Finalize kind
                   or detail, Materialize (the missing-node substrate),
                   or category=parallelism — executor-substrate divergences
                   OUTSIDE M0145's flow work (parallel hash build,
                   row-emitting PartialAgg).
  D4-upperrel      Sort/Limit/Unique/WindowAgg/LockRows/ProjectSet/Result
                   or an aggregate kind, or category in
                   {aggregation-strategy, sort-strategy} — M0145-0006's
                   upper-rel pathlist surface.
  D5-narrowing     CLOSED — M0144-0003c refuted the premise and the fix
                   landed inert; nothing in the census maps here by
                   construction. Kept in the report so the closure is
                   visible rather than silently absent.
  jointree-search  residual: a first-divergence record on the plain
                   searched jointree (join-order / join-method /
                   scan-type / qual-placement) with no D1-D6 marker —
                   M0145-0005's single-pass-DP surface.
  verdict          error / timeout / unparsed records — not a plan-shape
                   decision point at all.

Only the diverging pair (the record's pg/goopg children) is matched — the
parent is the agreed context, not the decision point: a scan-type or
join-order record UNDER a matched Semi join or CTE header is
jointree-search (M0145-0005), not the pull-up or inline gap.

Usage: pg-plan-divergence-class.py [--verbose] <goopg-capture> <pg-capture>
Same operand contract as pg-plan-first-divergence.py. Exit 2 on usage
error, 0 otherwise — a divergent corpus is the measurement, not a failure.
"""

import importlib.util
import os
import re
import sys
from collections import Counter

_HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location(
    "pg_plan_first_divergence",
    os.path.join(_HERE, "pg-plan-first-divergence.py"))
fd = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fd)
d = fd.d

VERDICT_CATS = ("error", "timeout", "unparsed")

_KIND_RES = (
    ("D2-unionall", re.compile(
        r"MergeAppend|\bAppend\b|\bSetOp\b|\bHashSetOp\b")),
    ("D1-sublink", re.compile(
        r"SubPlan|InitPlan|Subquery Scan|\bSemi\b|\bAnti\b")),
    ("D6-cte", re.compile(
        r"\bCTE\b|WorkTable Scan|Recursive Union")),
    ("D3-partialpath", re.compile(
        r"Gather|\bParallel\b|\bPartial\b|\bFinalize\b|Materialize")),
    ("D4-upperrel", re.compile(
        r"Incremental Sort|\bSort\b|\bLimit\b|\bUnique\b|\bWindowAgg\b|"
        r"\bLockRows\b|\bProjectSet\b|\bResult\b|Aggregate")),
)

# Classes whose stage is a category rather than a node kind. parallel_flag /
# workers divergences never produce a Gather-token record when the trees are
# otherwise aligned, so the category is the only signal for them.
_CAT_CLASS = {
    "parallelism": "D3-partialpath",
    "aggregation-strategy": "D4-upperrel",
    "sort-strategy": "D4-upperrel",
}


def classify(r):
    """One divergence class for one first-divergence record."""
    if r["cat"] in VERDICT_CATS:
        return "verdict"
    if r["cat"] == "missing-node":
        # The detail names the PG-only kinds (e.g. "PG-only kinds:
        # Materialize"); run them through the same kind rules — Materialize
        # and Incremental Sort are the D3/D4 executor-substrate gap.
        text = r["detail"]
        for cls, rx in _KIND_RES:
            if rx.search(text):
                return cls
        return "verdict"
    text = " | ".join((r["pg"], r["goopg"]))
    for cls, rx in _KIND_RES:
        if rx.search(text):
            return cls
    if r["cat"] in _CAT_CLASS:
        return _CAT_CLASS[r["cat"]]
    return "jointree-search"


def main(argv):
    verbose = "--verbose" in argv or "-v" in argv
    args = [a for a in argv if not a.startswith("-")]
    if len(args) != 2:
        sys.stderr.write(__doc__)
        return 2
    results = fd.run_census(args[0], args[1])

    counts = Counter()
    by_class = {}
    nmatch = 0
    for key in sorted(results, key=lambda k: (int(re.sub(r"\D", "", k) or 0), k)):
        r = results[key]
        if r is None:
            nmatch += 1
            continue
        cls = classify(r)
        counts[cls] += 1
        by_class.setdefault(cls, []).append(key)
        if verbose:
            print("%-6s %-15s cat=%-20s parent=%-18s pg=%-30s goopg=%s  %s"
                  % (key, cls, r["cat"], r["parent"], r["pg"], r["goopg"],
                     r["detail"]))

    order = ("D1-sublink", "D2-unionall", "D3-partialpath", "D4-upperrel",
             "D5-narrowing", "D6-cte", "jointree-search", "verdict")
    divergent = sum(counts.values())
    print("DIVERGENCE-CLASSES: queries=%d match=%d divergent=%d %s"
          % (len(results), nmatch, divergent,
             " ".join("%s=%d" % (c, counts.get(c, 0)) for c in order)))
    stage = {
        "D1-sublink": "M0145-0003 pull_up_sublinks analogue",
        "D2-unionall": "M0145-0004 appendrel citizenship",
        "D3-partialpath": "executor substrate — outside M0145 flow",
        "D4-upperrel": "M0145-0006 upper-rel pathlists",
        "D5-narrowing": "closed — M0144-0003c premise refuted",
        "D6-cte": "deferred — CTE boundary preserved in M0145",
        "jointree-search": "M0145-0005 single-pass DP (no D1-D6 marker)",
        "verdict": "error/timeout/unparsed — not a plan record",
    }
    for cls in order:
        qs = by_class.get(cls, [])
        print("  %-16s n=%-3d %s" % (cls, len(qs), stage[cls]))
        if qs:
            print("      %s" % " ".join(qs))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
