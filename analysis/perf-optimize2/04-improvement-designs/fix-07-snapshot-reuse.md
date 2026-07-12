# fix-07 — Snapshot reuse via a completion counter (P3)

## Problem (evidence)

Under Read Committed, goopg captures a fresh snapshot per statement:
`mvcc.(*Manager).captureSnapshot` (`internal/mvcc/manager.go:1277`) scans
all ProcArray slots (atomic loads, lock-free), copies the aborted-XID list
under an RLock, allocates an in-progress slice, and sorts it. Today this is
only **1.5 % of CPU** (the May-2026 `Manager.mu` bottleneck is resolved),
which is why this is P3 — but the cost is O(max_connections) per statement
and will resurface at higher backend counts or after fixes 01–03 remove the
larger overheads.

## PostgreSQL approach (03 §4)

PG14+ `GetSnapshotDataReuse`: every snapshot stores
`snapXactCompletionCount`, a copy of the global monotone
`TransamVariables->xactCompletionCount` bumped (under exclusive
ProcArrayLock) whenever an XID-bearing transaction ends. The next
`GetSnapshotData` compares counters under a *shared* lock; if unchanged, the
previous snapshot is provably identical and is returned without scanning.
Dense `ProcGlobal->xids[]` arrays keep the fallback scan cache-friendly.

## Design

1. `Manager` gains `xactCompletionCount atomic.Uint64`, incremented in
   `finish()` for every transaction that ever held an XID (commit *and*
   abort — abort changes visibility too), after the slot is cleared and the
   aborted-list updated (ordering: bump **after** all visibility-relevant
   state is published, so a reader that sees the new count sees the new
   state).
2. Each session caches its last snapshot + the count it was built at
   (`Session.lastSnap`, `lastSnapCompletion uint64`). `SnapshotFor` (RC
   path, `manager.go:403`) fast path:
   ```go
   if c := m.xactCompletionCount.Load(); c == s.lastSnapCompletion {
       return s.lastSnap // provably identical; refresh curcid only
   }
   ```
   Fallback: `captureSnapshot()` as today, then store snap + count read
   **before** the scan started (conservative: a concurrent commit during
   the scan forces one extra rebuild, never a stale reuse).
3. Command-ID semantics: goopg snapshots must keep per-statement `curcid`
   behavior for own-transaction visibility — reuse the snapshot's XID sets
   but refresh the command id, exactly as PG does.
4. The aborted-XID list copy participates: bump the counter on abort-list
   mutation (covered by "abort bumps too").

## Expected lift

Small today (≤1.5 % CPU + one alloc+sort per statement). Grows with
connection count; primarily a scalability-headroom and allocation-reduction
item. Bundle with the fix-06 re-profile round.

## Risks

- **Correctness-critical ordering**: a reuse when the counter *hasn't*
  changed must be provably identical — every path that changes visibility
  (commit, abort, subxact abort with visible effects, prepared-xact
  resolution, `MaterializeWriterXID` edge at first write) must bump or be
  proven not to affect snapshot contents. The savepoint/sub-XID lessons
  (memory: three-fix savepoint visibility) say to enumerate these paths
  explicitly in review.
- RR/SSI pin `firstSnap` and are unaffected; ensure the fast path is
  RC-only.

## Verification plan

1. Unit: counter-bump coverage test per finish path (commit/abort/subxact);
   reuse-equivalence property test (random commit interleavings: reused
   snapshot ≡ freshly captured one).
2. Isolation suite: the ported isolation specs
   (`internal/testport`, per M0118 groups) are the real gate — visibility
   bugs surface there; run the full isolation set, not a sample.
3. `make race-gate`; units + pgbench smoke; `run_su50.sh` acceptance
   (expect allocs/statement down; TPS neutral-to-slightly-up at c=50).
