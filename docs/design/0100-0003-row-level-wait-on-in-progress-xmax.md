# 0100-0003 — Row-Level Wait on In-Progress xmax for UPDATE/DELETE

**Status:** accepted
**Date:** 2026-05-13
**Milestone:** M0100-0003
**Closes:** M0096-0013 (one of the documented remaining blockers)

## Problem

When session 1 holds an uncommitted UPDATE on row R and session 2 issues
`UPDATE … WHERE id = R.id`, PostgreSQL blocks session 2 on session 1's
XID via `XactLockTableWait`. Once session 1 commits or aborts, session 2
re-fetches R, re-evaluates visibility, and (for ReadCommitted) re-runs
EvalPlanQual against the latest row version before applying its UPDATE.

goopg `updateOp.Next` / `deleteOp.Next` does call `epqWait`
(`internal/executor/operators_storage.go:78-95`, landed in M0098-0004
and extended in M0099-0004), but **`epqWait` deliberately does not block
on `WaitForXID` anymore** — the M0099-0004 commit removed the blocking
call because it caused pgbench client goroutines to hang past the 180 s
measurement window. The current path:

1. Register the wait-for-graph edge (cycle ⇒ raise 40001 with deadlock
   message — `registerWFGAndCheckCycle`, lines 38–55).
2. Refresh the snapshot (no actual wait).
3. Retry up to `maxEPQRetries = 3` (line 20).
4. After exhaustion, raise 40001.

This is correct for OLTP TPS but **incompatible with the 21 RC isolation
specs**: their expected output requires session 2 to literally block and
emit `<waiting …>` before session 1's commit. With snapshot-refresh-only
retry, session 2 either succeeds immediately on a stale view or 40001s
out — neither matches the expected output.

Goal of M0100-0003: re-enable the blocking branch of `WaitForXID` for
UPDATE/DELETE without regressing the M0099-0004 pgbench-hang fix.

## Solution

### Diagnose the M0099-0004 hang first (loop opening)

Before re-introducing `WaitForXID`, read the M0099-0004 commit + any
attached log/post-mortem to identify the exact failure mode:

- Did pgbench clients hold a page pin while blocked? (Page-pin deadlock
  is a classic source of the hang.) `epqWait`'s call site at
  `tryApplyHOTUpdate` (line 922-925) already unlocks + unpins **before**
  calling `epqWait`. Verify the same invariant holds at the other three
  call sites (lines 1157, 1331, 1518).
- Did the wait fail to wake on commit? `WaitForXID` waits on
  `commitCond.Wait`; verify `commitCond.Broadcast` runs unconditionally
  in `endTransaction`.
- Did the wait participate in deadlock detection? M0099-0004 added WFG
  cycle detection *before* `epqWait` returned; if blocking is restored,
  cycles must still be detected (call `registerWFGAndCheckCycle` first,
  return on cycle, only then block).

If the hang was an unrelated bug already fixed by M0099-0004's other
changes, blocking is safe to restore globally. If it's a structural
issue, fall back to **session-scoped opt-in** (below).

### Re-enable blocking `WaitForXID` in `epqWait`

In `internal/executor/operators_storage.go:78-95`, between the WFG
cycle check (which currently runs) and the snapshot refresh:

```go
if registerWFGAndCheckCycle(ctx.Tx.XID, xmax) {
    return true // deadlock
}
defer deregisterWFG(ctx.Tx.XID)

// PG parity: block on the holder XID. WFG-cycle short-circuit above
// already guarantees liveness for two-cycle deadlocks; lockmgr's
// existing N-cycle detector covers longer chains.
if err := ctx.TxnMgr.WaitForXID(ctx.Ctx, xmax); err != nil {
    return false // context cancelled — fall through to retry/abort
}

// Refresh snapshot (existing).
ctx.Snap, _ = ctx.TxnMgr.SnapshotFor(ctx.Tx)
return false
```

### Caller-side invariants

Every `epqWait` call site must satisfy:

1. **Release page pins before calling.** Verify lines 922-925, 1157,
   1331, 1518 each unlock+unpin before invoking `epqWait`. If any caller
   leaks a pin, blocking will hang forever waiting for the holder to
   acquire a pin we still hold.
