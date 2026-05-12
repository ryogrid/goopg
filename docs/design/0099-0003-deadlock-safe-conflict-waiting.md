# Design: Deadlock-Safe Conflict Waiting (M0099-0004)

**Status**: draft  
**Milestone**: M0099-0004  
**Filed**: 2026-05-12

## Background

M0098-0004 (EvalPlanQual) replaced immediate SQLSTATE 40001 on concurrent
xmax conflict with an EPQ retry loop. The original implementation called
`WaitForXID` inside `epqWait` to block until the conflicting transaction
committed or aborted, then refreshed the snapshot and re-read the tuple.

This produced a circular deadlock in the standard pgbench workload:
- TX1 holds row `teller[T1]` and calls `WaitForXID(TX2)` (TX2 holds `branch[B1]`)
- TX2 holds row `branch[B1]` and calls `WaitForXID(TX1)` (TX1 holds `teller[T1]`)
- Both goroutines block indefinitely → TPS drops to 0

M0098-0004 fixed this by removing the `WaitForXID` call from `epqWait` entirely.
`epqWait` now only refreshes the snapshot (non-blocking). With `maxEPQRetries=3`,
transactions that conflict 3 times still escalate to SQLSTATE 40001, producing a
2.215% abort rate in the standard pgbench workload.

The goal of M0099-0004 is to restore actual waiting (for transaction commit/abort)
while preventing circular deadlocks, materially reducing the 40001 abort rate.

## Problem Statement

At 443 TPS standard with 2.215% abort rate, ~9.8 transactions/second abort and
are retried by pgbench (`-M prepared` pgbench automatically retries 40001). Each
retry adds an extra round-trip (~1 ms) and re-contends for the same rows.
Eliminating or greatly reducing the abort rate would directly increase throughput.

The teller/branch contention pattern in TPC-B is inherently concurrent: many
transactions update the same `branches` table row (`bid` is shared across many
tellers). Under `-c 100` this creates high row-level conflict. Proper waiting
(yield-until-commit instead of spin-retry) is the standard database solution.

## Proposed Design

### Wait-For Graph with Cycle Detection

Maintain a process-global wait-for table:

```go
type conflictWaiter struct {
    waitingXID  uint64
    blockingXID uint64
}

var (
    wfgMu      sync.Mutex
    waitForGraph = map[uint64]uint64{} // waitingXID → blockingXID
)
```

**Before blocking** (in `epqWait`):
1. Acquire `wfgMu`
2. Register `waitForGraph[myXID] = conflictingXID`
3. Walk the graph: follow `conflictingXID → its blocker → its blocker → …`
   until we reach a XID with no entry (free) or reach `myXID` (cycle detected).
4. If cycle detected: remove our entry, release `wfgMu`, return `ErrDeadlock` (40001).
5. If no cycle: release `wfgMu`, call `WaitForXID(conflictingXID)` with a
   `maxWaitTimeout = 5s` context deadline as a safety net.
6. After `WaitForXID` returns: acquire `wfgMu`, delete `waitForGraph[myXID]`, release.

**On transaction commit or abort** (`TxnMgr.Commit`/`Rollback`): no extra work needed
— `WaitForXID` already watches the commit/abort broadcast channel.

### epqWait revised signature

```go
func epqWait(ctx *ExecutionContext, xmax uint64) (deadlock bool) {
    if deadlock = registerWFGAndCheckCycle(ctx.Tx.XID, xmax); deadlock {
        return
    }
    waitCtx, cancel := context.WithTimeout(ctx.Ctx, 5*time.Second)
    defer cancel()
    ctx.TxnMgr.WaitForXID(waitCtx, xmax)
    deregisterWFG(ctx.Tx.XID)
    return false
}
```

In the retry loop, if `epqWait` returns `deadlock == true`, immediately escalate
to SQLSTATE 40001 (same as `epqRetry >= maxEPQRetries`).

### Increase maxEPQRetries

With proper waiting, most conflicts resolve on the first retry (the blocking TX
commits within microseconds). Increase `maxEPQRetries` from 3 to 10 to handle
cases where a transaction takes longer than the snapshot refresh window.

### Cycle detection algorithm

Walk the wait-for graph with a bounded depth limit (64 hops) to prevent O(N)
scans under adversarial workloads:

```go
func hasCycle(start, check uint64) bool {
    visited := start
    cur := check
    for i := 0; i < 64; i++ {
        if cur == visited { return true }
        next, ok := waitForGraph[cur]
        if !ok { return false }
        cur = next
    }
    return false // give up; 40001 only on confirmed cycle
}
```

Holding `wfgMu` across the walk is safe since the walk is bounded and
WFG operations are infrequent (one per concurrent conflict, not per query).

## Correctness

- **Deadlock freedom**: A cycle is detected before blocking, so no goroutine
  waits for another that (transitively) waits for it.
- **5s safety timeout**: Even if the cycle detection has a false negative
  (e.g., the graph changed between check and block), the 5s context deadline
  prevents permanent blocking.
- **MVCC validity**: After `WaitForXID` returns, `epqWait` returns and the
  caller refreshes the snapshot via `TxnMgr.SnapshotFor`. The EPQ re-check
  re-reads the tuple under the new snapshot, so only committed rows are visible.

## Expected Impact on Abort Rate

In TPC-B standard workload, most conflicts are teller→branch or branch→teller
and involve only two transactions. Cycle detection will identify these as
2-node cycles and abort one participant immediately (40001 on cycle detection).
The other participant proceeds unblocked.

The key improvement: the non-cycle participant **waits** rather than spin-retrying.
Waiting eliminates the high-frequency 40001 + retry overhead for non-cyclic
conflicts (which are the majority under moderate concurrency).

Expected abort rate reduction: from 2.2% → ~0.3% (only true deadlock cycles abort,
not retry-exhaustion aborts).

Expected TPS gain: 5–15% additional for Standard workload (fewer aborted txns).

## Interaction with Other M0099 Work

- M0099-0002 (evictMu): independent; apply in any order.
- M0099-0003 (WAL batching): independent.
- M0099-0005 (matrix validation): measures the combined effect.

## Regression Test Requirements

1. `TestEPQDeadlockCycleDetected`: two concurrent goroutines each hold a row
   the other wants; verify one gets 40001 quickly (not indefinite hang).
2. `TestEPQWaitResolves`: TX1 waits on TX2 (no cycle); TX2 commits; verify
   TX1 proceeds without 40001.
3. `TestEPQMaxRetriesEscalates`: simulate conflict that never resolves; verify
   40001 after `maxEPQRetries` attempts.

## Files to Modify

| File | Change |
|------|--------|
| `internal/executor/operators_storage.go` | `epqWait` + WFG registration + `maxEPQRetries=10` |
| `internal/executor/advisory.go` (or new `wfg.go`) | `waitForGraph` table + cycle detection |
| `internal/executor/operators_storage_test.go` | 3 new regression tests |
| `docs/design/README.md` | Index entry |
