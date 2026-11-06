# M0148\-0001e1 — composite\-index range quals without a repeated Filter; VERBOSE Index Cond qualification

**Parent:** M0148\-0001 · **Status:** landed 2026\-11\-03; closed (residue in
M0148\-0001e2)

## Divergence

```
-- aggregates.sql, btg (y, x, w) index, EXPLAIN (VERBOSE, COSTS OFF)
PG 18.3                                goopg (before)
Index Only Scan using btg_y_x_w_idx    Index Only Scan using btg_y_x_w_idx
  Output: y, x, w                        Output: y, x, w
  Index Cond: (btg.y < 0)                Index Cond: (y < 0)
                                         Filter: (btg.y < 0)
```

Two separate gaps show here:

- **Repeated Filter.** On a multi\-column btree, a range on the leading
  column was an index qual for costing, yet it stayed in the Filter.
- **Unqualified column.** Under VERBOSE the index qual's column side
  printed bare.

## PostgreSQL

- `create_indexscan_plan` (createplan.c) builds qpqual as the scan clauses
  minus those `is_redundant_with_indexclauses` finds. A leading\-column
  range is applied entirely by the scan's boundary keys
  (`_bt_first` / `_bt_checkkeys`), so no Filter repeats it.
- `show_scan_qual` (explain.c) deparses the index quals with
  `useprefix = IsA(plan, SubqueryScan) || es->verbose`. Under VERBOSE the
  index column prints as `<refname>.<column>`, for example `(btg.y < 0)`
  and `(t3.a = t5.a)`.

## Change

- **Guard relaxed.** `restrictionRangeOnColumn` (pathindexrestrict.go)
  records `local` for a leading\-column bound on any btree, not only a
  single\-column one. The plain, index\-only and bitmap lowerings then drop
  the conjunct from the reinstated Filter.
  - The guard dated from before M0134\-0001 S4, which made the executor pad
    a composite bound by strictness.
  - An inclusive upper or exclusive lower bound gets trailing \+infinity
    (`compositeUpperBound`); otherwise it stays a bare prefix.
  - So a leading\-column range is exact on both key formats.
- **VERBOSE qualification.** `indexCondColumnPrefix` (operators_explain.go)
  returns `<refname>.` under VERBOSE, where refname is the disambiguated
  name, else the alias, else the table. The `*Prefixed` variants of
  `formatIndexCond`, `formatIndexOnlyCond` and `formatIndexCondParts` put
  it before every index\-column name. This covers Index Scan, Index Only
  Scan and Bitmap Index Scan.

## Verification

- **Differential probe.** 52 range queries on a composite index with NULLs
  and duplicates in every key column:
  - leading\-column `<`, `<=`, `>`, `>=` at boundary and out\-of\-range
    values;
  - two\-sided bands;
  - an equality prefix with a range;
  - a text composite index.

  Each query ran as an index scan, as a bitmap scan and as a seq scan,
  over tuple\-format indexes and over blob\-format indexes (explicit
  operator classes). All results are identical.
- `TestRangeScanCompositeIndexDropsFilter` replaces
  `TestRangeScanCompositeIndexKeepsFilter`. It runs on both key formats and
  pins:
  - no Filter on the plan;
  - row counts for every bound kind, NULL keys included;
  - the VERBOSE `Index Cond: (btg.y < 0)`.
- **Regress runner, HEAD → change, 18 suites.**
  - aggregates: 459 → 443 lines, 10 → 9 hunks; the btg hunk is gone.
  - subselect: 2492 → 2488.
  - join: 17855 → 17853 (VERBOSE qualifications).
  - The other 15 suites are byte\-identical.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no change; fire\-set fires=none on all three
  corpora.

## Remaining (M0148\-0001e2)

- **Later\-column clauses** are still Filter\-only: `a < 3 AND b > 5` on
  `(a, b, c, d)` prints `Index Cond: (a < 3)` and `Filter: (b > 5)`. PG
  prints `Index Cond: ((a < 3) AND (b > 5))`, which needs non\-boundary
  scan keys checked per index tuple (`_bt_checkkeys`).
- **A range behind an equality prefix** (`a = 1 AND b > 5`) keeps its
  Filter recheck. The open\-ended bound runs to the prefix's padded upper
  bound and past entries whose `b` is NULL. PG's strict\-operator scan
  keys stop at NULLs.
- **Legacy rule path.** `tryRangeIndexScan` still keeps its composite
  guard; it is not on the searched path.
