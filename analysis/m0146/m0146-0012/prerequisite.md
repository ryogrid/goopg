# M0146-0012 prerequisite: executor rescan semantics over a correlated leaf (2026-10-05)

The task's precondition is to prove that a hash build, Memoize, Materialize
or CTE cache over a correlated leaf is rebuilt for each outer binding (PG's
chgParam/extParam signalling) before correlated restrictions become leaf
quals.

## Per-operator state: safe by construction

`classifySubPlan` (`internal/executor/subplan.go`) chooses how a SubPlan body
is re-run for each outer row:

- **rescanReOpen** for Filter/Project/Aggregate/Limit/Distinct chains over
  SeqScan/IndexScan/Values leaves. None of these caches anything across
  outer rows.
- **rescanCloseOpen** for any join (`Join`, `NestedLoopIndexJoin`), Sort,
  IncrementalSort, WindowAgg, LockRows, IndexOnlyScan and the single-producer
  BitmapHeapScan. Close+Open reconstructs runtime state: hash tables, the
  NL inner cache and sort buffers.
- **rescanRebuild** (Build afresh) for every other node, including
  Materialize, Memoize, CTEScan, Gather and Append.

So no per-operator cache survives into the next outer binding. A correlated
leaf placed under a join would be re-read with the new binding.

## Context-level caches

| cache | key | verdict |
|---|---|---|
| sublink result caches (`subqCacheSafe` / `subqCacheScoped`) | outer row or bound PARAM values; disabled for volatile bodies | safe |
| `CorrSubqHashMaps` | built only for `Project(Filter(SeqScan, col = OuterColumnRef))` | safe for the shape it serves |
| `CTERowCache` | CTE declaration key, not the outer binding | **stale: M0146-0050** |

## SQL probes (`rescan-probe.sql`, PG 18.3 vs goopg)

Seven correlated bodies were probed: hash join, sort+limit, NL+Materialize,
a correlated MATERIALIZED CTE, a correlated derived table on the hash build
side, LATERAL with a HashAggregate, and EXISTS over a grouped join.

- All match PG except the correlated CTE. That one returns 81 for every
  outer row, PG returns 75–99: M0146-0050.
- Today's plans never put a correlated conjunct at a leaf under a join. It
  sits as a Filter above the Hash Join or Nested Loop, so the probes cannot
  yet reach the shape this task creates. The guard test belongs in the slice
  that first produces it.

## Verdict

The prerequisite holds for every operator. The one hole is M0146-0050 (S2,
awaiting owner placement). Until it is fixed, a correlated CTE must not be
treated as rescan-safe.
