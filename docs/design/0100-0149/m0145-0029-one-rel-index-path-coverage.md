# M0145-0029 — one-relation index-path coverage before the flip

Status: slices 1, 2a, 3 and 4 landed 2026-09-23 (`fe3d1b0aa`, `fc716f1f8`, `6d50210f4`, `729027e28`); slice 5 re-run done with its root-cause fix (`9e368ade8`); slice 2b open, and the multiplier-dependent residue is escalated to the owner. Task:
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

- `TestIndexScanVarcharEndToEnd`'s query (`p_type = '…'` on 3 rows): with
  the index created on an empty table, PG plans a **Bitmap Heap Scan**
  (4.18..12.64). **Correction (slice 5):** the test creates the index AFTER
  its rows, and then PG plans a **Seq Scan** (1.04), because the index build
  records the heap's size. The test's `IndexScan` expectation is the legacy
  rule's, not PG's, in both orders.
- `TestIOS_CompositeInt4Int4`'s query (`a = 1 AND b = 20` on an `(a,b)` index):
  PG plans **Index Only Scan with `Index Cond: ((a = 1) AND (b = 20))`**.
  goopg's index-only producer cannot build an index-only scan WITH quals — a
  genuine generation gap (slice 3).

## Slices

| slice | scope | status |
|---|---|---|
| 1 | plain restriction IndexScan path, **equality prefix** on constants; lowering drops the consumed conjuncts from the reinstated leaf Filter | **landed** `fe3d1b0aa` |
| 2a | range bounds (`<`, `<=`, `>`, `>=`, BETWEEN) on the index's leading column — plain producer | **landed** `fc716f1f8` |
| 2b | equality prefix followed by a range on the next column (new probe shape `IndexScan.RangePrefix`); bitmap range and SAOP-prefix + range still open | **landed** `ea8fb4fce` (eq-prefix + range) |
| 3 | index-only scan with index quals (`create_index_path(indexonly=true)` with `index_clauses`), one path per index | **landed** `6d50210f4` |
| 4 | ScalarArrayOp (`col IN (…)` / `= ANY`) quals on the leading column — plain producer (bitmap SAOP: ledgered) | **landed** `729027e28` |
| 5 | re-run group I under a local flip, per-test PG oracle, disposition table below; root-cause fix: index-only `allvisfrac` read the VM under the wrong database | **re-run done** `9e368ade8`; test edits ride the flip commit |

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

## Slice 2a design (`pathindexrestrict.go`, `createplanindex.go`)

- `restrictionLeadingRange` takes the first lower (`>`/`>=`) and first upper
  (`<`/`<=`) bound on the index's **leading** column, either operand order,
  and puts the operator in canonical `col op key` form (`BETWEEN` arrives as
  the same pair). It runs only when no equality prefix binds the index. The
  executor's range probe (`LowKey`/`HighKey`) carries no equality prefix, so
  PG's `a = 1 AND b > 5` shape on `(a, b)` is not expressible yet (2b).
- `indexPathClause.op` records the operator; its zero value is equality, so
  every existing clause keeps its meaning. `createRangeIndexScanPlan` lowers
  the bounds onto `LowKey`/`HighKey`, with the original strictness in
  `LowOp`/`HighOp`: the fields the rule-based `tryRangeIndexScan` fills and
  the executor and EXPLAIN already read.
- Selectivity: `clauselist_selectivity` over the index quals, which is
  goopg's `conjunctionSelectivity` (a lower and upper bound on one column
  form one band, `hibound + lobound - 1`).
