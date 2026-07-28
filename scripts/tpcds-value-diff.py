#!/usr/bin/env python3
"""Field-normalising value comparison for TPC-DS sweep result files.

Why this exists (M0124-0006): the SF=1 re-sweep's headline result was "row
counts reproduce set A".  That is much weaker than "goopg agrees with PG" --
Q16 was a wrong answer behind a matching row count since chunk 2 and went
unnoticed for ten chunks.  A raw `diff` is useless for triage because two
answer-neutral renderers dominate it:

  * `char(n)` is not blank-padded by goopg (ledger row 2026-07-06, M0122-0005),
    so every bpchar column differs in width on every row; and
  * numeric division drops scale when the quotient is exactly zero
    (`0.00` vs `0.00000000000000000000`), so a single zero cell differs.

Both are rendering, not answers.  This script strips them in two graded passes
so a cell is classified by *what actually differs*:

  pass 0  raw            -- byte identical?
  pass 1  field-strip    -- split on '|', strip each field.  Kills bpchar
                            padding and psql column-width alignment.
  pass 2  numeric-canon  -- additionally canonicalise anything that parses as a
                            Decimal (normalize(), so 0.00 == 0.00000000000).
                            Kills the zero-scale gap.
  each pass also compares the row multiset (sorted), which separates a genuine
  value divergence from an ordering-only difference (ties under ORDER BY).

Only a difference surviving pass 2 *unsorted and sorted* is a real wrong
answer.  For those the script reports which columns diverge and probes one
specific signature -- column replication, where goopg emits column 1's value in
columns 2..n while PG's differ (the M0125-0009 aggregate-key fallback).

Usage:
    scripts/tpcds-value-diff.py <resultdir> <query-number>...
    scripts/tpcds-value-diff.py bench/tpcds/runtime_goopg/tpcds-results 2 7 16
"""

import sys
import os
from decimal import Decimal, InvalidOperation


def parse(path):
    """Parse psql aligned output into (header, rows).

    psql aligned form is: header line, a `---+---` rule, N data lines, a blank
    line, then `(N rows)`.  Anything after the rule and before the tally is a
    row; a data value may itself contain a newline, but no TPC-DS column here
    does, so a line-oriented parse is exact for this corpus.
    """
    with open(path, encoding="utf-8", errors="replace") as fh:
        lines = fh.read().split("\n")
    header, rows, seen_rule = None, [], False
    for line in lines:
        if not seen_rule:
            if set(line.strip()) <= {"-", "+"} and "-" in line:
                seen_rule = True
            elif "|" in line or line.strip():
                header = line
            continue
        s = line.strip()
        if not s or s.startswith("(") and s.endswith("rows)") or s == "(1 row)":
            continue
        rows.append([f.strip() for f in line.split("|")])
    return header, rows


def canon_numeric(field):
    """Canonicalise a numeric field; leave non-numerics untouched."""
    try:
        d = Decimal(field)
    except (InvalidOperation, ValueError):
        return field
    # normalize() collapses trailing zeros; the Exponent guard re-expands
    # 1E+3 back to 1000 so exponent form never masks a value difference.
    n = d.normalize()
    return format(n, "f")


def canon_rows(rows, numeric):
    out = []
    for r in rows:
        out.append(tuple(canon_numeric(f) for f in r) if numeric else tuple(r))
    return out


def replication_signature(g_rows, p_rows):
    """Detect goopg emitting some *earlier* column's value in column j.

    This is the M0125-0009 signature: sibling aggregates whose dedup key
    collapses, so the 2nd..Nth read the first one's slot.  It is NOT always
    column 0 -- Q66 replicates within three separate 12-column blocks, and Q28
    replicates pairwise inside each of six cross-joined blocks -- so search
    every earlier column, not just the first.

    Returns [(j, k)] meaning "goopg col j == col k on every row, while PG's col
    j differs from its own col k on at least one row".  The asymmetry against PG
    is what makes it a goopg defect rather than a property of the data.
    """
    if not g_rows or not p_rows:
        return []
    hits = []
    ncol = min(len(g_rows[0]), len(p_rows[0]))
    for j in range(1, ncol):
        for k in range(j):
            g_repl = all(len(r) > j and r[j] == r[k] for r in g_rows)
            p_diff = any(len(r) > j and r[j] != r[k] for r in p_rows)
            if g_repl and p_diff:
                hits.append((j, k))
                break
    return hits


