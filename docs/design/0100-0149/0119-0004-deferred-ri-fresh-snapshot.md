# 0119-0004 — Deferred-RI fresh-snapshot semantics (drop the `ConstraintsOverrideActive` gate)

Status: accepted
Milestone: M0119-0004
Supersedes the `ConstraintsOverrideActive` gate added in
[`0119-0004-set-constraints-deferred`](0119-0004-set-constraints-deferred.md).

## Problem

A `DEFERRABLE INITIALLY DEFERRED` foreign key queues its referential-integrity
check at the triggering INSERT/UPDATE/DELETE and runs it at `COMMIT`. The
simple-query `COMMIT` path (`internal/server/dispatch.go`, `case
planner.TxCommit`) bypasses `transactionOp.execCommit`, so until loop #18 those
queued checks were never executed there — the path psql, lib/pq, and the
isolation runner all use (each statement is its own simple-query message). Loop
#18 added `executor.RunDeferredFKChecks` to that path but **gated it on an active
`SET CONSTRAINTS … DEFERRED` override** (`sess.ConstraintsOverrideActive()`):
running it unconditionally regressed the pass-required `fk-snapshot` isolation
spec, so a plain `INITIALLY DEFERRED` constraint's commit-time enforcement stayed
dead on the simple-query path.

### Why unconditional activation regressed `fk-snapshot`

The deferred check scanned both the child rows and the parent table with the
**transaction's** snapshot (`ctx.Snap`). `fk-snapshot`'s second/third
permutations insert into `fk_noparted` (whose FK is `INITIALLY DEFERRED`) in a
session whose snapshot was taken *before* a concurrent session committed the
satisfying `fk_parted_pk` parent row:

```
s2ip2 s2brr s1brc s1ifp2 s2sfp s1c s2sfp s2ifn2 s2c s2sfn
                         ^^^^ s1 inserts fk_parted_pk(2)
                                       ^^^ s1 COMMITs (2)
                                                     ^^^^^^ s2 inserts fk_noparted(2)
                                                            ^^^ s2 COMMIT → deferred check
```

`s2` is `REPEATABLE READ`; its pinned snapshot was captured at `s2brr`, before
`s1c`. A deferred check scanning the parent with that snapshot cannot see
`fk_parted_pk(2)` and raises a spurious `23503` — but PostgreSQL's expected
output shows `s2c` committing cleanly.

PostgreSQL avoids this because its deferred-RI machinery does **not** use the
firing statement's snapshot. `RI_FKey_check` / `ri_PerformCheck`
(`src/backend/utils/adt/ri_triggers.c`) push `GetLatestSnapshot()` before running
the check query, so a queued constraint always sees the *latest committed* state
at `COMMIT`, independent of the transaction's isolation level.

## Change

1. **`internal/mvcc/manager.go` — `FreshSnapshot()`** (exported): wraps the
   existing unexported `captureSnapshot()` (which already attaches the CLOG and
   stamps the partition-detach epoch). Returns a brand-new "latest" snapshot
   reflecting every transaction committed up to the call, ignoring any pinned
   per-transaction snapshot. Mirrors `GetLatestSnapshot()`.

2. **`internal/executor/operators_fk.go` — `runAllDeferredFKChecks`**: before
   running the queued checks, save `ctx.Snap`, install
   `ctx.TxnMgr.FreshSnapshot()`, and restore it with `defer`. Every downstream
   scan (`fullTableFKCheck`'s child-row visibility test and
   `assertParentExists`→`scanTableForMatchFKWait`'s parent probe) reads
   `ctx.Snap`, so both now run against the latest committed state. This is the
   single chokepoint for **both** the `execCommit` path (`operators_tx.go`) and
   the simple-query dispatch path, so the two stay consistent.

   The committing transaction's own uncommitted child rows remain visible: the
   fresh snapshot still lists `ctx.Tx.XID` as in-progress, but
   `mvcc.TupleVisibleSubxact` is passed `ctx.Tx.XID` and classifies
   `xmin == currentXID` as self-visible regardless of snapshot — so the rows this
   transaction is about to commit are still scanned and checked.

3. **`internal/server/dispatch.go` — drop the gate**: the simple-query `COMMIT`
   path now calls `RunDeferredFKChecks` whenever a session is present, not only
   under a `SET CONSTRAINTS` override. With (1)+(2) backing it, plain
   `INITIALLY DEFERRED` constraints are enforced at `COMMIT` on the simple-query
   path exactly as PG does, and `fk-snapshot` stays green.

## Why it is safe

- **Strictly more correct for `execCommit` too.** That path previously used the
  transaction snapshot; switching it to the fresh snapshot only *adds* visibility
  of rows committed by other transactions after this transaction's snapshot —
  precisely PG's deferred-RI semantics. Rows already visible under the
  transaction snapshot (committed earlier, or this transaction's own writes) stay
  visible.
- **Own deletes still hide their target.** A parent row this transaction deleted
  has `effXmax == ctx.Tx.XID`, which `TupleVisibleSubxact` treats as
  self-deleted → invisible, independent of the snapshot.
- **Zero blast radius when nothing is deferred.** `RunDeferredFKChecks` /
  `runAllDeferredFKChecks` return early when the deferred-check queue is empty, so
  the fresh snapshot is never taken for workloads without deferred FKs
  (TPC-H/pgbench, every IMMEDIATE constraint).

## Oracle

- `postgres/src/backend/utils/adt/ri_triggers.c` — `RI_FKey_check` /
  `ri_PerformCheck` push `GetLatestSnapshot()` for the deferred check query.
- `postgres/src/test/isolation/specs/fk-snapshot.spec` /
  `expected/fk-snapshot.out` — the concurrent partitioned-parent permutations
  this fix makes pass while enforcing plain `INITIALLY DEFERRED`.

## Tests / gates

- `internal/testport` — `TestPort_IsolationFkSnapshot` PASS (all 7 permutations),
  full FK isolation group (`TestPort_IsolationFk*`,
  `TestPort_IsolationInsertConflict*`, partition-key-update) PASS.
- New `internal/testport` — `TestPort_InitiallyDeferredFKCommit`: plain
  `INITIALLY DEFERRED` (no `SET CONSTRAINTS`) over the simple-query path —
  child-before-parent ordered insert commits; a parent still missing at `COMMIT`
  raises `23503` at `COMMIT` (not at the INSERT) and rolls the orphan back.
- `TestPort_SetConstraintsDeferral` PASS (override path unchanged).
- `-race` on `internal/mvcc` (snapshot/commit/visibility) and `internal/executor`
  (FK/upsert/conflict) PASS; `internal/server` FK/commit units PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

- Deferred `UNIQUE` / `EXCLUDE` constraint enforcement.
- Extended-protocol commit-time deferral (the queued checks only run on the
  simple-query and `execCommit` paths).
- The pg_dump 002–010 catalog-view parity battery.
