# M0122-0021 — EXPLAIN ANALYZE: nodes below a Gather report their own actuals

Status: slice 1 landed 2026-10-29 (`fc4b696a3`). The task stays open for the CTE-body, InitPlan, shared-hash-build
and Memoize-subtree gaps listed under "Remaining".

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
2. **Memoize below a Gather.** Q72's Memoize and its date_dim d1 probe have no entries in any site table, so they
   print no actuals. PG prints `(actual rows=0.19 loops=61015)` plus per-worker `Hits` lines.
3. **CTE bodies and InitPlans** have no actuals, even in serial plans. Their nodes are built lazily outside the
   instrumentation scope (loop #261 correction in the fix_plan entry).
4. **Buffers/WAL/memory** are not aggregated for sites. Each site's deltas come from pool-global counters that the
   other sites advance at the same time (ledger).
5. **`(never executed)`.** PG prints it for `nloops == 0`. goopg prints nothing for a node that has no entry.
