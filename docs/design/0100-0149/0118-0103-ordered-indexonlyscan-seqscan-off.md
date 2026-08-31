# 0118-0103 — Ordered IndexOnlyScan under `enable_seqscan = off` (M0118-0009 horizons enabler)

**Status:** accepted
**Scope:** `internal/catalog`, `internal/planner`, `internal/server` (planner enabler — NOT a spec promotion)
**Spec target:** `postgres/src/test/isolation/specs/horizons.spec` (stays `failed`/`defer`)
**Predecessors:** 0118-0100 (`->`/`->>`), 0118-0101 (`EXECUTE … INTO STRICT`), 0118-0102 (EXPLAIN `"Plan"` wrapper + IOS `Heap Fetches`)

## Problem

`horizons.spec` verifies pruning/vacuum horizon behaviour through an **ordered
index-only scan**, and asserts the plan shape directly:

```
step pruner_query_plan { EXPLAIN (COSTS OFF) SELECT * FROM horizons_tst ORDER BY data; }
```

with expected output (no Sort — the index provides the order):

```
Index Only Scan using horizons_tst_data_key on horizons_tst
```

The `pruner` session runs `SET enable_seqscan = false; SET enable_bitmapscan =
false;` in its setup, so PostgreSQL prefers an IOS that satisfies the `ORDER BY`
for free over a Sort-over-SeqScan.

