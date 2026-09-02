# 06 — goopg statistics infrastructure and selectivity estimation (current state, HEAD 2026-09-02)

Counterpart of [03 — PostgreSQL statistics infrastructure](03-pg-statistics-infrastructure.md).
Section numbers and headings mirror doc 03 one-to-one so the two can be read
side by side; the closing §13 fidelity table is the artefact the gap analysis
consumes.

Every symbol below was re-read at HEAD (branch `review-bug-fix`, 2026-09-02).
Claims that could not be settled by reading code are marked **UNVERIFIED**.
Line numbers are omitted where a `grep`/Serena lookup by name is more durable;
where a comment is load-bearing it is quoted.

---

## 0. Two contradictions in the prior record, resolved by reading the code

### 0.1 Haas–Stokes: the deferral-ledger row is STALE

`.ralph/deferral_ledger.md` row 777 (2026-08-04, M0127-P5.6-e-ii) says:

> per-column NDistinct consumed as-is by live estimator … **no Haas-Stokes
> scale-up: NDistinct=len(freq) of ~30k sample**, so 1.5M-row unique key reads
> ~30k and every join divides 50x small

That row describes the code as it stood *before* the very next slice landed. It
is **stale at HEAD**. The commit that changed it:

```
30293f788  planner(cost): M0127-P5.6-e-iii — ANALYZE reported a sample count
           as a table count, and the query that got slower because it stopped lying
```
(`git log -S ndistinctEstimate --oneline -- internal/executor/operators_analyze.go`
returns exactly this one commit.)

What `internal/executor/operators_analyze.go:ndistinctEstimate` actually
computes today — a branch-for-branch transcription of `compute_scalar_stats`'
ndistinct block (`analyze.c:2588-2648`):

```
ndistinctEstimate(d, nmultiple, n_nonnull, nullfrac, totalRows) -> float64:
    if d == 0 or n_nonnull == 0:              return 0
    N = totalRows * (1 - nullfrac)            # PG's totalrows*(1-stanullfrac)
    if totalRows <= 0:  return d              # test-only wrapper; no measured count
    if nmultiple == 0:  return N              # PG's stadistinct = -(1-nullfrac)
    if nmultiple == d:  return d              # whole value set is in the sample
    f1 = d - nmultiple                        # values seen exactly once
    n  = n_nonnull
    est = (n*d) / ((n - f1) + f1*n/N)         # Duj1 / Haas-Stokes
    clamp est into [d, N]
    return floor(est + 0.5)
```

`toowide_cnt` is constant-folded to zero (goopg never truncates a sampled
value, so there is no wide-value bucket) — this is the only structural
simplification.

**What is stored.** `computeColumnStats` writes BOTH renderings of the one
estimate:

```
ndAbs              = ndistinctEstimate(len(freq), nmultiple, nonNull, NullFrac, totalRows)
stats.NDistinct     = int64(ndAbs + 0.5)                 # ABSOLUTE, always >= 0
stats.NDistinctFrac = min(ndAbs / totalRows, 1.0)        # scale-free fraction
```

There is no negative `NDistinct`. PG's signed `stadistinct` is reconstructed on
demand by `catalog.ColumnStats.StaDistinct()`:

```
StaDistinct():
    if NDistinctFrac > 0.1: return -NDistinctFrac      # PG's analyze.c 10% rule
    if NDistinct   > 0:     return  float64(NDistinct)
    if NDistinctFrac > 0:   return -NDistinctFrac      # restored-from-heap case
    return 0                                            # unknown
```

The third arm exists because `initdb.loadStatisticsFromHeapForDB` can recover a
negative on-disk `stadistinct` only as a fraction (converting needs a row count
the reload does not have).

**What the planner reads.**

| consumer | file:symbol | reads |
|---|---|---|
| PG-shaped search, join equality | `internal/optimizer/joinselectivity.go:getVariableNumDistinct` | `v.stats.StaDistinct()`, then PG's branch order |
| legacy `estimateJoin` | `internal/optimizer/cardinality.go:pairNDistinct` → `keyNDistinct` / `rightExprNDistinct` → `columnNDistinctForChild` | `NDistinct` (absolute) via `resolveBaseColumn` |
| parameterized index paths | `internal/optimizer/pathparamindex.go:variableNumDistinct` | thin wrapper on `getVariableNumDistinct` (deliberately not a 4th copy) |
| `estimate_num_groups` | `internal/optimizer/cardinality.go:groupVarNDistinct` | `baseColumnRef.ndistinct` (absolute, already scaled out) |
| parallel-agg split gate | `internal/optimizer/parallel_agg.go` | `NDistinctFrac` directly |
| `pg_stats.n_distinct` / `pg_statistic.stadistinct` | `catalog/pgstats.go`, `executor/pg18_user_catalog_rows.go:buildUserPGStatisticRow` | `StaDistinct()` |

`getVariableNumDistinct` reproduces `get_variable_numdistinct`'s branch order
exactly:

```
stadistinct = stats.StaDistinct()   (or 2.0 for a bool operand with no stats)
if stadistinct > 0:            return clampRowEst(stadistinct),   isdefault=false
if tuples <= 0:                return DEFAULT_NUM_DISTINCT(200),  isdefault=true
if stadistinct < 0:            return clampRowEst(-stadistinct * tuples), false
if tuples < 200:               return clampRowEst(tuples),        false
return 200, isdefault=true
```

`tuples` is the relation's RAW pre-filter row count (`relInfos[i].baseRows`),
which is PG's `vardata->rel->tuples` — correct.

**Residual defect found while verifying this.** `analyzeRelationWith` applies
the per-column `n_distinct` reloption override *after* `computeColumnStats`:

```go
stats.Columns[i] = computeColumnStats(...)           // sets NDistinct AND NDistinctFrac
if nd, ok := columnNDistinctOverride(&tbl.Columns[i], stats.RowCount); ok {
        stats.Columns[i].NDistinct = nd              // NDistinctFrac left untouched
}
```

`StaDistinct()` tests `NDistinctFrac > 0.1` FIRST, so for any column whose
Haas–Stokes fraction exceeds 10 % the user's `ALTER TABLE … SET (n_distinct = …)`
is silently ignored by every `StaDistinct()` consumer (planner join estimator,
`pg_stats`, the `pg_statistic` heap row). Only `columnNDistinctForChild`, which
reads `NDistinct` directly, honours it. This is a live sibling-path divergence,
not something the ledger records.

### 0.2 "Stats are per-connection": FALSE. They are process-wide, per-database, and they DO survive restart

Two stale claims are in the tree:

- `internal/optimizer/small_dimension.go` (comment on `smallDimensionMaxRows`):
  *"Cold (goopg's normal state — TableStats does not survive a restart, and
  ANALYZE stats are per-connection)"*.
- older cost-model design docs repeat the same sentence.

The code says otherwise. The chain, verified end to end:

1. **Storage is on the shared catalog object.** `catalog.Table.Stats
   *TableStats` is a field of the single `*catalog.InMemory` table object.
   `InMemory.SetTableStats(table, stats)` takes `c.mu.Lock()` and does
   `table.Stats = stats` — a pointer replacement on the SHARED object. There is
   no per-session copy anywhere in the write path.

