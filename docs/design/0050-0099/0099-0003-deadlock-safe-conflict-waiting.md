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

---

## Follow-up 2026-08-10 — WFG edge provenance (`GOOPG_DEBUG_WFG=1`), and the pgbench TPC-B false-deadlock diagnosis (AI-20260810-011258-006)

### Why the diagnostic exists

The WFG's verdict is only as trustworthy as its edges. When
`registerWFGAndCheckCycle` closes a cycle the caller sees a bare `dl == true`
and raises `40001 could not serialize access due to concurrent update
(deadlock)` — an edge that does not correspond to a live blocking relationship
is indistinguishable, at the call site, from a real deadlock.

pgbench TPC-B is deadlock-free by construction: every transaction takes its row
locks in the fixed order `pgbench_accounts` → `pgbench_tellers` →
`pgbench_branches`, so wait-for edges only ever point "forward" and can never
close a cycle. The nightly stage nevertheless reports thousands of these 40001s
(AI-20260810-011258-006: 1488 failed transactions, 0.154%, TPC-B only). Every
one of them is therefore a false positive, and the question is which edge is
wrong.

`GOOPG_DEBUG_WFG=1` answers it. It costs nothing when unset (one bool read on
the register path) and, when set, records per waiting XID: the wall-clock age of
the edge, the `epqWait` call site (`runtime.Caller`), and the exact tuple the
waiter is about to block on — `rel/blk/slot`, its `xmin`/`xmax`, its `t_ctid`,
whether that ctid makes it a **superseded** (non-head) version, and its
infomask. On cycle detection the whole path is dumped to stderr. Repro driver:
`analysis/wfg-tpcb-repro.sh` (s=10/20, c=100, T=60–120, `--verbose-errors`).

### What the dumps show

Reproduced on every run (2 – 12 cycles per 60–120 s at s=10–20, c=100):

1. **Every cycle is a 2-cycle, and both edges are microseconds-to-milliseconds
   old.** No stale-edge/leak theory survives: `deregisterWFG` is doing its job,
   both participants really are inside `WaitForXID` at the moment the cycle
   closes.
2. **Both participants are always waiting on the same relation, almost always
   the same block, at *different* slots** — e.g.
   `blk=564 slot=49` vs `blk=564 slot=20`. They are not contending for one
   tuple; they are contending for two different *versions* sitting in the same
   hot page.
3. **The waited-on version is usually `superseded=true`** — its `t_ctid`
   already points at a successor. Upstream never blocks on an arbitrary
   mid-chain version: `heap_update` returns `TM_Updated` carrying the
   successor's ctid and `ExecUpdate` re-enters EvalPlanQual on the **head** of
   the chain (`EvalPlanQualFetch` / `heap_lock_tuple` walk `t_ctid` until the
   version is its own successor). goopg instead blocks on the xmax of whatever
   version its scan happened to land on.
4. **The waiter has usually already stamped a version of its own in the same
   page.** In the cycle `299166 → 299284 → 299166`, tuple `(564,20)` carries
   `xmax=299166` with `ctid=(564,48)` — 299166 had already applied its update
   and created a successor, yet it is blocked *again* inside the same statement.
   A TPC-B transaction updates exactly one `pgbench_branches` row, so a
   transaction that both holds a version and waits for another one in the same
   relation is re-visiting the row pile — which is also what makes the mutual
   wait possible in the first place.
5. **Some waited-on versions carry `xmax` numerically *older* than their own
   `xmin`** (`(732,131) xmin=408031 xmax=408026`; `(801,89) xmin=449649
   xmax=449604`). A transaction cannot legally stamp xmax on a version created
   by a transaction that started after it — it cannot see it. Those stamps are
   invalid, and an edge derived from one points at a transaction that never held
   the row.

### Reading

The deadlock verdict is a symptom, not the disease. The WFG cycle detector is
correct given its inputs; the inputs are wrong because the EPQ conflict path
identifies the blocker from a non-head tuple version (and, per (5), sometimes
from an invalid xmax stamp). Two consequences, in priority order:

- **Correctness beyond the 40001s:** if a single UPDATE really is re-visiting
  more than one version of the same logical row, `bbalance = bbalance + :delta`
  can be applied twice, breaking the TPC-B invariant
  `sum(pgbench_branches.bbalance) = sum(pgbench_accounts.abalance)`. That
  invariant is not currently asserted anywhere and should be, independently of
  this fix.
