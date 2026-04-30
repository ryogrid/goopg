# REF-017: WAL Redo / Crash Recovery

## Overview

Crash recovery replays WAL records from the last checkpoint forward to restore the database to a consistent state. goopg's recovery re-applies heap, B-tree, and transaction-marker WAL records to bring data files up to date.

## goopg Implementation

**Packages:** `internal/wal/recovery.go`, `internal/wal/stream_replayer.go`

### Key Types

- `ReplayFromDir` — walks WAL segment files from a given starting
  LSN, decodes records, and calls `ApplyRecord`.
- `ApplyRecord` — dispatches on rmgr type and applies the record:
  - `RmgrHeap`: heap insert / delete / vacuum / lock.
  - `RmgrBtree`: B-tree insert / split.
  - `RmgrXact`: transaction commit / abort markers.
- `StreamReplayer` — receives WAL records from the WAL receiver
  and applies them in a continuous loop (standby mode).

### Recovery Flow

```
ReplayFromDir(walDir, startLSN)
  ├─ DetectWALFormat — auto-detect page-header vs legacy format
  ├─ ReadAll (segments) or readAllPageAware
  ├─ For each record:
  │    ├─ ApplyRecord (rmgr dispatch)
  │    │    ├─ RmgrHeap: heap (un)do — insert / delete / vacuum
  │    │    ├─ RmgrBtree: btree (un)do — insert / split
  │    │    └─ RmgrXact: commit/abort markers
  │    └─ Advance replay LSN
  └─ Return final redo position
```

### Recovery Types

- **Crash recovery** — happens at startup when `pg_wal` contains
  records past the last checkpoint. goopg replays all records.
- **Streaming recovery** — the `StreamReplayer` applies records as
  they arrive from the WAL receiver, enabling hot standby.

### WAL Record Types

| Rmgr ID | Record Kind | Payload |
|---------|-------------|---------|
| RmgrHeap | XlogHeapInsert | rel, blk, tuple bytes |
| RmgrHeap | XlogHeapDelete | rel, blk, slot, xmax |
| RmgrHeap | XlogHeapVacuum | rel, blk, dead-slots |
| RmgrBtree | XlogBtreeInsert | rel, blk, item bytes |
| RmgrBtree | XlogBtreeSplit | rel, left_blk, right_blk, left_page, right_page |
| RmgrXact | XlogXactCommit | xid |
| RmgrXact | XlogXactAbort | xid |

## PostgreSQL Implementation

PostgreSQL's recovery (`xlogrecovery.c`) is structured similarly
but significantly more complex:

- **Redo point** — PostgreSQL starts replay from the Redo LSN
  recorded in the checkpoint record. goopg uses the checkpoint
  marker's LSN.
- **Full-page images** — PostgreSQL writes full-page images (FPI)
  on the first modification after a checkpoint, enabling recovery
  to reconstruct the full page without relying on the previous
  page contents. goopg also writes FPIs via `logFPI`.
- **Consistency check** — PostgreSQL checks `pd_lsn` on each page
  during recovery to skip already-applied records (idempotent
  replay). goopg relies on record-level idempotency.
- **Recovery conflicts** — on a standby, recovery conflicts with
  queries must be resolved (e.g., by cancelling queries whose
  snapshots conflict with the WAL record being applied). goopg
  does not have a standby query path.
- **WAL summariser** — PG 17+ adds a WAL summariser for faster
  recovery by skipping over unused WAL segments.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Recovery starting point | Checkpoint marker LSN | Checkpoint Redo LSN |
| Consistency checking | Record-level idempotency | Page-level `pd_lsn` check |
| Recovery conflicts | None (no standby queries) | Query cancellation on conflict |
| WAL summariser | Not implemented | PG 17+ |
| Parallel replay | None | PG 18+ parallel redo |

## References

- goopg: `internal/wal/recovery.go`
- goopg: `internal/wal/stream_replayer.go`
- PG recovery: `postgres/src/backend/access/transam/xlogrecovery.c`
- PG redo: `postgres/src/backend/access/transam/README`
