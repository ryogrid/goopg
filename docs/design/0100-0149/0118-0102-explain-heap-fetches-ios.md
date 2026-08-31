# 0118-0102 — EXPLAIN: `"Plan"` JSON wrapper + Index-Only-Scan `Heap Fetches` (M0118-0009, horizons enabler)

Status: accepted
Date: 2026-06-25
Spec: `postgres/src/test/isolation/specs/horizons.spec`
Milestone: M0118 (Upstream Isolation Spec Suite Pass-Through)

## Summary

**Enabler, NOT a spec promotion.** The next rung on the `horizons.spec`
ladder after 0118-0100 (JSON `->`/`->>`) and 0118-0101 (plpgsql
`EXECUTE … INTO STRICT`). Three independent, PG-faithful EXPLAIN fixes that
together let an `EXPLAIN (FORMAT json, BUFFERS, ANALYZE)` over an index-only
scan be navigated as PG emits it:

1. **`"Plan"` wrapper.** `EXPLAIN (FORMAT JSON)` now nests the plan tree under
   a top-level `"Plan"` key — `[ { "Plan": {root}, "Planning Time": …,
   "Execution Time": … } ]` — matching upstream exactly. goopg previously
   flattened the plan-node object straight into the array element
   (`[ {root} ]`), so `…->0->'Plan'->…` (which `horizons` uses) returned NULL.
2. **Index-Only-Scan label.** `describePlan`/`describePlanVerbose` gained an
   `*planner.IndexOnlyScan` case rendering `Index Only Scan using <idx> on
   <table>`; before this the node fell to the `%T` default and rendered as
   `*planner.IndexOnlyScan`.
3. **`Heap Fetches` count.** EXPLAIN ANALYZE now reports a `Heap Fetches`
   number for index-only-scan nodes (JSON key + text detail line), tallied
   from the IOS operator's non-`ALL_VISIBLE` fallback path.

## Why

`horizons.spec`'s `pruner_query` step reads:

```sql
SELECT explain_json($$
    EXPLAIN (FORMAT json, BUFFERS, ANALYZE)
      SELECT * FROM horizons_tst ORDER BY data;$$)->0->'Plan'->'Heap Fetches';
```

i.e. it expects `[ { "Plan": { …, "Heap Fetches": N } } ]`. goopg emitted
neither the `"Plan"` wrapper nor a `Heap Fetches` key, so the navigation
produced NULL (empty cell) for every permutation. These three fixes are the
EXPLAIN-output prerequisites for that navigation to resolve to a number.

## Mechanism

### `"Plan"` wrapper (`operators_explain.go`)

Both JSON emit paths (the plain `FORMAT JSON` path and the ANALYZE+JSON path)
now build `root := map[string]any{"Plan": planObj}` and attach
`Planning Time` / `Execution Time` (ANALYZE+SUMMARY) as *siblings* of `"Plan"`
rather than fields of the plan node. `[]any{root}` is marshalled as before.

### IOS label (`operators_explain.go`)

`describePlan` and `describePlanVerbose` mirror their existing `IndexScan`
arms: `Index Only Scan using <Index.QualifiedName()> on <Table.QualifiedName()>`
(verbose schema-qualifies the table).

### Heap-fetch counter (`instrument.go`, `operators_indexonly.go`)

- `nodeStats` grows a `heapFetches int64`.
- A new interface `heapFetchCounter { setHeapFetchCounter(*int64) }` is
  implemented by `indexOnlyScanOp`. In `maybeInstrument`, when the wrapped
  operator implements it, the operator is handed `&stats.heapFetches`.
- `indexOnlyScanOp` increments `*o.heapFetchCount` once per index entry whose
  heap page is **not** `ALL_VISIBLE` (the fallback branch that pins the heap),
  mirroring upstream's `IndexOnlyScanState.ioss_HeapFetches++` which fires per
  visited entry regardless of the eventual visibility verdict. The `ALL_VISIBLE`
  fast path (decode from key, zero heap reads) does not increment.
