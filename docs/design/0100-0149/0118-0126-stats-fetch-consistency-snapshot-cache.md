# 0118-0126 — `stats` enabler rung 4: `stats_fetch_consistency` cache/snapshot

**Status:** accepted
**Milestone:** M0118-0009 (Upstream isolation-spec suite pass-through — misc/system-level specs)
**Spec:** `postgres/src/test/isolation/specs/stats.spec`
**Predecessor:** [0118-0125](0118-0125-stats-transactional-drop-function.md) (rung 3 — transactional DROP FUNCTION + stats lifecycle)
**Kind:** ENABLER, not a promotion. `stats.spec` stays `defer`.

## Summary

Rung 4 of the `stats` isolation-spec enabler implements the
`stats_fetch_consistency = 'cache' | 'snapshot'` per-transaction stat-value
caching semantics, advancing the spec's first divergence from line **1587** to
line **2036** — the dead-trivial per-model permutations (L1347–L1452), the
`none`/`cache`/`snapshot` cross-transaction permutations (L1452–L1694), and the
non-existent-stats permutations (L1696–L1768) now match PostgreSQL 18.3
byte-for-byte.

The spec stays `defer`: the new first divergence (L2036) is the 2PC
`PREPARE TRANSACTION 'a'` stats interaction (`s1_prepare_a` /
`s{1,2}_{commit,rollback}_prepared_a`), which rides the same-backend 2PC work in
0118-0110 — a distinct unbuilt rung.

## Problem

PostgreSQL's cumulative-statistics views are read through a per-backend snapshot
governed by `stats_fetch_consistency` (`src/backend/utils/activity/pgstat.c`,
`pgstat_fetch_*` / `pgstat_build_snapshot`):

- **`none`** — every access reads the most-recent values from shared memory.
- **`cache`** — the first access to a given object's stats caches them until the
  end of the transaction; a *different* object accessed later still reads live.
- **`snapshot`** — the first access to *any* object copies the entire stats
  store; every subsequent access (any object) reads from that frozen copy.

The snapshot is discarded at end of transaction (`AtEOXact_PgStat`) and by
`pg_stat_clear_snapshot()`. goopg's getters
(`pg_stat_get_function_calls/_total_time/_self_time`) read the live shared store
directly (rung 2/3), and `pg_stat_clear_snapshot()` was a no-op (rung 1), so the
three consistency models were indistinguishable. The spec's discriminating
permutations are:

```
# cache: s1 reads func (→1, cached), s2 bumps func+func2, s1 re-reads
#        func (→1 cached) but func2 first-accessed now (→2 live)
s1_fetch_consistency_cache
s2_func_call s2_func_call2 s2_ff
s1_begin  s1_func_stats             -- func=1
s2_func_call s2_func_call2 s2_ff
s1_func_stats s1_func_stats2        -- func=1 (cached), func2=2 (live)
s1_commit

# snapshot: identical, but the first s1_func_stats freezes EVERYTHING,
#           so the later func2 reads its snapshot value
... s1_func_stats2                  -- func=1, func2=1 (frozen)
```

The only observable difference between the two models is `func2`: **cache → 2**
(first accessed after the concurrent bump, so live), **snapshot → 1** (frozen at
the first stats access, before the bump).

## Key insight — the snapshot only matters across statements

A snapshot/cache distinction is only observable when stats are read in **more
than one statement of the same transaction with a concurrent flush in between**.
Within a single statement no other backend's flush can interleave (the isolation
tester serialises steps), so a live read is already consistent for that
statement. Cross-statement reads of the same transaction only happen inside an
**explicit** transaction (in autocommit each statement is its own transaction).

Therefore the snapshot is built **only inside an explicit transaction**; in
autocommit (and with `none`) the getters keep reading live. This is why the
dead-trivial permutations (L1347–L1452: `… s1_func_call s1_ff s1_func_stats`,
each step its own autocommit transaction) pass unchanged regardless of model —
they read each value exactly once.

## Design

All changes are in `internal/executor`; blast radius is nil for any session that
does not read `pg_stat_get_function_*` inside an explicit transaction.

### 1. Per-transaction snapshot state (`pgstat_functions.go`)

