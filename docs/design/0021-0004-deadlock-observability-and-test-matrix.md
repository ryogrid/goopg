# 0021-0004 — NOWAIT Runtime + SKIP LOCKED Gate

**Status:** accepted (NOWAIT runtime; SKIP LOCKED + observability
counters deferred)
**Milestone:** [0021 — Pessimistic Row Locking](../milestones/0021-pessimistic-lock-select-for-update.md)
**Spans seam:** lockmgr non-blocking acquire path, executor
NOWAIT/SKIP LOCKED dispatch, SQLSTATE 55P03 surface.
**Cross-links:**
[0021-0001](0021-0001-for-update-parser-analysis-and-ast.md)
(parser + analyzer slices),
[0021-0002](0021-0002-row-lock-planner-executor-integration.md)
(planner LockRows wrapper),
[0021-0003](0021-0003-wait-policy-nowait-skip-locked.md)
(Stage A blocking executor),
[0012-0001](0012-0001-lock-manager-architecture.md)
(lockmgr core).

## Filename note

Reserved as the M0021-0004 deadlock + observability slot;
NOWAIT runtime attaches here because it shares the
operator and SQLSTATE machinery with the wider
deadlock/observability surface. Observability counters and
tuple-level pessimistic locking remain deferred.

## Context

M0021-0003 landed the Stage A executor with a relation-level
`RowShareLock` acquired through `Context.acquireRelLock`.
Wait-policy modifiers (`NOWAIT`, `SKIP LOCKED`) parsed and
analyzed cleanly but the executor rejected non-Block policies
with `0A000`. This slice promotes `NOWAIT` to a working runtime
at the relation-coarse layer goopg has today and clarifies
`SKIP LOCKED`'s deferred status.

## NOWAIT runtime

### lockmgr.TryAcquire

New non-blocking acquire path on `*LockManager`:

```go
var ErrLockNotAvailable = errors.New("lockmgr: could not obtain lock immediately")

func (lm *LockManager) TryAcquire(b BackendID, t LockTag, m Mode) error {
    // Same fast path as Acquire's first branch (idempotent
    // re-grant; FIFO-fair grant when no waiters and no
    // conflict). Returns ErrLockNotAvailable instead of
    // queueing on contention.
}
```

Locks granted via `TryAcquire` are tracked in the same state
as `Acquire`'s and released identically by `Release` /
`ReleaseAll(BackendID)`. The function is byte-identical to
`Acquire`'s synchronous fast path (lines 240-260 of
lockmgr.go) — keeps grant semantics and FIFO fairness rules
unchanged.

### Context.tryAcquireRelLock

Mirrors `acquireRelLock` for the non-blocking variant:

```go
func (c *Context) tryAcquireRelLock(rel storage.RelFileNode, mode lockmgr.Mode) error {
    err := c.LockMgr.TryAcquire(c.BackendID, tag, mode)
    if err == lockmgr.ErrLockNotAvailable {
        return &ExecError{Code: "55P03", Message: "could not obtain lock on relation"}
    }
    // ... error mapping ...
}
```

`55P03` is upstream's canonical "could not obtain lock"
SQLSTATE. The message says "relation" (not "row") because
goopg's locking is relation-coarse today; the SQLSTATE is
what tooling greps for, the message is human-readable
detail.

### lockRowsOp dispatch

```go
switch lk.WaitPolicy {
case planner.LockWaitBlock:
    err = ctx.acquireRelLock(rel, lockmgr.RowShareLock)
case planner.LockWaitNoWait:
    err = ctx.tryAcquireRelLock(rel, lockmgr.RowShareLock)
case planner.LockWaitSkipLocked:
    return &ExecError{Code: "0A000", ...}  // deferred
}
```

The default-Block path is unchanged from M0021-0003. NOWAIT
runs the new TryAcquire path. SKIP LOCKED is rejected with a
specific Stage-A-deferred message.

## SKIP LOCKED — deferred

Goopg's row-locking is relation-coarse: `SELECT FOR UPDATE`
acquires a single relation-level `RowShareLock`, not per-row
locks. SKIP LOCKED's intended behaviour is "silently drop
contended rows from the result", which requires per-row lock
probing and tuple-level lock state. Without tuple-level
infrastructure (the deferred follow-up "Tuple-level
pessimistic locking on top of M0012 lock manager" item),
SKIP LOCKED at the relation level would either:

1. Silently produce empty results when contended (surprising
   semantics).
2. Behave as a no-op (also wrong).

Neither matches user expectations, so we surface a precise
`0A000` reject pointing at the deferred item. This keeps the
diagnostic stable for tooling: an operator who tries SKIP
LOCKED today sees the same message as one trying it tomorrow,
until the tuple-level layer lands.

## Observability — deferred

Per-statement row-lock-wait counters (`lock_wait_count`,
`lock_wait_time`), pg_locks-style introspection of waiting
backends, and EXPLAIN ANALYZE Stage-A row-lock timing detail
all stay deferred. The relation-coarse layer doesn't have
the granularity to make these informative: every lock wait
on a SELECT FOR UPDATE is a single relation-level wait, not
the per-row contention pattern users care about.

The natural slice for these counters lands alongside the
tuple-level pessimistic-locking infrastructure where the
metrics actually distinguish hot rows from cold rows.

## Tests

`internal/executor/operators_lockrows_test.go`:

- `TestLockRowsAcquiresRowShareLock` (M0021-0003) — pre-existing.
- `TestLockRowsForShareAlsoUsesRowShareLock` (M0021-0003) —
  pre-existing.
- `TestLockRowsBlocksOnExclusiveLock` (M0021-0003) —
  pre-existing; updated to seed before wiring the lockmgr so
  the seed-insert RowExclusiveLock doesn't conflict with the
  test's blocker.
- `TestLockRowsNoWaitSucceedsUncontended` — NEW. NOWAIT path
  works when the lock is immediately grantable. Replaces the
  previous "NOWAIT rejected" test from M0021-0003.
- `TestLockRowsNoWaitFailsOnContention` — NEW. Backend 1 holds
  `ExclusiveLock`; backend 2's `SELECT … FOR UPDATE NOWAIT`
  surfaces `55P03` immediately (no waiting).
- `TestLockRowsRejectsSkipLocked` — NEW. SKIP LOCKED → `0A000`
  with the specific tuple-level-deferred message so tooling
  can branch on the diagnostic.

Full `go test ./...` green.

## Out of scope

- Tuple-level pessimistic locking — separate fix_plan item.
- SKIP LOCKED runtime — gated on tuple-level locking.
- Observability counters / pg_locks introspection — gated on
  tuple-level locking.
- Cross-partition row-lock coherence — out of M0021 entirely.
