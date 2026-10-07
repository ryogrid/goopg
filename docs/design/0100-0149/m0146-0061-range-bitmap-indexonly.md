# M0146-0061 — a range restriction gets PG's bitmap and index-only paths

Status: done 2026-10-07 (`83d54d10b`). Parent: M0145-0029. Unblocks the
range half of M0146-0060 (which stays held on M0146-0068).

## Symptom

Fixture `dsc`: 3000 rows, `CREATE INDEX dsc_a ON dsc (a DESC)`, VACUUM and
ANALYZE (`analysis/m0146/m0146-0060/probe-desc-range.sql`).

| query | PG 18.3 | goopg search before | goopg final plan before |
|---|---|---|---|
| `a, c … a > 97` | Bitmap Heap Scan 4.75..22.50 | Seq Scan 54.50 | Index Scan (rule) |
| `a, c … a BETWEEN 3 AND 4` | Bitmap Heap Scan 22.79 | Seq Scan | Index Scan (rule) |
| `a … a > 97` | Index Only Scan 0.28..5.33 | Seq Scan | Index Only Scan (rule) |

The search had no bitmap path for a range: `matchBitmapIndexQuals` binds
equalities only. Its index-only producer also declined a range
(`plainRestrictionBindsOnlyPrefix`). The plain range Index Scan was priced at
144.93 (B8), so the search kept the Seq Scan. The rule-based single-relation
producer (`planIndexScanFromWhere`) then replaced that Seq Scan with a
heuristic Index Scan.

## PG

- `match_clause_to_indexcol` (indxpath.c) accepts any operator in the
  column's btree opfamily, so `build_index_paths` gives a range the same
  index clauses as an equality. `get_index_paths` puts them into the
  bitmap paths too.
- `build_index_paths` makes one path per index, index-only when
  `check_index_only` holds, with the index's useful pathkeys either way.
- `cost_bitmap_heap_scan` prices the bitmap with `clauselist_selectivity`
  over the index quals, which turns a lower and an upper bound into one
  band.

## Change

### Bitmap range path

- **Path (pathbitmap.go).** With no leading-column equality,
  `buildOneBitmapPath` takes the leading-column range of
  `restrictionLeadingRange`, limited to constant bounds
  (`bitmapLeadingRange`), and prices it with `rangeIndexSelectivity`.
  - An outer-level bound would make the probe correlated, and
    `clonePlanReplacingOuter` harvests only equality keys from a
    `BitmapIndexScan`, so such a bound stays a Filter.
- **Plan node.** `BitmapIndexScan` gains `LowKey`/`HighKey`/`LowOp`/`HighOp`,
  on the leading column only, never alongside `Key`/`Keys`.
  `createBitmapIndexScanPlan` lowers range clauses onto them.
  - The heap scan's recheck takes a single-column index's bound conjuncts
    (`local`). A composite index's bounds stay the Filter.
- **Bound carriers.** `walkPlanExprs`, `lowerTraverseNode`,
  `WalkPlanExprs`' export, `rebaseBitmapProbeKeys`, `spliceField`'s clone,
  `bitmapHeapScanRows`, the bitmap→IndexScan harvest view and the EXPLAIN
  `Index Cond` arm all carry the bounds.
- **Executor.**
  - `indexScanOp.lookupRangeBounds`' body is now `ctx.indexRangeBounds`
    over an `indexRangeSpec`: DESC swap, strictness, and the NULL-keyed stop
    at an open end.
  - The bitmap index scan op probes `LowKey`/`HighKey` through it, passing
    the index-order exclusivity to `RangeScanWithPos`.
  - A NULL bound yields an empty bitmap. On a composite index the range
    marks its TIDs for recheck, as a leading equality does.

### Index-only range path

- `indexOnlyLeafClauses` admits the leading range that the plain producer
  would bind: no leading equality and no leading SAOP, which is
  `addOneRestrictionIndexPath`'s order. The plain producer then defers to
  the index-only path, as it does for an equality prefix.
- `addOneIndexOnlyPath` prices the range by range selectivity. It also
  gives the path the index ordering the plain range path carried, so an
  ordered consumer sees no Sort that the plain path used to avoid.
- The IOS arm of `createIndexScanPlan` lowers the bounds onto
  `IndexOnlyScan.LowKey`/`HighKey`. The executor op already probed ranges.

### Correctness fix found by the probes

A numeric literal against an integer column was encoded into the int key,
which rounded it. On HEAD, both through the rule and through the search:
- `a > 198.5` returned 0 rows (PG: 30);
- `a = 198.5` returned the a = 199 rows (PG: 0).

PG compares `(a)::numeric` there, and `integer_ops` has no numeric member,
so the clause is not indexable. `restrictionKeyUsable` and the rule's
equality and range arms (`planIndexScanFromWhereShape`,
`tryRangeIndexScan`) now refuse such a key, and the clause stays a filter.
This was fixed in-slice because the new bitmap arm routes more probes
through the same key check.

## Verification

- dsc probe: all five EXPLAINs have PG's node shapes and Recheck/Index Cond
  text: bitmap for `a > 97`, `BETWEEN`, `a < 2` and `a >= 98 AND c > 10`
  (with Filter), and Index Only Scan for `a` alone. The ordered index-only
  LIMIT plans on the ASC and DESC indexes are PG's.
- Values match PG on every probe, including a nullable column, a composite
  index, correlated and NULL bounds, and the numeric literals.
- `TestRangeRestrictionBitmapAndIndexOnly`: the bitmap pins and the numeric
  cases fail without the change. The index-only pin also holds on HEAD,
  through the rule.
- `TestRestrictionRangeIndexScanOnJointreePipeline` expected a range Index
  Scan by default. PG elects a Bitmap Heap Scan (13.42..1649.51) on the
  same no-statistics table (VACUUMed, so `relpages` 10000), so the test now
  pins PG's bitmap and checks the IndexScan lowering with bitmap scans off.
- Gates:
  - units, tpch-spotcheck, arm 24/24 and ea-ratchet 9/9 PASS;
  - fire set: no plan changed at SF0.25 or SF1;
  - sf025 96/96, plan shapes 99/99 the same.
- Regress A/B, 8 files:
  - join −18 lines;
  - create_index −4 (Parallel Index Only Scan and Gather Merge on
    `tenk1_thous_tenthous`);
  - aggregates +28 and subselect +2;
  - select, btree_index, inherit and partition_prune unchanged.

## Not covered

- **B8 at the shipped multiplier.** goopg prices a plain index probe at
  `indexProbeCostMultiplier` (2) times PG's. For a tiny range PG keeps the
  Index Scan over its bitmap; goopg now elects the bitmap: regress
  aggregates' `agg_sort_order`, `TestIndexScanCarriesCtid`'s two-row band.
  Both tests pin PG's plan at multiplier 1. M0146-0068's owner decision
  settles it. Ledgered.
- A correlated (outer-level) range bound builds no bitmap path. Ledgered.
- An index-only equality path still carries no pathkeys, and an equality
  prefix plus range on the next column (`b = 3 AND c > 5990` on `(b, c)`)
  builds no index-only path, where PG has one. Both predate this slice.
  Ledgered.
- The bitmap equality arm (`matchBitmapIndexQuals`) still probes
  `a = 198.5` on an int column, with the clause kept as its Filter. Results
  are correct, but PG builds no index path. Ledgered.