goopg's planner is rule-based and **ignored the planner-toggle GUCs entirely**
(`internal/config/defaults.go`: "v0's planner ignores them — the SET succeeds so
test scripts don't trip"). For `SELECT * FROM t ORDER BY data` with no WHERE it
built `Project(Sort(SeqScan))` unconditionally. The existing IOS promotion
(`tryPromoteIndexOnlyScan`, M0046-0004) only fires when the Project's child is an
`*IndexScan` produced by an equality/range WHERE — a bare ORDER-BY-only scan
stayed `Sort → Seq Scan`, so `pruner_query_plan` diverged and (per 0118-0102) the
`Heap Fetches` JSON key was NULL because no IOS node existed.

## Change

Add an **ordered-full-index → IndexOnlyScan** promotion, gated on the session's
`enable_seqscan = off`, that mirrors PG's choice of an order-providing covering
index once a SeqScan is disabled. Three pieces:

1. **GUC threading (`internal/server/dispatch.go`).** Both `sessionPlanCatalog`
   and `ctxPlanCatalog` already build a per-session `*catalog.SearchPathCatalog`
   carrying statement-scoped planner inputs (`TempOwnerToken`,
   `SnapshotPartitionDetachEpoch`). They now also read `enable_seqscan` (bool
   GUCs normalise to `"on"`/`"off"`) and set the new
   `SearchPathCatalog.DisableSeqScan` when it is `off`.

2. **Planner carrier (`internal/planner/planner.go`).** `currentSeqScanDisabled`
   walks the catalog wrapper chain (peeling `Unwrap()`, exactly like
   `currentTempOwner`/`currentPartitionDetachEpoch`) and returns whether any
   wrapper reports `SeqScanDisabled() == true`. Returns `false` when no carrier
   is attached (internal/test contexts) so legacy plans are unchanged.

3. **Promotion (`tryPromoteOrderedIndexOnlyScan`).** Called in `planSelect`'s
   promotion block as an `else if` after `tryPromoteIndexOnlyScan`. Conditions
   (deliberately conservative — only the shape horizons needs):
   - the session set `enable_seqscan = off`;
   - `proj.Child` is a `*Sort` directly over a bare `*SeqScan` (no WHERE/Filter);
   - every projected target is a `*ColumnRef`;
   - every ORDER BY key is a `*ColumnRef`, **ascending, NULLS LAST** (the default
     B-tree order goopg's full-range `RangeScan` produces);
   - there is a non-partial **B-tree** index (`!DeclaredHash`, `!HasPredicate`,
     `Method == "" || "btree"`) whose leading key columns match the ORDER BY keys
     in order with default ASC/NULLS-LAST per-column ordering
     (`ColDescending`/`ColNullsFirst` unset), and whose key + INCLUDE columns
     cover every projected column.

   On a match it returns an `IndexOnlyScan{Table, Index, Covered, schema}` with
   **nil** `Key`/`Keys`/`LowKey`/`HighKey` — the executor `RangeScan`s the whole
   index in ascending key order, so the Sort (and the absorbed Project) are
   dropped. This reuses the already-ordered `indexOnlyScanOp` scan path
   (`tree.RangeScan(loBytes=nil, hiBytes=nil, …)`); no executor change.

## Why gate on `enable_seqscan = off`

The promotion changes scan/plan selection — the project's highest-blast-radius,
most expensive failure mode. PG picks the IOS here precisely *because* the
SeqScan was disabled (`disable_cost` penalty), so gating on the same GUC is
PG-faithful AND bounds the blast radius to sessions that explicitly disabled
seqscan (essentially isolation/regression test scripts like horizons). The
default-toggle path — TPC-H, pgbench, every ordinary query — never sets the GUC,
so `currentSeqScanDisabled` returns false and the new branch is an immediate
no-op return (one cheap wrapper-chain walk). Plans there are byte-identical.

## Correctness notes

- **Ordering.** A full-range B-tree `RangeScan` returns ascending, NULLS-LAST
  order. The promotion only fires for ASC/NULLS-LAST ORDER BY keys matching the
  index's default per-column ordering, so eliminating the Sort preserves the
  result order. DESC / NULLS FIRST / non-default index ordering disqualify.
- **Coverage.** Only columns present in the index key or INCLUDE list are
  covered; any other projected column aborts the promotion (the query keeps its
  Sort-over-SeqScan).
- **No locking clauses.** The promotion shares `planSelect`'s `len(s.Locking)==0`
  guard with the existing IOS promotion (FOR UPDATE/SHARE need the heap leaf).

## Result

`horizons.spec`'s `pruner_query_plan` now renders
`Index Only Scan using horizons_tst_data_key on horizons_tst` byte-for-byte, and
the `EXPLAIN (FORMAT json …)->0->'Plan'->'Heap Fetches'` navigation resolves to a
real IOS node. Re-probe isolates the residual blocker to the **MVCC
pruning-horizon core** (Effort-L, the spec's actual subject):

```
L125 expected 0 / actual 2   # temp table: deleted rows SHOULD be prunable despite
                             #   a concurrent older snapshot → 0 heap fetches; goopg
                             #   does not opportunistically prune the temp heap.
L244 expected 2 / actual 0   # permanent table: VACUUM must NOT remove rows still
L254 expected 2 / actual 0   #   visible to lifeline's older RR snapshot → stay 2;
                             #   goopg VACUUM ignores the concurrent horizon → 0.
```

So the spec stays `failed`. The next (final) rung is the pruning/VACUUM horizon:
opportunistic prune during the IOS that respects the global xmin horizon for
permanent relations but treats temp relations as always-prunable
(`GlobalVisHorizon` / `vacuum_get_cutoffs` `OldestXmin` vs the temp short-circuit),
and the matching VACUUM cutoff. That is a separate MVCC change with its own race
gate (`go test -race ./internal/mvcc/... ./internal/wal/...`).

## Gates

- `TestOrderedIndexOnlyScanPromotedWhenSeqScanDisabled` / `…NotPromotedByDefault`
  / `…RequiresAscending` (planner) PASS.
- Full `internal/planner` + `internal/catalog` + `internal/executor` +
  `internal/server` suites PASS, no regression.
- horizons re-probe: `pruner_query_plan` + `explain_json` IOS navigation now
  match; only the 3 pruning-count lines differ (above).
- `go build ./...` + `go vet` clean; pgbench smoke = pre-commit hook (TPC-H plans
  unchanged by construction — the GUC gate makes the new branch a no-op there).