def ulp_only(g_rows, p_rows, tol=Decimal("1e-14")):
    """True when every differing field is numeric and agrees to a relative 1e-14.

    float8 aggregate accumulation order differs between engines, so stddev/avg
    columns can disagree in the last significant digit without either being
    wrong.  Distinguishing that from a real value divergence needs a relative
    tolerance, not string equality.
    """
    saw = False
    for g, p in zip(g_rows, p_rows):
        for a, b in zip(g, p):
            if a == b:
                continue
            try:
                da, db = Decimal(a), Decimal(b)
            except (InvalidOperation, ValueError):
                return False
            scale = max(abs(da), abs(db))
            if scale == 0:
                return False
            if abs(da - db) / scale > tol:
                return False
            saw = True
    return saw


def differing_columns(g_rows, p_rows):
    """Column indices that differ on a positional row-by-row comparison."""
    cols = set()
    for g, p in zip(g_rows, p_rows):
        for j in range(min(len(g), len(p))):
            if g[j] != p[j]:
                cols.add(j)
    return sorted(cols)


def classify(resultdir, q):
    gp = os.path.join(resultdir, f"goopg_q{q}_result.txt")
    pp = os.path.join(resultdir, f"pg_q{q}_result.txt")
    if not (os.path.exists(gp) and os.path.exists(pp)):
        return {"q": q, "verdict": "MISSING"}

    ghdr, g = parse(gp)
    phdr, p = parse(pp)
    res = {"q": q, "grows": len(g), "prows": len(p), "header": phdr}

    if len(g) != len(p):
        res["verdict"] = "ROWCOUNT-MISMATCH"
        return res

    for label, numeric in (("field-strip", False), ("numeric-canon", True)):
        gc, pc = canon_rows(g, numeric), canon_rows(p, numeric)
        if gc == pc:
            res["verdict"] = (
                "RENDERING-ONLY (bpchar/width)"
                if not numeric
                else "RENDERING-ONLY (numeric scale)"
            )
            return res
        if sorted(gc) == sorted(pc):
            res["verdict"] = f"ORDERING-ONLY (after {label})"
            return res

    gc, pc = canon_rows(g, True), canon_rows(p, True)
    if ulp_only(gc, pc):
        res["verdict"] = "FLOAT8-ULP-ONLY"
        return res
    res["verdict"] = "VALUE-DIVERGENT"
    res["cols"] = differing_columns(gc, pc)
    res["repl"] = replication_signature(gc, pc)
    ndiff = sum(1 for a, b in zip(gc, pc) if a != b)
    res["ndiff"] = ndiff
    for a, b in zip(gc, pc):
        if a != b:
            res["sample_g"] = "|".join(a)
            res["sample_p"] = "|".join(b)
            break
    return res


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    resultdir, qs = sys.argv[1], sys.argv[2:]
    for q in qs:
        r = classify(resultdir, q)
        print(f"=== Q{r['q']} : {r['verdict']}")
        if r["verdict"] == "VALUE-DIVERGENT":
            print(f"    rows={r['grows']} differing={r['ndiff']} cols={r['cols']}")
            if r["repl"]:
                pairs = ", ".join(f"{j}<-{k}" for j, k in r["repl"])
                print(f"    COLUMN-REPLICATION (M0125-0009 signature): {pairs}")
            print(f"    hdr   : {r['header'].strip()}")
            print(f"    goopg : {r['sample_g']}")
            print(f"    pg    : {r['sample_p']}")


if __name__ == "__main__":
    main()
