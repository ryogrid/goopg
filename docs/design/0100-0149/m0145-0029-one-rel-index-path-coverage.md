# M0145-0029 — one-relation index-path coverage before the flip

Status: slices 1 and 3 landed 2026-09-23 (`fe3d1b0aa`, `6d50210f4`); slices 2, 4, 5 open. Task:
`.ralph/fix_plan.md` M0145-0029 (Kind: impl, Parent: M0145-0008). Origin: the
M0145-0008 flip triage, group I (`m0145-0008-flip-test-triage.md`).

## The question, per the task

On the jointree arm a single-relation scope is planned by the search instead of
the rule-based bypass. For each group-I test that loses its index shape: is the
index path generated-but-out-costed (record the gap) or never generated (port
the producer)?

## Measurement (GOOPG_PGSHAPED_DP_TRACE=1, local flip)

On the `saopFixture` catalog, the one-relation search filed:

| predicate | paths filed before slice 1 |
|---|---|
| `i_item_sk = 2` | `joinsearch.prebuilt`, `bitmap.heap` |
| `i_item_sk > 2` | `joinsearch.prebuilt` only |
| `i_item_sk IN (2, 3)` | `joinsearch.prebuilt` only |
| `… IN (2, 3) AND i_flag > 1` | `joinsearch.prebuilt` only |

So the search had **no plain restriction-driven index path at all**. Base
relations reached an index only through bitmap paths (equality quals only), the
ordering-only full index scan (`pathindexordered.go`) and the index-only scan
(`pathindexonly.go`, a full index scan without quals). PG's
`create_index_paths` → `build_index_paths`
(`./postgres/src/backend/optimizer/path/indxpath.c:975`) builds a plain
`IndexPath` for every index whose `index_clauses`
(`match_restriction_clauses_to_index`, `indxpath.c:2240`) are non-empty, beside
the bitmap one. The bypass was the only place goopg built that shape.

## Oracle check: several witnesses are stale, not gaps

A private PG 18.3 on the same shapes (never-analysed tables, as the executor
tests build them):

- `TestIndexScanVarcharEndToEnd`'s query (`p_type = '…'` on 3 rows): PG plans
  **Bitmap Heap Scan** (4.18..12.64); with bitmaps off it prefers the **Seq
  Scan** (20.12) over an Index Scan. goopg on the jointree arm now elects the
  same bitmap (16.73 vs seq 20.13; the plain index path costs 40.3, also above
  seq, as in PG). The test's `IndexScan` expectation is the legacy rule's,
  not PG's.
- `TestIOS_CompositeInt4Int4`'s query (`a = 1 AND b = 20` on an `(a,b)` index):
  PG plans **Index Only Scan with `Index Cond: ((a = 1) AND (b = 20))`**.
  goopg's index-only producer cannot build an index-only scan WITH quals — a
  genuine generation gap (slice 3).

## Slices

