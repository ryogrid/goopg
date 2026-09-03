# 05 — goopg cost model (current state, HEAD 2026-09-02)

*The goopg counterpart of [02 — PostgreSQL cost model](02-pg-cost-model.md).
Section numbers mirror doc 02 one-to-one, so §3.6 here is the goopg answer to
§3.6 there. Every symbol was re-verified with Serena / `grep` against
`/home/ryo/work/goopg/goopg` @ HEAD on branch `review-bug-fix`; the codemap's
line numbers were treated as hints only. Claims that could not be settled by
reading the Go source are marked **UNVERIFIED**.*

**Scope note.** goopg has two planners (codemap §0). Only the PG-shaped
Path/RelOptInfo join search (`tryJoinSearch` → `searchOneProblem`) has a cost
model in PG's units. Everything above the FROM clause — aggregation, DISTINCT,
set-ops, windows, LIMIT, subplan lowering — and everything the seam declines is
planned by *rewrite rules* with no cost at all, or by the integer "unit-row"
estimator in `joincost.go`. This document covers all three, because a
gap-analysis has to know which PG cost functions are absent versus which are
present-but-unreachable.

**Three facts that colour every section below.**

1. `costParams` is **always** `defaultCostParams()`. No session GUC — not
   `work_mem`, not `random_page_cost`, not `effective_cache_size` — reaches the
   planner (§1.1).
2. `Path.DisabledNodes` is **never assigned** anywhere in the tree. The
   `enable_*` GUCs that are honoured at all are honoured by *skipping producers*,
   not by counting disabled nodes (§1.2).
3. **EXPLAIN prints no costs at all** — the format string is a literal
   `(cost=0.00..0.00 rows=%d width=0)` and `rows=` comes from the *legacy*
   `optimizer.EstimateRows`, not from the `Path` the search selected (§9).

---

## 1. Cost currency and GUCs

### 1.1 Cost GUCs — `costParams` / `defaultCostParams`

`internal/optimizer/cost_funcs.go:47` (`costParams`), `:83`
(`defaultCostParams`).

| field | default | PG counterpart | PG 18 boot value | agrees? |
|---|---|---|---|---|
| `seqPageCost` | 1.0 | `seq_page_cost` | 1.0 | yes |
| `randomPageCost` | 4.0 | `random_page_cost` | 4.0 | yes |
| `cpuTupleCost` | 0.01 | `cpu_tuple_cost` | 0.01 | yes |
| `cpuIndexTupleCost` | 0.005 | `cpu_index_tuple_cost` | 0.005 | yes |
| `cpuOperatorCost` | 0.0025 | `cpu_operator_cost` | 0.0025 | yes |
| `parallelSetupCost` | 1000.0 | `parallel_setup_cost` | 1000 | yes |
| `parallelTupleCost` | 0.1 | `parallel_tuple_cost` | 0.1 | yes |
| `effectiveCacheSize` | `4 GiB / 8192 = 524288` pages | `effective_cache_size` | 524288 blocks | yes |
| `workMem` | `hashsize.DefaultMemLimitBytes` = **512 MiB** | `work_mem` | **4 MB** | **no** |

Absent from the struct entirely: `min_parallel_table_scan_size`,
`min_parallel_index_scan_size` (they live on `ParallelSettings` instead),
`hash_mem_multiplier`, `parallel_leader_participation` (passed as a bool
argument to `getParallelDivisor`, never read from a GUC in production), any
tablespace-level page-cost override (doc 02 §10 item 3 — **absent**;
`spccache.c` has no goopg counterpart).

**The `work_mem` divergence is real but self-consistent inside goopg.**
`internal/utils/misc/defaults.go:785` registers `work_mem` with
`BootVal: "512MB"`, not PG's 4 MB, and
`hashsize.DefaultMemLimitBytes = 512 << 20` is the same number, so the
planner's hard-wired budget matches goopg's *own* session default. It does not
match PG's, which is why every spill threshold in §8 lands in a different
regime from doc 02's.

#### Do session GUCs reach the planner? No — proof

All callers of `defaultCostParams()` in non-test code:

```
internal/optimizer/joinsearchseam.go:337   cp: defaultCostParams(),   // the join search
internal/optimizer/planner.go:9241         cp := defaultCostParams()  // bitmapOverCorrelatedProbe
```

That is the complete set. `searchCtx.cp` is initialised from the seam's literal
and is never mutated; `joinsearchseam.go:337` sits inside the
`planJoinlistSearch(jl, &joinlistProblem{…})` literal, which has no session
argument. `optimizer.Plan(stmt, cat)` takes a statement and a catalog only, so
there is no channel for a GUC value to arrive on: the session's overrides are
carried on the *catalog wrapper* (`sessionPlanCatalog`,
`internal/postmaster/dispatch.go:1790-1829`), and that wrapper carries only
search_path, dbOid, temp-owner token and four boolean `Disable*Scan` flags.

The file says so itself (`cost_funcs.go:77-79`):

> The per-session value does not reach the planner yet: cost time has no
> session in scope (the same gap `ParallelSettings` exists to bridge for the
> parallel post-pass). Deferral ledger 2026-08-05 M0127-P5.7-a.

Consequence for `work_mem` specifically: `internal/postmaster/dispatch.go:1646`
(`sessionWorkMem`) *does* read the session value and hand it to the
**executor** (`Context.WorkMem` → `hashsize.EffectiveMemLimit`). So a session
that runs `SET work_mem = '4MB'` changes what the executor spills at while the
planner keeps pricing a 512 MiB build. That is exactly the planner/executor
divergence the shared `internal/executor/hashsize` package was created to
prevent, reintroduced one level up.

#### `hash_mem_multiplier` — declared, never consumed

Two references exist in the whole tree, both registrations:

```
internal/utils/misc/defaults.go:1342   Name: "hash_mem_multiplier", Type: TypeReal, BootVal: "2.0"
internal/catalog/catalog.go:12098      {"hash_mem_multiplier", "2.0", …}   // pg_settings row
```

There is **no third reference**. PG's `get_hash_memory_limit()` (doc 02 §10
item 2) has no goopg counterpart: `hashsize.Choose` is handed `cp.workMem`
un-multiplied. goopg's hash budget is therefore `work_mem`, not
`work_mem × 2`. Same for `hashagg` and Memoize, which both go through
`hashsize.EffectiveMemLimit`.

#### `enable_*` handling

`Path.DisabledNodes` (`path.go:120`) is **read** in three places
(`comparePathCostsFuzzily` `path.go:391`, the pathlist ordering `path.go:604`,
`tuplefraction.go:227`) and **written in zero** places outside tests — a grep
for `DisabledNodes\s*[:+]*=` over `internal/` returns only test files. The
field's own comment states the situation: *"goopg has no enable_* GUCs, so it
is always 0 today; carried so the dominance order matches PG"*. Doc 02 §10
items 7, 9, 10 and 14 are therefore **absent**; items 11 and 12 are present but
operate on a constant.

What *is* bridged, and how:

| GUC | mechanism | reaches |
|---|---|---|
| `enable_seqscan` | `sessionPlanCatalog` sets `wrapped.DisableSeqScan` (dispatch.go:1817) | `currentSeqScanDisabled` → index-promotion rewrites (planner.go:14868ff) |
| `enable_indexscan` | `wrapped.DisableIndexScan` (dispatch.go:1827) | `currentIndexScanDisabled` → **producer skip** at `pathindexordered.go:57-61` |
| `enable_bitmapscan` | `wrapped.DisableBitmapScan` (:1828) | `currentBitmapScanDisabled` → producer skip at `pathindexordered.go:66-69` |
| `enable_indexonlyscan` | `wrapped.DisableIndexOnlyScan` (:1829) | `indexOnlyScanRejected` → producer skip at `pathindexordered.go:73-75` |
| `enable_memoize` | `registry.OnChange` in `cmd/goopg/main.go:404` → `optimizer.SetMemoizeEnabled` | package-global `memoizeOn` atomic, gate 1 of `getMemoizePath` |
| `enable_hashagg` | `main.go:419` → `SetHashAggEnabled` | `applyEnableHashAggRule` (rewrite, not cost) |
| `enable_presorted_aggregate` | `main.go:411` | `applyPresortedAggregateRule` |
| `enable_nestloop_index` (goopg-only) | `main.go:398` → `SetNLIEnabled` | `rewriteJoinsToNLI` kill switch |
| `geqo`, `geqo_threshold` | `main.go:425`, `:428` | GEQO switchover |

Not bridged at all: `enable_hashjoin`, `enable_mergejoin`, `enable_nestloop`,
`enable_sort`, `enable_material`, `enable_incremental_sort`, `enable_gathermerge`,
`enable_partitionwise_*`, `enable_parallel_hash`, `enable_tidscan`. A session
that sets any of them gets a `pg_settings` row and no behaviour change.

Two hazards worth recording. (a) The `OnChange` bridges write **package-global
atomics** (`memoizeOn` `memoize.go:31`, `nliEnabled` `nl_index_join.go:48`,
`hashAggEnabled` `groupagg_hashagg.go:18`), so one session's `SET
enable_memoize = off` changes planning for *every* session in the process —
PG's semantics are per-backend. (b) The producer-skip technique (doc 02 §10
item 8's mechanism) is applied to `enable_indexscan` / `enable_bitmapscan`,
which upstream handles by *pricing*, not by skipping; upstream skips only
`enable_indexonlyscan`, `enable_tidscan`, `enable_memoize` and
`enable_incremental_sort`. The skip is strictly stronger — with all four scan
toggles off goopg has literally no path but the seq scan `buildInitialRels`
always adds, whereas PG would still cost them at `disabled_nodes = 1`.

#### Environment calibration

One knob affects costs:

```go
// cost_funcs.go:438
var indexProbeCostMultiplier = envFloatDefault("GOOPG_INDEX_PROBE_MULT", 1.0)
```

It multiplies **every `random_page_cost` charge in the index and index-only
scan model** (`costindex.go:134,142,147,152,199`) and nothing else — sequential
fetches and CPU terms stay PG-native. Default 1.0, i.e. PG's arithmetic
exactly; the header explains the knob exists because goopg's NL-index probe
materialises the whole TID list eagerly, so a *calibrated* value >1 would be
needed to stop the DP picking ruinous NL plans. Note it does **not** reach
`costBitmapHeapScan`'s per-page interpolation, so raising it biases index scans
against bitmap scans.

`envFloatDefault` has exactly one other user: none. Other `GOOPG_*` variables in
`internal/optimizer/` (`GOOPG_PGSHAPED_DP`, `GOOPG_UNNEST_PREDP`,
`GOOPG_NLI_COSTGATE`, `GOOPG_MEMOIZE`, `GOOPG_HASH_OUTER_JOIN`,
`GOOPG_RELSIZE_FALLBACK`, `GOOPG_PARALLEL`, …) are shape/feature gates, not
cost calibration.

### 1.2 `Cost` and the comparison

`Cost` (`path.go:36`) is PG's two-number `(Startup, Total)` in `float64`,
same units. `comparePathCostsFuzzily` (`path.go:389`) reproduces PG's
`compare_path_costs_fuzzily` including the `disabled_nodes`-first ordering and
the 1.01 fuzz factor (doc 02 §10 item 11 — **present**, over a constant zero).
`RelOptInfo.ConsiderStartup` is set from `tupleFraction > 0`
(`joinsearch.go:369`), which is PG's `relnode.c:211` rule.

`tuplefraction.go:267` (`getCheapestFractionalPath`) is goopg's
`get_cheapest_fractional_path`; it re-implements the disabled-nodes-then-cost
ordering a third time (`tuplefraction.go:227`).

---

## 2. Helpers

### 2.1 Clamps

`clampRowEst` (`relsize.go:490`) is `clamp_row_est` **verbatim**, including
`math.RoundToEven` for `rint` and the `MAXIMUM_ROWCOUNT = 1e100` ceiling. Doc
02 §10 item 5 — **identical**.

Siblings that are *not* identical and are a live hazard: `clampRowEstF`
(`cardinality.go:1378`) and `saturateRowEst` (`:1391`) and `scaleByFloat`
(`:1401`) serve the legacy `int64` estimator. `clampSelectivity`
(`joinselectivity.go:384`) clamps to `[0,1]`.

`LOG2` (doc 02 §10 item 4) is `math.Log2` — same value, different implementation;
no observable difference at `%.2f`.

### 2.2 `getParallelDivisor`

`cost_funcs.go:104`. **Identical** to `get_parallel_divisor`:

```
d = workers
if leaderParticipates {
    leader = 1 - 0.3*workers
    if leader > 0 { d += leader }
}
if d < 1 { d = 1 }
```

Doc 02 §10 item 26 — identical. The `d < 1` floor is a goopg addition (PG
cannot reach it because `workers >= 1`). Its only caller is
`generateScanPaths`, which is **test-only** (§3.1).

### 2.3 `qualEvalCost` — goopg's `cost_qual_eval`

`cost_funcs.go:145`:

```
qualEvalCost(cp, numQuals, tuples) =
    numQuals <= 0 || tuples <= 0  ->  0
    otherwise                     ->  cpu_operator_cost * numQuals * tuples
