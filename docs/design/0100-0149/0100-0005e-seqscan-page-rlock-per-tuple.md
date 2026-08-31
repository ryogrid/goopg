# 0100-0005e — seqScanOp page RLock per-tuple scoping

**Status:** accepted
**Milestone:** M0100-0005 (lock-committed-update deadlock fix)
**Date:** 2026-05-15

## Problem

`TestPort_IsolationLockCommittedUpdate` deadlocked on every permutation that
followed the shape

    s1: BEGIN
    s2: BEGIN
    s1: SELECT pg_advisory_lock(K)
    s2: SELECT * FROM lcu_table WHERE pg_advisory_lock(K) IS NOT NULL FOR KEY SHARE   ← waits
    s1: UPDATE lcu_table SET value = 'two' WHERE id = 1   ← waits forever
    s1: COMMIT

Goroutine dump captured at the 5-minute mark (loop 20 investigation note in
`.ralph/fix_plan.md` §M0100-0005) showed s1's UPDATE blocked inside
`lib/pq.(*conn).simpleQuery` waiting for the server to reply, with s1c COMMIT
and s1ul UNLOCK queued behind it on the same `*sql.Conn`. The runner had given
up on s2l after `drainWindow` and dispatched the rest of s1's steps; they all
piled up because s1u UPDATE never returned.

## Root cause

`seqScanOp.Next` (`internal/executor/operators_storage.go:313`) acquired the
page's read lock once per page open and released it only at
`releasePinned()` — i.e. at the page boundary or scan EOF. The intent was to
keep arena-backed Datum slices safe (the arena bytes alias page memory) for
the duration of the page's tuple iteration.

The consequence: when a parent operator's WHERE-clause evaluation blocked on
a user-defined function (here `pg_advisory_lock`), the page RLock was held
for the entire duration of the block. A concurrent UPDATE on the same page
needed the page WLock and stalled behind it. Because s1's COMMIT and
advisory-unlock were queued behind the stalled UPDATE on s1's own
connection, s1 could never release the advisory lock that s2 was waiting on.

The deadlock cycle:

    s2.seqScan holds page RLock  → blocks s1.UPDATE (page WLock)
    s1.UPDATE blocks             → s1.UNLOCK queued on same conn
    s1.UNLOCK never runs         → s2's advisory_lock(K) never acquires
    s2's WHERE never returns     → s2.seqScan never releases RLock

## Fix

Scope the page RLock to a single tuple's decode path instead of the whole
page iteration. After `PageGetHeapTuple` + `DecodeRowIntoArena` + optional
`DetoastRow`, the row is materialised via `cloneRowOwned` (deep-copy of any
arena-backed string/bytes Datums into owned `[]byte`) and the RLock is
released **before** the slot is yielded to the caller. The buffer pin is
retained so the page is not evicted between yields.

On the next call to `Next()` the inner loop re-RLocks briefly to read the
next slot's tuple. Page-line-pointer count is also captured under a brief
RLock at page open, so the per-tuple loop can read `o.slotMax` without
holding the lock.

Implementation sites in `internal/executor/operators_storage.go`:

* `seqScanOp.Next` — RLock now bracketed around `PageLinePointerCount` and
  around `PageGetHeapTuple` + `DecodeRowIntoArena` + `DetoastRow` +
  `cloneRowOwned` only. RUnlock happens on every exit branch from the inner
  loop (skip-on-error, invisible-tuple, decode-fail, detoast-fail,
  successful yield).
* `seqScanOp.releasePinned` — drops the per-page RLock acquisition. RLock
  is no longer outstanding when this method runs (always RUnlocked inside
  the inner loop), so calling RUnlock here would panic.

The ring-strategy branch is unchanged: ring buffers are per-scan private
memory with no concurrent writers, so no buffer lock is needed.

## Cost

`cloneRowOwned` runs once per yielded tuple (was once per retained tuple
in `lockRowsOp.drainAndStamp`, `sortOp.Open`, `windowOp.Open`,
`aggregateOp`, etc.). For pass-through pipelines (filter → project →
client) this is one extra alloc per row. For pipelines that already
materialised at a downstream retention boundary, the downstream
`Materialize()` is now a no-op (the row's arena Datums are already
promoted to owned bytes), so the steady-state allocation profile is
unchanged for those pipelines and bounded-extra for the pass-through
case.

The arena itself is still useful: it remains the per-page decode
landing pad inside `DecodeRowIntoArena` (one-time alloc per page,
amortised across all of the page's tuples). `cloneRowOwned` then
promotes the row's arena views into owned bytes before the RLock is
dropped.

## Regression evidence

`TestPort_IsolationLockCommittedUpdate` no longer deadlocks. All 24
permutations execute to completion in ≈7.5 s (was: 5-minute timeout
hang). Remaining diff is unrelated to the RLock change — the s1hint
SELECT after a committed UPDATE returns both the old and new heap tuple
versions; this is a separate visibility / dead-tuple bug in the
read-after-commit path on the same session, masked previously by the
deadlock.

`go test -race ./internal/executor/ ./internal/storage/ ./internal/server/`
all pass. Existing isolation-test SKIPs (FkSnapshot etc.) are unchanged.

## Out of scope

* `indexScanOp` likely has the same RLock-across-yield pattern; the
  lock-committed-update path goes through `seqScanOp` so this fix
  unblocks the M0100-0005 milestone, but a follow-up should mirror the
  pattern in `indexScanOp.Next` for parity.
* The s1hint dead-tuple visibility bug surfaced after the deadlock fix
  is its own follow-up — root cause is in the visibility check, not in
  the lock-scoping path.