- **The fix direction:** before registering a WFG edge, the EPQ path must walk
  `t_ctid` to the head of the update chain and take the blocker from the head's
  xmax (upstream `ExecUpdate` → `EvalPlanQualFetch`,
  `postgres/src/backend/executor/execMain.c`; `heap_lock_tuple`,
  `postgres/src/backend/access/heap/heapam.c`). goopg has a chain-follow helper
  already (`epqFollowHOT`), but it runs *after* the wait, not before it.

Both are carried as deferral-ledger rows; `AI-20260810-011258-006` stays open.

### Files

| File | Change |
|------|--------|
| `internal/executor/operators_storage.go` | `wfgDebug`, `wfgEdgeInfo`, `wfgDebugTarget`, `wfgNoteTarget`, `wfgCallSite`, `dumpWFGCycle`; note calls at the two hot `epqWait` sites |
| `analysis/wfg-tpcb-repro.sh` | repro driver (untracked analysis area) |

## Resolution (2026-08-10) — the disease was a duplicate index entry, not the wait target

The previous section's "fix direction" was **tested and rejected**, then the real
cause was isolated. Both experiments used the same driver
(`SCALE=10 T=120 REPO_ROOT=$PWD bash analysis/wfg-tpcb-repro.sh`,
s=10 c=100 j=20, ~460k transactions per run), which reproduced 8 `WFG deadlock`
cycles and 8 failed transactions per run, deterministically.

### Rejected: skipping edges for settled holders

Hypothesis: `isConcurrentlyUpdated` reads a stale `ctx.Snap`, so an
already-committed writer is still named as the blocker, and two µs-lived edges to
finished transactions close a phantom cycle. Guarding edge registration with
`TxnMgr.IsXIDActive(xmax)` changed nothing — **8 cycles before, 8 after**. The
cycle dumps confirm why: every participant is genuinely still running.

### Rejected: redirecting the wait to the chain head

Hypothesis (the section above): walk `t_ctid` to the head before registering the
edge, so the blocker is the transaction holding the current version. Implemented
as `epqResolveHeadBlocker` at both hot `epqWait` sites, with `epqWait` treating a
self/invalid holder as "nothing to wait for". Again **8 cycles before, 8 after**.
The dumps then showed the redirect *working* — an edge whose target differed from
the landed version's xmax — and the cycle closing anyway, because the two
transactions each genuinely held the head of a different tuple in the same page
and waited on each other. Waiting on the wrong version was a symptom, not a
cause. The code was reverted.

### The cause: one UPDATE applying itself twice to the same tuple

That the two participants each held *several* tuples in one page is the tell.
TPC-B updates exactly one row per table per transaction, so a transaction should
never hold two versions in a hot page.

`updateOp`'s index-scan path drives writes from `tree.RangeScan` over the index,
resolving each scanned entry through `followHOTChain` to the live version. A
non-HOT update inserts a fresh index entry and leaves the superseded one indexed
until VACUUM, so under concurrency several live entries for one key can each
resolve to the **same** live tuple. Every one of them was appended to `pending`,
and the modification phase then applied the SET expression — and stamped xmax —
once per entry. That is the missing TPC-B balance invariant predicted above,
observed directly: the duplicate stamps left extra xmax'd versions in the hot
page, and two clients waiting on each other's leftovers closed a wait-for cycle
PostgreSQL cannot produce (`ExecUpdate` is driven by the plan's tuple stream, and
`heap_update` on a tuple the current command already modified returns
`TM_SelfModified` — `postgres/src/backend/access/heap/heapam.c` — it never
re-applies).

**Fix:** `updateOp`'s index-scan collector skips an entry whose resolved
`(block, live slot)` is already in `pending`. One physical tuple, at most one
modification per UPDATE.

**Result:** 8 → **0** `WFG deadlock` cycles and 8 → **0** failed transactions,
reproduced across two independent 120 s runs plus one 30 s run. Instrumenting the
skip shows it firing 13 times in 177k transactions — the same rarity as the
cycles it removed. TPS is unchanged (3.7k–4.2k, within run-to-run spread).

### Still owed

A deterministic regression test. The duplicate needs a concurrent non-HOT
interleaving: a sequential in-process fixture (50 index-scan updates of one row,
2 KB filler to force page overflow, plan verified as `*planner.IndexScan`) never
produces two entries resolving to the same tuple, so it passes with and without
the fix and was not committed. Carried as a deferral-ledger row.

### Files (resolution)

| File | Change |
|------|--------|
| `internal/executor/operators_storage.go` | `updateOp` index-scan path: skip an index entry whose resolved `(blk, actualSlot)` is already pending |
