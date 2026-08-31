# 0054-0004 — LIKE Prefix → Range Index Utilisation Audit (Q20 inner)

**Status:** Draft. Sub-task of M0054-0009.
**Author:** goopg perf-analysis branch (run-013 finding).
**Date:** 2026-05-06.

## 1. Problem

TPC-H Q20's outer-most predicate is `p_name LIKE 'forest%'` against
`part`:

```sql
WHERE p_name LIKE 'forest%'
  AND ps_partkey IN (SELECT p_partkey FROM part WHERE p_name LIKE 'forest%')
```

M0051-0004 (LIKE-prefix → range translation, landed earlier) is
supposed to convert `p_name LIKE 'forest%'` into the byte-range
`['forest', 'forest\xff…']` against an index on `p_name`. In Q20's
context the planner should:

1. Detect the `LIKE 'literal%'` shape.
2. Look up an index whose leading column is `p_name`.
3. Emit `IndexScan{ LowKey: 'forest', HighKey: 'forest\xff…' }`.

The audit verifies this actually happens for Q20 in the live
cluster. If the EXPLAIN baseline shows `Seq Scan on part` for
Q20's part-scan, the M0051-0004 path is broken or unreachable for
this Q's expression shape, and a follow-up fix is needed.

## 2. Acceptance

The audit is the deliverable. Two outcomes are acceptable:

**Pass (clean):** the EXPLAIN baseline (regenerated post-audit)
shows `Index Scan using <idx_part_name | similar> on part` for
Q20's part-scan, with `LowKey/HighKey` rendered (or their range
implied by the EXPLAIN renderer). Add a synthetic regression test
under `internal/planner/likeprefix_test.go` that pins this shape
and prevents regression.

**Fail (gap):** the baseline shows `Seq Scan on part`. Document
the precise reason in the audit deliverable:
- Is `p_name` indexed at all? Check the HammerDB schema indexes.
- Does M0051-0004 fire for this Expr's parser-stage shape (a
  `LIKE` lifted into the WHERE of a CTE, derived table, or
  uncorrelated subquery may not see the same expression rewriter
  that the top-level WHERE does)?
- Is the column type `varchar(55)` correctly recognised by
  `tryRangeIndexScan`?

Open a precise follow-up sub-task with the named failure mode.

## 3. Steps

1. Read `internal/planner/likeprefix*.go` and confirm the rewrite
   path; verify the IR shape it expects (a `*BinaryOp{Op:"LIKE",
   Left:*ColumnRef, Right:*StringConst}` over a top-level WHERE).
2. Inspect Q20's parsed AST and resolved Plan tree at the
   `tpch.Queries()[20]` invocation site (the diagnostic test
   pattern used for Q15b / Q19 in this session). Identify which
   Filter / Project / Subquery node holds the LIKE.
3. Regenerate `analysis/tpch-explain-baseline.md` and inspect
   Q20's row in the table.
4. Decide: pass or fail per §2. Land a one-line cross-link to
   this design doc in the fix_plan.md entry's evidence section.

## 4. Out of scope

- Improving `LIKE '%foo%'` (substring) — that's a non-prefix shape
  and inherently SeqScan unless we add a trigram or full-text
  index. Already explicitly carved out from M0051-0004 (the
  upstream design's "leading-wildcard not indexable" carve-out).
- Adding new B-tree indexes that HammerDB doesn't already create.
  We mirror HammerDB's index set faithfully (M0054-0003d note).

## 5. Critical files

- `internal/planner/likeprefix.go` — the existing rewriter.
- `internal/planner/range_index_scan_test.go` — the existing pin
  tests (extend with Q20 shape if pass).
- `analysis/tpch-explain-baseline.md` — Q20 row inspection.
- `internal/testutil/tpch/tpch.go::Queries()[20]` — the literal SQL.

## 6. References

- M0051-0004 commit & design doc.
- HammerDB pgolap.tcl Q20 SQL (line ~124 of `tpch.go`).