```

**Simplified.** `numQuals` counts **conjuncts** (one `restrictInfo` = one
charge), not operator nodes, and every operator is priced at `procost = 1`.
Everything doc 02 §2.3 / §10 items 15-22 describes is absent:

- no `procost` lookup (item 15) — goopg's `catalog` has no `pg_proc.procost`
  reaching the planner;
- no per-node-type table (item 16) — a `CaseExpr`, a `CoerceViaIO`, a
  `RowCompareExpr` and a bare `=` all cost the same 0.0025;
- no ScalarArrayOp arm (items 17, 18): a `x = ANY(array)` is one conjunct;
- no `RestrictInfo.eval_cost` cache and no pseudoconstant startup migration
  (item 21);
- no `SubPlan`/`AlternativeSubPlan` charge (item 22) — see §4.9.

The *tuple count* is the caller's choice, and that is the one place goopg is
deliberately faithful: `addNestLoopPath` charges the cross product
(`o.Rows*i.Rows`), `addHashJoinPath` and `tryMergeJoinPath` charge the join's
output rows.

### 2.4 `get_restriction_qual_cost` — **ABSENT**

There is no analogue. Base-relation restriction quals are never charged in the
Path model at all: `buildInitialRels` passes `numQualOps = 0` to `costSeqscan`
and `costIndexScan` deliberately adds no qpqual term, both on the stated ground
that the local quals live inside the already-built leaf node and their
selectivity is already inside `rows`. See `costindex.go:162-167`:

> The qpqual per-tuple term PG adds here is zero for goopg's search, for the
> same reason `buildInitialRels` passes numQualOps = 0 … Charging them again
> here would double-count them against the seq-scan rival that does not.

This is internally consistent (both rivals are uncharged) but it means goopg's
absolute scan costs are systematically **below** PG's by
`cpu_operator_cost × nquals × tuples`, and that a highly-selective expensive
qual is free. Doc 02 §10 item 30 (`indrestrictinfo` minus index-clause
redundancy) is **absent** — there is no qpqual list.

### 2.5 Byte-size helpers

PG's `relation_byte_size(tuples, width) = tuples × (MAXALIGN(width) + 24)` and
`page_size = ceil(bytes/BLCKSZ)` (doc 02 §10 item 6) are replaced by a
**column-count** model, not a byte-width model:

```go
// internal/executor/hashsize/hashsize.go
EntryBytes(ncols, avgVarBytes) = ncols*48 + 24 + avgVarBytes
```

`spillPages` (`cost_funcs.go:346`) is the `page_size` analogue:
`ceil(rows × EntryBytes(ncols, avgVarBytes) / 8192)`.

The substitution is justified by the executor's representation (a build row is
a `[]Datum` of 48-byte structs, not a packed `MinimalTuple`), and the package
comment forbids "fixing" it. The practical effect is that goopg's per-row
footprint is roughly **3-4× PG's** for a typical projection, so every
work_mem-threshold decision (hash batching, sort spill) fires at a much smaller
row count than PG's at the same nominal budget — partially offset by goopg's
128× larger default `work_mem`. `avgVarBytes` is the sum of ANALYZE
`AvgWidth` over *all* columns of the rel (`joinsearch.go:355-364`), and is 0
when never analysed.

`estScanPages(rows, width)` (`relsize.go:618`) is the *page* analogue used
where no live block count is in scope:
`max(1, ceil(rows × width / 8168))`. Note the denominator is
`usableBytesPerBlock` (8168), not 8192, and there is no per-tuple overhead
term — so it under-counts pages relative to `estimateRelSize`'s density model.

`baseRelPages(tbl, relTuples)` (`pathindexordered.go:305`) prefers
`tbl.Stats.Pages` (the real `relpages`) and falls back to the width-derived
estimate.

### 2.6 `indexPagesFetched`

`costindex.go:228`. **Identical** to `index_pages_fetched` (doc 02 §10 item 27),
branch for branch — the `T` floor at 1, the `b = ceil(ecs × T / total_pages)`
proration with the `b <= 1 → 1` clamp, both Mackert–Lohman regimes, the
`lim = 2Tb/(2T−b)` knee, the `ceil`, the `T` cap in the `T ≤ b` branch.

One goopg addition: `if totalPages < T { totalPages = T }`, replacing PG's
`Assert(T <= total_pages)`. Harmless when the caller sums correctly.

The caller supplies `totalTablePages` from `searchCtx.totalTablePages()`
(`pathindexordered.go:282`): the sum of `baseRelPages` over every base relation
of the search problem. Relations *outside* the search (pinned outer-join
spines, subqueries) are not counted, so `b` is proportionally larger than PG's
`root->total_table_pages` would give — i.e. goopg assumes more cache.

### 2.7 `approx_tuple_count` — **ABSENT**

PG's `approx_tuple_count` (doc 02 §2.7, §10 item 81) recomputes
`hashjointuples` from the *cached per-clause* `JOIN_INNER` selectivities.
goopg's `hashJoinCost` is simply handed `joinRel.Rows` from `calcJoinrelSize`
(§6.2) — the joinrel's single, once-computed row estimate. Since that estimate
already includes the superkey/FK reduction and the two clamps, the number
charged is *not* the product of the raw clause selectivities. Verdict:
**simplified — no separate approximate count; the joinrel's rows are reused.**

### 2.8 Width and target cost

`typeWidth` (`relsize.go:278`) is `get_typavgwidth`, including the
BPCHAR/32/1000 sliding scale and the UTF-8 `maxEncodingCharLen = 4` scaling —
a careful reproduction (doc 02 §10 item 24's *second* half).

The **first** half is absent: PG's `set_rel_width` prefers `stawidth`
(`get_attavgwidth`, the ANALYZE-measured width) over `get_typavgwidth`. goopg's
`tupleWidth`/`nodeTupleWidth`/`tableDataWidth` read *declared types only* and
never consult `ColumnStats.AvgWidth` for width purposes — `AvgWidth` is read
only to build `RelOptInfo.AvgVarBytes` for the hash geometry. Whole-row Vars
get no +24.

`RelOptInfo.Width` for a join is `outer.Width + inner.Width`
(`joinrelsize.go:121`), i.e. the full concatenation, where PG's
`build_joinrel_tlist` sums only columns needed above the join. The code
acknowledges this and ledgers it.

**Target-list evaluation cost is charged nowhere** (doc 02 §10 item 23,
`pathtarget->cost`): there is no `PathTarget` type. A projection with an
expensive expression is free in the cost model.

---

## 3. Scan costs

### 3.1 `cost_seqscan`

`costSeqscan` (`cost_funcs.go:122`):

```
run   = seq_page_cost*relPages + (cpu_tuple_cost + cpu_operator_cost*numQualOps)*relTuples
return {Startup: 0, Total: run}
```

The formula is **identical** to PG's serial arm. What differs is entirely in
the *inputs*, at the one production call site
(`joinsearch.go:393`, inside `buildInitialRels`):

```go
p.Cost = costSeqscan(cp, estScanPages(rows, width), rows, 0)
```

- `rows` is `initialRelRows(leaf, relInfos[i])` — the **post-local-filter**
  estimate. PG charges `baserel->tuples`, the pre-restriction count. A
  relation with a selective `WHERE` is therefore priced as if the filter
  happened for free before the scan.
- `relPages` is `estScanPages(rows, width)` — derived from the *filtered* rows
  and the leaf's output width, not `baserel->pages`. The real block count
  (`baseRelPages`) is available and is used by the *index* producers, so the
  seq-scan rival and the index rival in the same `addPath` comparison are
  costed over different page counts.
- `numQualOps = 0` always (§2.4).

The parallel arm exists in `generateScanPaths` (`pathgen.go:27`) but that
function has **no production caller** — `find_referencing_symbols` returns only
`*_test.go` files. Consequently `addPartialPath` is never called in production
either, `RelOptInfo.PartialPathlist` is always empty, and doc 02 §10 item 25's
"disk cost is never divided" rule is moot. Where `generateScanPaths` *is*
exercised (tests), it divides the **whole total including the disk term** by
the divisor, which is the thing PG explicitly does not do — see §8.5.

### 3.2 `cost_samplescan` — **ABSENT**

No `TABLESAMPLE` path kind.

### 3.3 `cost_index`

`costIndexScan` (`costindex.go:112`). Inputs are `indexScanInputs`
(`costindex.go:57`): `relPages`, `relTuples` (pre-restriction),
`indexPages`, `indexTuples`, `treeHeight`, `selectivity`, `correlation`,
`totalTablePages`, `loopCount`, `indexOnly`, `allVisFrac`.

```
(idxStartup, idxTotal) = btreeIndexAMCost(cp, in)
startup = idxStartup
run     = idxTotal - idxStartup
tuplesFetched = clampRowEst(selectivity * relTuples)

if loopCount > 1:
    pf     = indexPagesFetched(tuplesFetched*loopCount, relPages, indexPages,
                               totalTablePages, effectiveCacheSize)
    pf     = heapPagesAfterVM(pf)
    maxIO  = pf * random_page_cost * M / loopCount
    pf2    = ceil(selectivity * relPages)
    pf2    = indexPagesFetched(pf2*loopCount, …)
    pf2    = heapPagesAfterVM(pf2)
    minIO  = pf2 * random_page_cost * M / loopCount
else:
    pf     = heapPagesAfterVM(indexPagesFetched(tuplesFetched, relPages, indexPages, …))
    maxIO  = pf * random_page_cost * M
    cp2    = heapPagesAfterVM(ceil(selectivity * relPages))
    if cp2 > 0:
        minIO = random_page_cost * M + (cp2 > 1 ? (cp2-1)*seq_page_cost : 0)

csquared = correlation^2
run += maxIO + csquared*(minIO - maxIO)
run += cpu_tuple_cost * tuplesFetched          // no qpqual term
return {startup, startup + run}
```

`M` is `indexProbeCostMultiplier` (§1.1), 1.0 by default.

**Verdict: near-identical for the I/O interpolation; simplified on the CPU
side.** Present: both `loop_count` arms (doc 02 §10 item 28), the
`csquared` interpolation, `min_IO = random + (pages−1)×seq` at loop_count 1,
the all-random treatment at loop_count > 1.

`heapPagesAfterVM` (`costindex.go:407`) is the index-only reduction
`ceil(pages × (1 − allvisfrac))` applied at **all four** `pages_fetched` sites —
doc 02 §10 item 29, **identical**. `allVisFrac` comes from
`relAllVisibleFraction(tbl, relPages)`; it is 0 on a never-vacuumed table, so
the IOS earns its preference from the visibility map rather than being granted
it.

Missing terms: the qpqual per-tuple charge (§2.4); `path->rows` is set by the
caller to `rel.Rows` (unparameterised) or `parameterizedBaserelRows(...)`
(parameterised) rather than by the cost function.

**Partial indexes are declined outright** (`pathindexordered.go:146`:
`if idx.HasPredicate { return false }`) because goopg has no predicate-implication
prover. Doc 02 §10 item 37 — **absent**.

### 3.4 `genericcostestimate` — folded into `btreeIndexAMCost`

There is no separate generic estimator and no `amcostestimate` dispatch. See
§3.5.

### 3.5 `btcostestimate` → `btreeIndexAMCost`

`costindex.go:186`:

```
numIndexTuples = max(0, selectivity * indexTuples)
numIndexPages  = (indexPages > 1 && indexTuples > 1)
                 ? ceil(numIndexTuples * indexPages / indexTuples)
                 : 1
