# 0094-0005c — Standby Hot-Read MVCC Visibility

## Problem

After physical WAL streaming replication was wired (M0094-0005 loops 1–2), the
standby correctly received and applied WAL records to its storage pages, but
`SELECT` queries on the standby did not see the replayed rows.

Root cause: `mvcc.Manager.captureSnapshotLocked` sets `Xmax = nextXID`.  For
a committed tuple to be visible the snapshot check requires `xmin < Xmax`.  On
the standby `nextXID` was loaded from the cloned catalog snapshot (the primary's
`NextXID` at clone time, e.g. 6).  The primary then restarted from the same
snapshot, allocated XID 6 for the first INSERT, committed, and emitted
`RecordKindXactCommit(6)` to the WAL stream.  The standby's `StreamReplayer`
applied the `RecordKindHeapInsert` (storing the tuple with `xmin=6`) but treated
`RecordKindXactCommit` as a no-op.  Result: standby's `nextXID` remained 6, so
`Xmax = 6`, and the visibility check `6 >= 6 → invisible` hid the row.

## Fix

Two new methods on `mvcc.Manager`:

```
ReplayXactCommit(xid TransactionID)
    Advances nextXID to max(nextXID, xid+1).  Called when the standby
    replays a commit record so subsequent snapshots see xmin < Xmax.

ReplayXactAbort(xid TransactionID)
    Same nextXID advance, plus inserts xid into abortedXIDs so rolled-back
    tuples from the primary remain invisible (without this, an aborted XID
    that falls below Xmax would be incorrectly visible).
```

`wal.StreamReplayer` gains:

```
SetXactReplayHook(fn func(xid TransactionID, committed bool))
    Installs a callback called for every RecordKindXactCommit /
    RecordKindXactAbort the replayer processes (outside the replayer's
    own mutex to avoid lock ordering issues with concurrent snapshot
    takers).
```

`cmd/goopg/main.go` `startStandbyReplayer` wires the hook:

```go
sr.SetXactReplayHook(func(xid storage.TransactionID, committed bool) {
    if committed {
        txnMgr.ReplayXactCommit(xid)
    } else {
        txnMgr.ReplayXactAbort(xid)
    }
})
```

## Why This Is Sufficient for v0

The hot read path (`TupleVisible → SeesCommittedXID`) uses only the snapshot's
`Xmin / Xmax / InProgress / Aborted` fields — it does **not** consult the clog.
The clog is only used during cold-start catalog loading (`loadUserTablesFromHeap`)
which runs before the standby serves queries.  Advancing `nextXID` is therefore
sufficient to make streaming-replayed tuples visible without touching the clog.

## Verification

`TestE2E_PhysicalReplication` (internal/testport): inserts a row on the primary
after the standby is streaming and polls for it via SELECT on the standby.  Now
PASS (was perpetually timing out before this fix).

## Related Docs

- `docs/design/0094-0005-standby-iterator-tail-anchor.md` — loop 1 fix
- `docs/design/0094-0005b-virtual-view-plan-cache-staleness.md` — loop 2 fix
- `docs/design/0005-0002-standby-recovery-and-replay.md` — overall standby design
