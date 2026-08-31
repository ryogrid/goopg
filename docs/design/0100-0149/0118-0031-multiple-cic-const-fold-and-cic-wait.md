# 0118-0031 — `multiple-cic` isolation spec: const-folded partial-index predicate + CREATE INDEX CONCURRENTLY drain (M0118-0008)

Status: accepted
Milestone: M0118-0008 (DDL / VACUUM / maintenance concurrency)
Spec: `postgres/src/test/isolation/specs/multiple-cic.spec`
Test: `internal/testport/isolation_port_test.go::TestPort_IsolationMultipleCic` (strict, `runIsoSpecStrict`)

## Summary

Fifth M0118-0008 promotion. Promotes `multiple-cic` to pass-required —
byte-identical to PostgreSQL 18.3 across its single permutation
(`s2l s1i s2i(*)`).

The spec runs two `CREATE INDEX CONCURRENTLY` builds simultaneously, each with a
partial-index `WHERE` predicate that calls an IMMUTABLE PL/pgSQL function:

```
session s1  step s1i: CREATE INDEX CONCURRENTLY mcic_one_pkey ON mcic_one (id)
                      WHERE lck_shr(281457);   -- pg_advisory_lock_shared(281457)
session s2  step s2l: SELECT pg_advisory_lock(281457);      -- exclusive advisory lock
            step s2i: CREATE INDEX CONCURRENTLY mcic_two_pkey ON mcic_two (id)
                      WHERE unlck();            -- pg_advisory_unlock_all()
```

Both `mcic_one` and `mcic_two` are **empty**. The expected behaviour:

```
step s2l: ... (takes exclusive advisory lock 281457)
step s1i: ... <waiting ...>     -- predicate lck_shr blocks on the advisory lock
step s2i: ... <waiting ...>     -- (*) forces waiting display
step s1i: <... completed>       -- s2i's unlck() released the lock; s1i proceeds
step s2i: <... completed>       -- completes only after s1i
```

Two divergences had to be closed.

## Divergence 1 — the predicate function was never called (s1i did not block)

goopg evaluated a partial-index `WHERE` predicate **per heap tuple** during the
bulk build. Because `mcic_one` is empty, `lck_shr(281457)` was never evaluated,
the advisory lock was never taken, and `s1i` completed immediately instead of
blocking.

PostgreSQL const-folds the index predicate in `BuildIndexInfo` via
`eval_const_expressions`: an IMMUTABLE function with constant arguments is
evaluated **exactly once at build time**, independent of row count. That single
call is what takes the advisory lock and makes the build block.

### Fix

In `execCreateIndex` (btree path, `internal/executor/operators_ddl.go`), after
resolving the partial predicate, const-fold it when it references **no table
columns** (`planner.ExprContainsColumnRef` — a new exported wrapper over the
existing `exprContainsColumnRef`):

```go
if resolvedPred != nil && !planner.ExprContainsColumnRef(resolvedPred) {
    pv, pErr := evalExpr(resolvedPred, nil, o.ctx)   // call the IMMUTABLE fn once
    if pErr != nil { return pErr }
    if pv.IsNull() || (pv.Kind == KindBool && !pv.BoolValue()) {
        resolvedPred = &planner.BooleanConst{}   // const FALSE/NULL ⇒ index nothing
    } else {
        resolvedPred = nil                       // const TRUE ⇒ index every row
    }
}
```

`evalExpr` invokes the user PL/pgSQL function `lck_shr`, which calls
`pg_advisory_lock_shared(281457)` and blocks because session s2 already holds
the exclusive advisory lock. The **stored** predicate (`idx.Predicate`) is left
untouched, so `pg_get_indexdef` / `pg_dump` still render the `WHERE` clause
verbatim. This mirrors PG's "function called once, then folded constant used"
exactly — correct for any IMMUTABLE predicate and a no-op for the empty-table
case the spec exercises.

## Divergence 2 — completion order was reversed (s2i finished before s1i)

After divergence 1 was fixed, `s1i` blocked correctly but the two builds
completed in the wrong order: goopg reported `s2i` complete before `s1i`.
PostgreSQL completes `s1i` first because a concurrent index build waits, in its
final phase, for transactions whose MVCC snapshot could predate the new index —
the older build `s1i` is not waited on by anything, while the newer `s2i` must
wait for `s1i` to drain.