```go
type cachedFuncStat struct { c funcStatCounters; found bool }

type funcStatSnapshot struct {
    full       bool                        // a 'snapshot'-mode full copy taken
    allFlushed map[uint32]funcStatCounters // whole store frozen at first access (snapshot)
    perObject  map[uint32]cachedFuncStat   // lazily cached single-object reads (cache)
}
```

`functionStatsManager.copyAll()` returns a by-value copy of the entire `shared`
map (mirrors `pgstat_build_snapshot` copying the whole store) for `snapshot`
mode.

`fetchFuncStat(ctx, oid)` is the single read entry point used by all three
getters:

- `ctx.Session` not a `*BasicSession`, or **not in an explicit transaction** →
  `funcStats.get(oid)` (live). This covers autocommit and `none`.
- Read `stats_fetch_consistency` via `ctx.GetSetting`:
  - **`cache`** — return the per-object cached entry if present; otherwise read
    live, store it (including a cached *absence*, so a missing OID keeps reading
    NULL for the rest of the txn), return it.
  - **`snapshot`** — on first access build `allFlushed = copyAll()` and set
    `full`; thereafter return `allFlushed[oid]` (a then-absent OID stays NULL).
  - **`none`/default** — live read.

### 2. Snapshot lifecycle (`session.go`)

`BasicSession` gains a `statsSnapshot *funcStatSnapshot` field:

- `ensureStatsSnapshot()` lazily allocates it (with an initialised `perObject`
  map) on first use.
- `EndExplicitTransaction()` sets it to `nil` — PG's `AtEOXact_PgStat` drops the
  stats snapshot at every transaction boundary, so the next transaction reads
  live again. (COMMIT, ROLLBACK, and ROLLBACK on error all route through
  `EndExplicitTransaction`.)
- `ClearStatsSnapshot()` sets it to `nil` for `pg_stat_clear_snapshot()`.

### 3. Getter wiring (`expr.go`)

The three `pg_stat_get_function_*` getters call `fetchFuncStat(ctx, oid)` instead
of `funcStats.get(oid)`. `pg_stat_clear_snapshot()` now calls
`sess.ClearStatsSnapshot()` instead of being a no-op.

## Why the boot default `cache` is safe

The GUC's boot value is `cache` (matching PG, registered in 0118-0123). So normal
sessions now take the cache path for `pg_stat_get_function_*` reads inside an
explicit transaction. This is faithful to PG (same default, same semantics) and
touches nothing else: the snapshot is per-object, function-stats-only, and
discarded at every transaction boundary. TPC-H / pgbench / other isolation specs
do not read these getters inside a multi-statement transaction, so they are
byte-unchanged.

## Testing / gates

- New `TestFetchFuncStatConsistency` (executor) drives `none`/`cache`/`snapshot`
  through `fetchFuncStat`, asserting the cross-statement freeze behaviour, the
  per-object vs whole-store distinction, transaction-end discard, and
  `pg_stat_clear_snapshot` — including the discriminating `func2` case
  (cache → live, snapshot → frozen). PASS (also under `-race`).
- `go test ./internal/executor/ ./internal/config/` PASS;
  `go build ./...` + `go vet ./internal/executor/` clean.
- `TestPort_PLpgSQL*` function tests PASS (no regression to function execution).
- `TestPort_IsolationStats` soft probe: first divergence **L1587 → L2036**
  (the `stats_fetch_consistency` permutations now match PG 18.3); spec stays
  `defer` (CSV `D-002` unchanged).
- pgbench CI-parity smoke = `.githooks/pre-commit`.

## Remaining rungs (each Effort-L; spec stays `defer`)

- **L2036 — 2PC stats** (`s1_prepare_a` / `s{1,2}_{commit,rollback}_prepared_a`):
  `PREPARE TRANSACTION 'a'` then COMMIT/ROLLBACK PREPARED; goopg errors
  "prepared transaction … does not exist". Rides 0118-0110 same-backend 2PC.
- **Relation tuple stats** (`pg_stat_get_numscans` / `_tuples_*` /
  `pg_stat_get_xact_*`, L2130+).
- **SLRU stats** (`pg_stat_slru`).
