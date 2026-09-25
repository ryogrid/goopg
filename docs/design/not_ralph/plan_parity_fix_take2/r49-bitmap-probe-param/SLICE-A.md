# R49 Slice A — implementation report (LANDED 2026-09-10)

*Design: `DESIGN.md` (§1). Step-0 pair + Q5 witness archived in this dir.*

## 0. What landed (EXPLAIN-only)

`internal/executor/operators_explain.go`: new `case *optimizer.BitmapIndexScan`
in the shared `emitNodeDetailLines` (after the IndexScan case, before
`BitmapHeapScan`), rendering the bound probe keys as `Index Cond:` via the
shared `formatIndexCondParts(index, keys, key, …)` — the same mechanism as the
working NLI index probes. NOT from `bis.Pred` (SEARCH coordinates). Bitmap
probes are equality-only (no Low/High on the node); a key-less bitmap renders
nothing. The edit point is shared by the plain and ANALYZE walks, so there is
no twin to miss — and the probe test pins BOTH walkers.

Pins: `internal/executor/explain_bitmap_index_cond_test.go` (2 tests, each
through BOTH walkers per the R48 sibling-agreement doctrine):
`TestExplainBitmapIndexScanRendersProbeIndexCond` pins PG's own DS-Q3 shape
`Index Cond: (ss_item_sk = item.i_item_sk)` (inner column first, outer ref
qualified) from a hand-built probe node + two-binding name table;
`TestExplainBitmapIndexScanWithoutKeyRendersNoIndexCond` pins key-less silence
(no `Index Cond: ()` on the pre-Slice-B corpus shape).

## 1. Census + adjudication (DESIGN §3.1–3.2)

Corpora: `/tmp/pp2/oc-tpch-r48h2.txt` → `/tmp/pp2/oc-tpch-r49a.txt`;
`/tmp/pp2/oc-ds05-r48h2.txt` → `/tmp/pp2/oc-ds05-r49a.txt` (clone `:5533`/
`:5534`, binary `/tmp/pp2/bin/goopg-r49a`, inode-verified launch, GUCs
`work_mem='64MB'`, `max_parallel_workers_per_gather=4`).

Normalized diff (header + `(N rows)` counters excluded — counters shift when a
line is added above them):

- **TPC-H: 0 removals, 15 added `Index Cond:`, 0 other adds.** Pure addition.
- **TPC-DS: 0 plan removals, 40 added `Index Cond:`, 0 other adds.** The 3
  nominal non-IndexCond lines are capture-harness noise (temp filename inside
  pre-existing psql ERROR lines), not plan changes.
- Join-level `Filter:` counts UNCHANGED (62/62 TPC-H, 535/535 TPC-DS) — the
  probe clauses still recheck at the join, per the Slice-A contract.

Attribution: all 15 TPC-H + 40 TPC-DS added lines sit under a `Bitmap Index
Scan` node (was 0/40 TPC-DS carrying `Index Cond:`). The 28 in-scope lines
each gained exactly one; the 12 extra probes (standalone-keyed bitmaps,
`_1`-renamed bare renders, Q72 LEFT render-only, InitPlan `$0` params, Q17/
Q21) were each verified under a Bitmap Index Scan node — legitimate,
PG-faithful renders, not flips.

Step-0 re-pass: DS-Q3 probe now reads `Bitmap Index Scan on
store_sales_pkey … Index Cond: (ss_item_sk = item.i_item_sk)` — text-identical
to the archived `pg-dsq3.txt`. The join-level `Filter:` stays until Slice B
(as designed); `pg-plan-parity-diff.py` will STILL report shapediff on Q3
(parallel/estimate gaps, R48 F9 family) — a hand-pass here must never be read
as tool parity.

## 2. Values bind (DESIGN §3.3)

- TPC-H canonical digest (`cmd/tpch-runner --digest` vs
  `bench/tpch/baseline-digests.txt`, fresh capped `:5533` at `GOGC=100`):
  **24 MATCH, VERDICT: PASS** — and 24/24 MATCH vs the R48 reference
  `dig-r48h2.txt` too, ordered digests included (row order unchanged).
  Spotcheck tripwires read from the same run: Q12=2/Q13=34 (canonical).
- Units: `internal/executor` + `internal/optimizer` suites green (gate run,
  no `-count=1`). SF0.5 sweep deferred to Slice B per plan (renderer-only;
  no executor/optimizer behavior change possible — the edit adds one
  detail-line arm and touches no estimate, cost, or row path).
- Sibling-path audit: none — the edit point is the single shared
  `emitNodeDetailLines`; both walkers funnel through it and both are pinned.

## 3. Files

- `internal/executor/operators_explain.go` (new BitmapIndexScan case)
- `internal/executor/explain_bitmap_index_cond_test.go` (new, 2 tests)