total   = numIndexPages * random_page_cost * M
total  += numIndexTuples * cpu_index_tuple_cost
descent = (treeHeight + 1) * 50 * cpu_operator_cost
startup = descent
total  += descent
```

**Simplified.** Present: `numIndexPages` (doc 02 §10 item 31, single-scan
branch), the `DEFAULT_PAGE_CPU_MULTIPLIER = 50` page-descent charge (item 34,
second half), the `cpu_index_tuple_cost` per index tuple (item 32, first half).

Missing, each named:

| PG term | doc 02 item | status in goopg |
|---|---|---|
| `ceil(log2(index.tuples)) × cpu_operator_cost` descent | 34 (first half) | **absent** — for a 1e6-row index this is 0.05, i.e. 12 % of goopg's whole startup cost |
| `num_sa_scans` (ScalarArrayOp multiplier), and its `ceil(index.pages/3)` clamp | 35 | **absent** — implicitly 1 |
| per-index-tuple qual op cost `cpu_operator_cost × nquals` | 32 (second half) | **absent** |
| `indexStartupCost = qual_arg_cost` | 32 | **absent** — startup is the descent only |
| unique-index `numIndexTuples = 1` clamp | 33 | **absent as a clamp**; the effect is obtained upstream instead, in `parameterizedIndexSelectivity`'s `isunique` short-circuit, so the *selectivity* is `1/relTuples` and `numIndexTuples` comes out ≈1 anyway |
| Mackert–Lohman over `index.pages` for `num_scans > 1` | 31 (second branch) | **absent** — `btreeIndexAMCost` ignores `loopCount` entirely, so a parameterised probe pays the full index-side cost on **every** iteration where PG amortises it (see §8.1) |
| ScalarArrayOp / `IS NULL` handling | 33 | absent |

`btcost_correlation` → `indexCorrelationFor` (`costindex.go:286`):
reads `ColumnStats.Correlation`, negates for a DESC leading key, multiplies by
0.75 when `len(idx.Columns) > 1`. Doc 02 §10 item 36 — **identical**. Returns 0
(PG's uncorrelated default) when stats are missing, which on goopg is the
common case: ANALYZE statistics are per-connection and lost on restart, so most
production index scans are priced at `max_IO_cost`.

### 3.5b `estimateIndexGeometry` — no counterpart in PG

`costindex.go:318`. PG reads `index->pages`/`index->tuples` from the index's
`pg_class` row and `tree_height` from `_bt_getrootheight`. goopg's
`catalog.Index` carries none of those and ANALYZE does not visit indexes, so:

```
indexTuples = max(1, relTuples)                    // one entry per live heap tuple
width       = indexTupleWidth(idx, tbl)            // 8 + Σ typeWidth(key ∪ include) + 4
perPage     = max(1, floor(8168 * fill/100 / width))   // fill = idx.Fillfactor or 90
indexPages  = max(1, ceil(indexTuples / perPage))
if real, ok := catalog.IndexRealPages(idx); ok && real > 0:
    indexPages = real                              // the actual smgr block count
    perPage    = max(perPage, ceil(indexTuples/indexPages))
treeHeight  = perPage > 1 && indexTuples > perPage
              ? max(0, ceil(log_perPage(indexTuples)) - 1)
              : 0
```

The real block count wins when storage can answer (M0134-0183 — the
width-derived guess doubled every NUMERIC-keyed TPC-H index and put the
bitmap-vs-index choice on the wrong side). `indexTuples` is still *derived*,
never `pg_class.reltuples` of the index, and there is no bloat model:
a heavily-updated index reads as freshly built.

### 3.6 `cost_bitmap_heap_scan` and friends

All in `internal/optimizer/costbitmap.go`.

**`costBitmapIndexScan` (`:36`)** = `cost_bitmap_tree_node` for an index child:

```
(idxStartup, idxTotal) = btreeIndexAMCost(cp, in)
tuplesFetched = clampRowEst(selectivity * relTuples)
return {idxStartup, idxTotal + 0.1*cpu_operator_cost*tuplesFetched}
```

Doc 02 §10 item 41's first clause — **identical** (modulo `btreeIndexAMCost`'s
own gaps, §3.5). Note the `0.1 × cpu_operator_cost` rides `tuplesFetched`
rather than `path->rows`; for a base-rel bitmap those are the same number.

**`costBitmapHeapScan` (`:51`)**:

```
startup  = indexCost.Total
pageCost = (pagesFetched >= 2 && T > 0)
           ? random - (random - seq) * sqrt(pagesFetched / T)
           : random
run      = pageCost * pagesFetched
run     += cpu_tuple_cost * tuplesFetched
return {startup, startup + run}
```

Doc 02 §10 item 39 — **identical** (the file records that the interpolation
direction was inverted until recently, and that `indexTotalCost` was
double-charged). Missing: the qual per-tuple term
(`cpu_per_tuple = cpu_tuple_cost + qpqual.per_tuple`) — only
`cpu_tuple_cost` is charged, per §2.4.

**`computeBitmapPagesLooped` (`:129`)** is `compute_bitmap_pages`:

```
pages     = 2*T*tuplesFetched / (2*T + tuplesFetched)
heapPages = min(pages, T)
if loopCount > 1:
    pages = indexPagesFetched(tuplesFetched*loopCount, T, indexPages, totalTablePages, ecs)
    pages /= loopCount
pages = (pages >= T) ? T : ceil(pages)

if maxEntries > 0 && maxEntries < heapPages && heapPages > 0:
    lossyPages = max(0, heapPages - maxEntries/2)
    exactPages = heapPages - lossyPages
    if lossyPages > 0:
        tuplesFetched = clampRowEst(tuplesFetched*(exactPages/heapPages)
                                    + (lossyPages/heapPages)*relTuples)
