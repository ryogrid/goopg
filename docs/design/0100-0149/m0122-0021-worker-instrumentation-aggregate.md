# M0122-0021 — EXPLAIN ANALYZE: nodes below a Gather report their own actuals

Status: slices 1–4 landed 2026-10-29 — `881b3e314` (worker fold), `28a5ff4c9` (NLI inner / Memoize), `132bf92d2`
(CTE bodies, SubPlans/InitPlans, cooperative builds, `(never executed)`), `b93ad5337` (shared hash prebuild, worker
lazy subtrees). The task stays open for the ledgered residuals listed under "Remaining residuals" (end of file).

## Problem

A parallel plan runs one copy of the Gather's subtree per execution site: one per launched worker, plus the leader
when it participates. goopg gives each site its own fresh stats table (`gatherOp.buildChildForSlot`) and, at Close,
copies the per-node counters into `Context.GatherWorkerStats` (`foldGatherWorkerStats`), one entry per site. Nothing
ever folded those entries back into one per-node entry. A node below a Gather therefore had no entry in the main stats
table, and EXPLAIN ANALYZE showed:

- no `(actual …)` on the node's own line;
- one `Worker N:  actual …` line per site, printed without VERBOSE. The leader's slot printed as one more
  `Worker N:`, so a 3-worker plan showed `Worker 0..3`;
- for the inner probe of a parameterised nested loop, rows counted before the moved Filter. The M0122-0017
  subtraction read the join's main-table entry, and below a Gather the join had no such entry;
- no `Rows Removed by Join Filter` and no `Heap Fetches` for such nodes, because both read the main table.

TPC-DS Q72 (SF0.25): only 8 of the plan's 26 node lines carried actuals, and 60 `Worker N:` lines carried the rest.
PG prints 26 node lines with actuals and no worker lines.

## PG behaviour

- `ExecParallelRetrieveInstrumentation` (execParallel.c) adds each worker's `Instrumentation` into the node's own
  with `InstrAggNode` (instrument.c). `startup`, `total`, `ntuples`, `nloops`, `nfiltered1` and `nfiltered2` are
  summed. The leader's own run already counts in that same struct.
- `ExplainNode` (explain.c) divides the summed totals by the summed `nloops` for the node line.
- `ExplainNode` prints per-worker general lines only when `es->workers_state && es->verbose`. They come from
  `worker_instrument`, which holds workers only (no leader). A worker with `nloops <= 0` is skipped. With TIMING
  OFF the `time=` part is left out.
- Node-specific worker details print without VERBOSE: Sort's `Worker N:  Sort Method`, Memoize's
  `Worker N:  Hits`, and so on. This slice does not change them.

## Fix

- `aggregateWorkerInstrumentation` (`internal/executor/instrument.go`) runs in `explainOp.Open` after the inner
  plan closes, before either renderer. For each node in `GatherWorkerStats` it builds a single `nodeStats` summed over
  all of that node's sites: rows, loops, startup, total, filter, join-filter and probe-filter rejections, and heap
  fetches.
  - The site entries are the complete population, since the leader is one of the sites. So they replace the counters
    of any existing entry instead of adding to them. Other fields, such as buffers, come from the existing entry.
  - A node that no site ever opened gets no entry (see "Remaining", shared hash build).
- `workerNodeStat` now carries `JoinFilterRejected`, `ProbeFilterRejected`, `HeapFetches` and `Leader`. The fold
  sets `Leader` on the last slot.
- The `Worker N:` block in `walkPlanAnalyzeFiltered` follows the explain.c rules above: it prints only with VERBOSE,
  skips the leader and zero-loop sites, and omits `time=` under TIMING OFF. For a parameterised probe it subtracts
  that site's own probe share, read from the join's entry for the same slot (`paramProbeRejectedAtSite`).
- Every downstream reader now sees the aggregate without further changes: the node line, the M0122-0017
  subtraction, both `Rows Removed` lines, `Heap Fetches`, the Hash line's `hashInputStats`, and the JSON/YAML/XML
  renderer's actual fields.

## Verification

- Unit tests (`internal/executor/explain_parallel_workers_test.go`):
  - `TestAggregateWorkerInstrumentationSumsSites`: the sums, replace-not-add, an existing entry left unmutated, and a
    node that never looped getting no entry.
  - `TestExplainAnalyzeParallelNodeLineAggregatesSites`: with leader participation, the scan's `loops=` equals the
    sum over sites, rows × loops gives the table's rows, there are no worker lines without VERBOSE, VERBOSE prints no
    leader line, and TIMING OFF prints no `time=`.
  - `TestExplainAnalyzeParallelWorkerLinesShape` now runs under VERBOSE.
- Live check: Q72 on the SF0.25 private clone (`tmp/q64-clone`, `:5592`), compared with the PG 18.3 `:65438` capture.
  Captures are in `analysis/m0122/m0122-0021/q72-goopg-slice1{,-verbose}.explain`.
  - Node lines with actuals went from 8 to 24 (PG: 26).
  - `Index Scan using inventory_pkey on inventory (actual rows=9.88 loops=2185)` with `Rows Removed by Filter: 191`
    is identical to PG.
