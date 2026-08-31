# M0100-0005x — Upsert dirty-snapshot probe + RR/SER 40001 raise on in-flight insert commit

Status: accepted (2026-05-15 loop 39)

## Problem

`partition-key-update-3.spec` is one of the 21 RC-isolation specs that
M0100-0005 must close.  After M0100-0005s (upsert waits for in-flight
xmax on a visible match) and M0100-0005t (partition-aware upsert routing
+ per-leaf arbiter inheritance), all 8 permutations of that spec still
deferred with two distinct symptoms:

1. **Permutations 1 / 5 (RR / SER, `s2donothing` first)** — `s2donothing`
   inserts `(1, 'session-2 donothing')` on a partitioned table where
   `s1u` is concurrently moving the row at `a=1` cross-partition.  After
   `s1u` commits, upstream's `_bt_check_unique` re-probes via
   `DirtySnapshot`, observes the old tuple in `foo1` as dead
   (`xmax` committed), and lets the INSERT proceed.  Our `probeArbiter`
   re-probed via `mvcc.TupleVisible` under `s2`'s frozen RR snapshot,
   which still placed `s1.xid` on the `InProgress` list — so the dead
   tuple was reported as visible, the apparent conflict survived, and
   `DO NOTHING` silently skipped the insert.  Expected output line
   `1|session-2 donothing` never appeared in `s2select`.

2. **Permutations 2 / 6 (RR / SER, `s3donothing` first)** — `s3donothing`
   inserts `(2, ...), (2, ...)` while `s1u` has an in-flight insert in
   `foo2` (the cross-partition destination).  After `s1u` commits,
   upstream raises `40001 "could not serialize access due to concurrent
   update"` because the conflicting row's xmin is later than `s3`'s
   snapshot.  Our wait loop completed the wait but then either re-probed
   (Case 1 → snapshot-invisible → no conflict → silent INSERT producing
   a duplicate) or — if a tuple visibility quirk surfaced it — fell
   through to `DO NOTHING` skip.  No `40001` was emitted.

Both symptoms trace to the same underlying gap: `probeArbiter` was using
snapshot-MVCC semantics (`mvcc.TupleVisible`) where upstream uses
DirtySnapshot, and `probeArbiterWaiting` had no SERIALIZABLE/RR-specific
raise after waiting on a Case 1 conflict.

## Fix

Two coupled edits in `internal/executor/operators_upsert.go`:

### 1. `findInProgressConflict` distinguishes Case 1 vs Case 2

Signature widens to
`(xid storage.TransactionID, isInFlightInsert bool, found bool)`.
`isInFlightInsert == true` for the xmin-in-flight arm (Case 1);
`false` for the xmax-in-flight-on-visible-match arm (Case 2).
The caller needs this so it can decide whether `40001` is warranted
after the wait.

### 2. `probeArbiterWaiting` raises 40001 under RR / SER after Case 1 commits

After `WaitForXID` returns:

```go
if isInFlightInsert && o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted {
    if o.ctx.TxnMgr != nil && !o.ctx.TxnMgr.HasAbortedXID(inProgressXID) {
        return storage.ItemPointer{}, nil, false, &ExecError{
            Code:    "40001",
            Pos:     o.plan.Pos(),
            Message: "could not serialize access due to concurrent update",
        }
    }
}
```

- **Case 1 + RR/SER + xact committed** → raise 40001.  This is the
  permutation 2/6 path: the conflicting row's xmin is later than our
  snapshot, so allowing the INSERT to proceed (or DO NOTHING / DO UPDATE
  to act on it) would violate RR isolation.  Upstream's
  `_bt_check_unique` reaches `XactLockTableWait` → discovers the
  committer is later than our snap → calls
  `ereport(ERROR, errcode(ERRCODE_T_R_SERIALIZATION_FAILURE), ...)`.
- **Case 1 + RR/SER + xact aborted** → no raise; loop refreshes
  snapshot, re-probes, finds no row, INSERT proceeds.