### Fix (engine)

`CreateIndexStmt` gained a `Concurrently bool` field; the parser
(`parseCreateIndexTail`) now records the `CONCURRENTLY` keyword instead of
discarding it. goopg still builds the index synchronously, but a concurrent
build now:

1. **At build start (before the const-fold that may block)** captures the set of
   other backend slots currently in a transaction —
   `mvcc.Manager.SnapshotActiveOtherSlots(selfHandle)`.
2. **After the build** drains exactly that captured set —
   `mvcc.Manager.WaitForSlotsToCommit(ctx, slots)`.

`WaitForOlderSlotsToCommit` (used by DROP INDEX CONCURRENTLY) was refactored into
these two pieces; its behaviour is unchanged. Capturing the active set at *start*
rather than re-scanning at wait time is what prevents two simultaneous builds
from waiting on each other: `s1i` started before `s2i` existed, so `s1i`'s
snapshot is empty and it never waits; `s2i` started while `s1i` was active
(blocked on the advisory lock), so `s2i`'s snapshot is `{s1i}` and it waits for
`s1i` to commit. No mutual wait, deterministic `s1i`-then-`s2i` ordering.

Gated on `Concurrently` with a non-zero transaction handle, so plain
`CREATE INDEX` is unaffected. Only `multiple-cic` (and the deferred
`prepared-transactions-cic`) exercise `CREATE INDEX CONCURRENTLY` in the spec
suite, so blast radius is minimal.

### Fix (test runner — completion-order reporting)

`internal/testport/framework/isolation_runner.go`: a `(*)`-marked step that
completes immediately was always reported inline, *before* draining the
already-pending blocked steps. isolationtester instead reports waiting steps in
**dispatch order**, and a `(*)` step may itself have waited on an
earlier-dispatched blocked step (here `s2i` waits for `s1i`). The star branch now
drains **ungated** pending steps *before* writing the star step's own completion,
while excluding pending steps that are explicitly **gated on** the star step via
a `BlockerStepComplete` (`partitionGatedOn`) — those (e.g. deadlock-hard's
`s7a8(s8a1)`) must still print *after* it. Steps that are still genuinely blocked
simply time out of the pre-completion drain and stay pending, preserving the
deadlock specs' ordering.

## What is NOT changed

- No per-row predicate-evaluation change for column-referencing partial indexes.
- No actual concurrent (multi-phase) index build; goopg builds synchronously and
  only models the observable start-snapshot drain.
- WAL / crash recovery: a constant predicate folds to a literal, identical to a
  normal partial index on disk.

## Gates

- `TestPort_IsolationMultipleCic` strict PASS.
- Regression for every strict `(*)`-marked spec: deadlock-hard, deadlock-soft,
  deadlock-soft-2, classroom-scheduling, project-manager, serializable-parallel,
  serializable-parallel-2, temporal-range-integrity, multixact-no-deadlock,
  tuplelock-upgrade-no-deadlock, timeouts — all PASS.
- Lock-sibling regression: reindex-concurrently, reindex-schema,
  drop-index-concurrently-1, sequence-ddl, create-trigger — PASS.
- `internal/parser`, `internal/planner`, `internal/executor` units PASS;
  `-race` `internal/mvcc` (WaitForSlots) + `internal/lockmgr` PASS.
- pgbench TPC-B smoke 0-failed (pre-commit hook).

## Remaining M0118-0008 (deferred, ledger 2026-06-22)

`alter-table-1/2/3/4` (ADD CONSTRAINT … NOT VALID + VALIDATE CONSTRAINT parser +
lock semantics), the `*-conflict` family (truncate/vacuum/cluster — need
CREATE ROLE / GRANT / SET ROLE / ALTER … OWNER + privilege-denied infra),
`reindex-concurrently-toast` (`allow_system_table_mods` GUC + TOAST-relation
reindex + ALTER TABLE/INDEX RENAME of toast rels), the partition specs
(ATTACH/DETACH PARTITION), `vacuum-skip-locked` / `vacuum-concurrent-drop`
(partitioned VACUUM + log-message parity), `vacuum-no-cleanup-lock`
(reltuples accounting), `inherit-temp` (cross-session temp-relation inheritance
exclusion), `plpgsql-toast` (TOAST in PL/pgSQL DO blocks with COMMIT). Group
stays open.