- Filter: a single-column index drops the consumed bounds (PG's qpqual). A
  composite index keeps them as a recheck, the guard `tryRangeIndexScan`
  keeps: an exclusive lower bound's padded key can admit entries whose
  trailing columns share the bound value. They still count as index quals
  for `numQualOps`, as in PG.

Checked under a local flip on 5,002 rows including NULLs: strict and
inclusive bounds, one or two bounds, the mirrored form and `BETWEEN` all
return the right counts, with NULLs excluded. The same probe found a
**pre-existing wrong-results bug**: an index-only prefix probe on a composite
index skips entries whose trailing column is NULL (`SELECT a FROM r2 WHERE a
= 10` → 3 rows, PG 4), on the default pipeline too. It is filed as its own
bug task in `.ralph/fix_plan.md`. No corpus plan moved on either arm.

## Slice 4 design (`pathindexrestrict.go`, `createplanindex.go`)

- `restrictionLeadingSAOP` binds the leading column to the first local
  `col IN (list)` / `col = ANY (list)` usable as a btree multi-descent. It
  applies `match_saopclause_to_indexcol`'s gates (`indxpath.c:3136`) as the
  rule-based `trySAOPIndexScan` reproduces them:
  - OR of equality only: `NOT IN`, `!= ANY`, `ALL` and other operators are
    not a union of descents, so they decline;
  - a bare column operand;
  - constant elements the key encoder takes (no boolean, no enum string).

  It runs only when no equality prefix binds the index, because `SAOPKeys`
  excludes every other probe shape in the executor.
- `indexPathClause.saop` carries the elements. `createSAOPIndexScanPlan`
  lowers them onto `IndexScan.SAOPKeys` and drops the IN from the reinstated
  Filter: the descents are exactly the IN, and the executor dedupes rows by
  TID, so duplicate elements are safe.
- Cost: `scalararraysel` through `clauseSelectivity`'s InExpr arm, and
  `numSAScans` = element count (`btcostestimate`'s `num_sa_scans`).

Measured on the new pipeline: TPC-DS Q45's `SubPlan 1` (`item_pkey …
i_item_sk = ANY (10 constants)`) keeps its shape but is now costed by this
path. Its row estimate went from 18,000 to 10 (PG: 10), so Q45 joins the fire
set as a cost-only change and executes correctly at SF0.25 and SF1. The
default arm is unchanged (sweep same=99). `TestSAOPWithConjunctMoves` under
the flip generates the SAOP path, but its no-stats fixture (1 row, 1 page)
elects the seq scan, so that expectation is for slice 5; with 100k measured
rows the path wins (`TestRestrictionSAOPIndexScanOnJointreePipeline`). The
execution probe also showed that the trailing-NULL prefix-probe bug affects
plain and SAOP composite probes, not only index-only ones; the bug task was
widened.

## Slice 5: group-I re-run and dispositions (`9e368ade8`)

Every group-I test, re-run under a local flip, with its fixture replayed on a
private PG 18.3 in the same statement order (index before or after the rows,
VACUUM and ANALYZE where the test runs them). The live-server column is a
flip binary on a throwaway cluster.

| test | PG 18.3 | goopg, new pipeline | disposition |
|---|---|---|---|
| `TestIOS_CompositeInt4Int4`, `…Int4Text`, `…3Columns`, `TestArrayIndexOnlyScanAnswersFromKey` (6 subtests) | Index Only Scan (all-visible after VACUUM, 4.30) | **passes** after `9e368ade8` | fixed: the index-only path read `allvisfrac` = 0 (below) |
| `TestIOS_HeapFallback` | Index Scan (8.29) | Bitmap Heap Scan (12.27; index 16.26) | multiplier-dependent: `indexProbeCostMultiplier` = 2 doubles the index path's page fetches, not the bitmap's heap page |
| `TestIndexOnlyDeformColdAndVisible` | Index Only Scan (8.17, never analysed) | unit fixture: Seq Scan (1.01, no relation-size hook); live server: Bitmap (12.27) | multiplier-dependent on a live server; the fixture has no `RelNBlocksFunc` |
| `TestSAOPWithConjunctMoves` | Index Scan on `idx_item_sk_flag`, `Index Cond: ((i_item_sk = ANY …) AND (i_flag > 1))` | Seq Scan | capability gap: a SAOP followed by a range on the next column (slice 2b) |
| `TestIndexScanVarcharEndToEnd`, `…Char…`, `…Timestamp…` | Seq Scan (index built after the rows records the heap size) | Seq Scan | **stale expectation**: goopg already matches PG; update in the flip commit |
| `TestIndexDeformRescanPersistsBound` | Seq Scan (1.05), same reason | Bitmap Heap Scan (seq priced at the 10-page floor, 27) | real gap: goopg's CREATE INDEX never records the heap's `reltuples`/`relpages` (PG `index_update_stats`) |

**Root cause fixed (`9e368ade8`).** `relAllVisibleFraction`, PG's
`baserel->allvisfrac`, called `catalog.RelAllVisible`. That function keys a
table with no DBOid under `DefaultDBOid` (1), but VACUUM keys the VM bits
under the session's database (a server catalog is `SetDBOID(5)`), and the
`pg_class` view reads them there. On a live server `pg_class.relallvisible`
was 9 while the planner saw 0, so every index-only scan in the `postgres`
database was priced with all its heap fetches.
`InMemory.relAllVisibleKey` is now the one resolver for both the view cell and
`RelAllVisibleBlocks`, which the planner reads. `newVMFixture` installs the
same `RelAllVisibleFunc` hook `initdb`'s cluster open installs. The benchmark
tables carry their own DBOid, where both keys agreed, so no corpus plan or
cost moved. A **suspected sibling** is not yet verified: `TableRealPages` and
`IndexRealPages` key a DBOid-less relation under `DefaultDBOid` too (ledgered).

**Also observed (ledgered):** goopg renders a composite `Index Cond` as
`(a = 1 AND b = 20)`, where PG renders `((a = 1) AND (b = 20))`. This is
pre-existing and shared by the index, index-only and bitmap formatters
(`formatIndexCondParts`).

**What the flip needs from here.**
- The four stale expectations change to PG's shape in the flip commit, since
  they still hold on today's legacy default.
- Three tests are multiplier-dependent. PG prices the index and bitmap
  alternatives within 0.01 of each other, and goopg's 2x probe multiplier
  decides them. This is an owner decision: retire the multiplier, or accept
  the bitmap shape in these tests.
- `index_update_stats` and the SAOP-plus-trailing-range probe are real gaps,
  each with a ledger row.

## Follow-up: CREATE INDEX records the heap size (`292b1af2e`)

The `index_update_stats` gap from the slice-5 table is closed. PG's index
build writes the HEAP's `reltuples`/`relpages` to `pg_class`
(`./postgres/src/backend/catalog/index.c:2809`), which ends the never-vacuumed
10-page floor for a table indexed after loading. goopg's `CREATE INDEX` only
rewrote `relhasindex`.

- `collectBTreeEntries` counts the build scan's live heap tuples
  (`ddlOp.buildHeapTuples`), before any predicate or decode skip, because PG's
  `reltuples` is the heap's count, not the index's.
  `createBTreeIndex`, the one funnel for CREATE INDEX and for PRIMARY KEY /
  UNIQUE indexes, then calls `indexUpdateHeapStats`.
- PG's guards are kept:
  - an empty, never-measured heap stays unmeasured (`CREATE TABLE … PRIMARY
    KEY` must not look vacuumed);
  - nothing is written unless `AutoVacuumingActive` (the `autovacuum` GUC and
    `track_counts`) holds and the table's `autovacuum_enabled` is not false.
- The counts are published and persisted as VACUUM does (`UpdateRelStats` +
  `persistRelSize`). `relallvisible` is read live from the VM.

PG 18.3 on the same script: a 4-row table with one row deleted gives
`reltuples` 3 and `relpages` 1; the empty PRIMARY KEY table and the
`autovacuum_enabled = false` table stay at -1. `TestCreateIndexUpdatesHeapStats`
pins all three. The `w` witness now plans a Seq Scan at PG's exact cost (1.05).

Six executor tests (`TestIndexScanEndToEndConstantKey`,
`TestIndexScan{Varchar,Char,Timestamp}EndToEnd`,
`TestTPCHNumericSingleColumnIndexesAccepted`,
`TestIndexDeformRescanPersistsBound`) build a 3-4-row table, index it after
loading, and plan it unanalysed. With the size recorded, PG itself plans a Seq
Scan for them. They exist to exercise the executor's index scan, so they now
run with `autovacuum = off` (`withAutovacuumOff`), under which PG leaves the
size unmeasured too. **Their flip-arm disposition changes accordingly:** with
the size unmeasured, PG plans the Bitmap Heap Scan measured in the oracle
section above, so at the flip they need re-checking against that shape, not
the Seq Scan.

Non-btree builders, REINDEX and the index relation's own `pg_class` size are
not covered yet (ledgered). No benchmark plan moved on either arm; those
tables are analysed.

## Slice 2b design: `IndexScan.RangePrefix` (`ea8fb4fce`)

PG's `build_index_paths` binds a range on the column after an equality
prefix: `a = 7 AND b > 90` on `(a, b)` becomes
`Index Cond: ((a = 7) AND (b > 90))`. goopg's probe had no such shape, since
`Keys` meant equality only and `LowKey`/`HighKey` bounded the leading column.

- **Probe shape.** `IndexScan.RangePrefix` holds equality keys for index
  columns `[0, n)` and moves `LowKey`/`HighKey` onto column `n`. It is set only
  together with a bound, never with `Key`/`Keys`/`SAOPKeys`.
- **Executor.** `lookupRangeBounds` builds each bound as the prefix's parts
  plus the bound's part. An open side uses the bare prefix (low) or its padded
  upper bound (high), exactly as a `Keys` prefix probe does. With no prefix
  the output is byte-for-byte unchanged.
- **Producer.** After an equality prefix that stops short of the last column,
  `restrictionRangeOnColumn` binds the next column's first lower and upper
  bound. The prefix equalities are dropped from the Filter; the bounds stay as
  a recheck, because an open bound runs to the prefix's padded upper bound,
  past NULLs in the bounded column. Selectivity is the equality prefix times
  the range band.
- **Consumers.** A site-by-site audit of every reader of the probe fields
  decided each one:
  - taught: EXPLAIN; the UPDATE/DELETE `indexScanPredicate`, which now
    rebuilds the prefix equalities because the Filter no longer holds them;
    `clonePlanReplacingOuter` and its SeqScan demotion (which also now keeps
    `>`/`<` strictness, a pre-existing loss); `walkPlanExprs`, `NodeSubplans`,
    `lowerTraverseNode`, `graftNodeUncached`, `relConsiderParallel`,
    `rebaseNLIProbeKeys`, `indexScanRows`, `indexProbeHasOuterRef`;
  - refusing: `tryPromoteIndexOnlyScan` and `indexOnlyNLIInner`, because
    `IndexOnlyScan` has no `RangePrefix`. Copying only the bounds re-aimed them
    at the leading column; a live subquery-wrapped count returned 9,476 rows
    for PG's 2 before the guard. `indexOnlyNLIInner` now also refuses
    `SAOPKeys`, which it dropped the same way.
- **Found by the same probe:** `matchBitmapIndexQuals` did not stop at the
  first unbound column. On the jointree arm, `WHERE c = 99` on `(a, b, c)` built
  a gapped clause list and the backend panicked. It now binds a gapless
  prefix; PG 18 would use a btree skip scan there (ledgered).

Checked on a live flip server against PG 18.3, on 20,000 rows with NULLs in
the bounded columns: every SELECT/count/sum, a subquery-wrapped count, and
UPDATE and DELETE through prefix+range predicates are identical to PG.
`TestRestrictionPrefixRangeIndexScanOnJointreePipeline` and
`TestMatchBitmapIndexQualsIsGapless` pin the plan shape and the gapless
prefix. No corpus plan moved on either arm.

Still open:
- an `IN` list followed by a range (`TestSAOPWithConjunctMoves`, since
  `SAOPKeys` is an exclusive probe shape);
- range and SAOP quals on the bitmap producer;
- keeping the bound out of the Filter (PG has no recheck);
- index-only scans with a `RangePrefix`.