- Gates: units, tpch-spotcheck, tpch-acceptance-arm (24 MATCH) and the tpcds-sf025 sweep all PASS. PLAN-SHAPE
  same=99.

## Remaining (the task stays open)

1. **Shared hash build input.** `prebuildSharedHashJoins` builds a hash join's inner once, in the leader, from an
   uninstrumented tree (scope nil), before any site runs. Every site then reports `loops=0` for the build input.
   Q72's `Hash` over `household_demographics` has no actuals, where PG has `rows=1200.00 loops=4` (each participant
   builds its own copy when Parallel Hash is off). A faithful report needs a design call, because goopg does this
   work once where PG does it per participant.
2. ~~Memoize below a Gather~~ — landed as slice 2 (below). Residuals: the `Hits:` line below a Gather shows one
   site's counters rather than PG's leader line + per-worker `Worker N:  Hits:` lines, and the NLI probe's own
   `Cond` filter is evaluated inside `indexScanOp` without a rejection counter, so it prints no
   `Rows Removed by Filter` (PG: `Rows Removed by Filter: 1` on Q72's d1 probe).
3. **CTE bodies and InitPlans** have no actuals, even in serial plans. Their nodes are built lazily outside the
   instrumentation scope (loop #261 correction in the fix_plan entry).
4. **Buffers/WAL/memory** are not aggregated for sites. Each site's deltas come from pool-global counters that the
   other sites advance at the same time (ledger).
5. **`(never executed)`.** PG prints it for `nloops == 0`. goopg prints nothing for a node that has no entry.

## Slice 2 — the nested-loop index join's inner side

The cause was not the Gather. `NestedLoopIndexJoin`'s inner probe (an Index Scan, Index Only Scan or Bitmap Heap Scan)
and its optional Memoize (`InnerMemo`) are not Operators. The join drives them through `nliInner`
(`BindOuter` / `Rescan` / `Next`), so `buildNode` builds them directly and `maybeInstrument` never wraps them. Both
lines therefore printed no actuals, in serial plans as well as below a Gather.

- PG instruments both nodes. The Memoize gets one loop per rescan (`ExecReScanMemoize` per outer row). The probe gets
  one loop per cache miss, because only a miss calls it (nodeMemoize.c `ExecMemoize`).
- `instrumentedNLIInner` / `instrumentedMemoProbe` (instrument.go) are `maybeInstrument`'s sidecar for this
  protocol:
  - a loop per `Rescan`, which is timed like an Operator's Open;
  - rows and time per `Next`;
  - stats filed under the plan node in the active scope. The probe is keyed on `NestedLoopIndexJoin.Inner`, which
    is the same node as `InnerMemo.Child`, and the cache on `InnerMemo`.
  - Per-site scopes below a Gather fold these the same way slice 1 folds any other node.
- `unwrapNLIInner` strips the sidecar for the FOR UPDATE TID-provider walks (`nliInnerIndexScan`, and lockrows'
  bitmap arms), which need the concrete probe.
- Q72 SF0.25:
  - `Memoize (actual rows=0.19 loops=61015)` is identical to PG.
  - The d1 probe reports `loops=2954`; PG reports 2852. The per-worker caches fill differently.
  - Serially, every node line carries actuals, and probe loops equal misses (1718).
  - Captures: `analysis/m0122/m0122-0021/q72-goopg-slice2{,-serial}.explain`.
- Test: `TestMemoizeAnalyzeNodeActuals`. Gates: units, tpch-spotcheck, tpch-acceptance-arm (24 MATCH) and the sf025
  sweep all PASS. Census `routing-20261029b`: 22/22, 99/99, 99/99.

## Slice 3 — CTE bodies, SubPlans / InitPlans, cooperative builds, `(never executed)`

These subtrees are built outside the top-level operator tree, so before this slice none of them printed actuals,
serially as well as in parallel plans.

| subtree | where it was built | fix |
|---|---|---|
| materialized CTE body | `newCteScanOp` with `Build()` (no scope) | builds under the scan's own scope |
| SubPlan / InitPlan inner plan | `acquireSubPlanOp`, lazily during execution | `explainOp` publishes its instrumenter as `Context.explainScope` for its run; `acquireSubPlanOp` builds under it. Workers get nil, because `NewWorkerContext` copies fields explicitly |
| cooperative hash-build input (E-18) | producer goroutines, nil scope; the tree `buildNode` made is never opened | each producer gets a fresh table; `foldCoopBuildStats` folds them as **one** execution (rows summed, loops +1, earliest startup, latest total), which is what PG's serial Hash child reports |

- `scopeStats`: every build of one plan node within a scope files into the same record, because PG keeps one
  Instrumentation per plan node across all its loops. As a result:
  - the CTE reference that replays from the cache never opens its body and adds nothing;
  - a SubPlan rebuilt per call (`rescanRebuild`) accumulates its loops instead of each build overwriting the last.
- `(never executed)` (explain.c `ExplainNode`, `nloops == 0`) now prints on the node line and on the synthesized
  Hash line. Every executing path now files into its node's record, so a zero-loop record means the node really
  did not run.
- The Memoize `Hits:` line prints only when `cache_misses > 0` (`show_memoize_info`).
- The memoized probe is keyed on `InnerMemo.Child`, the node EXPLAIN renders. A rewrite that replaces `Inner`
  (unwrap's remap, the inner-join qual pushdown) can leave it distinct from `Inner`.
- Measured on TPC-DS SF0.25 serial (`analysis/m0122/m0122-0021/ds-g{14,23}-serial-slice3.explain` vs
  `tmp/rc261/p{14,23}.txt`):
  - Q14: node lines with actuals 35 → 76 (PG: 76).
  - Q23: 23 → 41 actuals plus 6 `(never executed)`. PG has 41 plus 6, on the same nodes.
- Tests: `TestExplainAnalyzeCTEBodyAndSubPlanActuals` (CTE body, InitPlan, correlated SubPlan, never-executed inner)
  and `TestExplainAnalyzeCoopBuildInputActuals` (mutation-checked).
  `TestExplainAnalyzeSubPlanScopeObservation` had pinned the old gap; it now requires actuals.
- Residuals (ledgered):
  - the SubPlan/InitPlan header still carries goopg's `(calls=… rebuilds=… rescans=… hits=… misses=…)`
    annotation, which PG does not print;
  - goopg's correlated-SubPlan value cache skips executions PG performs, so loops can come out lower than PG's;
  - SubPlans inside parallel workers and the other expression-level subqueries built with `Build()` (ARRAY(),
    row IN, multi-assignment SET, the correlated hash map) stay uninstrumented.

## Slice 4 — the Gather's shared hash prebuild, worker lazy subtrees

- `prebuildSharedHashJoins` builds each shareable hash join's table **once**, in the leader, from a throwaway tree.
  The participants adopt the table and never open their own build child, so before this slice the build input had no
  record anywhere (TPC-DS Q72's `household_demographics` Hash).
  - Fix: the function now takes the caller's scope. Under ANALYZE, Gather and Gather Merge pass a fresh table
    (`prebuildScope`) and fold it at Close as a leader-flagged site (`foldPrebuildStats`). The input therefore
    reports `loops=1` with every row.
  - The cooperative build's own nested prebuild folds into the statement scope instead.
- **Execution difference (recorded, not imitated):** PG with `parallel_hash` off has every participant build its own
  copy, so it prints `Hash (actual rows=1200.00 loops=4)`. goopg builds once and shares, so it prints `loops=1`.
  Reporting PG's count would describe work goopg did not do.
- `buildChildForSlot` gives each worker Context its site instrumenter as `explainScope`. Worker SubPlans and
  cooperative builds then file into that worker's table, which only its goroutine writes, and fold the same way slice 1
  folds other site entries.
- `aggregateWorkerInstrumentation` now **adds** site sums to an existing statement-scope entry instead of replacing
  it. Since slice 3, lazily built leader-side work (SubPlans, cooperative builds) lands in the statement scope, while
  the eager site trees still file only into site tables, so nothing is counted twice.
- Q72 (SF0.25): node lines with actuals went from 24 to 26 (PG: 26). Capture:
  `analysis/m0122/m0122-0021/q72-goopg-slice4.explain`.
- Test: `TestExplainAnalyzeSharedPrebuildInputActuals`, mutation-checked. It switches off only the runtime cooperative
  build, because the cooperative path (slice 3) otherwise masks the plain prebuild.
- `-race` on the parallel/coop/explain tests is clean. Gates PASS. Census `routing-20261029d`: 22/22, 99/99, 99/99.

## Remaining residuals (each ledgered 2026-10-29)

1. ~~The SubPlan/InitPlan header carries goopg's `(calls=… rebuilds=… rescans=… hits=… misses=…)` annotation~~ —
   dropped 2026-10-29 (`04cc96f3b`). PG prints the bare label (explain.c `ExplainSubPlans`), and the subtree's work now
   shows through each node's own actuals. The counters stay in `ctx.SubPlanStats` for tests and diagnostics (they were
   the correlated-subquery planning bundle's gate-V6 instrument).
2. Expression-level subqueries built with `Build()` print no actuals: ARRAY(), row-constructor IN, multi-assignment
   SET, and the correlated hash map.
3. Under a Gather, the Memoize `Hits:` line shows one site's counters. PG prints the leader line plus one
   `Worker N:  Hits:` line per worker.
4. The NLI probe's own `Cond` filter has no rejection counter, so it prints no `Rows Removed by Filter`.
5. Buffers/WAL/memory are not aggregated across sites, because their counters are pool-global.
6. goopg's correlated-SubPlan value cache lowers loops relative to PG. This is an execution difference.
7. A shared or cooperative build reports one loop where PG reports one per participant. This is an execution
   difference.
