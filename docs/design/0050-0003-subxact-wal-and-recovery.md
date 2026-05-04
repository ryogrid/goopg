# 0050-0003 — Subxact WAL and recovery

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0050 — Savepoints and subtransactions
**Supersedes:** —

## Context

Crash recovery has to rebuild the subxact-to-parent map and replay
abort records correctly. Without explicit subxact WAL, replay would see
subxact-xid-tagged rows but not know whether they were committed by the
top-level xact or rolled back by a `ROLLBACK TO`.

## Plan

1. New WAL record kinds:
   - `RecordKindXactAssignment` — emitted the first time a subxact
     allocates an xid. Payload: `(parentXid, subXids[])`.
   - `RecordKindXactRollbackTo` — emitted on `ROLLBACK TO SAVEPOINT`.
     Payload: `(parentXid, abortedSubXids[])`.
   - `RecordKindXactSubAbort` — emitted on subxact-only abort
     (top-level still in-progress). Payload: `(subXid)`.
   - Existing `RecordKindXactCommit` / `XactAbort` extended with a
     `subXids[]` list: every subxid still active at top-level
     commit/abort time is committed/aborted with the parent.
2. Replay rules:
   - Assignment: insert into the subxact-to-parent map.
   - RollbackTo / SubAbort: insert each into the rolled-back set.
   - Commit-with-subxids: every listed subxid commits with the parent.
   - Abort-with-subxids: every listed subxid aborts with the parent.
3. Idempotency: the maps are populated by record replay; replaying the
   same record twice produces the same state.
4. Wire alongside M0026's concurrent WAL append — assignment records
   are short and frequent; they ride the existing path.

## Definition of Done

- Crash + restart with an in-flight subxact pattern reproduces correct
  visibility (regression test extends the M0045 restart-after-retention
  harness).
- Replay round-trip test for each new record kind.
- WAL grow rate from a session that opens 100 savepoints / commits is
  not pathological (assignment records collapse via batching where
  possible — emit once per `N=64` new subxids).

## Upstream reference

- `postgres/src/backend/access/transam/xloginsert.c`,
  `xact.c` — `XLogRecordAssignmentInfo`, `RecordTransactionAbort`.
- `postgres/src/include/access/xact.h` —
  `xl_xact_subxacts`, `xl_xact_assignment` shape.

## goopg references

- `internal/wal/records.go` — record kind enum.
- `internal/mvcc/recovery.go` — replay driver.
- 0050-0001, 0050-0002 — in-memory model fed by replay.
