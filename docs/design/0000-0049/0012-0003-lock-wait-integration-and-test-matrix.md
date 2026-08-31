# 0012-0003 — Lock-Wait Integration and Test Matrix

**Status:** accepted
**Milestone:** [0012 — Lock Manager and Deadlock Detection](../../milestones/0012-lock-manager-and-deadlock-detection.md)
**Spans seam:** executor wiring, SQLSTATE 40P01 surfacing, multi-session
deadlock test matrix.
**Cross-links:**
[0012-0001](0012-0001-lock-manager-architecture.md) (lock manager core),
[0012-0002](0012-0002-deadlock-detection-algorithm.md) (cycle detection
+ ErrDeadlockDetected sentinel).

## Context

M0012-0001 and M0012-0002 delivered the lock manager core and deadlock
detection. This slice wires it into the executor for at least one
conflict-capable path (the milestone's DoD #1 calls for "at least one")
and pins the SQLSTATE `40P01` reporting contract via a multi-session
test matrix (DoD #3, #4, #5).

## What lands

### Context plumbing

`executor.Context` grows two fields:

```go
LockMgr   *lockmgr.LockManager
BackendID lockmgr.BackendID
```

Both are optional — `LockMgr == nil` makes the new acquire helper a
no-op, so existing tests and the COPY-only legacy server path keep
working unchanged.

`server.Config` grows the same `LockMgr` field; `dispatch.go` /
`dispatch_extended.go` / `copy.go` plumb it through and assign a
per-connection `BackendID` from a monotonically-increasing atomic
counter on the server. The youngest-backend victim policy
(M0012-0002) relies on this monotonic shape.

### Acquire helper

`acquireRelLock(ctx, mode, rel) error` is the single funnel every
operator goes through. It:

- No-ops if `ctx.LockMgr == nil`.
- Builds a `LockTag{DB, Rel}` from `rel.DBOid` / `rel.RelOid`.
- Calls `LockMgr.Acquire(ctx context, BackendID, tag, mode)`.
- Translates `ErrDeadlockDetected` → `*ExecError{Code: "40P01",
  Message: "deadlock detected"}`.
- Translates `context.Canceled` / `context.DeadlineExceeded` →
  `*ExecError{Code: "57014", Message: "canceling statement due to
  user request"}` (matches upstream's query-cancel SQLSTATE).
- Returns nil on grant.

### Where the helper is called

For v0 the executor wires these representative paths. Write-side
operators take `RowExclusiveLock` (matches upstream's INSERT/UPDATE/
DELETE level); read-side `seqScanOp` takes `AccessShareLock`; the
conflict-capable path the deadlock test exercises.

| Operator                | Mode                |
|-------------------------|---------------------|
| `seqScanOp.Open`        | AccessShareLock     |
| `insertOp.Open`         | RowExclusiveLock    |
| `updateOp.Open`         | RowExclusiveLock    |
| `deleteOp.Open`         | RowExclusiveLock    |
| `indexScanOp.Open`      | AccessShareLock     |

Locks are released via `LockMgr.ReleaseAll(BackendID)` at transaction
end — `dispatch.go` calls it after `TxnMgr.Commit` / `Rollback`. This
is the v0 hook point: locks are transaction-scoped, matching upstream.
Per-statement release is intentionally **not** done so locks survive
across statements within a multi-statement txn — required for any
real SQL deadlock to form.

### SQLSTATE 40P01 reporting

`ErrDeadlockDetected` flows up through `Acquire` → `acquireRelLock` →
`*ExecError{Code: "40P01"}` → standard executor error handling →
wire-layer `ErrorResponse` with `SQLSTATE=40P01` and message
`"deadlock detected"`. No new wire-layer plumbing is needed — the
`*ExecError` machinery already converts to the right `ErrorResponse`
shape.

## Test matrix

All tests live in `internal/executor/lock_integration_test.go` and
exercise the executor's `acquireRelLock` helper directly, using two
or three goroutines with distinct `BackendID`s and explicit
`*lockmgr.LockManager`s. Driving via `acquireRelLock` (rather than
the raw `lockmgr.Acquire`) is what proves the SQLSTATE 40P01 mapping
is wired correctly.

- **TestExecutorDeadlockTwoSession**: two backends form an A↔B
  cycle (each holds RowExclusive on one tag, then asks for
  AccessExclusive on the other). Detector cancels one; the cancelled
  goroutine's `acquireRelLock` returns `*ExecError{Code:"40P01"}`
  with message `"deadlock detected"`. The survivor's pending acquire
  returns nil.
- **TestExecutorDeadlockThreeSession**: A→B→C→A multi-edge cycle.
  Exactly one backend gets 40P01.
- **TestExecutorNonDeadlockContention**: linear waiter chain (1
  holds, 2 waits, 3 waits behind 2). After 1 releases, 2 then 3
  acquire successfully — no false-positive 40P01.
- **TestExecutorAcquireHelperNilLockMgr**: regression guard — when
  `ctx.LockMgr == nil`, the helper returns nil without touching
  anything. Pins backwards compatibility for tests that don't
  configure a lock manager.

## Out of scope

- Wiring lock acquisition into DDL paths (DROP TABLE, ALTER TABLE)
  — those need AccessExclusiveLock but the operator structure for
  DDL doesn't have a single Open hook; deferred.
- Catalog-level locks (pg_class, pg_attribute) — relation-level
  only in v0.
- The TAP-style multi-session test from the milestone reference
  list — would need the wire-protocol path; the Go-level tests
  prove the same contract.
- `lock_timeout` GUC honouring — separate from deadlock_timeout;
  follow-up.
- `pg_locks` SQL view for observability — follow-up.