- `planToJSONWithStats` emits `"Heap Fetches"` for `*planner.IndexOnlyScan`
  nodes; `walkPlanAnalyzeFiltered` emits a `Heap Fetches: N` detail line. Both
  are ANALYZE-only (the static JSON/text paths never carry stats), matching PG.

Because the IOS materialises all rows in `Open()` (via `RangeScan`), the count
is fully populated before the EXPLAIN renderer reads it back from the table.

## Blast radius

- The `"Plan"` wrapper changes the `EXPLAIN (FORMAT JSON)` output shape for
  **all** queries — but toward PG fidelity (PG always wraps under `"Plan"`).
  The only in-repo consumer of EXPLAIN-JSON output, `internal/testutil/tpch`'s
  `index_utilisation_test`, already prefers `top[0]["Plan"]` (falling back to
  the flat form), so it is forward-compatible. No `port`/`pass`-required oracle
  or isolation test compares `FORMAT JSON` output (`horizons` is the only
  isolation spec using it, and it is `defer`). Six goopg-internal EXPLAIN-JSON
  unit tests were updated to navigate the wrapper.
- The IOS label + Heap-Fetches keys only appear for index-only-scan nodes
  (label) / under ANALYZE (count); every other plan + the non-ANALYZE paths are
  byte-identical.

## What this does NOT do (remaining horizons blockers, Effort-L)

Re-probing `horizons` after this change: the divergence is **still** the
`Heap Fetches` value column (`actual ""`). Root cause now isolated: goopg's
**planner does not produce an Index-Only Scan** for
`SELECT * FROM horizons_tst ORDER BY data` — it plans `Sort → Seq Scan`, and it
does **not honor** the spec's `SET enable_seqscan/enable_indexscan/
enable_bitmapscan = false`. So no IOS node exists in the executed plan ⇒ no
`Heap Fetches` key ⇒ the navigation stays NULL.

The next rungs, in order:

1. **Planner: honor `enable_seqscan`/`enable_indexscan`/`enable_bitmapscan`
   GUCs**, and **promote an ordered full-index scan to an Index-Only Scan** when
   the index provides the `ORDER BY` and covers the projection (today IOS
   promotion requires an equality/range `IndexScan` child; a bare `ORDER BY`
   full scan stays `Sort → Seq Scan`). Only then does the executed plan contain
   an IOS node and `Heap Fetches` becomes non-NULL.
2. **MVCC pruning-horizon core**: the actual counts (`2` vs `0`) require
   index-only-scan heap-fetch counts that reflect opportunistic pruning + that
   prune/VACUUM respect a concurrent older snapshot for **permanent** tables but
   **not** for **temporary** ones (the spec's whole point). With the IOS infra
   from this loop, permutations 1/3/4 are expected to match (live, non-pruned
   rows → 2; uncommitted/blocked prune → 2) and 2/5 to differ on temp
   prunability — the genuine MVCC work.

## Tests / gates

- New `internal/executor/explain_heap_fetches_test.go`
  (`TestExplainHeapFetchesIndexOnlyScan`): an IOS over a unique index reports
  `Heap Fetches: 2` before VACUUM (page not `ALL_VISIBLE`) and `0` after VACUUM
  (fast path); asserts the COSTS-OFF IOS label and the text `Heap Fetches:`
  line, and extracts the JSON value from under the `"Plan"` wrapper.
- Updated 6 EXPLAIN-JSON unit tests to navigate the `"Plan"` wrapper
  (`explain_analyze_test.go`, `explain_options_off_test.go`,
  `explain_render_test.go`).
- `go test ./internal/executor/ ./internal/planner/` PASS (full packages).
- `go build ./...` clean; `go vet ./internal/executor/` clean.
- Live re-probe of `horizons` confirms the EXPLAIN plumbing is correct and the
  residual divergence is the planner-IOS + MVCC-core gap above.
- pgbench smoke = pre-commit hook.