2. **The per-connection catalog wrapper does not copy tables.**
   `internal/postmaster/dispatch.go:sessionPlanCatalog` / `ctxPlanCatalog`
   return a `*catalog.SearchPathCatalog`, which *embeds* `catalog.Catalog` and
   carries only scalar session state (`DBOid`, `TempOwnerToken`,
   `DisableSeqScan`, `DisableIndexScan`, `DisableBitmapScan`,
   `DisableIndexOnlyScan`, `SnapshotPartitionDetachEpoch`).
   `SearchPathCatalog.LookupTable` delegates to `InMemory.LookupTable`, which
   returns `ns.tables[key]` — the **live pointer**. (Contrast
   `InMemory.AllTables`, which deep-copies; `UserTableHandles` exists
   specifically because bare `ANALYZE` must NOT get copies — its comment:
   *"publishing stats onto a copy would silently discard the whole ANALYZE
   scan"*.)

3. **Per-database, not per-connection.** `InMemory` keys tables by namespace
   `ns(dbOid)`. Two connections to the same database share one `*Table` and
   therefore one `*TableStats`. Two connections to *different* databases see
   different `*Table` objects. So the scoping unit is the **database**, and
   within it the **process**.

4. **Restart survival is real, for ANALYZE.** `persistStatsToPGStatistic`
   writes per-column rows to `base/<dbOid>/2619` (real `pg_statistic` heap
   tuples, PG18 physical layout) plus a `(starelid, rowcount, pages)` row to
   the goopg-private sidecar `base/<dbOid>/9410`
   (`catalog.GoopgRelStatsRelationId`). `initdb.loadStatisticsFromHeap` runs at
   startup for `DefaultDBOid` and then for every other database
   (`loadStatisticsFromHeapForDB`), rebuilds `ColumnStats`
   (`NDistinct`/`NDistinctFrac`/`NullFrac`/`AvgWidth`/`Correlation`/MCV/
   histogram) plus `RowCount`/`Pages`, sets `Analyzed: true`, and calls
   `SetTableStats`.

**The truth, precisely:**

| fact | status |
|---|---|
| ANALYZE stats visible to other sessions in the same DB | **yes**, immediately (shared pointer) |
| ANALYZE stats visible to a session on a different DB | **no** (different namespace) |
| ANALYZE stats survive restart | **yes** — via `pg_statistic` heap + `goopg_relstats` sidecar |
| …except columns whose `pg_statistic` row exceeds one heap page | **lost** (no TOAST — see §2.11) |
| VACUUM's `reltuples`/`relpages` (`UpdateRelStats`) survive restart | **NO** — in-memory only; no writer to either heap |
| autovacuum/autoanalyze stats survive restart | **NO** — `runAnalyze` sets `tbl.Stats = ts` and never calls `persistStatsToPGStatistic` (needs an `executor.Context`; documented deviation in `launcher.go`) |
| `Correlation` survives restart | **yes** — stakind3/stanumbers3 slot |
| `AvgWidth` survives restart | table-level: **no** (`GoopgRelStatsColumns` deliberately omits it). Per-column: **yes** (`stawidth`) |
| a cached plan is re-planned after `ANALYZE` | **no** — see §11 |
| a cached plan is re-planned after `SET` | **no** for cost/parallel GUCs; a session with any `enable_{seq,index,bitmap,indexonly}scan = off` **bypasses** the cache entirely (`plannerScanTogglesActive`) |

The `small_dimension.go` comment (and the design docs that repeat it) should be
corrected; the *conclusion* it draws ("region and nation are still small in the
cold state") happens to survive, because the block-count fallback reaches the
same answer.

---

## 1. Relation-level statistics (goopg's `pg_class` analogue)

### 1.1 The structs and their writers

`internal/catalog/catalog.go`:

```go
type TableStats struct {
        RowCount int64          // pg_class.reltuples (exact, not sampled)
        Pages    int            // pg_class.relpages (live NBlocks at ANALYZE time)
        AvgWidth float64        // whole-tuple average bytes; no pg_class counterpart
        Columns  []ColumnStats  // POSITIONAL over Table.Columns
        Analyzed bool           // stand-in for PG's reltuples = -1 sentinel
}

type ColumnStats struct {
        NDistinct     int64      // absolute; see §0.1
        NullFrac      float64    // stanullfrac
        AvgWidth      float64    // stawidth, but VARIABLE PAYLOAD bytes only
        MCV           []MCVEntry // stakind 1
        Histogram     []string   // stakind 2, equi-depth bound TEXT
        NDistinctFrac float64    // the negative-stadistinct rendering
        Correlation   float64    // stakind 3
}

type MCVEntry struct { Value string; Frequency float64 }
```

Mapping to PG:

| PG | goopg | notes |
|---|---|---|
| `pg_class.reltuples` | `TableStats.RowCount` | exact live count; ANALYZE full-scans |
| `pg_class.reltuples = -1` | `TableStats.Analyzed == false` | goopg cannot store −1 in an `int64` "count", so the sentinel is a separate bool (`Analyzed`'s own doc comment says exactly this) |
| `pg_class.relpages` | `TableStats.Pages` | snapshot at ANALYZE; **not** re-read live by `baseRelPages` (§1.3) |
| `pg_class.relallvisible` | none stored; computed live by `catalog.RelAllVisibleFunc` | §1.3 |
| `pg_class.relallfrozen` (PG 18) | **absent** | autovacuum's `pcnt_unfrozen` factor is hardcoded 1 |
| `pg_statistic.stanullfrac` | `ColumnStats.NullFrac` | ✔ |
| `pg_statistic.stawidth` | `ColumnStats.AvgWidth` | **divergent**: `datumVariablePayloadWidth` counts only bytes BEYOND the fixed 48-byte `Datum` (arena/Buf payload). A fixed-width `int4` column has `AvgWidth = 0`, where PG stores 4 |
| `pg_statistic.stadistinct` | `NDistinct` + `NDistinctFrac` → `StaDistinct()` | §0.1 |
| `stakind 1` MCV | `MCV []MCVEntry` (text values) | values are `Datum.Format()` text, not typed `anyarray` |
| `stakind 2` HISTOGRAM | `Histogram []string` | ditto |
| `stakind 3` CORRELATION | `Correlation` | ✔ computed and persisted; **NULLed in `pg_stats`** (§3.2) |
| `stakind 4/5/6/7` (MCELEM, DECHIST, RANGE_*) | **absent** | never computed, never written |
| `stainherit` | **absent** — always `false` | no inheritance-tree statistics |

Writers of `Table.Stats`:

| writer | path | durable? |
|---|---|---|
| `ANALYZE` (SQL) | `executor/operators_analyze.go:analyzeOp.Next` → `Catalog.SetTableStats` + `persistStatsToPGStatistic` | yes (modulo page overflow) |
| partitioned-parent roll-up | same file, second loop: `parent.Stats = &TableStats{RowCount: Σkids, Pages: Σkids, Analyzed: true}` then `SetTableStats` | **no** — no persist call for the parent |
| `VACUUM` | `executor/operators_vacuum.go` → `vacuum.Analyze(...)` → `Catalog.UpdateRelStats(tbl, pages, rows)` | **no** |
| autovacuum / autoanalyze | `postmaster/autovacuum/launcher.go:runAnalyze` → `executor.AnalyzeRelationSampled` then `tbl.Stats = ts` (**direct field write, not `SetTableStats` — no catalog lock**) | **no** |
| startup reload | `initdb/open.go:loadStatisticsFromHeapForDB` → `SetTableStats` | — (is the reload) |

### 1.2 `UpdateRelStats` — goopg's `vac_update_relstats`, with no `vac_estimate_reltuples`

```
UpdateRelStats(table, pages, tuples):
    lock
    if table.Stats == nil:
        table.Stats = &TableStats{Pages: pages, RowCount: tuples, Analyzed: true}
        return
    merged := *table.Stats            # copy so a concurrent reader sees no torn struct
    merged.Pages, merged.RowCount, merged.Analyzed = pages, tuples, true
    table.Stats = &merged             # pointer replace; per-column stats preserved
```

Faithful in the part it implements — it merges rather than replaces, mirroring
"VACUUM and ANALYZE both call `vac_update_relstats` but only ANALYZE rewrites
`pg_statistic`".

**Absent:** the whole of `vac_estimate_reltuples`. goopg's VACUUM path calls
`vacuum.Analyze`, which counts live tuples under a fresh snapshot over the
whole relation, so there is no scanned-fraction extrapolation, no "keep the old
reltuples when < 2 % of an unchanged-size table was scanned", and no `<= 1
page` guard. Checklist items 6 and 7 of doc 03 have no counterpart.

### 1.3 How the planner reads them

Three entry points, and they do not agree about which page count to believe.

**(a) `relsize.go:estimateRelSize`** — a genuine transcription of
`table_block_relation_estimate_size`:

```
estimateRelSize(curpages, relpages, reltuples, analyzed, hasSubclass, fillfactor, dataWidth):
    if curpages < 0: return 0, 0
    if curpages < 10 and !analyzed and !hasSubclass:   curpages = 10     # (1) the 10-page floor
    pages = curpages
    if curpages == 0: return 0, 0                                       # (2) after the floor
    if analyzed and relpages > 0:
        density = reltuples / relpages                                  # (3)
    else:
        density = INTEGER( usable_page_bytes * fillfactor/100 / (dataWidth + tupleOverhead) )
        density = clamp_row_est(density)                                # (4) integer division, as upstream
    tuples = rint(density * curpages)                                   # (5)
```

`hasSubclass` is `len(tbl.PartitionKey) > 0` only — a plain (non-partition)
inheritance parent is **not** detected, so the 10-page floor is wrongly applied
to it. Ledgered under M0125-0003.

**(b) `relsize.go:estimateTableRowsFallback`** — the gated caller. It reads the
live block count via `InMemory.RelationBlocks` (→ `catalog.RelNBlocksFunc`,
wired by `initdb/open.go` to `pool.Exists` + `pool.NBlocks`, deliberately
avoiding the `O_CREATE` fork-recreation hazard), then calls `estimateRelSize`.

Gated by `GOOPG_RELSIZE_FALLBACK` (`relsizeFallbackStage`, default **2**).
`relSizeFallbackEnabled(stage)` lets a consumer be staged in.

**Deliberate divergence, stated in the code:** when `TableStats.RowCount > 0`
the fallback is not consulted at all — goopg does **not** scale stored
statistics by the live block count the way PG always does. The comment: *"That
divergence is the invariant that makes the flag's A/B honest … flag-on and
flag-off must produce byte-identical plans in any ANALYZEd state."* So a table
that grew 10× since its last ANALYZE keeps the old row estimate, where PG would
scale it.

**(c) `pathindexordered.go:baseRelPages`** — the Path model's `baserel->pages`:

```
baseRelPages(tbl, relTuples):
    if tbl.Stats != nil && tbl.Stats.Pages > 0: return tbl.Stats.Pages   # STORED relpages
    return estScanPages(relTuples, dataWidth + perTupleOverhead)
```

This is a **direct divergence from doc 03 checklist item 11**: PG's
`estimate_rel_size` always uses the live `RelationGetNumberOfBlocks` for
`pages`. goopg prefers the ANALYZE snapshot. On a grown table the cost model
under-charges I/O.

**allvisfrac.** `pathindexonly.go:relAllVisibleFraction(tbl, relPages)`:

```
visible = catalog.RelAllVisible(tbl)            # RelAllVisibleFunc(dbOid, relOid)
if visible <= 0: return 0
return min(visible / relPages, 1)
```

`catalog.RelAllVisibleFunc` **is** wired (`initdb/open.go`, → `rt.VM.CountAllVisible`),
so this is a real figure, not a stub. A never-vacuumed table returns 0 and the
index-only path loses on cost, which is the safe direction. The consumer is
`costindex.go:heapPagesAfterVM`, `cost_index`'s `pages_fetched * (1 - allvisfrac)`.

Note: `relAllVisibleFraction` divides by the caller's `relPages`, which comes
from `baseRelPages` — i.e. the **stored** page count, while `CountAllVisible`
reads the **live** VM. On a grown table the ratio can exceed 1 and is clamped.

### 1.4 Index information — `estimateIndexGeometry`

`catalog.Index` carries **no `relpages`, no `reltuples`, no tree height**, and
ANALYZE never visits an index. `costindex.go:estimateIndexGeometry` synthesises
all three:

```
estimateIndexGeometry(idx, tbl, relTuples) -> (pages, tuples, treeHeight):
    tuples  = max(relTuples, 1)                        # one entry per live heap tuple
    width   = indexTupleWidth(idx, tbl)                # 8 (IndexTupleData) + Σ typeWidth(key ∪ INCLUDE) + 4 (ItemId)
    fill    = idx.Fillfactor if 10<=ff<=100 else btreeDefaultFillfactor
    perPage = floor(usableBytesPerBlock * fill/100 / width);  perPage = max(perPage,1)
    pages   = max(ceil(tuples / perPage), 1)
    if real, ok := catalog.IndexRealPages(idx); ok && real > 0:      # M0134-0183
        pages   = real                                  # the REAL file size wins
        perPage = max(perPage, ceil(tuples / pages))
    treeHeight = 0
    if perPage > 1 and tuples > perPage:
        levels = ceil(log(tuples)/log(perPage));  if levels > 1: treeHeight = levels - 1
```

`catalog.IndexRealPages` → `RelNBlocksFunc` on the index's own relfilenode, so
in a live cluster `pages` is the true block count. The width-derived formula
remains for catalog-only fixtures, and its known failure is recorded in the
comment: HammerDB declares TPC-H integer keys `NUMERIC`, `typeWidth` prices an
unconstrained-precision key at 32 bytes, and every such index came out 2× real.

Absent versus doc 03 §1.4 / checklist 17–22: no metapage discount; no partial-index
`estimate_rel_size` capped at `rel->tuples`; no `amgettreeheight` (synthesised);
no `indcheckxmin` skip; `predOK` machinery lives in `predicate_implication.go`
but is deferred; no `sortopfamily`/`indoption` model (direction is a
`Path.IndexScanDir`).

### 1.5 Column-statistics access path — FOUR resolvers

goopg has four independent ways to get from a plan-tree column position to a
`*catalog.ColumnStats`. `joinkeyproof.go:resolveBaseColumn`'s own comment calls
this out as the sibling-path hazard it is.

| resolver | file | returns | node arms |
|---|---|---|---|
| `resolveBaseColumn` | `joinkeyproof.go` | `baseColumnRef{scan, table, col, ndistinct, rawRows, stats, unique}` | `SeqScan`, **`IndexScan`**, `Filter`, `Sort`, `Project` (remaps through `Targets`), `Limit`, `LockRows`, `Gather`, `GatherMerge`, `CTEScan`, `Join` (merged-coordinate shift) |
| `columnNDistinctForChild` | `cardinality.go` | `int64` ndistinct | delegates its base arms to `resolveBaseColumn` |
| `columnStatsForChild` | `selectivity.go` | `*ColumnStats` | `SeqScan`, `Filter`, `Sort`, `Project`, `Limit`, `LockRows`, `Gather`, `GatherMerge`, `CTEScan`, `Join` — **no `*IndexScan` arm** |
| `columnStatsByName` | `pathparamindex.go` | `*ColumnStats` | not a tree walk: name → ordinal → `tbl.Stats.Columns[i]`. Used by the search (`examineJoinVar`) because search clauses live in the pre-search concatenation coordinate space where `ColumnRef.Index` is a global offset |

**Ledger row 785 confirmed at HEAD**: `columnStatsForChild` still lacks the
`*IndexScan` arm. Consequence: a restriction clause over a leaf the planner
chose to index-probe resolves **no MCV list and no histogram**, and collapses to
`defaultEqSelectivity` / `defaultIneqSelectivity`. Whether a column has usable
statistics therefore depends on which scan shape the planner picked — the exact
failure `keyColumnStats`' comment says it routes around by using
`resolveBaseColumn` instead.

`isunique`: `joinVarStats` (the search's `VariableStatData`) has **no `isunique`
field at all**; `examineJoinVar` never sets one. `resolveBaseColumn` does carry
`UniqueKeys` from the scan node, but only the superkey prover
(`joinrelsize.go`, `joinkeyproof.go`) consumes it. So doc 03 checklist items 60
and 67 (`has_unique_index` → `stadistinct = -(1-nullfrac)`) have **no
counterpart in the selectivity path**; uniqueness reaches the estimate only
through the FK/superkey override (§10).

Positional alignment: `Stats.Columns[i]` ↔ `Table.Columns[i]`. Nothing
validates the two lengths agree at write time; every reader bounds-checks.

---

## 2. ANALYZE (`internal/executor/operators_analyze.go`)

### 2.1 `analyzeRelationWith` flow

```
analyzeRelationWith(pool, mgr, cat, tbl, target, rng, mxs, dsCtx):
    tx   = mgr.Begin(ReadCommitted);  snap = mgr.SnapshotFor(tx)     # fresh snapshot
    nBlocks = pool.NBlocks(rel)
    sampleCap = max(target * 300, 1)                                 # PG's targrows
    stats = TableStats{Pages: nBlocks, Columns: make(len(tbl.Columns)), Analyzed: true}

    for blk in 0 .. nBlocks-1:                                       # EVERY block
        pin; if IsNew(page): unpin; continue
        for slot in 1 .. linePointerCount:
            t = PageGetHeapTuple
            if !TupleVisible(t.Header, snap, tx.XID, curcid, combo, mxs): continue
            stats.RowCount++                                         # EXACT live count
            totalBytes += Hoff + len(t.Data)
            # Algorithm R reservoir; `keep` decided BEFORE the decode (review/260831 EO1-4)
            if seen < sampleCap:            keep = len(reservoir); reservoir.append(nil)
            elif j := rng.Int63n(seen+1); j < sampleCap:  keep = j
            else:                           keep = -1
            seen++
            if keep < 0: continue
            row = DecodeRowIntoMctxPGTuple(...); reservoir[keep] = row
        unpin
    if RowCount > 0: stats.AvgWidth = totalBytes / RowCount
    for i in columns:
        colTarget, ok = columnStatsTarget(&col[i], target);  if !ok: continue   # SET STATISTICS 0
        stats.Columns[i] = computeColumnStats(reservoir, i, colTarget, stats.RowCount, dsCtx)
        if nd, ok := columnNDistinctOverride(&col[i], stats.RowCount): stats.Columns[i].NDistinct = nd
    return stats
```

**Divergence from doc 03 §2.3 (the big one).** PG uses two-stage sampling —
Knuth Algorithm S over `min(targrows, nblocks)` blocks, then Vitter reservoir
within them — and *extrapolates* `totalrows = liverows/blocks_read × totalblocks`.
goopg reads **every block** and reservoir-samples visible tuples with
Algorithm R. Consequences:

- `RowCount` is **exact**, not extrapolated. Checklist item 28 has no
  counterpart and does not need one.
- ANALYZE cost is O(relation), not O(targrows). A 6 M-row `lineitem` ANALYZE
  reads 6 M tuples' visibility headers (though since review/260831 EO1-4 it
  decodes only the ~30 000 it keeps).
- The sample is uniform over *tuples*, where PG's is uniform over *blocks then
  tuples*. For a clustered relation goopg's sample is the better one; the
  divergence is in goopg's favour but is a divergence.
- No TID sort of the filled reservoir (checklist 29) — goopg's reservoir is in
  replacement order, which matters only for the correlation computation, which
  uses the ORIGINAL sample position, so it is unaffected.

### 2.2 Per-column target and type dispatch

```
columnStatsTarget(col, tableTarget):
    if col.StatTarget == nil: return tableTarget, true
    if *col.StatTarget == 0:  return 0, false      # caller skips the column AND the pg_statistic row
    return *col.StatTarget, true
```

`upstreamDefaultStatsTarget = 100`, `upstreamSampleMultiplier = 300`. The table
target comes from `ctx.StatsTarget` (`default_statistics_target`), defaulting to
100. **There is no `max(100, …)` floor** on `targrows` and no
`max` across columns / index expressions — `sampleCap` is `tableTarget × 300`
regardless of a per-column `SET STATISTICS 10000`. A column with a raised
target therefore gets more MCV/histogram slots out of the SAME sample.

**No type dispatch.** PG chooses `compute_scalar_stats` /
`compute_distinct_stats` / `compute_trivial_stats` via `std_typanalyze`.
goopg runs one `computeColumnStats` for every column and degrades internally:
`isOrderableKind(k)` (true for `KindInt`, `KindBool`, `KindString`, `KindTime`,
`KindNumeric`) gates the correlation and the histogram; a non-orderable kind
(bytes, interval) gets `NullFrac`/`AvgWidth`/`NDistinct`/`MCV` only, which is
`compute_distinct_stats`'s output reached by a different route.

**No `WIDTH_THRESHOLD`.** PG excludes varlena values wider than 1024 bytes from
value statistics and counts them as distinct. goopg has no such rule — a wide
`text` column's full values enter `freq`, the MCV list and the histogram. This
is the proximate cause of the page-overflow loss in §2.11.

### 2.3–2.4 `computeColumnStats` in full

```
computeColumnStats(sample, colIdx, statsTarget, totalRows, dsCtx) -> ColumnStats:
    freq: map[datumKey] -> {representative Datum, count}
    for pos, row in sample:
        d = row[colIdx]
        if d.IsNull(): nullCount++; continue
        nonNull++;  totalPayloadWidth += datumVariablePayloadWidth(d)
        freq[datumKey(d)].count++
        corrPairs.append({d, pos})

    NullFrac = nullCount / len(sample)                  # denominator INCLUDES nulls (PG-faithful)
    AvgWidth = totalPayloadWidth / nonNull              # VARIABLE payload only (divergent)

    nmultiple = count of freq buckets with count > 1
    ndAbs     = ndistinctEstimate(len(freq), nmultiple, nonNull, NullFrac, totalRows)
    NDistinct     = round(ndAbs)
    NDistinctFrac = min(ndAbs / totalRows, 1)

    if nonNull == 0: return

    # --- correlation (stakind 3) ---
    if len(corrPairs) > 1 and isOrderableKind(corrPairs[0].Kind):
        sort corrPairs by value
        if compareDatum(corrPairs[0], corrPairs[1]) succeeded:
            Σxy = Σ_i  originalPos[i] * sortedPos[i]
            n   = len(corrPairs);  Σx = (n-1)n/2;  Σx² = (n-1)n(2n-1)/6
            Correlation = (n·Σxy - Σx·Σx) / (n·Σx² - Σx·Σx)

    # --- MCV split (stakind 1) ---
    buckets = freq values sorted by count DESC
    mcvCap  = min(statsTarget, len(buckets));  mcvCount = 0
    while mcvCount < mcvCap:
        candidate        = buckets[mcvCount]
        remaining        = nonNull - Σ_{k<=mcvCount} buckets[k].count
        distinctRemaining = len(buckets) - (mcvCount+1)
        if distinctRemaining <= 0:
            if candidate.count > 1: mcvCount++          # never admit a singleton "MCV"
            break
        avgRemaining = remaining / distinctRemaining
        if avgRemaining <= 0: break
        if candidate.count < 1.25 * avgRemaining: break # mcvFreqMargin
        mcvCount++
    MCV[i] = { Value: formatDatumDateStyle(buckets[i].val, dsCtx),
               Frequency: buckets[i].count / len(sample) }      # denominator INCLUDES nulls (PG-faithful)

    # --- histogram (stakind 2) ---
    nonMCV = buckets[mcvCount:]
    if len(nonMCV) < 2 or !isOrderableKind(nonMCV[0].Kind): return
    expanded = every non-MCV value repeated by its sample count, sorted ascending
    bucketCount = min(statsTarget, len(nonMCV)-1);  if < 1: return
    last = len(expanded)-1
    bounds[i] = format(expanded[ i*last / bucketCount ])   for i in 0..bucketCount
    dedup adjacent equal bounds
    if len(dedup) >= 2: Histogram = dedup
```

### 2.5 Which MCVs are kept — the 1.25× rule

goopg uses `mcvFreqMargin = 1.25` — "a value qualifies when its sample
frequency exceeds the average frequency of the remaining values by at least
this multiplier". **Doc 03 checklist item 38 states flatly that no 1.25× rule
exists in PG 18.3**: upstream's `analyze_mcv_list` prunes from the least common
upward using a hypergeometric `2·stddev + 0.5` test against the non-MCV
selectivity.

This is one of the largest single divergences in the file. Direction:

- On a near-uniform column (PG's `tenk1.stringu1`, 676 values × ~15 rows each)
  goopg's rule admits every value whose count exceeds `1.25 × ~14.8 ≈ 18.5`,
  which for a Poisson-ish count distribution is roughly the top 10–15 % —
  **dozens of MCVs where PG 18.3 would keep few or none**.
- Each spurious MCV consumes histogram mass and shifts the non-MCV remainder
  denominator, so eq-selectivity for a miss reads slightly low and range
  selectivity is shifted from the histogram term to the MCV term.
- On a genuinely skewed column both rules admit the skewed values, so the
  divergence is largely a uniform-column artefact.

The "complete MCV list" case (checklist 37 — all sampled values fit and
`stadistinct > 0`) has **no counterpart**: goopg's loop stops at the first value
failing the ratio test, so it cannot produce a complete list except by
coincidence.

### 2.6 `compute_distinct_stats`

No separate function; see §2.2 — reached implicitly through `isOrderableKind`.

### 2.7 Width accounting

`AvgWidth` (per column) = `Σ datumVariablePayloadWidth / nonNull`, where
`datumVariablePayloadWidth` returns:

```
KindString / KindBytes / KindEnum:  ArenaID != 0 ? low-32-bits(Int) : len(Buf)
KindNumeric:                        flagBigNumeric ? low-32-bits(Int) : 0
default:                            0
```

So `int4`, `int8`, `bool`, `date`, small `numeric` all report **width 0**. The
`pg_statistic.stawidth` written is `int64(AvgWidth + 0.5)` — zero for those
types, where PG writes 4/8/1/4. Consumers: `costMemoizeRescan` (ledger row 869
explicitly names this as the missing input) and `pg_stats.avg_width`. The
table-level `TableStats.AvgWidth` is a *different* quantity — whole physical
tuple bytes (`Hoff + len(Data)`) — and is not persisted at all.

`tupleWidth` / `typeWidth` (`relsize.go`) is the planner's *declared-type*
width model and is entirely separate from `stawidth`; nothing bridges the two
(PG's `get_rel_data_width` sums `stawidth` per column and falls back to
`get_typavgwidth`).

### 2.8 `compute_index_stats`

**Absent.** ANALYZE never opens an index. No index `reltuples`, no expression-index
`pg_statistic` rows, no `tupleFract` partial-index accounting. Doc 03 checklist
items 9 and 44 have no counterpart. This is the same gap `estimateIndexGeometry`
(§1.4) papers over.

### 2.9 Inheritance statistics, stat targets, `n_distinct` override

- **Inheritance stats: absent.** Every `pg_statistic` row is written with
  `stainherit = false`; `pg_stats.inherited` is hardcoded `"f"`. There is no
  `acquire_inherited_sample_rows`. The only inheritance-aware behaviour is the
  partitioned-parent *roll-up* in `analyzeOp.Next`, which sums children's
  `RowCount`/`Pages` and leaves the parent's column stats empty.
- **`SET STATISTICS n`**: honoured per column (`columnStatsTarget`); `0` writes
  no `pg_statistic` row and produces no `pg_stats` row. The `-1 → default` and
  `max 10000` clamps are not enforced here (goopg stores `*int`).
- **`n_distinct` reloption**: `columnNDistinctOverride` implements PG's sign
  convention (`v > 0` absolute, `v < 0` fraction clamped to `[-1, 0)`), but see
  the defect in §0.1 — it writes only `NDistinct`, so `StaDistinct()` may ignore
  it. `n_distinct_inherited` is deliberately not honoured (single-relation scan).

### 2.10 Autovacuum / autoanalyze triggering

`internal/postmaster/autovacuum/launcher.go`:

```
needsAnalyze(tbl):
    _, _, mod = executor.TriggerSnapshot(tbl.OID)          # n_mod_since_analyze
    reltuples = tbl.Stats.RowCount if Stats != nil && RowCount > 0 else 0
    return mod > anlThresh + anlScale * reltuples          # defaults 50 + 0.1·reltuples
```

Matching PG's `relation_needs_vacanalyze` (checklist 49) including the
`reltuples < 0 → 0` behaviour, which goopg reaches through the `RowCount > 0`
guard. GUCs `autovacuum_analyze_threshold` / `_scale_factor` are read.

Additional goopg-only gate: `MinAnalyzeAge = 60 s` per table (`l.lastAnalyze`
map), which has no upstream counterpart.

`runAnalyze` calls `executor.AnalyzeRelationSampled` (default target 100,
wall-clock-seeded RNG, no `Context`), assigns `tbl.Stats = ts` **directly**
(bypassing `SetTableStats` and therefore the catalog mutex), and calls
`executor.ResetAnalyzeTriggers(tbl.OID)`.

`n_mod_since_analyze` analogue: `relationStatsManager` (`executor/pgstat_relations.go`),
reset by `resetAnalyzeTriggers` from both the SQL ANALYZE path and the launcher.
Vacuum triggers (`n_dead_tup`, `n_ins_since_vacuum`) are the sibling
`resetVacuumTriggers`. `pcnt_unfrozen` is hardcoded to 1 — goopg tracks no
`relallfrozen`.

### 2.11 `persistStatsToPGStatistic` and the page-overflow loss

```
persistStatsToPGStatistic(ctx, tbl, stats):
    dbOid = tableCatalogHeapDBOid(ctx)                        # the CONNECTION's database
    statRel     = base/<dbOid>/2619
    relStatsRel = base/<dbOid>/9410                           # GoopgRelStatsRelationId
    firstErr = nil
    for i, cs in stats.Columns:
        if i >= len(tbl.Columns): break
        if tbl.Columns[i].StatTarget != nil && *== 0: continue
        row = buildUserPGStatisticRow(tbl.OID, int16(col.Ordinal+1), cs)
        if _, err := writeHeapRowCanonical(ctx, statRel, pgStatisticColumnsPG18(), row); err != nil && firstErr == nil:
            firstErr = err                                    # keep going
    sizeRow = (tbl.OID, stats.RowCount, stats.Pages)
    writeHeapRowCanonical(ctx, relStatsRel, GoopgRelStatsColumns(), sizeRow)
    return firstErr                                           # caller ignores it
```

The comment block that documents the loss, verbatim:

> One failing column must not sink the others or the size row below: a wide-text
> column's histogram (e.g. TPC-H `partsupp.ps_comment`, `varchar(199)` × up to
> 101 bounds) builds a `pg_statistic` tuple larger than a heap page, and goopg's
> catalog heap writer does not TOAST, so `PageAddHeapTuple` rejects it even on a
> fresh page. Real PG toasts these rows (`pg_statistic` has a toast relation,
> `pg_statistic.h`) — deferral-ledger row, M0125-0029. Measured on the TPC-H
> bench cluster 2026-07-30: the early return here left `orders`/`customer`/
> `partsupp` with NO trailing-column rows and no size row, while
> `lineitem`/`part`/… — whose comment histograms fit — persisted fully. Keep the
> first error for the caller's (non-fatal) bookkeeping, write everything that
> fits.

Loop behaviour at HEAD: the failure is now **per column**, not fatal to the
relation. A column whose row exceeds `BLCKSZ` is silently skipped; every other
column and the size row still land. The relation therefore restarts with a
*partial* `Columns` slice — the skipped ordinals restore as the zero
`ColumnStats`, which every consumer reads as "no statistics", not as "wrong
statistics". Combined with the missing `WIDTH_THRESHOLD` (§2.2), any `text`
column wider than ≈ 80 bytes with a 101-bound histogram is at risk.

Heap rows are append-only; startup takes the most recent live tuple per
`(starelid, staattnum)`. There is no `DELETE` of the prior generation, so
`pg_statistic` grows monotonically with every ANALYZE.

### 2.12 `AnalyzeRelationSampled`

```go
func AnalyzeRelationSampled(pool, mgr, cat, tbl) (*catalog.TableStats, error) {
        return analyzeRelationWith(pool, mgr, cat, tbl, upstreamDefaultStatsTarget,
                rand.New(rand.NewSource(time.Now().UnixNano())), nil, nil)
}
```

The `Context`-free entry point for the autovacuum launcher. `mxs = nil` means
multixact resolution is skipped, so an only-row-locked live tuple under a
multi-xmax can be judged invisible and undercounted (the SQL path threads
`ctx.MultiXact` precisely to avoid this — M0118-0003). `dsCtx = nil` means
`DateStyle` falls back to ISO/MDY when rendering MCV / histogram text, so an
autoanalyze on a session with a non-ISO `DateStyle` writes bounds the planner's
`formatExprConstant` cannot byte-match.

---

## 3. `pg_statistic` layout and friends

### 3.1 Writer and reader

**Writer** — `executor/pg18_user_catalog_rows.go:buildUserPGStatisticRow`,
31 columns in PG18 physical order. Slots written:

| slot | stakind | staop | stanumbers | stavalues |
|---|---|---|---|---|
| 1 | `1` MCV, only when `len(MCV) > 0` | `98` (**`text =`, hardcoded, regardless of column type**) | `float4[]` of frequencies (`pgFloat4ArrayBytes`) | `text[]` of rendered values (`pgTextArrayBytes`) |
| 2 | `2` HISTOGRAM, only when `len(Histogram) > 0` | `0` (**PG writes the `<` operator**) | NULL ✔ | `text[]` of bounds |
| 3 | `3` CORRELATION, only when `Correlation != 0` | `0` (PG writes `<`) | one-element `float4[]` ✔ | NULL ✔ |
| 4, 5 | `0` | `0` | NULL | NULL |

`stacoll1..5` are all `0`. `stanullfrac` and `stadistinct` are `float4` columns
encoded as varlena TEXT by `EncodeRowPG`'s `float4` branch — a goopg storage
convention, not PG's IEEE-754 float4. `stawidth = int64(AvgWidth + 0.5)`
(§2.7). `stainherit` is always `false`.

**`stavalues` fidelity.** PG's `stavalues` is `anyarray` whose element type is
`stats->statypid[k]` (the column's own type). goopg writes a `text[]` of
`Datum.Format()` renderings for every column type. A real PG standby reading
this heap sees a `text[]` where it expects, e.g., `int4[]` — so goopg's
`pg_statistic` rows are structurally PG-shaped but **not type-faithful**, and a
standby's planner cannot use them (UNVERIFIED whether a standby errors or
silently mis-plans; not tested here).

**Reader** — `catalog/codec.go:DecodePGStatisticPhysicalRow` /
`catalog.PGStatisticRow`:

```
pgStatisticPhysicalFixed = 72   # starelid[4] staattnum[2] stainherit[1] pad[1]
                                # stanullfrac[4] stawidth[4] stadistinct[4]
                                # stakind1..5[10] pad[2] staop1..5[20] stacoll1..5[20]
fields decoded: StaRelid, StaAttNum, StaNullFrac, StaWidth, StaDistinct,
                StaKind1..3, MCVFreqs, MCVValues, HistBounds, Correlation
```

Only slots 1–3 are decoded. `staop`/`stacoll` are read past, not interpreted.
`stakind4`/`stakind5` are ignored. The nullable varlena columns 22–31 are
walked in order using the heap tuple's null bitmap.

### 3.2 `pg_stats` view

`internal/catalog/pgstats.go:PGStatsRowsForDBOid` builds the 17-column view row
in Go (there is no SQL evaluation over the virtual catalog). Column by column:

| # | column | goopg |
|---|---|---|
| 0–3 | schemaname, tablename, attname, inherited | `t.Schema` (`""` → `"public"`), name, name, `"f"` |
| 4 | null_frac | `cs.NullFrac` |
| 5 | avg_width | `cs.AvgWidth` (§2.7 — 0 for fixed-width types) |
| 6 | n_distinct | `cs.StaDistinct()` — shares the reduction with the heap writer and the planner ✔ |
| 7–8 | most_common_vals / _freqs | `arrayTextLiteral`, freqs round-tripped through `float32` to match the heap |
| 9 | histogram_bounds | `arrayTextLiteral(cs.Histogram)` |
| **10** | **correlation** | **`NULL`, always** |
| 11–16 | most_common_elems, _elem_freqs, elem_count_histogram, range_length_histogram, range_empty_frac, range_bounds_histogram | `NULL` |

The `correlation` NULL is a **stale-comment bug**: the file's header says
"goopg's ANALYZE … does not collect correlation (kind 3)", but ANALYZE has
computed `ColumnStats.Correlation` since the Pearson block landed, the planner
consumes it (`costindex.go:indexCorrelationFor`), and
`buildUserPGStatisticRow` persists it in slot 3. Only the view was never
updated. Anyone diagnosing a correlation-driven index-cost problem through
`pg_stats` sees `NULL` and concludes the statistic is absent.

Rows are emitted only for columns with a materialised `Stats.Columns` entry and
a non-zero `StatTarget`. No `has_column_privilege` / RLS filtering (goopg
connections are effectively superuser).

### 3.3 Extended-statistics catalogs

- **`pg_statistic_ext` (OID 3381)** — real, per-DB heap rows since B5 Bstat
  (`executor/sys_pg_statistic_ext.go`). Physical `FormData_pg_statistic_ext`
  attnum order: `oid, stxrelid, stxname, stxnamespace, stxowner,
  stxkeys(int2vector), stxstattarget(int2, nullable), stxkind(char[]),
  stxexprs(pg_node_tree-as-text)`. `stxkind` chars `'d'`/`'f'`/`'m'` map to
  goopg's kind strings, `'e'` is added when the object has expression targets.
  The `catalog.StatisticsObject` registry shadows the heap as the query path;
  the heap is the standby copy and the reload source.
- **`pg_statistic_ext_data` (OID 3429)** — the relation is *declared* by initdb
  (`internal/initdb/initdb.go`, with its `stxoid_inh` index 3433 and the FK to
  3381 in `pg_catalog_fk_data.go`) but **nothing ever writes a row to it**: a
  grep for `3429` across `internal/executor/` returns only the FK metadata
  entry. `pg_statistic_ext_data` is permanently empty.

**CREATE STATISTICS end to end:**

| stage | state |
|---|---|
| parsed | ✔ (kinds, simple columns, expression targets, `stxstattarget`) |
| validated | partially — name/schema/table resolution and kind-string mapping; no check that the columns exist as a multivariate-compatible set (UNVERIFIED whether column existence is checked at DDL time) |
| catalogued | ✔ `InMemory.RegisterStatisticsFull` + `pg_statistic_ext` heap row |
| WAL-durable / replayable | ✔ (heap insert/delete; `RegisterStatisticsDuringRecovery`) |
| deparsed for `pg_dump` | ✔ `pg_get_statisticsobjdef` |
| **built by ANALYZE** | **✗** — `analyzeRelationWith` never looks at `statisticsObjs`; nothing computes `'d'`, `'f'` or `'m'` payloads |
| **consumed by the planner** | **✗** — `grep -rn "StatisticsObject" internal/optimizer/` returns **0** |

---

## 4. Variable resolution — goopg's `examine_variable`

### 4.1 `getVariableNumDistinct`

Transcribed in §0.1. Faithful to `get_variable_numdistinct`'s branch order,
including the "small relation → assume unique" arm and `isdefault` being true
only when 200 is actually returned.

**Missing branches** versus doc 03 checklist 66–69:

| PG arm | goopg |
|---|---|
| `BOOLOID → 2` | ✔ — `examineJoinVar` sets `isBool` from `cr.Type.Name == "bool"`, and it is recorded even for an operand that did not resolve to a relation |
| VALUES RTE → unique | ✗ |
| `ctid` → unique, `tableoid` → 1 | ✗ |
| `isunique → -(1 - nullfrac)` | ✗ — no `isunique` field (§1.5) |
| negative `stadistinct` scales by `rel->tuples`, not `rel->rows` | ✔ |

### 4.2 `examineJoinVar` (search arm) and `resolveJoinVarColumn`

```
examineJoinVar(key Expr, relids RelSet) -> joinVarStats{stats, tuples, isBool}:
    i, cr, ok = resolveJoinVarColumn(key, relids)
    if cr != nil: isBool = (cr.Type.Name == "bool")
    if !ok: return                       # zero value == "unresolved", a legitimate answer
    tuples = relInfos[i].baseRows        # RAW pre-filter rows
    stats  = columnStatsByName(relInfos[i].table, cr.Name)   # BY NAME, not by Index

resolveJoinVarColumn(key, relids):
    key must be *ColumnRef
    relids must be a power of two (exactly one base relation)
    relInfos[i].table != nil  and  cr.Name != ""
```

Resolution is **by column name**, deliberately: search clauses live in the
pre-search concatenation coordinate space, so `ColumnRef.Index` is a global
offset and indexing `Stats.Columns` with it would read another relation's
statistics whenever the relation is not first in the FROM list.

Not modelled (doc 03 §4, checklist 61–65): expression statistics from
expression indexes or `'e'` extended stats; subquery/CTE target-list descent
(`examine_simple_variable`'s RTE_SUBQUERY arm — goopg's leaf is an
already-planned opaque subtree, ledgered); the security-barrier /
`statistic_proc_security_check` gate; a subquery `DISTINCT`/`GROUP BY`
uniqueness inference — except in `estimate_num_groups`, where
`groupUniqueNDistinct` (`joinkeyproof.go`) *does* implement exactly PG's
"propagate uniqueness, refuse to propagate statistics" rule for a
single-key grouped node.

### 4.3 `get_variable_range` / `get_actual_variable_range`

**Both absent.** goopg never probes an index to refresh a histogram endpoint,
and there is no `get_variable_range` — the histogram's first and last stored
bounds are used as-is with no MCV widening and no `Σmcv + nullfrac > 0.99999`
MCV-only fallback. Consequence: on a monotonically growing key (an `id`
sequence, `o_orderdate`) a predicate above the last stored bound reads
`histogramOpSelectivity` → `idx == -1` → `1.0` for `<`, i.e. "all rows", where
PG's index probe would find the true current maximum.

---

## 5. Restriction selectivity (`internal/optimizer/selectivity.go`, `cardinality.go`)

### 5.1 Constants

| PG (`selfuncs.h`) | value | goopg | value | where |
|---|---|---|---|---|
| `DEFAULT_EQ_SEL` | 0.005 | `defaultEqSelectivity` | 0.005 | `cardinality.go` |
| `DEFAULT_INEQ_SEL` | 0.3333333333333333 | `defaultIneqSelectivity` | `1.0/3.0` | `cardinality.go` |
| — | — | `defaultGenericSelectivity` | `1.0/3.0` | `cardinality.go` — goopg-only "unrecognised clause" constant on the restriction path |
| `clause_selectivity_ext`'s unhandled default | 0.5 | `defaultUnhandledClauseSel` | 0.5 | `joinselectivity.go` — used on the **join** path only |
| `DEFAULT_NUM_DISTINCT` | 200 | `defaultNumDistinct` | 200.0 | `joinselectivity.go` |
| `DEFAULT_RANGE_INEQ_SEL` | 0.005 | **absent** | — | no range-pair combination exists (§6) |
| `DEFAULT_MATCHING_SEL` (pattern) | 0.01 | **absent** | — | LIKE has no selectivity arm at all |
| `DEFAULT_NOT_UNK_SEL` / `DEFAULT_UNK_SEL` | 0.995 / 0.005 | **absent** | — | no `IS NULL` arm (§5.4) |
| `DEFAULT_INEQ_JOIN_SEL` | 0.3333… | `defaultIneqJoinSel` | 0.3333333333333333 | `joinselectivity.go` |

Note the goopg-only split: the restriction path's unhandled-clause fallback is
**1/3**, the join path's is **0.5**. `joinselectivity.go`'s comment says this is
deliberate — upstream also has two different constants — but goopg's restriction
constant is `defaultGenericSelectivity = 1/3` where upstream's
`clause_selectivity_ext` initialiser is 0.5. Every unhandled restriction clause
in goopg is therefore priced at 1/3 against PG's 0.5.

### 5.2 Equality — `eqOpSelectivity` / `eqSelectivityForColumn`

```
eqOpSelectivity(left, right, child):
    (col, val) = normalizeColumnConst(left, right)     # col op const OR const op col
    if not matched: return DEFAULT_EQ_SEL
    return eqSelectivityForColumn(columnStatsForChild(col.Index, child), val)

eqSelectivityForColumn(stats, val):
    literal, ok = formatExprConstant(val)              # int / string / numeric / bool('t'/'f') / typed-string
    if !ok or stats == nil: return DEFAULT_EQ_SEL
    for mcv in stats.MCV:
        if mcv.Value == literal: return mcv.Frequency  # MCV HIT — byte-equal TEXT compare
    mcvMass          = Σ mcv.Frequency
    remainingDistinct = stats.NDistinct - len(stats.MCV)
    if remainingDistinct <= 0: return DEFAULT_EQ_SEL
    mass = 1 - mcvMass - stats.NullFrac
    if mass <= 0: return DEFAULT_EQ_SEL
    return mass / remainingDistinct
```

Faithful to `var_eq_const`'s MCV-hit and MCV-miss arms. **Missing:**

- the unique-var arm (`1/tuples`) — no `isunique`;
- **the cap at the least-common MCV frequency** (`if (selec > mcv_freq_min)
  selec = mcv_freq_min`). On a skewed column with a short MCV list, goopg's
  miss estimate can exceed the smallest MCV's frequency, which is impossible;
- `var_eq_non_const` entirely — `isConstExpr` excludes `*ParamRef`, so a bound
  parameter takes the `DEFAULT_EQ_SEL` door rather than `(1-nullfrac)/nd`;
- the "no stats → `1/nd`" arm — goopg goes straight to 0.005 when `stats == nil`,
  never consulting a relation-size-derived `nd`.

`OpNe` → `1 - eq`. PG's `neqsel = 1 - eqsel - nullfrac` — goopg omits the
`nullfrac` subtraction.

`formatExprConstant` is the byte-equality contract: the planner renders the
literal the same way ANALYZE rendered the MCV/histogram entry. It covers
`IntegerConst`, `StringConst`, `NumericConst`, `BooleanConst` (`"t"`/`"f"`),
`TypedStringLit`. Any other constant shape (a cast, a folded expression that did
not collapse to one of these, a `ParamRef`) misses.

### 5.3 Range — `rangeOpSelectivity` → `histogramOpSelectivity` → `bucketFraction`

```
rangeOpSelectivity(op, left, right, child):
    (col, val, swapped) = normalizeColumnConstRange(...);  if !ok: return DEFAULT_INEQ_SEL
    if swapped: op = swapInequalityOp(op)
    stats = columnStatsForChild(col.Index, child)
    if stats == nil or len(stats.Histogram) < 2: return DEFAULT_INEQ_SEL
    literal, ok = formatExprConstant(val);  if !ok: return DEFAULT_INEQ_SEL
    mcvMass, mcvHits = 0, 0
    for mcv in stats.MCV:
        mcvMass += mcv.Frequency
        if rangeOpMatches(op, histCmp(mcv.Value, literal, col.Type.Name)): mcvHits += mcv.Frequency
    nonMCVMass = max(1 - mcvMass - stats.NullFrac, 0)
    return clamp01( mcvHits + histogramOpSelectivity(op, stats.Histogram, literal, type) * nonMCVMass )

histogramOpSelectivity(op, bounds, literal, type):
    k = len(bounds) - 1                                     # bucket count
    idx = first i with histCmp(bounds[i], literal) >= 0     # LINEAR scan, not binary search
    case Lt, Le:
        if idx <= 0:
            if idx == 0 and op == Le and bounds[0] == literal: return 1/k
            return 0
        if idx == -1: return 1                              # literal above every bound
        return (idx-1)/k + bucketFraction(bounds[idx-1], bounds[idx], literal)/k
    case Gt, Ge:
        return 1 - histogramOpSelectivity(Le if Gt else Lt, ...)

bucketFraction(lo, hi, lit, type):
    if any of numericValue(lo|hi|lit) fails, or hi <= lo: return 0.5     # <<< flat half
    if lit <= lo: return 0
    if lit >= hi: return 1
    return (lit - lo) / (hi - lo)
```

Structurally `scalarineqsel` — `mcv_part + (1 - nullfrac - Σmcv) × hist_part` —
which is the right shape. Divergences:

1. **`bucketFraction` returns a flat 0.5 for every non-numeric type.**
   `numericValue` recognises only `int/int2/int4/int8/smallint/integer/bigint/
   numeric/decimal/float/real/double precision`. **`date`, `timestamp`, `text`,
   `varchar`, `char`, `bpchar`, `bool` all take the 0.5 door.** There is no
   `convert_to_scalar` / `convert_string_to_scalar` / `convert_timevalue_to_scalar`.
   For TPC-H — whose selective predicates are overwhelmingly on `l_shipdate`,
   `o_orderdate`, `l_shipmode`, `p_type` — this means **every date and text range
   predicate is quantised to whole buckets ± half a bucket**.
2. **No histogram-selectivity clamp.** PG clamps to
   `[0.01/(nbounds-1), 1 - 0.01/(nbounds-1)]` (checklist 77). goopg can return
   exactly 0 or exactly 1.
3. **Linear scan** for the bin, where PG binary-searches — O(nbounds) per clause
   per estimate, with `nbounds` up to `statsTarget+1 = 101`.
4. **No `ctid` block-arithmetic arm** (checklist 78).
5. `histCmp` falls back to `strings.Compare` for non-numeric types. For ISO
   dates and C-collation text this is the correct order; for any other collation
   it is not.

### 5.4 NULL and boolean tests

**`IS NULL` / `IS NOT NULL`: no arm at all.** `optimizer.IsNullExpr` exists in
the plan IR (`plan.go:283`) but `clauseSelectivity`'s type switch has no case
for it, so both spellings fall through to `defaultGenericSelectivity = 1/3`.
PG's `nulltestsel` returns `stanullfrac` / `1 - stanullfrac` (defaults
0.005/0.995). goopg **collects `NullFrac` and never uses it for a null test** —
`NullFrac` reaches the estimator only as a subtrahend inside eq/range/join
formulas. A `WHERE x IS NOT NULL` on a NOT NULL column is priced at 1/3
instead of 1.0.

**Boolean:** a bare `*BooleanConst` is handled exactly (`true → 1.0`,
`false → 0.0`). A bare boolean *column* as a predicate (`WHERE flag`) is not a
`BinaryOp`/`UnaryOp`/`InExpr`/`BooleanConst`, so it takes
`defaultGenericSelectivity`. There is no `booltestsel` (`IS TRUE` / `IS NOT
FALSE` / `IS UNKNOWN`) and no `boolvarsel`.

### 5.5 Pattern operators

**No selectivity arm.** `clauseSelectivity` has no `LIKE`/`ILIKE`/regex case, so
every pattern predicate is `defaultGenericSelectivity = 1/3`. PG's
`patternsel_common` (exact-prefix → `var_eq_const`; otherwise a
histogram-fraction estimate weighted by `hist_size/100`, clamped to
`[0.0001, 0.9999]`, then MCV-adjusted) and its heuristic constants (fixed char
0.20, `_` 0.9, `%` 5.0, char range 0.25) have no counterpart.

What goopg *does* have is the **access-path** half: `likeprefix.go`
(`ExtractLikePrefix`, `IncrementString` — a faithful `make_greater_string` for
C-collation bytes) and `injectLikeRangePredicates`, which rewrite
`col LIKE 'abc%'` into `col >= 'abc' AND col < 'abd'` so an index can be used.
Once rewritten, the injected range predicates ARE priced by
`rangeOpSelectivity` — but on a text column, so `bucketFraction` returns 0.5
(§5.3) and the estimate is bucket-granular. A pattern that yields no prefix
(`'%abc'`) is neither rewritten nor priced.

### 5.6 `scalararraysel` / IN-lists

goopg handles only the literal-list form:

```
case *InExpr:
    if e.Plan != nil or len(e.List) == 0: return defaultGenericSelectivity
    cr, ok = e.Operand.(*ColumnRef);  if !ok: return defaultGenericSelectivity
    stats = columnStatsForChild(cr.Index, child)
    sel = Σ_v eqSelectivityForColumn(stats, v)          # disjoint-sum assumption
    if sel > 1: sel = 1
    return e.Negated ? 1 - sel : sel
```

Matches PG's disjoint-sum arm (checklist 84) and the `<> ALL` complement in
shape. **Missing:** the OR-combine fallback when the sum leaves `[0,1]` (goopg
just clamps at 1); the non-constant-array `estimate_array_length` = 10 default;
`= ANY (subquery)` (`e.Plan != nil` → 1/3, where PG converts it to a semi-join
and prices it with `eqjoinsel_semi`).

### 5.7 `rowcomparesel`

**Absent.** No row-comparison node reaches an estimator; a `ROW(a,b) < ROW(x,y)`
predicate takes `defaultGenericSelectivity`.

---

## 6. `clauselist_selectivity` — the function does not exist

There is no `clauselist_selectivity` in goopg. The AND product is **inlined**
into `clauseSelectivity`'s `parser.OpAnd` arm:

```
case parser.OpAnd:  return clauseSelectivity(Left, child) * clauseSelectivity(Right, child)
case parser.OpOr:   a, b := ...;  return a + b - a*b
case NOT:           return 1 - clauseSelectivity(Operand, child)
```

Everything doc 03 §6 describes as living *around* that product is therefore
absent:

| PG mechanism | goopg |
|---|---|
| range-pair detection (`addRangeClause`, `RangeQueryClause`) | **absent** |
| `hi + lo - 1 + P(x IS NULL)` combination | **absent** — the two bounds multiply |
| `DEFAULT_RANGE_INEQ_SEL` when a bound is default or the combination `< -0.01` | **absent** |
| duplicate-bound "keep the smaller selectivity" | **absent** |
| extended-statistics consultation before independence | **absent** (§9) |
| `treat_as_join_clause` / `varRelid` dispatch | **absent** — the restriction and join estimators are separate functions with separate entry points |
| `RestrictInfo.norm_selec` / `outer_selec` caching | **absent** — `restrictInfo` (`joinrestrict.go`) caches no selectivity; every call recomputes |
| pseudoconstant non-`Const` → 1.0 | **absent** |

**The `reliable` flag.** goopg adds a provenance bit PG does not have.
`selectivityEstimate{value, reliable}` and `clauseSelectivityWithSource` mirror
`clauseSelectivity` arm for arm, with:

- AND / OR: `reliable = a.reliable && b.reliable`
- NOT: inherits the operand's flag
- `BooleanConst`: `reliable = true` (exact)
- every fallback constant (`defaultEqSelectivity`, `defaultIneqSelectivity`,
  `defaultGenericSelectivity`): `reliable = false`

The consumer, `cardinality.go:applyLocalFilterSelectivity`:

```
applyLocalFilterSelectivity(baseRows, binding, scan, local):
    if local == nil or scan == nil or baseRows <= 0: return baseRows
    sel = clauseSelectivityWithSource(localizeExprToLeaf(local, binding), scan)
    if !sel.reliable: return baseRows            # <<< keep the PRE-filter count
    rows = scaleByFloat(baseRows, sel.value)
    return max(rows, 1)
```

This is **ledger row 871**, and the ledger's own framing is the right one:

> goopg's `reliable` gate ITSELF deviates from `set_baserel_size_estimates`:
> upstream ALWAYS multiplies by selectivity (`DEFAULT_*_SEL` constants without
> stats); goopg keeps the PRE-filter count when unreliable — up to 200×
> divergence, direction makes build sides look too expensive.

Effect on base-rel sizing: a WHERE clause the estimator cannot price at all
leaves the relation at its full row count, so it looks like an expensive hash
build side and an expensive nested-loop outer. On a query whose only predicates
are `IS NULL`, `LIKE`, or an unresolvable column (an index-probed leaf — §1.5),
**no filtering is applied to the size estimate whatsoever**.

The gate is applied identically by `relsize.go:applyRelSizeFallback` — the
factoring exists specifically so the two callers cannot drift.

---

## 7. Join selectivity

Two estimators run in production simultaneously — `joinkeyproof.go:11-35`
documents this as a live hazard ("the sibling-paths rule"):

- **search arm** — `joinrelsize.go:calcJoinrelSize` over `RelOptInfo` /
  `restrictInfo`, driven by `sizeJoinRel` inside `makeJoinRel`;
- **legacy arm** — `cardinality.go:estimateJoin` over `*Join` plan nodes,
  which is what `EstimateRows` (and therefore EXPLAIN's `rows=`) reports.

### 7.1 / 7.2 `eqjoinsel` — the no-MCV branch only

```
eqJoinSelectivityExt(v1, v2) -> (sel, isdefault):
    nd1, d1 = getVariableNumDistinct(v1)
    nd2, d2 = getVariableNumDistinct(v2)
    nf1 = v1.stats.NullFrac (or 0);  nf2 likewise
    selec = (1 - nf1) * (1 - nf2)
    if nd1 > nd2:  selec /= nd1;  isdefault = d1
    else:          selec /= nd2;  isdefault = d2
    return clampSelectivity(selec), isdefault
```

Exactly `eqjoinsel_inner`'s else-branch (checklist 98). The `isdefault` carried
out is goopg's own addition — it reports the flag of the side whose ndistinct
*actually landed in the denominator*, which is what `calcJoinrelSize`'s fallback
cap needs.

**No MCV pairing on the inner-join path.** `eqjoinsel`'s MCV arm
(`matchprodfreq`, `totalsel1`/`totalsel2`, `min` — doc 03 §7.2, checklist 97) is
not implemented for inner joins.

**But there IS MCV pairing in production** — in the **semi/anti** arm only.
`cardinality.go:semiPairMatchFraction` is the production function; it is what
`internal/optimizer/mcv_pairing_bench_test.go` (`BenchmarkMCVPairing`,
`TestMCVPairingMatchesNested` — the header says "`pairMCVsIndexed` mirrors the
shipped pairing in `semiPairMatchFraction`") and
`internal/optimizer/cardinality_semimcv_test.go` (13 tests) exercise. Its
pairing loop is `eqjoinsel_semi`'s, indexed rather than nested (review/260831
OP1-2):

```
semiPairMatchFraction(j, p, innerRows):
    nd1 = keyNDistinct(p.Left, j.Left);      st1 = keyColumnStats(p.Left, j.Left)
    nd2 = rightExprNDistinct(j, p.Right);    st2 = rightExprStats(j, p.Right)
    nullfrac1 = st1.NullFrac or 0
    if nd2 unknown: nd2 = 200
    if innerRows > 0 and nd2 >= innerRows: nd2 = innerRows; nd2Known = true   # PG's asymmetric clamp
    if st1 and st2 and both have MCVs:
        clamped2 = min(len(st2.MCV), nd2)                  # frequency-ordered prefix
        pair each st1 MCV to at most one unused st2 MCV by TEXT equality
        matchFreq1 = Σ frequencies of matched st1 entries;  nmatches = count
        uncertainFrac = 0.5
        if nd1Known and nd2Known:
            rem1, rem2 = nd1-nmatches, nd2-nmatches
            uncertainFrac = 1.0 if (rem1 <= rem2 or rem2 < 0) else rem2/rem1
        uncertain = clamp01(1 - matchFreq1 - nullfrac1)
        return matchFreq1 + uncertainFrac * uncertain
    if !nd1Known or !nd2Known: return 0.5 * (1 - nullfrac1)
    if nd1 <= nd2:             return 1.0 * (1 - nullfrac1)
    return (nd2/nd1) * (1 - nullfrac1)
```

Two recorded divergences: MCV equality is TEXT equality over the stored
renderings rather than a call through the operator's `oprcode`; and only the
inner INPUT's rows clamp `nd2`, not also `vardata2->rel->rows`.

`semiJoinMatchFraction(j, innerRows)` folds `semiPairMatchFraction` over **every**
equi-pair (`joinEquiPairs`).

### 7.3 Join-clause operator dispatch

```
joinClauseSelectivityExt(ri) -> (sel, isdefault):
    if ri == nil or ri.clause == nil:        return 0.5, true
    bo, ok = ri.clause.(*BinaryOp); if !ok:  return 0.5, true
    switch bo.Op:
      OpEq:                    return eqJoinSelectivityExt(operands(ri, bo))
      OpNe:                    s, d := eqJoinSelectivityExt(...);  return clamp(1-s), d
      OpLt, OpLe, OpGt, OpGe:  return DEFAULT_INEQ_SEL(0.3333…), true
      default:                 return 0.5, true

joinClauseOperands(ri, bo):
    if ri.isEquijoin: return examineJoinVar(ri.leftKey, ri.leftRelids), examineJoinVar(ri.rightKey, ri.rightRelids)
    return examineJoinVar(bo.Left, 0), examineJoinVar(bo.Right, 0)     # both unresolved → 1/200 = DEFAULT_EQ_SEL
```

`neqjoinsel`'s SEMI/ANTI arm (`1 - nullfrac(outer)`, checklist 101) is
unreachable — the seam pins special joins outside the search
(`joinselectivity.go:320-322` says so explicitly).

`clampSelectivity` is `CLAMP_PROBABILITY` plus a goopg-only NaN arm: a NaN maps
to `defaultUnhandledClauseSel = 0.5`, because a NaN selectivity would compare
false against every cost and silently disable `add_path`'s pruning.

### 7.4 `outerJoinRowFloor`

```
outerJoinRowFloor(j, rows, l, r):
    JoinTypeLeft:   if rows < l: return l
    JoinTypeRight:  if rows < r: return r          # goopg keeps RIGHT as its own type
    JoinTypeFull:   rows = max(rows, l, r)
    return rows
```

**Legacy arm only.** `calcJoinrelSize` has no outer-join clamp because the
search sees only INNER joins (outer joins are peeled by `splitOuterSpine`
before the search runs). The SEMI (`o·fk·j`) and ANTI (`o·(1 - fk·j)·p`) arms of
`calc_joinrel_size_estimate` (checklist 124) live only in `estimateJoin`.

### 7.5 `mergejoinscansel`

**Absent.** The only mention in the tree is a comment in
`joinpathsmergeouter.go:73` explaining that landing merge-join costs *without*
`mergejoinscansel`'s duplicate estimate "would move plans on a guess".
`mergeJoinCost` (`cost_funcs.go`) is the simplified form — no rescan modelling,
no start/end scan fractions.

### 7.6 `estimate_hash_bucket_stats`

**Absent.** No `bucketsize`, no MCV-driven skew factor; `hashJoinCost` uses
`hashJoinInputs` + `spillPages` with no per-bucket distribution model. Note the
separately-recorded hazard: goopg's hash join keys on ONE equi-pair, so a
constant-pinned key column degenerates to a single bucket — an execution-side
problem the cost model cannot see because there is no bucket-size statistic.

### 7.7 Multi-pair equality pricing — ledger rows 779 / 781 / 784 are STALE

Both arms now price **every** pair.

**Search arm** (`calcJoinrelSize`):

```
est = superkeyJoinSelectivity(cat, outer, inner, oneClausePerEquivClass(clauses))
sel = est.sel
allDefault = len(est.residual) > 0
for ri in est.residual:                        # every clause the keys did NOT consume
    s, isdefault = joinClauseSelectivityExt(ri)
    sel *= s
    if !isdefault: allDefault = false
rows = outer.Rows * inner.Rows * sel
if rows > est.rowsBound: rows = est.rowsBound                     # key-implied clamp
if !est.fired and allDefault:
    rows = min(rows, max(outer.Rows, inner.Rows))                 # M0126-0010 guess cap
return clampRowEst(rows), outer.Width + inner.Width
```

`oneClausePerEquivClass` is the equivalence-class reduction: an EC with n
members contributes ONE clause per (outer, inner) split, so a transitive
closure cannot charge the same restriction twice. That is goopg's counterpart
to PG's `nconst_ec` / EC-driven clause selection — it exists for the join
sizer, but there is **no `nconst_ec` divisor in the FK arm** (§10).

**Legacy arm** (`estimateJoin`, hash/merge case):

```
pairs = joinEquiPairs(j)                       # HashKeys, else splitAllEqualitiesForHash(Predicate), else (LeftKey,RightKey)
sk    = superkeyJoinEstimate(j, pairs)
sel, measured = sk.sel, sk.fired
for i, p in pairs:
    if sk.covered[i]: continue                 # priced by the key instead
    nd = pairNDistinct(j, p)                   # max(keyNDistinct(left), rightExprNDistinct(right))
    if nd > 0: sel /= nd; measured = true
    else:      sel *= DEFAULT_EQ_SEL
if measured:
    rows = l * r * sel * joinResidualSelectivity(j)
    if rows > sk.rowsBound: rows = sk.rowsBound
    return saturate(outerJoinRowFloor(j, rows, l, r))
# unmeasurable fallback:
est = clamp(l*r*DEFAULT_EQ_SEL, 1, max(l,r))
return saturate(outerJoinRowFloor(j, est, l, r))
```

The M0127-P5.6-f comment on `joinResidualSelectivity` records exactly the Q9
defect the ledger rows describe and states it is fixed: *"It used to be
`HashKeys` with a single-pair fallback while `estimateJoin` priced exactly one
pair, so a two-pair join like Q9's `l_suppkey = ps_suppkey AND l_partkey =
ps_partkey` had its second pair excluded here AND unpriced there — the clause
vanished from the estimate entirely."*

`rightExprNDistinct` also fixed the coordinate bug ledger row 776 describes: a
right-side operand's `ColumnRef.Index` counts from the start of the merged
left‖right schema and is shifted down by `len(j.Left.Output())` before the
lookup.

Remaining structural weakness (not a ledger row): both arms divide the product
by one `nd` **per clause**, so two correlated equalities between the same pair
of relations (composite FK) still multiply their selectivities — PG does the
same for non-FK clauses, so this is faithful, but PG's FK arm short-circuits it
and goopg's superkey arm only fires when a *full* key is covered.

---

## 8. Group / distinct estimation

### 8.1 `estimateNumGroups`

`cardinality.go:estimateNumGroups(groupExprs, child, inputRows)`:

```
rows = max(inputRows, 1)
if len(groupExprs) == 0: return 1

varinfos = []
for i, ge in groupExprs:
    vars, enumerated = groupVarsOfExpr(ge)                    # pull_var_clause
    if len(vars) == 0 and !enumerated:                        # walk gave up
        varinfos += {ndistinct: 200}; key = position          # DEFAULT_NUM_DISTINCT
        continue
    for cr in vars:
        key, info = examineGroupVar(cr, child)
        if seen[key]: continue                                # add_unique_group_var dedup
        varinfos += info
if len(varinfos) == 0: return 1                               # all-constant GROUP BY

numdistinct = 1
group varinfos by their base relation (vi.rel); rel-less ones multiply directly
for each rel:
    reldistinct = Π vi.ndistinct;  relmax = max vi.ndistinct
    tuples = vis[0].rawRows;  if tuples <= 0: continue        # "don't divide by zero"
    clamp = tuples
    if len(vis) > 1:
        clamp *= 0.1
        if clamp < relmax: clamp = min(relmax, tuples)
    reldistinct = min(reldistinct, clamp)
    if filtered, ok := relFilteredRows(child, rel); ok && reldistinct > 0 && filtered < tuples:
        reldistinct *= 1 - ((tuples - filtered)/tuples)^(tuples/reldistinct)     # Yao/Dell'Era
    numdistinct *= clampRowEstF(reldistinct)
return saturate(min(ceil(numdistinct), rows))
```

This is a close transcription of `estimate_num_groups` including the
per-relation product, the `0.1 × tuples` clamp with the `relmax` floor
(checklist 107), the restriction-scaling exponent (checklist 108), and the
`ceil` + input clamp (checklist 109).

`groupVarInfo` is fed by `examineGroupVar`:

```
examineGroupVar(cr, child):
    if ref, ok := resolveBaseColumn(cr.Index, child):
        return {rel: ref.scan, ndistinct: groupVarNDistinct(ref.ndistinct, ref.rawRows), rawRows: ref.rawRows}
    if nd, ok := groupUniqueNDistinct(cr.Index, child); ok && nd > 0:
        return {ndistinct: nd}          # no rel ⇒ no per-relation clamp
    return {ndistinct: 200}
```

`groupUniqueNDistinct` is `examine_simple_variable`'s GROUP-BY/DISTINCT arm
followed by `get_variable_numdistinct`'s `isunique` branch — the only place in
goopg where uniqueness reaches a distinct estimate.

**Missing** versus doc 03 §8.1: the boolean-expression ×2 factor
(checklist 105); the volatile-var-free `return input_rows` arm (goopg has no
volatility catalog in the planner — ledgered as
`estimate-num-groups volatile-groupexpr`); `estimate_multivariate_ndistinct`
(§9); and the cross-relation "equivalent vars collapse to the smaller
ndistinct" rule (checklist 111) — goopg groups strictly by the resolved base
relation.

### 8.2 DISTINCT

**Not sized.** `cardinality.go:76-83` — `EstimateRows` over a DISTINCT node does
not call `estimateNumGroups`. PG uses the same function for both.

### 8.3 LIMIT / tuple fraction

`tuplefraction.go:preprocessLimit` is a full transcription of
`preprocess_limit`, including the `limitEstimates{count, offset}` tri-state
encoding (0 = absent, >0 = estimate, −1 = present-but-unestimatable),
`unestimatableLimitFraction = 0.10` (checklist 112), the `count + offset`
maximum, and upstream's absolute/fractional comparison heuristic
("both-absolute or both-fractional take the smaller; a mix assumes the
absolute one is smaller"). Every production call site passes
`tupleFraction = 0` — there is no `cursor_tuple_fraction` and no
subquery-inherited fraction, but the merge arms are written for it.

`joinsearchseam.go:searchTupleFraction` feeds the search; `RelOptInfo.ConsiderStartup`
is set from `tupleFraction > 0`; `getCheapestFractionalPath` selects.

### 8.4 Aggregate / HAVING selectivity

`estimateAggregate` (`cardinality.go`) consumes `estimateNumGroups`. There is no
HAVING-clause selectivity model — a HAVING qual is not priced separately (PG
also does not have a dedicated one, applying `clauselist_selectivity` over the
HAVING quals against the grouped rel; goopg's grouped node has no restriction
list, so the effect is that HAVING never reduces the estimate).

### 8.5 CTE output statistics — ledger 944

CTE and subquery leaves carry **no column statistics** — `resolveBaseColumn`'s
`CTEScan` arm walks into the body, so a CTE over a base table can resolve, but
a CTE whose body is an aggregate or set-op cannot. `joinsearch.go:initialRelRows`
carries the stopgap:

```
default:                                        # subquery / CTE / VALUES / function / set-op
    rows = EstimateRows(leaf)
    if rows <= 1:
        if cte, ok := leafBaseScan(leaf).(*CTEScan); ok && cte.Child != nil:
            if bodyRows := EstimateRows(cte.Child); bodyRows > 1: rows = bodyRows
```

The comment records the failure it patches: TPC-DS `year_total` with 4
conjuncts over columns with 2 distinct values each priced at
`0.005⁴ × 17977 ≈ 0.000011` → 1 row, which made nested loops look free (Q74,
99 s → 14 s). The fallback over-estimates filtered CTE scans relative to PG's
materialised-CTE statistics; the ledger keeps it as a stopgap.

---

## 9. Extended statistics

**Nothing exists on the planner side.**

| PG | goopg |
|---|---|
| `pg_statistic_ext` catalog | ✔ real per-DB heap rows (§3.3) |
| `pg_statistic_ext_data` catalog | declared, permanently empty |
| `BuildRelationExtStatistics` from the ANALYZE sample | **✗** |
| `statext_ndistinct_build` / `dependencies` / `statext_mcv_build` | **✗** |
| `statext_is_compatible_clause` | **✗** |
| `statext_clauselist_selectivity` | **✗** |
| `choose_best_statistics` | **✗** |
| `mcv_combine_selectivities` | **✗** |
| `dependency_degree` / `clauselist_apply_dependencies` | **✗** |
| `estimate_multivariate_ndistinct` | **✗** |
| `'e'` expression statistics in `examine_variable` | **✗** |

`grep -rn "StatisticsObject" internal/optimizer/` returns **0**. `CREATE
STATISTICS` is a pure DDL/catalog/`pg_dump` feature in goopg: it parses,
catalogues, WAL-journals, replays, deparses, and is completely invisible to the
planner.

Ledger row 869 names the consequence for `costMemoizeRescan`: "`estimate_num_groups`
replaced by ndistinct product clamped to calls — no extended-statistics arm,
correlated composite keys over-estimated → cache under-sold".

---

## 10. Foreign-key / superkey selectivity

goopg's counterpart of `get_foreign_key_join_selectivity` is
`joinrelsize.go:superkeyJoinSelectivity`, mirrored arm-for-arm for the legacy
estimator in `joinkeyproof.go` (`superkeyJoinEstimate`).

```
superkeyJoinSelectivity(cat, outer, inner, clauses) -> {sel, residual, fired, rowsBound}:
    est = {sel: 1.0, residual: clauses, rowsBound: +Inf}
    if cat == nil or len(clauses) == 0: return est
    pairs[i] = joinKeyPairOf(clauses[i], outer.Relids, inner.Relids)   # (rel, col) x2, opposite sides
    if no usable pair: return est
    loop:
        best, ok = bestProvableKey(cat, pairs, removed)                # largest rawTuples first
        if !ok: break
        mark best.clauses removed
        est.sel *= 1.0 / best.rawTuples                                # RAW, unfiltered tuple count
        est.fired = true
        if b, ok := keyImpliedRowsBound(outer, inner, best.keyRel); ok && b < est.rowsBound:
            est.rowsBound = b
    est.residual = clauses not removed
    est.sel = clampSelectivity(est.sel)
```

**How it generalises PG.** PG's arm only matches *declared* foreign keys
(`root->fkey_list`, built by `match_foreign_keys_to_quals`). goopg's
`bestProvableKey` accepts any **UNIQUE index — including a composite one — as
evidence**, since a full cover of a unique key gives the same "at most one
partner" counting argument a declared FK does. `fkParentRel` handles the
declared-FK direction: for a declared FK the unique side is the **parent**, so
`keyRel` is the parent and the row bound is the *referencing* side's rows.

Three upstream properties reproduced deliberately (from the function header):

1. **The divisor is the RAW, unfiltered tuple count** — `1/rawTuples`, not
   `1/filteredRows`, because when the key side is filtered `|L|·|R_filt|/R_raw`
   is a real match fraction.
2. **The whole key must be covered** — PG's "chicken out" branch. A partially
   equated composite key proves nothing; extra equated columns beyond the key
   stay in the residual and are charged by `eqjoinsel` on top.
3. **A clause is consumed once** — removals happen before the next key is
   considered, so two overlapping keys cannot both charge.

Largest-divisor-first is goopg's tie-break, on the same "minimum of upper
bounds" logic as `eqjoinsel`'s `max(nd1, nd2)`.

`keyImpliedRowsBound` is goopg's own addition (04 §3.3): the structural clamp
`rows <= (the non-key side's Rows)`, valid **only** when `keyRel` is that one
base relation — if the key side sits inside a multi-relation side, a join below
may already have duplicated its rows and the counting argument gives nothing.
It is allowed to touch a *measured* estimate, unlike the `max(l,r)` guess cap.

**Absent versus PG:**

| PG | goopg |
|---|---|
| `1/max(ref_tuples, 1)` per FK | ✔ (`1/rawTuples`) |
| SEMI/ANTI variant `ref_rows/ref_tuples` | **✗** — the search sees only INNER; the legacy mirror has no semi arm either |
| **divide by the selectivity of any constant EC clause on the FK column** (`nconst_ec`) | **✗** — goopg has no counterpart; an FK join whose parent side is filtered by an equality on the key column is over-estimated by the reciprocal of that filter's selectivity |
| **nullfrac derate on the referencing column** | **✗** — PG's FK arm is exact only for NOT NULL FK columns and goopg does not derate either, but goopg also does not fall back to `eqjoinsel` when the column is nullable |
| FK-covered clauses removed before `clauselist_selectivity` | ✔ (`est.residual`) |
| `calc_joinrel_size_estimate`'s INNER/LEFT/FULL/SEMI/ANTI arms | INNER only in the search; LEFT/RIGHT/FULL floors and SEMI/ANTI in `estimateJoin` (§7.4) |

---

## 11. Plan-time invalidation, caching, cross-session and standby visibility

### 11.1 The plan cache

`internal/postmaster/plancache.go` + `dispatch.go`:

```
planCacheKey(sql, connDBOid) = strconv(catalog.NamespaceDBOid(connDBOid)) + "\x00" + normalizeCompatSQL(sql)
```

The key is **(namespace dbOid, normalised SQL text)** and nothing else. It
contains no `search_path`, no GUC state, no statistics generation counter, and
no relation OID set.

Cache eligibility (`dispatch.go:1155`) requires: `s.pc != nil`, exactly one
statement, not NOTIFY / two-phase / `CURRENT OF` DML, and none of
`plannerScanTogglesActive(sess)`, `sessionTempInheritanceActive`,
`partitionDetachPending`, `inheritanceChangePending`.

Invalidation is **all-or-nothing** (`planCache.Invalidate()` clears every
shard) and fires from exactly two places:

- `dispatch.go:3618` / `dispatch_extended.go:619` — after executing a node that
  type-asserts to `*optimizer.DDL`;
- `ectx.OnCommitDDL = s.pc.Invalidate` — `ON COMMIT DROP` at transaction commit.

**`ANALYZE` does not invalidate.** `optimizer.planStmt` maps
`*parser.AnalyzeStmt` (and `VacuumStmt`, `SetStmt`, `ResetStmt`, …) to
`&Utility{}`, not `*optimizer.DDL`. So a plan cached before an ANALYZE keeps
being served with the pre-ANALYZE cardinalities until some unrelated DDL
flushes the whole cache. PG's equivalent (checklist 125) — in-place `pg_class`
update → relcache invalidation → `PlanCacheRelCallback`) — has no counterpart.

**`SET` does not invalidate** either. The four scan toggles are handled by
*bypass* rather than invalidation: `plannerScanTogglesActive` makes such a
session neither read from nor write to the shared cache. Every other GUC
(`work_mem`, the cost GUCs, `max_parallel_workers_per_gather`) either never
reaches the planner or reaches it without any cache interaction.

There is no generic-vs-custom plan model (checklist 126): no five-execution
warm-up, no average-custom-cost comparison, no `plan_cache_mode`.

### 11.2 Statistics visibility

Restating §0.2 in the doc-03 frame:

| scope | visible? |
|---|---|
| same session | immediately |
| other sessions, same database | immediately (shared `*Table` pointer under `InMemory.mu`) |
| other databases | never (per-`dbOid` namespace) |
| after restart | ANALYZE-written stats yes; VACUUM and autoanalyze stats no |
| to a running plan already cached | no (§11.1) |

### 11.3 Standby / external visibility

- `pg_statistic` rows are **real heap tuples** in `base/<dbOid>/2619`, written
  through `writeHeapRowCanonical` and WAL-logged as ordinary heap inserts, so a
  PG 18 standby replays them. Their *content* is not type-faithful
  (`text[]` `stavalues`, `staop1 = 98` for every type, `staop2/3 = 0`,
  `stacoll* = 0`, `stanullfrac`/`stadistinct` as varlena text) — UNVERIFIED
  whether a standby's planner errors on them or silently mis-plans.
- `goopg_relstats` (OID 9410) is **goopg-private**. A PG standby has no such
  relation, so `reltuples`/`relpages` never reach it. The waiver is recorded on
  `catalog.GoopgRelStatsRelationId`: PG carries these in `pg_class`, and goopg's
  `pg_class` is virtual.
- `pg_class` is a **virtual** relation in goopg (built by
  `PGClassRowsForDBOid`), so `relpages`/`reltuples`/`relallvisible` are rendered
  at read time from `Table.Stats` and `RelAllVisibleFunc` — they are not stored
  rows and cannot be replayed.
- `pg_statistic_ext` rows ARE real heap rows and do replay; `pg_statistic_ext_data`
  is empty, so a standby sees statistics objects with no data.
- No `pg_restore_relation_stats` / `pg_restore_attribute_stats` and no
  `pg_dump --statistics` support (checklist 127). UNVERIFIED whether the
  functions exist as stubs.

---

## 12. Worked examples

The `tenk1` cases are doc 03's, re-run through goopg's formulas. Numbers are
derived by hand from the code above; they are not measured on a live cluster.

### 12.1 `tenk1 WHERE unique1 < 1000`

`unique1` is 0..9999, unique, 10 000 rows. `sampleCap = 100 × 300 = 30 000 >
10 000`, so the whole relation is in the reservoir.

- **ndistinct**: every value appears once → `nmultiple = 0` → `ndistinctEstimate`
  returns `N = 10 000`. `NDistinct = 10000`, `NDistinctFrac = 1.0`,
  `StaDistinct() = -1.0`. PG stores `-1`. **Agree.**
- **MCV**: every bucket has count 1. First iteration: `remaining = 9999`,
  `distinctRemaining = 9999`, `avgRemaining = 1.0`, `1 < 1.25 × 1.0` → break.
  `mcvCount = 0`. PG: no MCVs. **Agree.**
- **Histogram**: `bucketCount = min(100, 9999) = 100`, `last = 9999`,
  `bounds[i] = expanded[i*9999/100]` → `{0, 99, 199, 299, …, 999, 1099, …, 9999}`
  (101 bounds).
- **Estimate**: first bound ≥ 1000 is `bounds[11] = 1099`. `op = <`, `idx = 11 > 0`:
  `whole = 10/100 = 0.10`; `bucketFraction(999, 1099, 1000) = 1/100 = 0.01`
  (numeric type → real interpolation); `histSel = 0.10 + 0.01/100 = 0.1001`.
  `mcvMass = 0`, `nonMCVMass = 1` → `sel = 0.1001`.
  **rows = 10 000 × 0.1001 = 1001.** PG: **1007**.

Agreement to 0.6 %. The residual difference is the bound *placement*: PG's
`values[(i·(nvals−1))/(num_hist−1)]` is computed over the sorted non-MCV sample
with delta/posfrac stepping, goopg's over the expanded multiset with integer
division. Both are equi-depth; the bins land one value apart.

### 12.2 `tenk1 WHERE stringu1 = 'CRAAAA'` and `= 'xxx'`

676 distinct values, 10 000 rows, `null_frac = 0`.

- **ndistinct**: every value repeats → `nmultiple == len(freq) == 676` →
  `ndistinctEstimate` returns 676 exactly. `NDistinctFrac = 0.0676 ≤ 0.1` →
  `StaDistinct() = +676`. PG: `n_distinct = 676`. **Agree.**
- **MCV — divergent.** PG 18.3's `analyze_mcv_list` hypergeometric test keeps
  few or no MCVs for this near-uniform column. goopg's 1.25× rule admits every
  value whose count exceeds `1.25 × avg_remaining ≈ 18.5` against a mean of
  ~14.8 — **tens of MCVs**. Taking doc 03's 10-MCV list for comparability
  (`Σ freq = 0.03033333`):
- **`= 'CRAAAA'`**: MCV hit → `sel = 0.003` → **rows = 30**. PG: 30. **Agree.**
- **`= 'xxx'`**: miss. `remainingDistinct = 676 − 10 = 666`;
  `mass = 1 − 0.03033333 − 0 = 0.96966667`;
  `sel = 0.96966667/666 = 0.0014559` → **rows ≈ 15**. PG: ≈ 15. **Agree** —
  but goopg reaches it without PG's cap at the least-common MCV frequency
  (0.003), which does not fire here and would fire on a skewed column.
- **Combined with §12.1** by the inlined AND product:
  `0.1001 × 0.0014559 = 0.00014573` → `10000 × … = 1.46` → **1 row**. PG: 1.
  **Agree.**

### 12.3 `tenk1 WHERE stringu1 < 'IAAAAA'` — the flat-bucket divergence

`stringu1` is type `name`/`text`, so `numericValue` fails and
`bucketFraction` returns **0.5 unconditionally**.

Using doc 03's 11-bound histogram
`{BAAAAA, CQAAAA, FRAAAA, IBAAAA, KRAAAA, NFAAAA, PSAAAA, SGAAAA, UUAAAA, XLAAAA, ZZAAAA}`
(`k = 10`): first bound ≥ `'IAAAAA'` is `IBAAAA` at `idx = 3`.
`histSel = (3−1)/10 + 0.5/10 = 0.20 + 0.05 = 0.25`.
MCV part: the six entries below `'IAAAAA'` sum to `0.01833333`;
`nonMCVMass = 1 − 0.03033333 = 0.96966667`.

`sel = 0.01833333 + 0.25 × 0.96966667 = 0.26075` → **rows ≈ 2608**.
PG: `hist_selec = 0.298387` (from `convert_string_to_scalar` interpolating
inside the `FRAAAA…IBAAAA` bin) → `sel = 0.307669` → **3077**.

**goopg is 15 % low, entirely because `bucketFraction` has no string
interpolation.** With 101 bounds the per-bucket error shrinks to ±0.5 % of the
range, so this divergence matters most at low `default_statistics_target` and
for wide bins.

### 12.4 A worse case — the missing range-pair rule (TPC-H Q6 shape)

`WHERE l_shipdate >= DATE '1994-01-01' AND l_shipdate < DATE '1995-01-01'` over
`lineitem`, whose `l_shipdate` spans ~7 years (1992-01 … 1998-12). True
selectivity ≈ 1/7 = 0.1429.

goopg: `date` is not in `numericValue`'s type list, but the ISO rendering makes
`strings.Compare` the correct order, so bin location works. With 100 equi-depth
buckets over 7 years, `sel(< '1995-01-01') ≈ 3/7 = 0.4286` and
`sel(>= '1994-01-01') = 1 − sel(< '1994-01-01') ≈ 1 − 2/7 = 0.7143`
(each ± half a bucket from the flat `bucketFraction`).

- **goopg** (§6 — the two bounds are independent conjuncts):
  `0.7143 × 0.4286 = 0.3061` → **2.1× over-estimate**.
- **PG** (`clauselist_selectivity`'s `RangeQueryClause` pairing, checklist 89):
  `hi + lo − 1 = 0.4286 + 0.7143 − 1 = 0.1429` → **exact**.

This is the single highest-leverage restriction-side gap for TPC-H/TPC-DS: every
`BETWEEN`, every date-window filter, and every two-sided numeric bound is priced
with the independence assumption on two clauses that are maximally dependent.

### 12.5 Join — `orders ⋈ lineitem ON o_orderkey = l_orderkey` (TPC-H SF = 1)

`orders` 1 500 000 rows (`o_orderkey` unique); `lineitem` 6 001 215 rows,
~4.0 rows per order key.

**ANALYZE.** Sample `n = 30 000` of 6 001 215. Per key, `P(appears) = 1 − (1 −
0.005)^4 ≈ 0.0199` → `d ≈ 29 850`; `P(appears exactly once) = 4 × 0.005 ×
0.995^3 ≈ 0.01970` → `f1 ≈ 29 554`; `nmultiple = d − f1 ≈ 296`.

```
N     = 6 001 215
denom = (30 000 − 29 554) + 29 554 × 30 000 / 6 001 215 = 446 + 147.75 = 593.75
est   = 30 000 × 29 850 / 593.75 = 1 508 210
```

→ `l_orderkey.NDistinct ≈ 1 508 210` (true: 1 500 000, **+0.5 %**),
`NDistinctFrac = 0.2513 > 0.1` → `StaDistinct() = −0.2513`.

`o_orderkey`: all sampled values distinct → `nmultiple = 0` → returns
`N = 1 500 000`; `NDistinctFrac = 1.0` → `StaDistinct() = −1.0`.

**Estimate, no key proven** (`eqJoinSelectivityExt`):
`nd(o_orderkey) = 1.0 × 1 500 000 = 1 500 000`;
`nd(l_orderkey) = 0.2513 × 6 001 215 = 1 508 210`; `max = 1 508 210`;
`sel = 1/1 508 210`;
`rows = 1 500 000 × 6 001 215 / 1 508 210 ≈ 5 969 000` (true 6 001 215,
**−0.5 %**).

**Estimate, PK proven** (`superkeyJoinSelectivity` fires on `orders_pkey`):
`sel = 1/1 500 000`, `rowsBound = lineitem.Rows = 6 001 215`;
`rows = 1 500 000 × 6 001 215 / 1 500 000 = 6 001 215`, at the bound — **exact**.

**PG**: `o_orderkey` `stadistinct = −1`, `l_orderkey` ≈ `−0.25`; no useful MCVs
on either side, so `eqjoinsel_inner`'s no-MCV branch gives `1/1 500 000` and
`get_foreign_key_join_selectivity` gives the same. **Agree to within 0.5 %.**

The pre-`30293f788` behaviour — the state ledger row 777 describes — would have
stored `l_orderkey.NDistinct = 29 850` and `o_orderkey.NDistinct = 30 000`,
giving `sel = 1/30 000` and `rows ≈ 3.0 × 10^8`, a **50× over-estimate** that
compounds through every join above. This example is precisely why that slice
existed.

---

## 13. Fidelity table

`✔` faithful · `~` present but simplified · `✗` absent · `!` present and
divergent in a way that changes plans.

| # | PG mechanism | goopg symbol | verdict | what differs |
|---|---|---|---|---|
| 1 | `pg_class.relpages` | `catalog.TableStats.Pages` | `~` | snapshot at ANALYZE; `baseRelPages` prefers it over the live block count (PG always uses live) |
| 2 | `pg_class.reltuples` | `TableStats.RowCount` | `✔` | exact live count (ANALYZE full-scans) rather than sample-extrapolated |
| 3 | `reltuples = -1` sentinel | `TableStats.Analyzed bool` | `✔` | separate bool because `RowCount` is unsigned in effect |
| 4 | `pg_class.relallvisible` | `catalog.RelAllVisibleFunc` → `RelAllVisible` | `✔` | computed live from the VM, not stored |
| 5 | `pg_class.relallfrozen` (PG 18) | — | `✗` | autovacuum's `pcnt_unfrozen` hardcoded 1 |
| 6 | TRUNCATE resets `relpages/reltuples/relallvisible` to `0/-1/0` | — | `!` | `operators_ddl.go:execTruncate` never touches `Table.Stats`: after `TRUNCATE t` the planner still believes the pre-truncate `RowCount`, `Pages`, MCV list and histogram until the next ANALYZE |
| 7 | `vac_update_relstats` | `InMemory.UpdateRelStats` | `~` | merges correctly; **in-memory only, not durable** |
| 8 | `vac_estimate_reltuples` | — | `✗` | VACUUM recounts the whole relation instead |
| 9 | `table_block_relation_estimate_size` | `relsize.go:estimateRelSize` | `✔` | all five steps incl. integer-division density and the 10-page floor |
| 10 | `relhassubclass` guard on the floor | `len(tbl.PartitionKey) > 0` | `~` | plain inheritance parents not detected (ledgered) |
| 11 | scale stored stats by live `curpages` | — | `!` | goopg short-circuits when `RowCount > 0`; a grown table keeps a stale estimate |
| 12 | `allvisfrac = relallvisible/curpages` | `pathindexonly.go:relAllVisibleFraction` | `~` | denominator is the stored page count, numerator the live VM |
| 13 | `get_rel_data_width` (Σ `stawidth`) | `relsize.go:tableDataWidth`/`typeWidth` | `!` | declared-type widths; `stawidth` is never summed |
| 14 | index `relpages`/`reltuples`/`tree_height` | `costindex.go:estimateIndexGeometry` | `!` | synthesised; real file size used when storage answers; no index catalog stats |
| 15 | index metapage discount | — | `✗` | |
| 16 | partial-index `IndexOptInfo.tuples` | — | `✗` | |
| 17 | `attstattarget` resolution | `columnStatsTarget` | `~` | `0` disables ✔; no `-1`/10000 clamp; does not raise `targrows` |
| 18 | `std_typanalyze` 3-way dispatch | `isOrderableKind` inside one function | `~` | equivalent outcome, different structure |
| 19 | `targrows = 300 × target`, `max(100, …)` | `sampleCap = target × 300` | `~` | no 100 floor, no max across columns |
| 20 | Algorithm S block sampling | — | `!` | goopg scans **every block** |
| 21 | Vitter reservoir within blocks | Algorithm R over visible tuples | `~` | uniform over tuples, not blocks-then-tuples |
| 22 | `totalrows` extrapolation | exact count | `✔` (better) | |
| 23 | TID sort of the reservoir | — | `✗` | harmless — correlation uses original position |
| 24 | `WIDTH_THRESHOLD` (1024 B) exclusion | — | `!` | wide values enter MCV/histogram → §2.11 page overflow |
| 25 | `stanullfrac = null_cnt/samplerows` | `NullFrac` | `✔` | |
| 26 | `stawidth` over non-null values | `AvgWidth` | `!` | counts only variable payload; 0 for fixed-width types |
| 27 | `stadistinct = -(1-nullfrac)` when no dups | `ndistinctEstimate` `nmultiple==0` arm | `✔` | |
| 28 | `stadistinct = ndistinct` when all repeat | `nmultiple==d` arm | `✔` | |
| 29 | Duj1 `n·d/((n−f1)+f1·n/N)` clamped `[d,N]` | `ndistinctEstimate` | `✔` | `toowide_cnt` folded to 0 (**ledger row 777 is stale**) |
| 30 | `> 0.1 × totalrows` → negative fraction | `ColumnStats.StaDistinct()` | `✔` | stored as two fields, reduced on read |
| 31 | complete MCV list when all fit | — | `✗` | |
| 32 | `analyze_mcv_list` hypergeometric prune | `mcvFreqMargin = 1.25` greedy admit | `!` | **doc 03 checklist 38: no 1.25× rule exists in 18.3**; goopg over-admits on uniform columns |
| 33 | MCV freq = `count/samplerows` | ✔ | `✔` | denominator includes nulls, as upstream |
| 34 | `num_hist = min(nd−nmcv, target+1)`, ≥ 2 | `bucketCount = min(target, len(nonMCV)−1)` | `~` | equivalent bound count; needs ≥ 2 bounds ✔ |
| 35 | histogram bound stepping | `expanded[i·last/bucketCount]` | `~` | equi-depth over the expanded multiset; bins land ±1 value from PG's |
| 36 | correlation (Pearson, `staop = <`) | `computeColumnStats` correlation block | `✔` | formula identical; `staop` written as 0 |
| 37 | `compute_index_stats` | — | `✗` | ANALYZE never opens an index |
| 38 | expression-index `pg_statistic` rows | — | `✗` | |
| 39 | inheritance stats (`stainherit = true`) | — | `✗` | partitioned-parent size roll-up only |
| 40 | `n_distinct` reloption override | `columnNDistinctOverride` | `!` | writes only `NDistinct`; `StaDistinct()` may ignore it (§0.1) |
| 41 | `BuildRelationExtStatistics` | — | `✗` | |
| 42 | `pgstat_report_analyze` / `n_mod_since_analyze` reset | `relStats.resetAnalyzeTriggers` | `✔` | |
| 43 | autoanalyze `mod > 50 + 0.1·reltuples` | `launcher.go:needsAnalyze` | `✔` | plus a goopg-only `MinAnalyzeAge = 60 s` |
| 44 | autovacuum `dead > 50 + 0.2·reltuples`, capped | `launcher.go` `vacThresh/vacScale/maxThreshold` | `✔` | |
| 45 | 5 slots × `(stakind, staop, stacoll, stanumbers, stavalues)` | `buildUserPGStatisticRow` (31 cols) | `~` | slots 1–3 written; 4–5 always empty |
| 46 | MCV `staop = =`, hist/corr `staop = <` | `staop1 = 98` (`text =`), `staop2/3 = 0` | `!` | wrong for every non-text column |
| 47 | `stavalues` element type = column type | `text[]` of `Datum.Format()` | `!` | not type-faithful; blocks standby consumption |
| 48 | `pg_statistic` TOAST | — | `!` | rows > BLCKSZ silently skipped per column (§2.11) |
| 49 | transactional `update_attstats` (delete + insert) | append-only heap, last live tuple wins | `~` | heap grows monotonically |
| 50 | `pg_stats` view | `catalog/pgstats.go:PGStatsRowsForDBOid` | `!` | **`correlation` always NULL** though it is collected and persisted |
| 51 | `pg_statistic_ext` | `sys_pg_statistic_ext.go` + registry | `✔` | real per-DB heap rows, physical attnum order |
| 52 | `pg_statistic_ext_data` | declared, never written | `✗` | |
| 53 | `examine_variable` | `examineJoinVar` / `resolveBaseColumn` / `columnStatsForChild` / `columnStatsByName` | `!` | four resolvers; `columnStatsForChild` lacks the `*IndexScan` arm (ledger 785) |
| 54 | `vardata->isunique` | — | `✗` | uniqueness reaches only the superkey prover and `groupUniqueNDistinct` |
| 55 | `get_variable_numdistinct` | `joinselectivity.go:getVariableNumDistinct` | `✔` | branch order, bool arm, `isdefault` semantics; missing VALUES/ctid/tableoid/isunique arms |
| 56 | `get_variable_range` | — | `✗` | |
| 57 | `get_actual_variable_range` (index probe) | — | `✗` | growing keys estimate 1.0 past the last bound |
| 58 | `statistic_proc_security_check` | — | `✗` | |
| 59 | `DEFAULT_EQ_SEL` 0.005 | `defaultEqSelectivity` | `✔` | |
| 60 | `DEFAULT_INEQ_SEL` 1/3 | `defaultIneqSelectivity`, `defaultIneqJoinSel` | `✔` | |
| 61 | unhandled-clause 0.5 | `defaultUnhandledClauseSel` (join) / `defaultGenericSelectivity` 1/3 (restriction) | `!` | restriction path uses 1/3 where PG uses 0.5 |
| 62 | `DEFAULT_NUM_DISTINCT` 200 | `defaultNumDistinct` | `✔` | |
| 63 | `var_eq_const` MCV hit | `eqSelectivityForColumn` | `✔` | TEXT equality on rendered values |
| 64 | `var_eq_const` MCV miss | `mass/remainingDistinct` | `~` | **no cap at the least-common MCV frequency** |
| 65 | `var_eq_const` unique-var `1/tuples` | — | `✗` | |
| 66 | `var_eq_non_const` | — | `✗` | `ParamRef` excluded by `isConstExpr` → 0.005 |
| 67 | `neqsel = 1 − eqsel − nullfrac` | `1 − eq` | `~` | nullfrac term dropped |
| 68 | `scalarineqsel` shape | `rangeOpSelectivity` | `✔` | `mcv_part + (1−nullfrac−Σmcv)·hist_part` |
| 69 | `ineq_histogram_selectivity` binary search | linear scan | `~` | O(nbounds) |
| 70 | `convert_to_scalar` interpolation | `bucketFraction` | `!` | **flat 0.5 for date/timestamp/text/bool** — numeric types only |
| 71 | histogram clamp `[0.01/(n−1), 1−0.01/(n−1)]` | — | `✗` | can return exactly 0 or 1 |
| 72 | `ctid` inequality block arithmetic | — | `✗` | |
| 73 | `nulltestsel` | — | `✗` | `IsNullExpr` has no arm → 1/3; `NullFrac` collected but unused for null tests |
| 74 | `booltestsel` / `boolvarsel` | — | `✗` | bare bool column → 1/3 |
| 75 | `patternsel_common` (LIKE/regex) | — | `✗` | no selectivity arm; `likeprefix.go` is access-path only |
| 76 | `make_greater_string` | `likeprefix.go:IncrementString` | `✔` | C-collation bytes |
| 77 | `scalararraysel` disjoint sum | `InExpr` arm | `~` | no OR-combine fallback, no 10-element default |
| 78 | `rowcomparesel` | — | `✗` | |
| 79 | `clauselist_selectivity` | inlined AND product | `!` | no function, no range pairing, no EC handling, no caching |
| 80 | `RangeQueryClause` pairing (`hi+lo−1`) | — | `✗` | **§12.4: 2.1× over-estimate on every BETWEEN / date window** |
| 81 | `DEFAULT_RANGE_INEQ_SEL` 0.005 | — | `✗` | |
| 82 | `RestrictInfo.norm_selec` cache | — | `✗` | recomputed per call |
| 83 | provenance of an estimate | `selectivityEstimate.reliable` | goopg-only `!` | unreliable ⇒ **keep the pre-filter row count** (ledger 871, up to 200× divergence) |
| 84 | `eqjoinsel` MCV arm (inner) | — | `✗` | |
| 85 | `eqjoinsel_inner` no-MCV branch | `joinselectivity.go:eqJoinSelectivityExt` | `✔` | plus a goopg `isdefault` carry-out |
| 86 | `eqjoinsel_semi` MCV pairing | `cardinality.go:semiPairMatchFraction` | `✔` | **the only production MCV pairing**; TEXT equality; `nd2` clamped by inner input rows only |
| 87 | `eqjoinsel_semi` nd heuristic + `(1−nullfrac1)` | same function | `✔` | |
| 88 | `neqjoinsel` inner (`1 − eqjoinsel`) | `joinClauseSelectivityExt` `OpNe` | `✔` | |
| 89 | `neqjoinsel` semi/anti (`1 − nullfrac`) | — | `✗` | unreachable (special joins pinned outside the search) |
| 90 | `scalarltjoinsel` etc. = 0.3333 | `defaultIneqJoinSel` | `✔` | |
| 91 | `mergejoinscansel` | — | `✗` | |
| 92 | `estimate_hash_bucket_stats` | — | `✗` | no bucket-skew model |
| 93 | multi-pair equality pricing | `calcJoinrelSize` residual loop / `estimateJoin` pair loop | `✔` | **ledger rows 779/781/784 are stale**; both arms fold every pair |
| 94 | EC de-duplication of join clauses | `oneClausePerEquivClass` | `✔` | |
| 95 | `calc_joinrel_size_estimate` INNER | `calcJoinrelSize` | `✔` | plus `rowsBound` and a guess-only `max(l,r)` cap |
| 96 | LEFT/FULL/RIGHT floors | `outerJoinRowFloor` | `✔` | legacy arm only |
| 97 | SEMI `o·fk·j` / ANTI `o·(1−fk·j)·p` | `estimateJoin` semi/anti arm | `~` | legacy arm only; no `p` (`pselec`) term |
| 98 | `estimate_num_groups` | `cardinality.go:estimateNumGroups` | `✔` | product, `0.1×tuples` clamp with `relmax` floor, Yao/Dell'Era scaling, ceil+clamp |
| 99 | boolean group expr ×2 | — | `✗` | |
| 100 | volatile group expr → `input_rows` | — | `✗` | no volatility catalog in the planner |
| 101 | cross-relation equivalent-var collapse | — | `✗` | |
| 102 | `estimate_num_groups` over DISTINCT | — | `✗` | DISTINCT not sized |
| 103 | `preprocess_limit` / tuple fraction | `tuplefraction.go:preprocessLimit` | `✔` | incl. the 10 % punt and the absolute/fractional heuristic |
| 104 | extended statistics (all of `src/backend/statistics/`) | — | `✗` | `grep StatisticsObject internal/optimizer/` = **0** |
| 105 | `get_foreign_key_join_selectivity` | `joinrelsize.go:superkeyJoinSelectivity` (+ `joinkeyproof.go` mirror) | `✔`/generalised | raw-tuple divisor, whole-key cover, once-per-clause; **accepts composite UNIQUE indexes as evidence** |
| 106 | `nconst_ec` (divide by constant EC clause selectivity) | — | `✗` | filtered-parent FK joins over-estimated |
| 107 | FK nullfrac derate | — | `✗` | |
| 108 | key-implied row bound | `keyImpliedRowsBound` | goopg-only `✔` | sound only when the key side is one base relation |
| 109 | relcache invalidation → `PlanCacheRelCallback` | `planCache.Invalidate()` on `*optimizer.DDL` | `!` | **ANALYZE does not invalidate** (it plans to `Utility`) |
| 110 | generic vs custom plan | — | `✗` | no `plan_cache_mode`, no 5-execution warm-up |
| 111 | `pg_restore_relation_stats` / `pg_restore_attribute_stats` / `pg_dump --statistics` | declared only | `✗` | both functions exist in `pg_proc` seed data (OID 6362 and sibling) and in `pg_nonimmutable_builtins.go`'s list, but there is **no executor handler** — no implementation site in `internal/executor/`. `pg_dump --statistics` support **UNVERIFIED** |
| 112 | statistics visible cross-session | shared `*catalog.Table` under `InMemory.mu` | `✔` | **per-database**, process-wide — the "per-connection" claim is false |
| 113 | statistics survive restart | `persistStatsToPGStatistic` + `loadStatisticsFromHeap` | `~` | ANALYZE yes; VACUUM and autoanalyze **no** |

---

## Appendix — corrections this document makes to the existing record

1. **`.ralph/deferral_ledger.md` row 777** ("no Haas-Stokes scale-up;
   `NDistinct = len(freq)`") is **stale**; superseded by commit `30293f788`
   (M0127-P5.6-e-iii). §0.1.
2. **`.ralph/deferral_ledger.md` rows 779 / 781 / 784** ("estimateJoin divides
   by ONE nd; later pairs price nothing") are **stale**; M0127-P5.6-f landed
   `joinEquiPairs` + per-pair pricing in both arms. §7.7.
3. **`internal/optimizer/small_dimension.go`** comment ("TableStats does not
   survive a restart, and ANALYZE stats are per-connection") is **false on both
   counts**. §0.2.
4. **`internal/catalog/pgstats.go`** header ("does not collect correlation
   (kind 3)") is **stale**; correlation is collected, persisted in stakind3, and
   consumed by `costindex.go` — only the view column is still NULLed. §3.2.
5. **New defect, not previously recorded**: the per-column `n_distinct`
   reloption is silently ignored by every `StaDistinct()` consumer whenever the
   Haas–Stokes fraction exceeds 0.1, because `analyzeRelationWith` overrides
   `NDistinct` but not `NDistinctFrac`. §0.1.
6. **New observation**: `autovacuum/launcher.go:runAnalyze` assigns
   `tbl.Stats = ts` directly rather than through `Catalog.SetTableStats`, so the
   publication is not under the catalog mutex that every other writer takes.
   §1.1.
7. **Ledger row 785 confirmed live**: `columnStatsForChild` still lacks the
   `*IndexScan` arm, so whether a restriction clause finds an MCV list depends
   on the scan shape the planner chose. §1.5.
8. **New observation**: `TRUNCATE` does not reset `Table.Stats`
   (`operators_ddl.go:execTruncate`), so a truncated relation keeps its old
   `RowCount`, `Pages`, MCV list and histogram until the next ANALYZE. PG resets
   all three `pg_class` counters through the relfilenode swap
   (`RelationSetNewRelfilenumber`). Fidelity row 6.