| slice | scope | status |
|---|---|---|
| 1 | plain restriction IndexScan path, **equality prefix** on constants; lowering drops the consumed conjuncts from the reinstated leaf Filter | **landed** `fe3d1b0aa` |
| 2 | range bounds (`<`, `<=`, `>`, `>=`, BETWEEN) as index quals — plain AND bitmap producers | open |
| 3 | index-only scan with index quals (`create_index_path(indexonly=true)` with `index_clauses`), one path per index | **landed** `6d50210f4` |
| 4 | ScalarArrayOp (`col IN (…)` / `= ANY`) quals — plain and bitmap | open |
| 5 | re-run group I under a local flip; update stale expectations to the PG-faithful shape with the oracle capture (or pin the executor feature they test with `enable_seqscan`/`enable_bitmapscan` off, as PG's regress does) | open |

## Slice 1 design (`internal/optimizer/pathindexrestrict.go`)

- `addRestrictionIndexPaths`, called from `addBaseRelIndexPaths` beside the
  parameterised, ordered, bitmap and index-only producers, so it competes in
  the same pathlist.
- `restrictionEqualityPrefix`: for each index column in order, the first local
  `col = const` conjunct on it (via `normalizeColumnConst`, the bitmap arm's
  recogniser); the first unbound column ends the prefix (btree
  `amoptionalkey`). Key kinds follow the rule-based producer: literals via
  `isConstExpr`, declining boolean and enum-string keys.
- Selectivity from the index quals only; PG's `isunique` short circuit for a
  unique index bound on every key. `cost_index` via `costIndexScan` with
  `numQualOps` = local quals minus the index quals (`costsize.c:806-820`).
  `rows = baserel->rows`. Pathkeys only when useful (`hasUsefulPathkeys`).
- `indexPathClause.local` records the consumed conjunct; `createIndexScanPlan`
  rebuilds the leaf's Filter chain without it (`rewrapLeafDropping`) — PG's
  `create_indexscan_plan` leaves quals redundant with the index quals out of
  qpqual (`./postgres/src/backend/optimizer/plan/createplan.c:3068-3088`).
- Partial indexes decline (no predicate-implication prover, as the ordered
  arm); the partial (parallel) twin is not built yet.

Measured: default arm SF0.25 plans `same=99 changed=0`; knob-arm TPC-DS fire-set
plans unchanged at SF0.25 and SF1. The path competes on the corpora but does
not win there — bitmap and join-driven paths already cover those shapes; it is
the single-relation scopes the flip moves off the bypass that need it.

## Slice 3 design (`pathindexonly.go`, `createplanindex.go`)

- `addIndexOnlyPaths` used to refuse every leaf with local quals, because a
  reinstated Filter's `ColumnRef`s point into the FULL leaf schema, not the
  narrowed index-only one. It now admits a filtered leaf when an index's
  equality prefix consumes **every** local conjunct (`consumingIndexClauses`),
  so there is nothing left to reinstate. A leaf that would keep a residual qual
  is still refused; PG keeps such a qual as the Index Only Scan's Filter
  (ledgered).
- Cost: `restrictionIndexSelectivity` (shared with the plain producer, with
  PG's `isunique` short circuit) and `numQualOps` = local quals minus index
  quals.
- One path per index. PG's `build_index_paths` builds a single `IndexPath` and
  sets `indexonly` when `check_index_only` holds (`indxpath.c:1010`). goopg's
  two producers both built one, with equal cost at a cold visibility map, and
  the plain one, filed first, won `add_path`'s tie.
  `restrictionPathIsIndexOnly` applies the index-only producer's three
  conditions (`enable_indexonlyscan`, a covering index, every qual consumed),
  so the plain producer declines exactly where the index-only one builds.
- Lowering: the clauses become `IndexOnlyScan.Key` (one) or `Keys` (several;
  the executor pads a short prefix, `operators_indexonly.go`). It panics if
  dropping them would leave a leaf Filter.
- `IndexOnlyScan` embeds `searchedTree`. A covering index-only scan can be a
  one-relation search root with no boundary Project (`SELECT a … WHERE a = …`
  over an index on `a`), and `markSearchedTree` panicked on it.

Measured under a local flip: `TestIOS_*` (4) and
`TestArrayIndexOnlyScanAnswersFromKey` elect PG's `Index Only Scan … Index
Cond` only at `GOOPG_INDEX_PROBE_MULT=1`. At the shipped multiplier 2 the
bitmap wins. On the composite witness PG prices the index-only scan at 8.17
and the bitmap at 8.18; goopg prices them at 16.27 and 12.27, because the
multiplier doubles the index path's random heap fetch but not the bitmap's
heap page. That is a calibration question (`cost_funcs.go`
`indexProbeCostMultiplier`, owner-owned), not a generation gap; slice 5
dispositions each test. `TestIndexOnlyDeformColdAndVisible` runs on 4
analysed rows, where the seq scan (1.01) correctly wins, as in PG, so its
expectation is stale. No corpus plan moved on either arm.

