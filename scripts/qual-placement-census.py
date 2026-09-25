#!/usr/bin/env python3
"""qual-placement-census.py -- Filter:/Index Cond: line census, diffed
between two goopg EXPLAIN captures (M0137-0010, M5 in
docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md
§2.5).

Why this exists
----------------
Two bugs shipped past the existing correctness channels because neither one
looks at qual PLACEMENT, only at final row counts / plan-vs-PG shape:

- R56's Q78 lost three `Filter:` lines (a `pushConjunctTraced` regression)
  while every values-green sweep (row-count + checksum digests) kept
  passing -- the query still returned the right rows on that data, it just
  filtered later in the tree than it should have.
- R83's Limit-below-Unique truncation bug was masked on the corpus's actual
  data only because the duplicate count stayed under the LIMIT (`78 < 100`);
  a values digest that never varies the duplicate/limit ratio cannot see it.

`pg-plan-parity-diff.py` (M0137's structural-diff sibling) WOULD flag a
qual-placement move, but only when both arms are (goopg, PG) pairs -- it
does not apply to a same-engine before/after arm pair, which is the shape
every M0139 slice actually produces (goopg pre-change vs goopg post-change,
same corpus, same stats epoch). This tool fills that gap: a much cheaper,
literal-count census that needs no PG oracle and no tree normalisation, so
it can run on every slice as a fast gate rather than an occasional deep
diff.

What it does NOT do: attempt any structural comparison, tree parsing, or
qual-signature normalisation. It only counts, per query section, how many
`Filter:` and `Index Cond:` property lines appear, and diffs those counts
between two arms. A count that changes is not automatically a regression
(a genuinely different plan shape can legitimately gain or lose a qual
line) -- it IS always something that must be looked at and adjudicated,
which is why this tool fails loudly (nonzero exit) rather than merely
reporting, unlike pg-plan-parity-diff.py's report-only contract.

Input format: files with one `=== KEY` section per query, e.g. an
`estimate-audit -plan-only` capture or a `scripts/capture-tpch.sh` /
`scripts/capture-tpcds.sh` output (the same convention every other tool in
this milestone group uses).

Usage:
  qual-placement-census.py ARM_A ARM_B [--verbose]
  qual-placement-census.py --self-test

Exit status:
  0  every query key present in both arms has identical Filter:/Index Cond:
     counts (and no key is present in only one arm)
  1  at least one query's counts differ between arms, or a query key is
     present in only one arm (this IS the gate -- see K91 in AGENT.md
     §"Plan-parity harness": a harness must fail loudly)
  2  an input file could not be read
"""

import argparse
import re
import sys

SECTION_RE = re.compile(r"^===\s*(\S+)\s*$")
COND_RE = re.compile(r"^\s*(Filter|Index Cond):")


def parse_sections(path):
    """file with === KEY sections -> {key: [lines]} (headers excluded).

    Same convention as scripts/pg-plan-parity-diff.py's parse_sections and
    the embedded splitter in
    docs/design/not_ralph/plan_parity_fix_take2/methodology/shape-delta.sh.
    """
    blocks, cur = {}, None
    with open(path, encoding="utf-8", errors="replace") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            m = SECTION_RE.match(line.strip())
            if m:
                cur = m.group(1)
                blocks[cur] = []
                continue
            if cur is None:
                continue
            blocks[cur].append(line.rstrip())
    return blocks


def count_conds(lines):
    """(filter_count, index_cond_count) over one query's captured lines."""
    filt = sum(1 for ln in lines if COND_RE.match(ln) and
               COND_RE.match(ln).group(1) == "Filter")
    idx = sum(1 for ln in lines if COND_RE.match(ln) and
              COND_RE.match(ln).group(1) == "Index Cond")
    return filt, idx


def census(arm_a, arm_b):
    """-> (rows, mismatches) where rows is a list of per-query dicts."""
    keys = sorted(set(arm_a) | set(arm_b), key=lambda k: (len(k), k))
    rows, mismatches = [], 0
    for key in keys:
        in_a, in_b = key in arm_a, key in arm_b
        if not (in_a and in_b):
            rows.append({"key": key, "status": "ARM-ONLY",
                        "only": "A" if in_a else "B"})
            mismatches += 1
            continue
        fa, ia = count_conds(arm_a[key])
        fb, ib = count_conds(arm_b[key])
        if fa != fb or ia != ib:
            rows.append({"key": key, "status": "MISMATCH",
                        "filter_a": fa, "filter_b": fb,
                        "index_cond_a": ia, "index_cond_b": ib})
            mismatches += 1
        else:
            rows.append({"key": key, "status": "OK",
                        "filter": fa, "index_cond": ia})
    return rows, mismatches