2. **Re-fetch the tuple after `epqWait` returns.** The current code
   already does this (via the M0098-0004 retry loop reading `oldTup`
   anew); preserve.

### Fallback: session-scoped opt-in if global blocking regresses pgbench

If even after the pin-release audit the pgbench hang reproduces,
gate the blocking on a session-level flag (`session.WaitOnXmax bool`)
that defaults `false` for client traffic and is set `true` by
`IsolationRunner` via a connection-init `SET goopg.wait_on_xmax = on`
(introduce as a session GUC). Document this as a v0 compromise; track
the global-enable as a follow-up.

### Mapping to the test expectation

This mirrors `heap_update` / `heap_delete` in upstream PostgreSQL's
`heapam.c`. Specifically the `HeapTupleUpdated` / `HeapTupleBeingUpdated`
branches that call `XactLockTableWait`.

### IsolationRunner output coupling

`IsolationRunner.RunAndCompare` already emits `<waiting …>` when a
step's goroutine doesn't return within 300 ms (lines 160–170 of
`internal/testport/framework/isolation_runner.go`). The block on
`WaitForXID` naturally exceeds 300 ms in the spec runs, so output
generation is automatic — no IsolationRunner changes needed.

## Files touched

- `internal/executor/operators_storage.go` — re-introduce blocking
  inside `epqWait` (lines 78–95); audit callers at lines 922-925,
  1157, 1331, 1518 for the page-pin-release invariant.
- `internal/executor/operators_storage_test.go` — new race-tested unit
  test: two goroutines UPDATE the same row, second blocks on first,
  unblocks on commit, sees latest version.
- `internal/mvcc/manager.go` — no change. Reuses `WaitForXID`
  (lines 338–358) which already drains `commitCond` correctly.
- (Optional, fallback path) `internal/config/guc.go` — add
  `goopg.wait_on_xmax` GUC, default off; `internal/server/session.go` —
  honour the GUC when entering `epqWait`.

## Reference (upstream)

- `postgres/src/backend/access/heap/heapam.c` — `heap_update`,
  `heap_delete`: `HeapTupleBeingUpdated` branch + `XactLockTableWait`
  on the holder XID.
- `postgres/src/backend/utils/time/tqual.c` — visibility helpers
  consulted on re-fetch.

## Verification

- `TestPort_IsolationLockCommittedUpdate`, `…Keyupdate`,
  `TestPort_IsolationPartitionKeyUpdate{1..4}` reach `pass`.
- `go test -race ./internal/executor/... ./internal/mvcc/...` clean.
- Deadlock-detection sanity: two sessions UPDATE rows in opposite
  order — one must be aborted by deadlock detection, the other
  proceeds. M0012's `lockmgr` is the deadlock owner; confirm
  `WaitForXID` participates correctly.

## Risks

- **Reintroduce the M0099-0004 hang.** The whole reason blocking was
  removed. Required mitigation: page-pin-release audit at all four
  `epqWait` call sites (above), plus the pgbench regression check
  in Definition of Done. If global re-enable fails, fall back to
  the session-scoped GUC opt-in (above).
- **Deadlock detection coverage.** Two-cycle deadlocks are caught by
  `registerWFGAndCheckCycle` before the block, which is unchanged.
  N-cycle deadlocks rely on `lockmgr`'s timeout-based detection.
  Verify `WaitForXID` participates by registering the wait as a
  lockmgr edge before blocking; symmetric two-session test
  (`insert-conflict-specconflict` and similar) must abort one side.
- **Spurious wakeups.** `commitCond` is broadcast on every commit; the
  re-fetch loop must tolerate that. `WaitForXID` already checks
  `xidInProgress` after each wake (`manager.go:352-356`).
- **Partition cross-routing.** M0096-0007 / M0096-0013 added
  partition-aware UPDATE re-routing in `remapRowForPartition`. The
  wait path must run *before* re-routing so that the tuple identity
  used in the re-fetch is the original partition's, not the destination's.
