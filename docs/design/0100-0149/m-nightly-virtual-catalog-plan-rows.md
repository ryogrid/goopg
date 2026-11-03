# M\-NIGHTLY — virtual catalog Values carry a row count, not plan\-time cells

**Task:** `regress/suite-wedge` (AI\-20261103\-004134\-004, the `inherit` case
timing out at 120 s) · **Status:** landed 2026\-11\-03

## Diagnosis

- `inherit` runs in 19 s inside the regress suite on an idle host, against
  4 s on a fresh cluster. Nightly co\-load stretched it to 71, 94 and 120 s,
  and 120 s is the per\-case timeout.
- The cost grows with the catalog. After 3000 filler tables, inherit.sql
  takes 66 s.
- The 50 psql `\d` commands dominate, and the cost depends on DDL: warm,
  `\d+ c` takes 0.27 s, but the first `\d+` after any DDL takes 1.26 s.
  Per\-statement timings and a CPU profile point at re\-planning catalog
  queries once a DDL invalidates the plan cache.
  - `buildVirtualValues` turned every row of a virtual catalog (pg\_class,
    pg\_attribute, …) into one typed constant expression per cell.
  - The plan walkers (`NodeSublinks`, `NodeSubplans`, `walkExprRefs`,
    `renumberRTIDsFlatRtableOrder`, …) then visited every one of those
    cells. Planning took 64% of the profile.
- The executor never reads those cells: `valuesOp.Open` re\-materialises a
  virtual source from `VirtualRows()` at every Open (M0094\-0005).
- The part this file does not cover — whole\-index catalog rebuilds on DDL —
  was fixed separately (`m-nightly-catalog-btree-split.md`). That fix alone
  cut the standalone runner time from 123 s to 64 s.

## Change

- `buildVirtualValues` records only `VirtualRowCount`, from the same
  `VirtualRows()` call. It builds no `Rows`.
- `Values.RowCount()` returns the literal row count, or the virtual count
  when a virtual source has no rows. It also handles clones, which carry an
  empty `Rows` slice.
- Row estimation (`EstimateRows`) and EXPLAIN's `"*VALUES*"` numbering use
  `RowCount()`, so estimates and labels are unchanged.
- `constantOutputExprs` no longer treats a one\-row virtual catalog as a
  list of plan\-time constants. Those were snapshots of a dynamic relation.

## Results

At 3000 relations:

- `\d+` after a DDL takes 0.35 s, down from 1.26 s.
- inherit.sql takes 24 s, down from 66 s, with byte\-identical output.

Gates:

- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass.
- The TPC\-H and TPC\-DS gates show no plan or value change.
- Pin: `TestPlanSelectFromVirtualPgClass` now asserts the count is present
  and no plan\-time rows are built.

## Remaining

- Catalog queries still scan materialised virtual rows in full. PG reaches
  catalog rows through syscache and catalog indexes (`oid = …` lookups), so
  a warm `\d+` still costs 0.27 s at 3000 relations against PG's
  milliseconds.
- Re\-materialising a virtual catalog at every Open costs O(catalog) per
  query.