def print_report(rows, mismatches, verbose=False):
    for r in rows:
        if r["status"] == "OK":
            print("%s OK filter=%d index_cond=%d" %
                  (r["key"], r["filter"], r["index_cond"]))
        elif r["status"] == "ARM-ONLY":
            print("%s ARM-ONLY present only in arm %s" % (r["key"], r["only"]))
        else:
            parts = []
            if r["filter_a"] != r["filter_b"]:
                parts.append("filter %d->%d" % (r["filter_a"], r["filter_b"]))
            if r["index_cond_a"] != r["index_cond_b"]:
                parts.append("index_cond %d->%d" %
                             (r["index_cond_a"], r["index_cond_b"]))
            print("%s MISMATCH %s" % (r["key"], ", ".join(parts)))
    print("QUAL-PLACEMENT-CENSUS: queries=%d ok=%d mismatch=%d" %
          (len(rows), len(rows) - mismatches, mismatches))


def run(arm_a_path, arm_b_path, verbose=False):
    try:
        arm_a = parse_sections(arm_a_path)
    except OSError as e:
        print("unavailable: cannot read arm A %s: %s" % (arm_a_path, e))
        return 2
    try:
        arm_b = parse_sections(arm_b_path)
    except OSError as e:
        print("unavailable: cannot read arm B %s: %s" % (arm_b_path, e))
        return 2
    rows, mismatches = census(arm_a, arm_b)
    print_report(rows, mismatches, verbose=verbose)
    return 1 if mismatches else 0


# ---------------------------------------------------------------- self-test

SELF_TESTS = [
    {
        "name": "identical arms: OK, exit 0",
        "a": ["=== Q1",
              "Seq Scan on lineitem  (cost=0.00..5.00 rows=100 width=550)",
              "  Filter: (l_shipdate < '1995-01-01')"],
        "b": ["=== Q1",
              "Seq Scan on lineitem  (cost=0.00..5.00 rows=100 width=550)",
              "  Filter: (l_shipdate < '1995-01-01')"],
        "mismatches": 0,
    },
    {
        "name": "R56 Q78 shape: three Filter lines lost -> MISMATCH",
        "a": ["=== Q78",
              "Hash Join  (cost=1.00..2.00 rows=1 width=1)",
              "  ->  Seq Scan on a  (cost=0.00..1.00 rows=1 width=1)",
              "        Filter: (a.x > 1)",
              "  ->  Seq Scan on b  (cost=0.00..1.00 rows=1 width=1)",
              "        Filter: (b.y > 1)",
              "        Filter: (b.z > 1)"],
        "b": ["=== Q78",
              "Hash Join  (cost=1.00..2.00 rows=1 width=1)",
              "  ->  Seq Scan on a  (cost=0.00..1.00 rows=1 width=1)",
              "  ->  Seq Scan on b  (cost=0.00..1.00 rows=1 width=1)"],
        "mismatches": 1,
    },
    {
        "name": "Index Cond count changes -> MISMATCH",
        "a": ["=== Q1",
              "Index Scan using i on t  (cost=0.00..1.00 rows=1 width=1)",
              "  Index Cond: (t.x = 1)"],
        "b": ["=== Q1",
              "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)",
              "  Filter: (t.x = 1)"],
        "mismatches": 1,
    },
    {
        "name": "query present only in one arm -> ARM-ONLY, MISMATCH",
        "a": ["=== Q1",
              "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)"],
        "b": ["=== Q2",
              "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)"],
        "mismatches": 2,
    },
    {
        "name": "unrelated line changes (cost/rows) do not count as a qual change",
        "a": ["=== Q1",
              "Seq Scan on t  (cost=0.00..1.00 rows=1 width=1)",
              "  Filter: (t.x = 1)"],
        "b": ["=== Q1",
              "Seq Scan on t  (cost=0.00..99.00 rows=999 width=44)",
              "  Filter: (t.x = 1)"],
        "mismatches": 0,
    },
]


def self_test():
    passed = 0
    for t in SELF_TESTS:
        arm_a = parse_sections_from_lines(t["a"])
        arm_b = parse_sections_from_lines(t["b"])
        rows, mismatches = census(arm_a, arm_b)
        ok = mismatches == t["mismatches"]
        status = "PASS" if ok else "FAIL"
        if ok:
            passed += 1
        else:
            print("%s: %s (want mismatches=%d got=%d)" %
                  (status, t["name"], t["mismatches"], mismatches))
    print("%d/%d passed" % (passed, len(SELF_TESTS)))
    return passed == len(SELF_TESTS)


def parse_sections_from_lines(lines):
    blocks, cur = {}, None
    for raw in lines:
        line = raw.rstrip("\n")
        m = SECTION_RE.match(line.strip())
        if m:
            cur = m.group(1)
            blocks[cur] = []
            continue
        if cur is None:
            continue
        blocks[cur].append(line.rstrip())
    return blocks


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("arm_a", nargs="?")
    ap.add_argument("arm_b", nargs="?")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()
    if args.self_test:
        sys.exit(0 if self_test() else 1)
    if not args.arm_a or not args.arm_b:
        ap.error("ARM_A and ARM_B are required unless --self-test is given")
    sys.exit(run(args.arm_a, args.arm_b, verbose=args.verbose))


if __name__ == "__main__":
    main()
