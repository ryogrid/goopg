# 0118-0125 — `stats` enabler rung 3: transactional DROP FUNCTION + stats lifecycle

**Status:** accepted
**Milestone:** M0118-0009 (Upstream isolation-spec suite pass-through — misc/system-level specs)
**Spec:** `postgres/src/test/isolation/specs/stats.spec`
**Predecessor:** [0118-0124](0118-0124-stats-function-statistics-enabler.md) (rung 2 — cumulative function statistics + setup-result echo)
**Kind:** ENABLER, not a promotion. `stats.spec` stays `defer`.

## Summary

Rung 3 of the `stats` isolation-spec enabler makes **DROP FUNCTION
transactional** and finishes the **function-stats lifecycle** semantics, advancing
the spec's first divergence from line **449** to line **1587** — the whole
DROP-FUNCTION cross-session-visibility block (commit + rollback), the
autocommit-drop-drops-stats cases, and the `pg_stat_reset*` zeroing cases now
match PostgreSQL 18.3 byte-for-byte.

The spec stays `defer`: the new first divergence (L1587) is
`stats_fetch_consistency = 'cache'/'snapshot'` (per-transaction stat-value
caching), and a later one (L2026) is the 2PC `PREPARE TRANSACTION` stats
interaction — each a distinct unbuilt rung.

## Problem

The `stats` spec drops `test_stat_func()` in several ways and checks both the
catalog visibility and the cumulative stats afterwards:

1. **Uncommitted DROP, cross-session (perm 198/201).** `s1 BEGIN; … DROP
   FUNCTION test_stat_func(); …` and a concurrent `s2` must still
   `SELECT test_stat_func()` until `s1` commits (PG transactional-DDL
   visibility). goopg keeps all routines in one shared registry and removed the
   routine immediately, so `s2`'s call `42883`'d. On `s1 COMMIT` the function —
   and its cumulative stats — must disappear; on `s1 ROLLBACK` both survive.

2. **Autocommit DROP drops stats (perms 206-227).** `DROP FUNCTION` in
   autocommit removed the routine from the registry but left its
   `pg_stat_get_function_*` counters behind, so the getters returned the stale
   count instead of NULL.

3. **`pg_stat_reset_single_function_counters` / `pg_stat_reset` ZERO, not
   delete.** PG resets the shared entry's counters to 0 but keeps the entry, so
   a subsequent getter returns `0` (not NULL). Rung 2 deleted the entry, so the
   getter wrongly returned NULL. (A reset of an OID with no entry stays NULL —
   PG does not materialise a zeroed entry.)

## Design

### Transactional DROP FUNCTION (deferred removal)

Mirrors the existing deferred DROP TABLE / DROP INDEX precedents
(`PendingTableDrop`, design 0118-0117/0081).

- New `executor.DeferredRoutineDrop{Routine, SavepointDepth}` + a
  `BasicSession.deferRoutineDrops` list with `AddDeferredRoutineDrop` /
  `TakeDeferredRoutineDrops` / `CancelDeferredRoutineDropsToDepth` /
  `TakeDeferredRoutineDropMatching`. `EndExplicitTransaction` clears the list.
- `execDropFunction`: **inside an explicit transaction**, the target routine is
  *resolved* (so a missing/ambiguous function still errors at statement time)
  via the new read-only `Routines.ResolveByName` / `ResolveBySig` (the twins of
  `DropByName` / `Drop`, same `ErrRoutineNotFound` / `ErrRoutineAmbiguous`
  contract) but **not removed** — a `DeferredRoutineDrop` is recorded instead.
  The routine therefore stays in the shared registry (and keeps its stats) so a
  concurrent session resolves and calls it until commit.
- **Commit** (`ApplyDeferredRoutineDrops`, called before `TxnMgr.Commit` on both
  the `execCommit` path and the simple-query dispatch path): performs the real
  `rs.DropRoutine` + `funcStats.dropFunction(oid)` for each deferred entry.
- **Rollback**: `TakeDeferredRoutineDrops` simply discards the entries (the
  routines were never removed). **ROLLBACK TO savepoint**:
  `CancelDeferredRoutineDropsToDepth` drops entries recorded at the rolled-back
  depth, so the function survives the outer commit.

### Autocommit DROP also drops stats

The autocommit branch of `execDropFunction` now *resolves first* (to obtain the
OID), then `rs.DropRoutine(target)` **and** `funcStats.dropFunction(target.OID)`
— mirroring `pgstat_drop_function` on the implicit commit.

### `functionStatsManager` lifecycle

- New `dropFunction(oid)`: deletes the OID from `shared` **and** from every
  session's `pending`, so the getter returns NULL and a concurrent backend's
  stale pending count is not revived into `shared` on its next flush
  (`pgstat_drop_function`).
- `resetSingle(oid)` / `resetAll()` now **zero the existing entries in place**
  (kept, not removed); a reset of an OID with no entry is a no-op.

### DROP-then-CREATE same signature in one transaction

A deferred drop would otherwise leave the old routine registered, so a
`CREATE FUNCTION` of the same signature in the same transaction would collide
and the deferred drop would later remove the freshly created routine. Guard:
`execCreateFunction` calls `TakeDeferredRoutineDropMatching(schema, name, sig)`
before `rs.Create`; on a match it applies the deferred drop *early* (removes the
old routine + its stats), so the recreate proceeds cleanly and nothing extra
happens at commit.

## Known limitation

Like the deferred DROP TABLE precedent, the dropping session itself keeps seeing
its own dropped function until commit (goopg's shared catalog has no per-session
MVCC visibility). The `stats` spec never calls `test_stat_func` from the
dropping session after the drop, and a DROP-then-CREATE of the same signature is
handled by the recreate guard, so the limitation is not observable here. Full
per-session catalog MVCC is the shared `alter-table-4` gap, tracked separately.

## Blast radius

- Deferral engages only inside an explicit transaction; autocommit DROP FUNCTION
  keeps its immediate behaviour (now also dropping stats). The resolve twins are
  behaviour-identical to `DropByName`/`Drop`.
- Function-stats lifecycle only changes already-stats-tracked functions
  (`track_functions != 'none'`, boot `none`). Default sessions (TPC-H, pgbench,
  all other specs) are byte-unchanged.

## Gates

- New `internal/executor/deferred_routine_drop_test.go`
  (`TestDeferredRoutineDropSession`), updated `TestFunctionStatsManager`
  (zeroing + `dropFunction`), new `internal/catalog` `TestRoutinesResolveForDrop`
  — PASS.
- `go test ./internal/executor/... ./internal/catalog/... ./internal/server/...`
  PASS; `go vet` clean.
- Regress-port `create_function_sql` + `drop_if_exists` PASS (autocommit DROP /
  CREATE OR REPLACE path unchanged); `plpgsql` remains pre-existing `defer`.
- `stats.spec` probe: first divergence **L449 → L1587** (`TestPort_IsolationStats`,
  soft anchor).
- `go build ./...` clean; pgbench smoke = pre-commit hook.
