# 0021-0003 — Stage A Row-Lock Executor (relation-level RowShareLock)

**Status:** accepted (Stage A executor — relation-level coverage;
tuple-level pessimistic locking + NOWAIT/SKIP LOCKED runtime
deferred)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
**Spans seam:** Executor `*planner.LockRows` build path,
`Context.acquireRelLock` reuse, transaction-scoped lock release.
**Cross-links:**
[0021-0001](0021-0001-for-update-parser-analysis-and-ast.md)
(parser + analyzer slices),
[0021-0002](0021-0002-row-lock-planner-executor-integration.md)
(planner LockRows wrapper),
[0012-0001](0012-0001-lock-manager-architecture.md)
(lock manager core).

## Context

M0021-0001 / M0021-0002 produced the parser + analyzer + planner
state for `SELECT … FOR UPDATE / FOR SHARE`. The planner emits a
`*planner.LockRows` wrapper carrying resolved `LockedRel` entries;
the executor previously rejected this with a "not yet supported"
error. This slice is the runtime: acquire the upstream-canonical
**relation-level** lock per LockedRel.Table at Open time and pass
child rows through unchanged.

## Filename note

The reserved filename `0021-0003-wait-policy-nowait-skip-locked.md`
covers the wait-policy semantics; this slice attaches the Stage A
core (relation-level lock acquisition) under the same numbering
because runtime wait-policy honoring (NOWAIT, SKIP LOCKED) lives
in the same operator. The wait-policy gate itself stays deferred —
the operator rejects non-Block policies with `0A000` and a Stage A
follow-up promotes them.

## Stage A scope

What lands:

- **`RowShareLock` on each LockedRel.Table** at LockRows.Open time.
  Mirrors upstream — both `FOR UPDATE` and `FOR SHARE` take
  `RowShareLock` at the relation level. RowShareLock conflicts
  with `ExclusiveLock` and `AccessExclusiveLock` (the modes
  DROP TABLE / ALTER TABLE / VACUUM FULL hold), which is the
  correctness property Stage A delivers: schema changes can't
  yank the table out from under a running SELECT FOR UPDATE.
  RowShareLock is COMPATIBLE with `RowExclusiveLock` (the mode
  UPDATE / INSERT / DELETE hold) — concurrent writers proceed
  unblocked at the relation level.
- Locks are transaction-scoped — released by
  `LockMgr.ReleaseAll(backendID)` in `internal/server/dispatch.go`
  at commit/rollback. Mirrors the existing relation-lock
  lifecycle (`acquireRelLock` callers don't release manually).
- Deadlock detection from M0012 just works — the lockmgr's
  cycle detector treats LockRows acquires the same as any
  other relation-lock acquire. `ErrDeadlockDetected` flows
  through `Context.acquireRelLock` to surface as SQLSTATE
  `40P01`.

What stays deferred:

- **Tuple-level pessimistic locking** — xmax stamping with
  `HEAP_XMAX_LOCK_ONLY` infomask, MVCC visibility hooks for
  lock-only xmax, row-lock WAL records, MultiXact handling for
  multiple FOR SHARE holders. Tracked as a separate fix_plan
  item ("Tuple-level pessimistic locking on top of M0012 lock
  manager"). Without it, concurrent UPDATEs to the same row
  a SELECT FOR UPDATE just observed proceed without blocking
  at the row level. The relation-level lock is the structural
  seam follow-up work attaches to.
- **Wait-policy runtime** — NOWAIT (55P03 fail-immediately),
  SKIP LOCKED (silent drop). Operator rejects non-Block
  policies with `0A000`.
- **Strongest-wins merge** of duplicate LockedRels — left to
  the tuple-locking layer where it actually matters.

## Operator shape

```go
type lockRowsOp struct {
    plan  *planner.LockRows
    ctx   *Context
    child Operator
}

func (o *lockRowsOp) Open(ctx *Context) error {
    o.ctx = ctx
    for i := range o.plan.Locks {
        lk := &o.plan.Locks[i]
        if lk.WaitPolicy != planner.LockWaitBlock {
            return &ExecError{Code: "0A000", …}
        }
        rel := ctx.Catalog.RelFileNode(lk.Table)
        if err := ctx.acquireRelLock(rel, lockmgr.RowShareLock); err != nil {
            return err
        }
    }
    return o.child.Open(ctx)
}

func (o *lockRowsOp) Next() (Row, error) { return o.child.Next() }
func (o *lockRowsOp) Close() error       { return o.child.Close() }
```

The Open pre-pass acquires every lock first, then opens the child.
The natural alternative — open child first, acquire lazily on
first Next — would have the SELECT see rows it might still get
blocked from later. Acquiring up front matches upstream's
`ExecLockRows` placement.

## Build dispatch

```go
case *planner.LockRows:
    child, err := Build(p.Child)
    if err != nil { return nil, err }
    return maybeInstrument(p, newLockRowsOp(p, child)), nil
```

Replaces the M0021-0002 reject. Child is built first so
EXPLAIN ANALYZE timing on the wrapped tree comes out as
expected.

## Tests

`internal/executor/operators_lockrows_test.go`:

- `TestLockRowsAcquiresRowShareLock` — runs
  `SELECT … FOR UPDATE` end-to-end through
  parser→analyzer→planner→executor, verifies
  `lm.Holders(tag)[backend]` has the RowShareLock bit set.
- `TestLockRowsForShareAlsoUsesRowShareLock` — pins that
  FOR SHARE also acquires RowShareLock (matches upstream's
  relation-level uniformity; tuple-level distinguishing
  lands in the follow-up).
- `TestLockRowsRejectsNoWait` — Stage A scope guard: NOWAIT
  surfaces `0A000` so users see the specific "Stage A
  executor follow-up" message.
- `TestLockRowsBlocksOnExclusiveLock` — multi-session
  blocking guarantee: backend 1 holds `ExclusiveLock`,
  backend 2's `SELECT … FOR UPDATE` registers as a waiter,
  blocker releases, the SELECT completes. Pins the lockmgr
  conflict-matrix integration via the operator.

Full `go test ./...` green.

## Out of scope

- Tuple-level row-locking infrastructure (xmax stamping,
  MultiXact-aware visibility, row-lock WAL) — separate
  follow-up fix_plan item.
- NOWAIT / SKIP LOCKED runtime semantics — Stage A executor
  follow-up.
- Observability: per-statement row-lock-wait counters,
  pg_locks-style introspection — M0021-0004.
- Cross-partition row-lock coherence — out of M0021 entirely.