- **Case 2 (xmax-in-flight)** → no raise; the deletion clears the
  apparent conflict, INSERT proceeds.  Upstream's INSERT path has no
  serialization check on a deleted row (the deleter, not the inserter,
  is the one whose write-write conflict surfaces).
- **RC (any case)** → no raise; loop refreshes snapshot and re-probes.

### 3. `probeArbiter` uses `isLiveForUniqueCheck` (dirty-snapshot semantics)

Replaces `mvcc.TupleVisible(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID)`
with `isLiveForUniqueCheck(o.ctx, tuple.Header.Xmin, tuple.Header.Xmax)`
(defined in `operators_storage.go`).  The helper already implements the
upstream DirtySnapshot subset for unique-check needs:

- `xmin` settled / live unless aborted or in-flight (returned live so
  the caller's downstream wait+check sees the conflict).
- `xmax` dead iff committed (regardless of whether our frozen RR
  snapshot still treats the deleter as in-progress).
- Self-xact `xmax` short-circuited to dead (M0100-0005u).

This makes the Case 2 post-wait re-probe correctly classify the
just-deleted tuple as dead under RR, so `probeArbiterWaiting`'s outer
loop exits with `(_, _, false, nil)` and the INSERT proceeds — closing
permutation 1/5.

For RC the change is a no-op: the per-statement snapshot already sees
committed deletes as committed, so `isLiveForUniqueCheck` and
`TupleVisible` agree on every code path the upsert exercises.

For RR/SER on the **non-waited** path the change is a strict
correctness improvement: a row inserted by an xact that committed after
our snapshot is now correctly surfaced as a conflict (dirty rules),
where `TupleVisible` would have hidden it under the InProgress list.
The 40001-on-commit raise still gates whether that conflict is allowed
to proceed; the dirty probe just ensures it surfaces in the first place.

## Verification

### Unit / server-layer (new)

- `internal/server/upsert_rr_inflight_insert_test.go`:
  - `TestUpsertDoNothing_RR_RaisesSerializationOnInFlightInsertCommit`
    — s2 begins RR, materialises snapshot, s1 INSERTs in-flight, s2
    INSERT … ON CONFLICT DO NOTHING blocks ≥ 250 ms on s1.xid, then
    surfaces `40001 "could not serialize access due to concurrent
    update"` within 5 s of `s1c`.
  - `TestUpsertDoNothing_RC_DoesNotRaiseSerializationOnInFlightInsertCommit`
    — same scenario under RC; expected to silently DO NOTHING after
    waking up, final table has `(1,'s1')`.

### Existing regression pins (unchanged)

- `TestUpsertDoNothing_WaitsForInFlightDelete` (M0100-0005s) —
  Case 2 still works under RC.
- `TestUpsertPartitioned_RoutesToLeafAndProbesLeafArbiter`
  (M0100-0005t) — partition routing untouched.

### Isolation suite (new pass)

- `TestPort_IsolationPartitionKeyUpdate3` flips from `SKIP (deferred)`
  to PASS end-to-end (all 8 permutations green).

### Adjacent isolation pass (unchanged)

- `TestPort_IsolationLockCommittedUpdate` — PASS
- `TestPort_IsolationInsertConflictDoUpdate` — PASS
- `TestPort_IsolationInsertConflictDoNothing` — PASS
- `TestPort_IsolationFkSnapshot` — PASS
- `TestPort_IsolationPartitionKeyUpdate1` — PASS
- `TestPort_IsolationPartitionKeyUpdate2` — PASS

### Package suites

`go test -count=1 -race ./internal/executor/ ./internal/server/
./internal/storage/ ./internal/mvcc/ ./internal/wal/` PASS.

## Files

- `internal/executor/operators_upsert.go` —
  `findInProgressConflict` signature widened (extra return); 
  `probeArbiterWaiting` adds RR/SER + Case 1 → 40001 raise;
  `probeArbiter` switches to `isLiveForUniqueCheck`.
- `internal/server/upsert_rr_inflight_insert_test.go` — new test file
  with the two regression pins.
- `docs/design/0100-0005x-upsert-dirty-probe-and-rr-serialization-raise.md`
  — this document.
- `docs/design/README.md` — index entry.