return (pages, tuplesFetched)
```

Doc 02 §10 items 38 and 40 — **identical in shape**, including the deliberate
use of the *single-scan* `heapPages` for the lossiness test. The one substitution
is the entry budget:

```go
// costbitmap.go:236,244
tbmEntryBytes() = 256 + 64 = 320              // goopg MaxOffsetNumber = 2048 → 256-byte bitmap
bitmapMaxEntries(workMem) = max(16, workMem / 320)
```

PG's is `work_mem × 1024 / 64`. goopg's per-entry cost is **5× PG's**, but its
`work_mem` default is 128× larger, so in practice goopg lossifies at ~1.68 M
pages where PG (4 MB) lossifies at 65 536. The two error directions do not
cancel at any particular table size; they cross over around a 500 MB heap.

**`costBitmapAndCost` (`:286`) / `costBitmapOrCost` (`:308`)** — product /
clamped-sum selectivity, summed costs, `100 × cpu_operator_cost` per combine
after the first. Doc 02 §10 items 41 (second clause) and 42 — **identical**,
except PG's BitmapOr skips the `cost_bitmap_tree_node` markup for `IndexPath`
children and goopg does not distinguish.

`bitmapScanCostEst` / `bitmapAndScanCostEst` (`:329`, `:339`) are
`bitmap_scan_cost_est` / `bitmap_and_cost_est` for `chooseBitmapAnd`.
`indexPagesForPath` (`:348`) is `get_indexpath_pages`, summing over BitmapAnd/Or
children.

**`costBitmapTree` panics** on a non-bitmap path and on a `PathBitmapHeapScan`
with no child. That is a deliberate totality assertion, not a bug, but it means
a future path kind reaching `chooseBitmapAnd` crashes the backend rather than
declining.

### 3.7-3.8 `cost_tidscan`, `cost_tidrangescan` — **ABSENT**

No TID path kind, no `enable_tidscan` bridge. Doc 02 §10 item 43 — absent.

### 3.9-3.11 Subquery / function / tablefunc / values / CTE / named-tuplestore / result scans — **ABSENT as cost functions**

There is no `PathSubqueryScan`, `PathFunctionScan`, `PathValuesScan`,
`PathCteScan`, `PathResultScan` — the `PathKind` enum (`path.go:47-84`) has
exactly: `PathPrebuilt`, `PathSeqScan`, `PathIndexScan`, `PathHashJoin`,
`PathMergeJoin`, `PathNestLoop`, `PathAgg`, `PathSort`, `PathGather`,
`PathGatherMerge`, `PathMemoize`, `PathBitmapIndexScan`, `PathBitmapHeapScan`,
`PathBitmapAnd`, `PathBitmapOr`.

Such a FROM item enters the search as a `PathPrebuilt` leaf whose cost is
`costSeqscan(cp, estScanPages(rows, width), rows, 0)` over
`rows = EstimateRows(leaf)` (`joinsearch.go:393` + `initialRelRows`
`:401`). So a VALUES list, a CTE scan and a 6 M-row table are all priced by the
same seq-scan formula, differing only in their row estimate. Doc 02 §10 items
44, 45, 47 — **absent**. Item 88's per-kind row defaults are partly reproduced
inside `EstimateRows` (`Values` is exact; CTE scans get a documented
anti-collapse floor at `initialRelRows`) but not as a cost input.

### 3.12 `cost_recursive_union` — **ABSENT**

Doc 02 §10 item 46. Recursive CTEs are lowered by the legacy planner with no
cost.

### 3.13 `cost_gather` / `cost_gather_merge`

`gatherCost` (`cost_funcs.go:414`) exists and reproduces `cost_gather`:

```
startup = sub.Startup + parallel_setup_cost
total   = sub.Total + parallel_setup_cost + parallel_tuple_cost*outputRows
```

**It has no caller anywhere in the tree** (grep for `gatherCost(` returns the
definition only). `PathGather` and `PathGatherMerge` are declared `PathKind`
constants that are never constructed. `cost_gather_merge` (doc 02 §10 item 48,
second half) is **absent** entirely.

Parallelism is decided by `optimizer.MaybeAddGather` (`parallel.go:88`), a
post-planning pass invoked from `dispatch.go:1496`. It is a pure **size rule**
(`computeParallelWorkers`, `parallel.go:588`, reproducing
`compute_parallel_worker`'s block thresholds and the `parallel_workers`
reloption, plus goopg's own refinement of sizing an IOS by
`IndexRealPages(index)`), followed by parallel-safety walks. Its own comment is
explicit: *"a SIZE RULE, not a cost comparison — which is exactly why it is
reproducible here despite goopg having no absolute node costs to add
parallel_setup_cost to."*

Consequence: `parallel_setup_cost` and `parallel_tuple_cost` are **never
charged against anything**. The comparison doc 02 §8.5 turns on — Gather's
`0.1 × rows` beating a serial scan — cannot happen in goopg; parallelism is
granted or refused on block count alone.

---

## 4. Sort and materialization

### 4.1-4.2 `cost_tuplesort` / `cost_sort` → `costSortRun`

`cost_funcs.go:186`:

```
tuples = max(inputRows, 2.0)
comparisonCost = 2 * cpu_operator_cost
startup = comparisonCost * tuples * log2(tuples)

if ncols > 0 && workMem > 0:
    inputBytes = tuples * hashsize.EntryBytes(ncols, 0)
    if inputBytes > workMem:
        npages        = ceil(inputBytes / 8192)
        nruns         = inputBytes / workMem
        mergeorder    = tuplesortMergeOrder(workMem)
        logRuns       = (nruns > mergeorder) ? ceil(ln(nruns)/ln(mergeorder)) : 1
        npageaccesses = 2 * npages * logRuns
        startup += npageaccesses * (0.75*seq_page_cost + 0.25*random_page_cost)

return {startup, startup + cpu_operator_cost*tuples}
```

Doc 02 §10 items 49 and 50 — **identical in arithmetic**, with two input
substitutions: the caller's `comparison_cost` extra is always 0 (there is no
per-datatype comparison cost), and `input_bytes` uses the column-count model
(§2.5) instead of `relation_byte_size`, with `avgVarBytes` hard-coded to 0
(*not* taken from `RelOptInfo.AvgVarBytes` as the hash path does — an internal
inconsistency worth flagging).

`tuplesortMergeOrder` (`:229`) is `tuplesort_merge_order`:
`clamp(allowedMem / (2×8192 + 8192×32), 6, 500)` = `clamp(mem/278528, 6, 500)`.
**Identical** (item 50's tail).

**Bounded top-N sort: verified ABSENT.** The function has exactly two branches
(in-memory, external); there is no `limit_tuples` parameter and no caller could
pass one. The header states it: *"goopg has no LIMIT-aware sort path, so
`output_tuples == tuples` and `output_bytes == input_bytes` identically."*
Doc 02 §10 item 51 — absent.

`cost_sort`'s "add the input's total to startup" (item 52) is done by the
*caller*, `sortPathFor` (`joinpathsmerge.go:404`):
`Cost{Startup: sub.Cost.Total + s.Startup, Total: sub.Cost.Total + s.Total}`.
`disabled_nodes += !enable_sort` — absent (§1.2).

**Reachability.** `costSortRun` has exactly one production caller,
`sortPathFor`, which has exactly one caller, `tryMergeJoinPath`. So the sort
cost model prices **only** merge-join input sorts. The `ORDER BY` / `GROUP BY`
sorts the legacy planner emits above the search boundary are never costed.

### 4.3 `cost_incremental_sort` — **ABSENT**

No incremental sort node, no `enable_incremental_sort`. Doc 02 §10 item 53 —
absent.

### 4.4-4.5 `cost_append`, `cost_merge_append` — **ABSENT**

No `PathAppend`/`PathMergeAppend`. Partition/inheritance fan-out is built by
the legacy planner as a `SetOp`/UNION ALL shape with no cost. Doc 02 §10 items
54, 55 — absent.

### 4.6 `cost_material` — **ABSENT**

Doc 02 §10 item 56. `pathgen.go:107-112` says so explicitly:

> The inner rescan cost is the inner path's own total: no `Material` is
> interposed, because Material is a plan node placed by `cost_rescan` and is
> P5.7's (leftdeep-joins 04 §4 / the P4.3 ledger row). Until it lands this
> over-charges a rescan of a cheap inner, which biases against nested loops —
> the safe direction.

and `joinpathsmergeouter.go:67`: *"No `PathMaterial` kind is introduced, and one
would be wrong…"*. The **executor** has a work_mem-bounded materialiser
(`operators_material.go`), so the capability exists and only the *path-level
pricing* is missing. The mergejoin materialize-inner decision (doc 02 §10 item
72) therefore cannot be made at all.

### 4.7 `cost_rescan` — **ABSENT as a function**

There is no `cost_rescan`. Its job is done inline, per join arm:

- **plain nested loop** (`addNestLoopPath`, `pathgen.go:122`): the rescan cost
  is `i.Cost.Total` — the inner path's *full* total, re-paid on every outer
  row. PG's default arm agrees for most node types; PG's special arms
  (FunctionScan / single-batch HashJoin rescan at `total − startup`;
  CTE/WorkTable at `cpu_tuple_cost × rows`) are **absent**, so a hash-join inner
  under a nested loop is charged its full build on every iteration.
- **NLI** (`addNLIPaths`, `joinpathsnli.go:250`): `pathRescanTotal(in)`, which
  for a bare parameterised index path is `in.Cost.Total` (PG's default for an
  index scan, which caches nothing) and for a `PathMemoize` wrapper is the
  memoize rescan cost.

Doc 02 §10 item 57 — absent.

### 4.8 `cost_memoize_rescan` → `costMemoizeRescan`

`joinpathsmemoize.go:125`. **Near-identical transcription**:

```
calls = max(calls, 1); tuples = max(tuples, 0)
estEntryBytes = EntryBytes(ncols,0)*tuples + (memoizeEntryFixedBytes
                + memoizeTupleFixedBytes*tuples) + EntryBytes(nkeys,0)
estEntryBytes = max(estEntryBytes, memoizeMinEntryBytes)
estCacheEntries = max(memoizeMinCacheEntries,
                      floor(EffectiveMemLimit(workMem) / estEntryBytes))
if isDefaultND: ndistinct = calls
ndistinct = clamp(ndistinct, 1, calls)
evictRatio = 1 - min(estCacheEntries, ndistinct)/ndistinct
hitRatio   = clamp01(((calls-ndistinct)/calls) * (estCacheEntries/max(ndistinct,estCacheEntries)))
total   = inner.Total*(1-hitRatio) + cpu_operator_cost
        + cpu_tuple_cost*evictRatio
        + (cpu_operator_cost/10)*evictRatio*tuples
        + cpu_tuple_cost + cpu_operator_cost*tuples
startup = inner.Startup*(1-hitRatio) + cpu_tuple_cost
est     = min(ndistinct, estCacheEntries, memoizeMaxEstEntriesPG)
```

Doc 02 §10 item 58 — **identical**, with three documented substitutions:
`relation_byte_size` → `hashsize.EntryBytes` (column count), `get_expr_width`
per key → the same, and `estimate_num_groups` → the caller's
`memoizeKeyNDistinct`. The `SELFLAG_USED_DEFAULT` → `ndistinct = calls`
behaviour (item 92's Memoize half) is reproduced faithfully. goopg clamps where
PG asserts.

`getMemoizePath` (`:216`) reproduces PG's eligibility gauntlet with two gates
noted as **inexpressible**: the SEMI/ANTI gate (a searched `RelOptInfo` carries
no join type) and the `inner_unique` ppi-serial test. Both are vacuous today
because the seam admits only INNER joins.

### 4.9 `cost_subplan` → `estimateSubplanCostPerCall`

`subplan_cost.go:29`. **Not in PG's units at all.** It returns an `int64`
*row count* interpreted as a per-invocation work estimate:

```
SeqScan                 -> tableRows(tbl)
IndexScan               -> tableRows / NDistinct(first key col)      // 0 if unknown
IndexOnlyScan           -> same by column name
Filter/Project/Aggregate/Sort/Limit/Distinct/OrdinalityWrap -> recurse into child
Join(l, r)              -> (l<=0 || r<=0) ? 0 : l + r
NestedLoopIndexJoin     -> (l<=0 || r<=0) ? 0 : l + l*r
anything else           -> 0                                          // "unknown", not "free"
```

`0` means *no information* and callers must not read it as free. There is no
hashed/EXISTS/ANY-ALL distinction, no `plan.total_cost`, no
`per_call_cost` split, no `startup once if uncorrelated` rule. Doc 02 §10 item
59 — **absent**; this is a different quantity in a different unit, used only to
rank "rerun the SubPlan per outer row" against a set-oriented rewrite
(D6.3a). It is consumed by the unnest/sublink machinery (`unnest.go:3734`),
never by `addPath`.

Correspondingly, doc 02 §10 item 22 (SubPlan cost inside `cost_qual_eval`) is
absent: a qual containing a SubPlan costs one `cpu_operator_cost` like any
other conjunct.

### 4.10 `cost_agg` → `aggCost`

`cost_funcs.go:452`:

```
perInput = cpu_operator_cost*numAggs + cpu_operator_cost*numGroupCols
startup  = child.Total + perInput*inputRows
perGroup = cpu_operator_cost*numAggs + cpu_tuple_cost
total    = startup + perGroup*numGroups
```

This is the `AGG_HASHED` arm only. **It has no production caller** — grep for
`aggCost(` returns the definition and nothing else; `PathAgg` is never
constructed. Aggregation is planned entirely by the legacy rewrite rules
(`applyEnableHashAggRule`, `applyIndexOrderedGroupingRule`,
`applyPresortedAggregateRule`) which compare *shapes*, not costs.

Absent even in the dead function: `AGG_PLAIN` and `AGG_SORTED` arms, HashAgg
spill (doc 02 §10 item 61), HAVING-qual costing and its selectivity on
`path->rows` (item 62), and the `enable_hashagg` disabled-node increment
(item 9).

### 4.11 `cost_windowagg` — **ABSENT**

Doc 02 §10 item 63. Window functions are lowered by the legacy planner with no
cost model.

### 4.12 `cost_group` — **ABSENT**

Doc 02 §10 item 64.

### 4.13 Set operations

`estimateSetOp` (`cardinality.go:190`) is a *cardinality* rule only (UNION →
sum, INTERSECT → min, EXCEPT → left, with DISTINCT halving). There is no
`SetOp` cost function.

---

## 5. Join costs

goopg has **no two-stage `initial_cost_*` / `final_cost_*` split**. Each join
path is priced by one function at `addPath` time, so PG's `initial_cost_*`
early-abort (which lets the DP reject a pair before computing the expensive
half) has no counterpart — every candidate is fully costed.

### 5.1 `compute_semi_anti_join_factors` — **ABSENT**

Doc 02 §10 item 68. The searched seam admits only INNER joins
(`joinsearchseam.go` pins every outer/semi/anti construct outside it), so there
are no semi/anti join paths to compute factors for. The legacy estimator has
its own `semiJoinMatchFraction` (`cardinality.go:713`), which is a
*cardinality* rule, not a cost input.

### 5.2-5.3 Nested loop → `nestloopCost`

`cost_funcs.go:384`:

```
startup = outer.Startup + inner.Startup
ntuples = max(outerRows,1) * max(innerRows,1)
run     = (outer.Total - outer.Startup)
        + outerRows * innerRescanTotal
        + cpu_tuple_cost * ntuples
return {startup, startup + run}
```

plus, at both call sites,
`cost.Total += qualEvalCost(cp, len(quals), outerRows*innerRows)`.

**Simplified.** Present: PG's `ntuples = outer_rows × inner_rows` "processed,
not emitted" discipline (the header documents the Q47 regression that forced
it), the `cpu_per_tuple × ntuples` charge split across two sites, the
per-outer-row rescan term, and the `inner_path->rows` = `ppi_rows` convention
for a parameterised inner.

Absent:

- the `(outer_rows − 1)` factor: PG charges the first inner scan in
  `initial_cost` and the *remaining* `outer_rows − 1` at the rescan cost.
  goopg charges `outerRows × innerRescanTotal` **plus** `inner.Startup` in
  startup, i.e. it double-charges the first scan's startup and under-charges
  nothing. At large `outerRows` the difference is negligible; at
  `outerRows = 1` it is 100 %.
- **all SEMI/ANTI early-out logic** (doc 02 §10 item 66): `outer_matched`,
  `outer_match_frac`, `inner_scan_frac = 2/(match_count+1)`, and the
  indexed-vs-unindexed branches. **Verified absent** — `nestloopCost` takes no
  join type and there is no `has_indexed_join_quals` (item 67).
- `enable_nestloop` disabled-node increment.

### 5.4-5.5 Merge join → `mergeJoinCost`

`cost_funcs.go:400`:

```
startup = outer.Startup + inner.Startup
run     = (outer.Total - outer.Startup) + (inner.Total - inner.Startup)
        + cpu_operator_cost*(outerRows + innerRows)
        + cpu_tuple_cost*outputRows
return {startup, startup + run}
```

plus `qualEvalCost(cp, len(residual), joinrel.Rows)` at the call site
(`joinpathsmerge.go:367`).

**Heavily simplified.** This is "one pass over each input, one operator
comparison per input row, one tuple emit per output row". Everything that makes
PG's merge cost interesting is absent:

| PG mechanism | doc 02 item | goopg |
|---|---|---|
| `mergejoinscansel` start/end scan fractions, cached on the RestrictInfo | 69 | **absent** — both sides are always scanned in full |
| jointype forcing of scan fractions (LEFT/ANTI outer 0..1, etc.) | 69 | absent (INNER only) |
| pro-rating the input sort costs by the scan selectivities | 70 | absent |
| incremental sort for a partly-presorted outer | 70 | absent |
| `rescannedtuples` / `rescanratio` (duplicate handling) | 71 | **absent** — a merge over a many-to-many key is priced as one pass |
| `mat_inner_cost` and the materialize-inner decision | 72 | absent (§4.6) |
| `skip_mark_restore` | 73 | absent |
| merge-qual cost charged over the skipped prefix at startup | 74 | absent |

The input sorts *are* charged, via `sortPathFor` (§4.1), which is the one term
that keeps merge from winning trivially. `joinpathsmergeouter.go:69` records
that the cost of goopg's outer-side buffering is not charged either.

### 5.6-5.7 Hash join → `hashJoinCost` + `hashJoinInputs`

`cost_funcs.go:309`, inputs at `:251`.

```
build   = (cpu_operator_cost*numHashClauses + cpu_tuple_cost)*innerRows + inner.Total
startup = outer.Startup + build
run     = (outer.Total - outer.Startup)
        + cpu_operator_cost*numHashClauses*outerRows
        + cpu_tuple_cost*outputRows

sizing = hashsize.Choose(innerRows, innerCols, innerAvgVarBytes, workMem)
if sizing.NBatch > 1:
    innerPages = spillPages(innerRows, innerCols, innerAvgVarBytes)
    outerPages = spillPages(outerRows, outerCols, outerAvgVarBytes)
    startup += seq_page_cost * innerPages
    run     += seq_page_cost * (innerPages + 2*outerPages)

return {startup, startup + run}
```

plus `qualEvalCost(cp, len(residual), joinRel.Rows)` at the call site.

**Present and faithful:** the `initial_cost_hashjoin` startup shape (outer
startup + inner **total** + `(cpu_operator_cost × nclauses + cpu_tuple_cost) ×
inner_rows`, doc 02 §10 item 75); the probe's `cpu_operator_cost × nclauses ×
outer_rows`; the multi-batch I/O charge (`innerpages` at startup,
`innerpages + 2×outerpages` at run — item 76) **conditioned on the same
`hashsize.Choose` the executor calls**, which is the strongest
planner/executor agreement anywhere in goopg's cost model; both build
orientations generated so `add_path` picks the side (`generateHashJoinPaths`).

`hashsize.Choose` reproduces `ExecChooseHashTableSize` (item 77) including the
1024-bucket floor, the re-derivation of `nbuckets` from the full budget once
multi-batch is forced, and PG 18's walk-back loop — with goopg's constants
(48-byte Datum, 24-byte slice header, 48-byte map slot) instead of PG's
`HJTUPLE_OVERHEAD + MAXALIGN(SizeofMinimalTupleHeader) + MAXALIGN(width)`.
Skew buckets and the parallel combined budget are absent on both sides (the
package says so).

**Absent — and this is the single largest hash-cost gap:**

- **`virtualbuckets` and the inner bucket-size / MCV skew term** (doc 02 §10
  items 78, 79, 80). PG's probe cost is
  `hq.per_tuple × outer_rows × clamp_row_est(inner_rows × bucketsize) × 0.5`;
  goopg charges a flat `cpu_operator_cost × nclauses × outer_rows`. So goopg
  cannot see a skewed hash key at all: a build whose keys collapse into one
  bucket costs exactly what a perfectly-distributed one costs. (This is the
  planner-side twin of the executor bug in the auto-memory note
  "goopg hash join keys on ONE equi-pair — degeneracy trap".)
- **`disable_cost = 1e10` when the inner MCV bucket exceeds hash memory**
  (item 10) — absent.
- **SEMI/ANTI probe variants** (0.5 matched / 0.05 unmatched, item 80) — absent.
- **`hashjointuples = approx_tuple_count`** (item 81) — replaced by
  `joinRel.Rows` (§2.7).
- **Parallel hash** (item 82) — absent; there are no partial join paths.

### 5.8 NLI costing (`addNLIPaths`, `joinpathsnli.go:205`)

For each parameterised inner path `i` of the inner rel, and for each of
`{i, getMemoizePath(...)}`:

```
cost = nestloopCost(cp, o.Cost, in.Cost, o.Rows, in.Rows, pathRescanTotal(in))
cost.Total += qualEvalCost(cp, len(residual), o.Rows*in.Rows)
```

The key discipline: `in.Cost` prices **one execution with the parameter bound**
and `in.Rows` is its `ppi_rows`, so `in.Cost.Total` *is* the per-outer-row
rescan cost, and `nestloopCost` multiplies it by `o.Rows`. That is PG's
structure exactly.

Two goopg-only refusals: an outer with `RequiredOuter != 0` is rejected, and a
result that is *still* parameterised (`req != 0`) is rejected outright — PG
accepts the star-schema case and gives it `ppi_rows` from
`get_parameterized_joinrel_size`, which goopg has no sizer for. So every join
path in the search is unparameterised, which is what lets `addHashJoinPath`,
`addNestLoopPath` and `tryMergeJoinPath` set `Rows: joinRel.Rows`
unconditionally.

`s.loopCountFor(req)` supplies `cost_index`'s `loop_count` (PG's
`get_loop_count`), so the inner index path is priced with the amortisation of
§3.3 — modulo `btreeIndexAMCost`'s missing `num_scans` arm.

### 5.9 The legacy costing paths — still reachable

**`joincost.go` — `chooseInnerJoinAlgo`.** Verified **live in production**, two
callers:

```
internal/optimizer/pushdown.go:327   if algo, ok := chooseInnerJoinAlgo(lRows, rRows); ok { … }
internal/optimizer/planner.go:2901   if algo, ok := chooseInnerJoinAlgo(lRows, rRows); ok { … }
```

Its currency is **unit-rows**, not PG cost units:

```
costInnerHash(l,r)     = min(l,r) + max(l,r)                     // = l + r, always
costInnerMerge(l,r)    = rowSortCost(l) + rowSortCost(r) + l + r  // n·log2 n, no constants
costInnerNestLoop(l,r) = l * r
```

Note `costInnerHash` reduces to `l + r` regardless of build side, and
`costInnerMerge` charges a full sort of both inputs with **no constant factor**,
so an 11-row sort prices like a real one — the file itself documents the
resulting 210-line regress divergence and gates the RIGHT/FULL variant
(`chooseOuterFillJoinAlgo`) behind `GOOPG_HASH_OUTER_JOIN=1` because of it.
This is a **second, independently calibrated cost model deciding join algorithm
for every path the seam declines**, which is precisely what the design's
one-currency invariant forbids.

**`nl_index_join.go` — `nliCostGateAccepts` (`:1332`).** Also live: called from
`rewriteJoinsToNLI` (`:464`). Modes:

| mode | condition | rule |
|---|---|---|
| legacy (`GOOPG_NLI_COSTGATE=legacy`) | any join type | accept iff `outerRows <= nliMaxOuterRowsHeuristic` or `outerRows <= 0` |
| default, INNER/LEFT | — | same outer-row heuristic (the stats-aware fan-out test died with `costDrivenJoinOrder` at M0127-P6.3) |
| default, SEMI/ANTI | stats known | accept iff `outerRows × matchSet < innerRows + outerRows`, where `matchSet = innerRows / NDistinct(probe col)` |
| default, SEMI/ANTI | stats unknown | **optimistic accept** |

`GOOPG_NLI_COSTGATE_DEBUG=1` dumps the decision. The SEMI/ANTI rule is a
recognisable simplification of `compute_semi_anti_join_factors` +
`final_cost_nestloop`, in row units, with the pessimistic full-match-set bound
instead of PG's `inner_scan_frac = 2/(match_count+1)`.

---

## 6. Cardinality inside costs

### 6.1 Base relations

Three layers, in order.

**(a) `estimateRelSize` (`relsize.go:440`)** is
`table_block_relation_estimate_size` reproduced rule-for-rule: the
never-analysed 10-page floor (skipped for `relhassubclass`), the
`curpages == 0` early exit *after* the floor, density from
`reltuples/relpages` when analysed, otherwise the integer-arithmetic
width-derived density scaled by fillfactor and pushed through `clampRowEst`,
finally `rint(density × curpages)`. Doc 02 §10 item 89 — **identical**, except
the third output `allvisfrac` is not produced here (it is computed separately
by `relAllVisibleFraction` for the IOS path).

**(b) `estimateTableRowsFallback` (`:559`)** wires (a) to the catalog's live
block count — but **only when `TableStats.RowCount <= 0`**. When ANALYZE data
exists goopg uses `RowCount` verbatim and does *not* scale it by the live block
count, which PG always does. The divergence is deliberate and documented (it
keeps the `GOOPG_RELSIZE_FALLBACK` A/B honest) and is a real fidelity gap: a
table that grew 10× since its last ANALYZE is planned at its old size.
`SetRelSizeFallbackStage` / `GOOPG_RELSIZE_FALLBACK` selects the stage
(default 2).

**(c) `estimateBaseRelInfo` (`cardinality.go:417`) +
`applyLocalFilterSelectivity` (`:449`)** is
`set_baserel_size_estimates`' second half:

```
rows = clauseSelectivityWithSource(localizeExprToLeaf(local, binding), scan)
if !rows.reliable: return baseRows           // <-- the deviation
return max(1, scaleByFloat(baseRows, rows.value))
```

**The `reliable` gate is the deviation from doc 02 §10 item 83.** PG always
multiplies, falling back to `DEFAULT_EQ_SEL`/`DEFAULT_INEQ_SEL`. goopg keeps
the *pre-filter* count whenever the selectivity came from a
`defaultEqSelectivity` (0.005) / `defaultIneqSelectivity` (1/3) /
`defaultGenericSelectivity` (1/3) constant. Ledgered (2026-08-06 row). The
practical effect on a stats-less server: every filtered base relation is sized
as if unfiltered, which makes the seq-scan cost (charged over `rows`, §3.1)
*too high* and every downstream join estimate too large.

`initialRelRows` (`joinsearch.go:401`) then decides which number the search
sees: `filteredRows` for a `SeqScan`/`IndexScan`/`IndexOnlyScan` leaf, and
`EstimateRows(leaf)` for a subquery/CTE/VALUES/function/set-op/pre-built-join
leaf, with an anti-collapse floor for CTE scans (M0129-S1) because
`filterSelectivity`'s 0.005-per-conjunct default was collapsing them to 1 row.

### 6.2 Join relations — `calcJoinrelSize`

`joinrelsize.go:117`. Called once per joinrel on the create path
(PG's rows-once discipline).

```
width = outer.Width + inner.Width
est   = superkeyJoinSelectivity(cat, outer, inner, oneClausePerEquivClass(clauses))
sel   = est.sel
allDefault = len(est.residual) > 0
for ri in est.residual:
    (s, isdefault) = joinClauseSelectivityExt(ri)
    sel *= s
    if !isdefault: allDefault = false

rows = outer.Rows * inner.Rows * sel

if rows > est.rowsBound: rows = est.rowsBound            // clamp 1, structural
if !est.fired && allDefault:                             // clamp 2, heuristic
    rows = min(rows, max(outer.Rows, inner.Rows))
return clampRowEst(rows), width
```

**Jointype handling: there is none.** `calcJoinrelSize` takes no join type; the
function header states it is *"`calc_joinrel_size_estimate` for the only
jointype the search can see (INNER)"*. Doc 02 §10 item 85's LEFT floor, FULL
double floor, SEMI `o×fk×j` and ANTI `o×(1−fk×j)×p` arms are all **absent from
the Path model**. They exist only in the legacy estimator (§6.3).

**The `max(outer, inner)` cap from M0126-0010: verified present**
(`joinrelsize.go:158-162`), and correctly narrowed — it fires only when no key
was proven **and** every residual clause was priced by a `selfuncs.h` constant.
`len(est.residual) > 0` keeps it off a CROSS product.

**Parameterized join rows (doc 02 §10 item 86): ABSENT.** There is no
`get_parameterized_joinrel_size`; `addNLIPaths` refuses any parameterised join
result outright rather than sizing it, and every join path carries
`Rows: joinRel.Rows`. The `ppi_rows` analogue exists for **base rels only**:
`parameterizedBaserelRows(rel, idx, selectivity, fullyBound)`
(`pathparamindex.go:364`), fed by `parameterizedIndexSelectivity` over *all*
movable clauses while the *index quals alone* feed `cost_index`'s
`selectivity` — which is PG's own split (item 84).

**`superkeyJoinSelectivity` (`joinrelsize.go:213`)** is
`get_foreign_key_join_selectivity` generalised. It reproduces three upstream
properties deliberately: the divisor is the **raw** tuple count, the **whole
key must be covered** (PG's chicken-out), and a clause is **consumed once**.
Keys are applied greedily largest-divisor-first.

Where it *extends* PG: goopg accepts a **composite UNIQUE index** as key
evidence, not just a declared FK or a single-column unique index
(`has_unique_index` requires `nkeycolumns == 1` upstream). That was a
deliberate decision — TPC-H/TPC-DS declare no FKs, so PG's own machinery would
find nothing. `keysCovering` (`:412`) gets the divisor asymmetry right: a
UNIQUE index on `r` divides by `r`'s raw count, a FK declared on `r` divides by
the **parent's**.

`keyImpliedRowsBound` (`:284`) has no PG counterpart: a proven key on a
*single base relation* bounds the join's output by the other side's post-filter
rows. It refuses to answer when the key relation sits inside a multi-relation
side, which is sound.

Absent relative to doc 02 §10 item 87: the `nmatched_ec − nconst_ec +
nmatched_ri` clause-removal accounting, the `ec_has_const` division, and the
SEMI/ANTI `ref_rows/ref_tuples` variant.

`oneClausePerEquivClass` (`:528`) is the EC de-duplication PG gets from
`clauselist_selectivity`'s EC handling.

### 6.3 The legacy estimator — `EstimateRows` / `estimateJoin`

`cardinality.go:43` / `:572`. This is a **completely independent** cardinality
model over executor `Node` trees, and it is the one EXPLAIN prints (§9) and the
one `chooseInnerJoinAlgo`, `nliCostGateAccepts`, `maybeAttachMemoize` and the
parallel pass consult.

`estimateJoin` shape:

```
l, r = EstimateRows(Left), EstimateRows(Right)
if l<=0 || r<=0: return 0
CROSS      -> l*r
SEMI/ANTI  -> scaleByFloat(l, semiJoinMatchFraction(j,r)*joinResidualSelectivity(j) [1-x for ANTI])
HASH/MERGE -> superkeyJoinEstimate + per-pair /ndistinct; unmeasured pairs *= 0.005
              rows = l*r*sel*joinResidualSelectivity(j)
              rows = min(rows, sk.rowsBound)
              return outerJoinRowFloor(j, rows, l, r)
fallback   -> est = l*r*0.005; est = min(est, max(l,r)); outerJoinRowFloor(...)
```

`outerJoinRowFloor` (`:682`) *does* implement doc 02 §10 item 85's LEFT/FULL
floors (and a RIGHT arm PG does not need because it commutes RIGHT to LEFT).
So the outer-join clamp exists in the **legacy** estimator and not in
`calcJoinrelSize`.

**`EstimateRows` and `Path.Rows` are different numbers for the same relation.**
`Path.Rows` comes from `calcJoinrelSize` / `initialRelRows`; `EstimateRows`
re-derives from the rebuilt `Node` tree using a different selectivity stack
(`filterSelectivity` / `clauseSelectivity` over `ColumnStats`) and different
join arms. Nothing reconciles them, and `createPlanAtSearchRootRange` does not
stamp the Path's rows onto the Node. `joinkeyproof.go:11-35` documents this as
the "sibling-paths rule" hazard.

### 6.4 Rows from `compute_bitmap_pages`

Present — `computeBitmapPagesLooped` returns the corrected `tuplesFetched`
(§3.6) and `costBitmapHeapScan` charges `cpu_tuple_cost` on it. Doc 02 §10
item 40's row half is reproduced; the *path's* `Rows`, however, is set by the
caller from the bitmap selectivity, not from the lossy-corrected count — which
matches PG (PG also keeps `path->rows = baserel->rows`).

---

## 7. Selectivity entry points the cost model calls

Interface only; full detail belongs to doc 06.

| entry point | file:symbol | consumed by |
|---|---|---|
| `clauseSelectivity(expr, child)` | `selectivity.go:28` | legacy `filterSelectivity`, `applyLocalFilterSelectivity` |
| `clauseSelectivityWithSource(expr, child)` | `selectivity.go:477` | `applyLocalFilterSelectivity` (needs the `reliable` flag) |
| `joinClauseSelectivity(ri)` / `joinClauseSelectivityExt(ri)` | `joinselectivity.go:323`, `:340` | `calcJoinrelSize`'s residual loop |
| `eqJoinSelectivity(v1,v2)` / `eqJoinSelectivityExt` | `joinselectivity.go:261`, `:282` | `joinClauseSelectivity*` |
| `getVariableNumDistinct(v)` | `joinselectivity.go:211` | `eqJoinSelectivity*` |
| `estimateNumGroups(exprs, child, inputRows)` | `cardinality.go:1086` | `estimateAggregate`, grouping rules; **not** by Memoize (which uses `memoizeKeyNDistinct`) |
| `varEqNonConstSelectivity(stats, relTuples)` | (planner.go, used by `bitmapOverCorrelatedProbe`, `parameterizedIndexSelectivity`) | index-probe selectivity |
| `superkeyJoinSelectivity` | `joinrelsize.go:213` | `calcJoinrelSize` |

**`clauselist_selectivity` has no counterpart.** There is no function that
takes a *list* of clauses and applies PG's conditioning (range-pair merging via
`addRangeClause`, `s1*s2` independence with the `estimate_multivariate_*`
extended-stats hooks, `varRelid` scoping, or the `RestrictInfo.norm_selec` /
`outer_selec` caching of doc 02 §10 item 90). Every caller multiplies
per-clause selectivities in a loop:
`calcJoinrelSize`'s residual loop, `filterSelectivity`'s conjunct walk,
`parameterizedIndexSelectivity`. Consequences: `x BETWEEN a AND b` is charged
`1/3 × 1/3 = 1/9` rather than PG's merged range selectivity; there is no
per-RestrictInfo caching, so the same clause is re-estimated at every level of
the DP.

`restriction_selectivity` / `join_selectivity` operator dispatch through
`oprrest`/`oprjoin` (item 91) is **absent** — selectivity is dispatched on
`parser.OpCode` in a Go `switch`, so a user-defined operator gets the generic
default.

`estimate_num_groups`'s `SELFLAG_USED_DEFAULT` (item 92) is reproduced only in
the Memoize path (`memoizeKeyNDistinct` returns `(nd, isDefault)`);
`estimateNumGroups` itself returns a bare `int64`.

---

## 8. Costing walkthrough examples

Same five cases as doc 02 §8, computed from goopg's formulas with goopg's
defaults. Where goopg's default differs from PG's (`work_mem` above all), both
regimes are shown. **PG's number from doc 02 §8 is quoted beside each.**

### 8.1 Equality index scan on a unique key, `loop_count = 1` and `1000`

Same table: `relTuples = 1e6`, `relPages = 10000`; index `indexPages = 2745`,
`indexTuples = 1e6`, `treeHeight = 2`; `selectivity = 1e-6`; `correlation = 0`;
`totalTablePages = 10000` (single-table query); `M = 1.0`.

**`btreeIndexAMCost`:** `numIndexTuples = 1e-6 × 1e6 = 1`;
`numIndexPages = ceil(1 × 2745 / 1e6) = 1`;
`total = 1 × 4.0 = 4.0`; `+ 1 × 0.005 = 4.005`;
`descent = (2+1) × 50 × 0.0025 = 0.375`.
→ `startup = 0.375`, `total = 4.380`. *(PG: 0.425 / 4.4325 — goopg is short by
the `ceil(log2(1e6)) × 0.0025 = 0.05` descent term and the
`1 × 0.0025` index-qual op cost.)*

**`costIndexScan`, `loopCount = 1`:** `tuplesFetched = clampRowEst(1) = 1`.
`indexPagesFetched(1, 10000, 2745, 10000, 524288)`: `T = 10000`,
`totalPages = 12745`, `b = ceil(524288 × 10000 / 12745) = 411368 ≥ T` →
`2 × 10000 × 1 / 20001 = 0.99995 → ceil = 1`. `maxIO = 4.0`.
`correlatedPages = ceil(1e-6 × 10000) = 1` → `minIO = 4.0`.
`csquared = 0` → `run = (4.380 − 0.375) + 4.0 = 8.005`; `+ 0.01 × 1 = 8.015`.

> **goopg: `startup = 0.375`, `total = 8.390`, `rows = 1`.**
> **PG: `startup = 0.425`, `total = 8.4425`.** Gap 0.6 %.

**`loopCount = 1000`:** the heap side amortises correctly —
`indexPagesFetched(1 × 1000, 10000, 2745, …) = ceil(2 × 10000 × 1000 / 21000) =
ceil(952.38) = 953`; `maxIO = 953 × 4 / 1000 = 3.812`; the correlated bound
gives the same 3.812. But `btreeIndexAMCost` **ignores `loopCount`**, so the
index side stays at `4.005` run + `0.375` startup where PG's
`genericcostestimate` with `num_scans = 1000` amortises it to `3.3915`.

> **goopg: `startup = 0.375`, `total = 0.375 + 4.005 + 3.812 + 0.01 = 8.202`
> per iteration.**
> **PG: `startup = 0.425`, `total = 7.6385`.** goopg is **7.4 % dearer**, and
> the error grows with `loop_count` — it is the missing item-31 branch.

### 8.2 Hash join `lineitem` (6e6) ⋈ `orders` (1.5e6)

goopg sizes by **column count**, so widths become `orders: ncols = 9`,
`lineitem: ncols = 16`, `avgVarBytes = 0` (no ANALYZE — the common case).
`numHashClauses = 1`. `outputRows = 6e6` (taking doc 02's `hashjointuples`).

`EntryBytes(9,0) = 9×48 + 24 = 456`; `EntryBytes(16,0) = 16×48 + 24 = 792`.
`innerBytes = 1.5e6 × 456 = 684,000,000` — **3.35× PG's 204,000,000**.

Common CPU terms: `build = (0.0025 + 0.01) × 1.5e6 = 18,750`;
`probe = 0.0025 × 1 × 6e6 = 15,000`; `emit = 0.01 × 6e6 = 60,000`.
*(PG's probe term is `0.0025 × 6e6 × clamp_row_est(1.5e6 × 1e-6 = 1.5 → 2) ×
0.5 = 15,000` — the same number by coincidence, because
`clamp(2) × 0.5 = 1`. Any skewed key breaks the coincidence and goopg cannot
see it.)*

**`hashsize.Choose` at goopg's default `work_mem = 512 MiB` (536,870,912 B):**
`ptrs = prevPow2(536,870,912/48 = 11,184,810) = 8,388,608`;
`nbuckets = nextPow2(1.5e6) = 2,097,152`; `bucketBytes = 100,663,296`;
`684,000,000 + 100,663,296 > 536,870,912` → multi-batch;
`bucketSize = 456 + 48 = 504`; `sbuckets = nextPow2(536,870,912/504 =
1,065,220) = 2,097,152`; `bucketBytes = 100,663,296`;
`avail = 436,207,616`; `dbatch = ceil(684,000,000/436,207,616) = 2` →
`nbatch = 2`. Walk-back: `2 < 536,870,912/8192 = 65,536` → stop.
→ **`NBuckets = 2,097,152`, `NBatch = 2`.**

**At `work_mem = 4 MB`:** `ptrs = prevPow2(87,381) = 65,536`;
multi-batch; `sbuckets = nextPow2(4,194,304/504 = 8322) = 16,384`;
`bucketBytes = 786,432`; `avail = 3,407,872`;
`dbatch = ceil(684,000,000/3,407,872) = 201` → `nbatch = 256`.
Walk-back: `256 < 512` → stop. → **`NBuckets = 16,384`, `NBatch = 256`.**

**Spill charge (identical in both regimes — it depends only on `NBatch > 1`):**
`innerPages = ceil(1.5e6 × 456 / 8192) = 83,497`;
`outerPages = ceil(6e6 × 792 / 8192) = 580,079`;
`startup += 83,497`; `run += 83,497 + 2 × 580,079 = 1,243,655`.

> **goopg (both 4 MB and 512 MB): startup increment `18,750 + 83,497 =
> 102,247`; run increment `15,000 + 60,000 + 1,243,655 = 1,318,655`.**
> **PG 4 MB: startup 42,188, run 347,814. PG 512 MB: startup 18,750, run
> 90,000.**

Three findings. (a) goopg's spill charge is **3.8× PG's** at 4 MB, because the
column-count entry model inflates both `innerPages` and `outerPages`, and
`spillPages`' own header admits it over-states (the batch *file* encoding is
narrower than the in-memory footprint). (b) goopg **batches at 512 MB where PG
does not**, so the "large work_mem removes the spill" transition PG shows does
not exist at goopg's default — the join is charged 1.24 M cost units of I/O in
both regimes. (c) goopg's spill charge is **step-shaped**: identical for
`NBatch = 2` and `NBatch = 256`, where PG's grows with the batch count only via
the batching decision itself (PG's charge is also `NBatch`-independent, so this
last one matches — the divergence is only the magnitude).

### 8.3 Bitmap heap scan at 1 % selectivity on a 100,000-page table

`T = 100,000`, `relTuples = 1e7`, `indexPages = 27,000`, `treeHeight = 3`,
`selectivity = 0.01`, `loop_count = 1`.

**Index side.** `numIndexTuples = 100,000`;
`numIndexPages = ceil(100,000 × 27,000 / 1e7) = 270`;
`total = 270 × 4 = 1080 + 100,000 × 0.005 = 1580`;
`descent = (3+1) × 50 × 0.0025 = 0.5`.
`costBitmapIndexScan` → `{0.5, 1580.5 + 0.1 × 0.0025 × 100,000 = 1605.5}`.
*(PG: 1855.56 — goopg is short by the `100,000 × 0.0025 = 250` index-qual op
cost and the 0.06 log₂ descent.)*

**`computeBitmapPages`.** `pages = 2 × 1e5 × 1e5 / 3e5 = 66,666.67`;
`heapPages = 66,666.67`; `loopCount = 1` → `pages = ceil = 66,667`.
`maxEntries = bitmapMaxEntries(536,870,912) = 536,870,912/320 = 1,677,721`.
`1,677,721 > 66,666.67` → **no lossiness**; `tuplesFetched` stays 100,000.
*(PG at 4 MB: `maxentries = 65,536 < 66,666.67` → lossy, `tuples_fetched`
blows up to 5,133,952.)*

**`costBitmapHeapScan`.** `startup = 1605.5`;
`pageCost = 4 − 3 × sqrt(66,667/100,000) = 4 − 2.4495 = 1.5505`;
`run = 1.5505 × 66,667 = 103,367.6`; `+ 0.01 × 100,000 = 1,000`.

> **goopg: `startup = 1605.5`, `total = 105,973.1`, `rows = 100,000`.**
> **PG: `startup = 1855.56`, `total = 169,397.2`.**

The 60 % gap is almost entirely the lossy-tuple CPU term PG charges and goopg
does not reach at its default `work_mem`. Forcing goopg to 4 MB:
`maxEntries = 4,194,304/320 = 13,107 < 66,666.67` →
`lossyPages = 66,666.67 − 6,553.5 = 60,113.17`, `exactPages = 6,553.5`,
`tuplesFetched = clampRowEst(100,000 × 0.09830 + 0.90170 × 1e7) = 9,026,806`,
so `run` becomes `103,367.6 + 90,268 = 193,636` and
`total = 195,241` — now *above* PG's, because goopg's 320-byte TBM entry
lossifies harder than PG's 64-byte one.

Seq-scan rival: `costSeqscan(cp, 100,000, 1e7, 0) = 100,000 + 100,000 =
200,000` (PG with one qual: 225,000). goopg's bitmap wins at 512 MB
(105,973 < 200,000) and roughly ties at 4 MB.

### 8.4 Sort of 1,000,000 rows: in memory vs external

goopg needs a **column count**, not a byte width. Take `ncols = 8`
(`EntryBytes(8,0) = 8×48 + 24 = 408`), so `inputBytes = 408,000,000` versus
PG's `128,000,000`.

`tuples = 1e6`; `comparisonCost = 0.005`;
`startup_cpu = 0.005 × 1e6 × log2(1e6) = 99,657.8` — **identical to PG**;
`run = 0.0025 × 1e6 = 2,500` — identical.

- **goopg default (`work_mem = 512 MiB = 536,870,912`)**: `408,000,000 <
  536,870,912` → **in-memory**. `startup = 99,657.8`, `total = 102,157.8`.
  *(PG needs 256 MB to stay in memory at its narrower width; same startup.)*
- **`work_mem = 4 MB`**: `npages = ceil(408e6/8192) = 49,806`;
  `nruns = 408e6/4,194,304 = 97.28`;
  `mergeorder = clamp(4,194,304/278,528 = 15.06, 6, 500) = 15.06`;
  `97.28 > 15.06` → `logRuns = ceil(ln 97.28 / ln 15.06) = ceil(1.688) = 2`;
  `npageaccesses = 2 × 49,806 × 2 = 199,224`;
  `disk = 199,224 × (0.75 + 1.0) = 348,642`.
  > **goopg: `startup = 448,299.8`, `run = 2,500`.**
  > **PG: `startup = 209,032.8`, `run = 2,500`.** goopg is **2.1× dearer**,
  > exactly the ratio of the two byte models (408/128 = 3.19 into `npages`,
  > damped by the log).
- **`LIMIT 10`**: **goopg has no bounded-sort arm**; it charges the full
  99,657.8 (or 448,299.8). PG charges `0.005 × 1e6 × log2(20) = 21,609.6`.
  A top-N query is priced **4.6× too high** — and since the only consumer of
  `costSortRun` is the merge-join input sort, the effect is to make merge joins
  look worse under a LIMIT than they are.

### 8.5 Parallel seqscan with 4 workers

`relPages = 100,000`, `relTuples = 1e7`, no quals, `parallel_workers = 4`.

`costSeqscan(cp, 100000, 1e7, 0) = 1.0 × 100,000 + 0.01 × 1e7 = 200,000`
(serial). *(PG: 200,000 with no quals — identical.)*

`generateScanPaths`' partial arm (test-only, §3.1):
`d = getParallelDivisor(4, true) = 4 + max(0, 1 − 1.2) = 4.0`;
`Rows = 1e7/4 = 2,500,000`;
`Cost = {0, 200,000/4 = 50,000}`.

> **goopg partial: `total = 50,000`, `rows = 2,500,000`.**
> **PG partial: `total = 125,000`, `rows = 2,500,000`.**

goopg divides the **whole** total, including the `seq_page_cost × pages` disk
term that PG explicitly refuses to divide (doc 02 §10 item 25). The partial
path is therefore **2.5× too cheap**.

**And there is no Gather.** `gatherCost` has no caller, `PathGather` is never
constructed, and the parallel decision is made afterwards by
`MaybeAddGather`'s block-count rule. So doc 02 §8.5's conclusion — that
`parallel_tuple_cost × 1e7 = 1,000,000` makes the serial plan win — has no
analogue: goopg parallelises whenever the driving scan clears
`min_parallel_table_scan_size` and the plan is parallel-safe, whatever the
row volume. `parallel_setup_cost` and `parallel_tuple_cost` are dead constants.

---

## 9. EXPLAIN surface (what a plan-parity diff needs)

`internal/executor/operators_explain.go`.

**The cost annotation is a literal.** Two sites, one for plain EXPLAIN and one
for EXPLAIN ANALYZE:

```go
// operators_explain.go:494  (walkPlanFiltered)
label += fmt.Sprintf("  (cost=0.00..0.00 rows=%d width=0)", est)
// operators_explain.go:1540 (walkPlanAnalyzeFiltered)
label += fmt.Sprintf("  (cost=0.00..0.00 rows=%d width=0)", est)
```

`startup_cost`, `total_cost` and `width` are **hardcoded zeros**. The comment
at `:493` says *"The mock 0.00 costs are replaced by 'N' in EXPLAIN
normalization"* — i.e. the plan-parity tooling normalises them away rather than
comparing them. Doc 02 §10 item 93's `%.2f..%.2f` / `width=%d` — **absent**.
There is no non-text (JSON/XML/YAML) cost emission either.

**`rows=` comes from the legacy estimator, not from the Path.**

```go
est := optimizer.EstimateRows(rowSrc)   // rowSrc = attachedFilterNode ?: n
if est <= 0 { est = 1 }
```

`rowSrc` is the collapsed `*Filter` wrapper when one was folded into the scan
line (so the printed count is post-qual), else the node itself. In the ANALYZE
walker (`:1536`) the `attachedFilterNode` substitution is **not** applied — it
reads `EstimateRows(n)` directly — so `EXPLAIN` and `EXPLAIN ANALYZE` can print
**different `rows=` for the same node** on a filtered scan. That is a concrete
parity hazard worth a ledger row.

Because `EstimateRows` walks the rebuilt `Node` tree, the number bears no
guaranteed relation to the `Path.Rows` that `add_path` compared (§6.3). For a
Gather, PG prints `compute_gather_rows`; goopg's `EstimateRows` has no Gather
arm producing that, so partial/leader row accounting is not reproduced.

**`COSTS OFF`** is handled correctly:

```go
showCosts := !opts.Set.Costs || opts.Costs
```

i.e. costs print by default and are suppressed only when the user *explicitly*
wrote `COSTS OFF` (`Set.Costs == true && Costs == false`). Matches PG.

**Relation-name dedup: present, with a documented numbering divergence.**
`internal/executor/explain_names.go` is goopg's
`select_rtable_names_for_explain` analogue. `explainNames` maps
`ColumnRef.SourceTableIdx → printed name`, disambiguating repeats with a `_N`
suffix and leaving the first bare (`date_dim`, `date_dim_1`). `qualify()`
reproduces `useprefix = es->rtable_size > 1`. Two divergences it records
itself:

- PG numbers suffixes over the **query's** range table, which includes RTEs
  goopg's plan tree never materialises (pulled-up subqueries, eliminated
  joins), so goopg's suffix for the N-th duplicate can differ from PG's.
- `nextSourceIdx` restarts at 1 for every query level, so a subquery binding
  and a base relation inside it can share a `SourceTableIdx`; the `cols` guard
  turns that case back into an unqualified name rather than a confidently wrong
  one.

**Everything else a plan diff must normalise or accept as missing:**

| PG output | goopg |
|---|---|
| `(cost=A..B rows=R width=W)` | `(cost=0.00..0.00 rows=R width=0)` — normalise A, B, W |
| `Disabled: true` | **absent** (`DisabledNodes` always 0) |
| `Parallel ` node-name prefix | present (`parallel_label_test.go` exercises it) |
| `Async ` prefix, `Async Capable` | absent |
| `Workers Planned: N` | present after Gather/GatherMerge |
| `Planned Partitions: N` (HashAgg) | absent |
| `JIT:` block | absent (no JIT) |
| `Settings:` (`EXPLAIN (SETTINGS)`) | UNVERIFIED — not found in `operators_explain.go` |
| `Sort Method` / `Batches` / `Memory Usage` | ANALYZE-only in both; goopg emits `Heap Fetches: N` for IOS |
| indent rule | reproduced exactly, including PG's non-flat `->  ` columns 0, 2, 8, 14, 20 (`:441-457`) |
| `actual rows=%.2f loops=%d` | present |

Because no cost is printed, **a plan-shape diff is the only available parity
instrument** — a cost regression is invisible in EXPLAIN. That is the single
most consequential item in this document for the refactor: any change to the
cost model has to be validated by row counts, plan shapes and wall-clock time,
never by reading EXPLAIN.

---

## 10. Fidelity table

One row per PG cost function from doc 02 §3-§6. "Item" cites doc 02 §10.

| PG function | items | goopg symbol | verdict | differing terms | inputs it lacks |
|---|---|---|---|---|---|
| `clamp_row_est` | 5 | `relsize.go:clampRowEst` | identical | — | — |
| `get_parallel_divisor` | 26 | `cost_funcs.go:getParallelDivisor` | identical (unreachable) | — | GUC `parallel_leader_participation` |
| `cost_qual_eval*` | 15-22 | `cost_funcs.go:qualEvalCost` | simplified | conjunct count × 0.0025; no procost, no node-type table, no SAOP, no CoerceViaIO, no RowCompare, no SubPlan, no caching, no pseudoconstant→startup | `pg_proc.procost`, `RestrictInfo.eval_cost` |
| `get_restriction_qual_cost` | 30 | — | **absent** | whole qpqual term | `indrestrictinfo`, `ppi_clauses` |
| `relation_byte_size` / `page_size` | 6 | `hashsize.EntryBytes` + `cost_funcs.go:spillPages` | simplified | column-count model (48 B/Datum + 24) replaces `MAXALIGN(width)+24` | per-column byte widths at cost time |
| `index_pages_fetched` | 27 | `costindex.go:indexPagesFetched` | identical | — | `root->total_table_pages` counts only searched rels |
| `approx_tuple_count` | 81 | — | **absent** | joinrel rows reused instead | cached per-clause `JOIN_INNER` selectivities |
| `set_rel_width` | 24 | `relsize.go:typeWidth`/`tupleWidth` | simplified | no `stawidth`; no whole-row +24; joinrel width = full concatenation | ANALYZE `AvgWidth` (read only for hash sizing) |
| `cost_seqscan` | 25 | `cost_funcs.go:costSeqscan` | formula identical / inputs simplified | charged over post-filter rows and `estScanPages`, not `baserel->tuples`/`pages`; `numQualOps = 0`; parallel arm test-only and divides the disk term | real `relpages`, pre-restriction `tuples`, qual op count |
| `cost_samplescan` | — | — | **absent** | — | — |
| `cost_index` | 28, 29 | `costindex.go:costIndexScan` | near-identical | no qpqual CPU term; `random` terms scaled by `GOOPG_INDEX_PROBE_MULT` | qpqual list |
| `genericcostestimate` | 31, 32 | folded into `btreeIndexAMCost` | simplified | no `num_scans` Mackert–Lohman branch; no per-tuple qual op cost; `indexStartupCost = descent` not `qual_arg_cost` | `loop_count` (ignored), `num_sa_scans` |
| `btcostestimate` | 33-35, 37 | `costindex.go:btreeIndexAMCost` | simplified | no `ceil(log2(tuples))` descent; no `num_sa_scans`/`ceil(pages/3)` clamp; no unique `numIndexTuples = 1` clamp (obtained via selectivity instead); partial indexes declined | ScalarArrayOp info, `IS NULL` info, index predicate prover |
| `btcost_correlation` | 36 | `costindex.go:indexCorrelationFor` | identical | — | correlation stat is usually missing (per-connection ANALYZE) |
| `get_relation_info` index geometry | — | `costindex.go:estimateIndexGeometry` | substituted | derives pages from key width @ fillfactor 90 unless `IndexRealPages` answers; `tree_height` from log-fanout; `indexTuples = relTuples` | `pg_class` relpages/reltuples of the index, `_bt_getrootheight`, bloat |
| `cost_bitmap_tree_node` | 41 | `costbitmap.go:costBitmapIndexScan`, `costBitmapTree` | identical | inherits `btreeIndexAMCost` gaps | — |
| `cost_bitmap_heap_scan` | 39 | `costbitmap.go:costBitmapHeapScan` | near-identical | only `cpu_tuple_cost` per tuple, no qpqual | qpqual list |
| `compute_bitmap_pages` | 38, 40 | `costbitmap.go:computeBitmapPagesLooped` | identical shape | `maxentries = work_mem/320` (PG: `/64`) | session `work_mem` |
| `cost_bitmap_and_node` | 41, 42 | `costbitmap.go:costBitmapAndCost` | identical | — | — |
| `cost_bitmap_or_node` | 41, 42 | `costbitmap.go:costBitmapOrCost` | near-identical | does not skip the tree-node markup for `IndexPath` children | — |
| `bitmap_scan_cost_est` | — | `costbitmap.go:bitmapScanCostEst` | identical | — | — |
| `cost_tidscan` / `cost_tidrangescan` | 43 | — | **absent** | — | — |
| `cost_subqueryscan` | 47 | — | **absent** | leaf priced as a seq scan over `EstimateRows` | — |
| `cost_functionscan` / `cost_tablefuncscan` | 44 | — | **absent** | — | — |
| `cost_valuesscan` / `cost_ctescan` / `cost_namedtuplestorescan` / `cost_resultscan` | 45 | — | **absent** | — | — |
| `cost_recursive_union` | 46 | — | **absent** | — | — |
| `cost_gather` | 48 | `cost_funcs.go:gatherCost` | present but **unreachable** (no caller) | — | a `PathGather` producer |
| `cost_gather_merge` | 48 | — | **absent** | — | — |
| `cost_tuplesort` | 49, 50 | `cost_funcs.go:costSortRun` | near-identical | `input_bytes` from column count with `avgVarBytes = 0`; caller's `comparison_cost` always 0 | per-type comparison cost, ANALYZE var widths |
| `cost_tuplesort` bounded arm | 51 | — | **absent** | no `limit_tuples` parameter at all | LIMIT push-down |
| `cost_sort` | 52 | `joinpathsmerge.go:sortPathFor` | simplified | input total folded into startup correctly; no `enable_sort` | — |
| `cost_incremental_sort` | 53 | — | **absent** | — | — |
| `cost_append` / `cost_merge_append` | 54, 55 | — | **absent** | — | — |
| `cost_material` | 56 | — | **absent** (executor has the node) | — | — |
| `cost_rescan` | 57 | inline at `pathgen.go:122`, `joinpathsnli.go:250` | simplified | default arm only; no FunctionScan / single-batch-HashJoin `total − startup`; no CTE/WorkTable arm | node-kind dispatch |
| `cost_memoize_rescan` | 58 | `joinpathsmemoize.go:costMemoizeRescan` | identical | byte model substituted; clamps where PG asserts | `estimate_num_groups` over param exprs |
| `cost_subplan` | 59 | `subplan_cost.go:estimateSubplanCostPerCall` | **different quantity** | returns int64 rows, not `Cost`; no hashed/EXISTS/ANY-ALL arms | `plan.total_cost`, subplan kind |
| `cost_agg` | 60-62 | `cost_funcs.go:aggCost` | present but **unreachable** (no caller); AGG_HASHED arm only | no PLAIN/SORTED arms, no spill, no HAVING | a `PathAgg` producer |
| `cost_windowagg` | 63 | — | **absent** | — | — |
| `cost_group` | 64 | — | **absent** | — | — |
| `initial_cost_nestloop` + `final_cost_nestloop` | 65, 66 | `cost_funcs.go:nestloopCost` (+ caller `qualEvalCost`) | simplified | `outerRows × rescan` not `(outerRows−1) ×`; **no SEMI/ANTI arms**; no `inner_scan_frac`; no `has_indexed_join_quals` | join type, `SpecialJoinInfo` semifactors |
| `compute_semi_anti_join_factors` | 68 | — | **absent** | — | searched seam is INNER-only |
| `has_indexed_join_quals` | 67 | — | **absent** | — | — |
| `initial_cost_mergejoin` + `final_cost_mergejoin` | 69-74 | `cost_funcs.go:mergeJoinCost` | **heavily simplified** | no `mergejoinscansel` scan fractions, no jointype forcing, no `rescanratio`, no materialize-inner decision, no `skip_mark_restore`, no startup-prefix qual charge | `mergejoinscansel`, `RestrictInfo` scansel cache, `enable_material` |
| `initial_cost_hashjoin` + `final_cost_hashjoin` | 75, 76, 80, 81 | `cost_funcs.go:hashJoinCost` | partly identical | **no `virtualbuckets`/bucket-size/MCV skew term**; no SEMI/ANTI probe variants; no `disable_cost` MCV bail-out; `hashjointuples` = joinrel rows | `estimate_hash_bucket_stats`, MCV frequencies |
| `ExecChooseHashTableSize` | 77 | `hashsize.Choose` | identical algorithm, goopg constants | 48 B slot / 456 B entry vs PG's 8 B / 136 B; no skew buckets; no parallel combined budget | tuple byte width |
| `estimate_hash_bucket_stats` | 79 | — | **absent** | — | MCV freq, ndistinct at cost time |
| parallel hash sizing | 82 | — | **absent** | — | partial paths |
| `set_baserel_size_estimates` | 83 | `cardinality.go:estimateBaseRelInfo` + `applyLocalFilterSelectivity` | simplified | `reliable` gate keeps the **pre-filter** count when selectivity is a default constant | `clauselist_selectivity` conditioning |
| `get_parameterized_baserel_size` | 84 | `pathparamindex.go:parameterizedBaserelRows` | near-identical | index-qual vs movable-clause split reproduced | `varRelid` scoping |
| `calc_joinrel_size_estimate` | 85 | `joinrelsize.go:calcJoinrelSize` | INNER only | **no LEFT/FULL floor, no SEMI/ANTI arms** in the Path model; adds a `max(l,r)` heuristic cap PG has no analogue for | join type on `RelOptInfo` |
| (legacy) join sizing | 85 | `cardinality.go:estimateJoin` + `outerJoinRowFloor` | separate model | has the LEFT/RIGHT/FULL floors and SEMI/ANTI arms the Path model lacks; different selectivity stack | — |
| `get_parameterized_joinrel_size` | 86 | — | **absent** | parameterised joinrels refused instead of sized | — |
| `get_foreign_key_join_selectivity` | 87 | `joinrelsize.go:superkeyJoinSelectivity` | extended | accepts composite UNIQUE indexes as evidence (PG requires `nkeycolumns==1` or a declared FK); adds `keyImpliedRowsBound`; no `ec_has_const` division, no `nmatched_ec` accounting, no SEMI/ANTI variant | equivalence-class const info |
| per-RTE-kind default rows | 88 | partly in `cardinality.go:EstimateRows` / `initialRelRows` | simplified | Values exact, CTE floored; no tablefunc 100 / named-tuplestore 1000 / foreign 1000 defaults | — |
| `estimate_rel_size` | 89 | `relsize.go:estimateRelSize` (+ `estimateTableRowsFallback`) | identical formula, gated application | analysed relations are **not** rescaled by the live block count | live `curpages` for analysed rels |
| `clause_selectivity_ext` | 90 | `selectivity.go:clauseSelectivity*` | simplified | no `norm_selec`/`outer_selec` caching, no `varRelid` scoping, no pseudoconstant rule | `RestrictInfo` cache slots |
| `clauselist_selectivity` | — | — | **absent** | callers multiply per-clause selectivities in a loop; no range-pair merging, no extended stats | — |
| `restriction_selectivity` / `join_selectivity` | 91 | Go `switch` on `parser.OpCode` | simplified | no `oprrest`/`oprjoin` dispatch → user-defined operators get the generic default | `pg_operator.oprrest/oprjoin` |
| `estimate_num_groups` | 92 | `cardinality.go:estimateNumGroups` | simplified | no EC de-dup, no multivariate ndistinct, no boolean short-circuit; returns no `SELFLAG_USED_DEFAULT` | extended statistics |
| `ExplainNode` cost output | 93 | `operators_explain.go:494`, `:1540` | **absent** | literal `cost=0.00..0.00`, `width=0`; `rows` from the legacy estimator; EXPLAIN vs EXPLAIN ANALYZE can disagree on a collapsed Filter line | `Path.Cost` never reaches the Node |
| (goopg-only) legacy join algo choice | — | `joincost.go:chooseInnerJoinAlgo` | **second cost model, live** | unit-row currency: hash `l+r`, merge `n·log n` with no constants, NL `l*r` | anything in PG units |
| (goopg-only) NLI admission gate | — | `nl_index_join.go:nliCostGateAccepts` | **third cost model, live** | row-unit heuristic; SEMI/ANTI compare `outerRows×matchSet` against `innerRows+outerRows`; optimistic accept without stats | — |

### Summary counts

- **Identical or near-identical:** 12 (`clamp_row_est`, `get_parallel_divisor`,
  `index_pages_fetched`, `cost_index`, `btcost_correlation`,
  `cost_bitmap_*` family, `compute_bitmap_pages`, `cost_tuplesort`,
  `cost_memoize_rescan`, `ExecChooseHashTableSize` (algorithm),
  `estimate_rel_size`, `get_parameterized_baserel_size`).
- **Present but simplified:** 10 (`cost_qual_eval`, `cost_seqscan`,
  `genericcostestimate`/`btcostestimate`, `cost_sort`, `cost_rescan`,
  `final_cost_nestloop`, `final_cost_mergejoin`, `final_cost_hashjoin`,
  `set_baserel_size_estimates`, `calc_joinrel_size_estimate`).
- **Present but unreachable:** 3 (`cost_gather`, `cost_agg`,
  `generateScanPaths`' parallel arm).
- **Absent:** 20+, dominated by the whole non-scan/non-join node family
  (append, material, incremental sort, window, group, recursive union,
  tid, subquery/function/values/CTE scans, gather-merge) and by every
  jointype other than INNER inside the Path model.
- **Extra models goopg has that PG does not:** 3 (`joincost.go` unit-row
  algorithm choice, `nliCostGateAccepts`, `estimateSubplanCostPerCall`), plus
  the parallel size rule that replaces `cost_gather`'s comparison — all four
  live in production and none is denominated in PG cost units.
