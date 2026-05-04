# pg_xact Commit Log (Milestone 0030, Sub-task 7)

| Field       | Value                                      |
| ----------- | ------------------------------------------ |
| Status      | accepted (landed 2026-05-04)               |
| Date        | 2026-05-04                                 |
| Milestone   | 0030 — Catalog Persistence and DDL WAL     |
| Refines     | [0030-0006-transactional-ddl.md](0030-0006-transactional-ddl.md) |

## Problem

After a server crash mid-transaction (no COMMIT or ROLLBACK was reached),
heap pages for partially-written catalog rows survive WAL replay.
`loadUserTablesFromHeap` checks only `xmax == 0` to decide if a pg_class row
is live, so it re-registers any table whose creating transaction was in-progress
at crash time — even though that transaction never committed.

The previous loop (crash+restart xmax-stamping) fixes the explicit-ROLLBACK
case: when `execRollback` runs, `deleteCatalogRowsForOID` stamps `xmax` on the
catalog rows so the startup scan skips them.  But if the server crashes **before
any ROLLBACK**, no xmax is written, and the table reappears after restart.

## Upstream reference

`postgres/src/backend/access/transam/clog.c` — the PostgreSQL commit log
(CLOG, now called pg_xact) stores 2 bits per XID in 8 KB pages under
`$PGDATA/pg_xact/`:

- `TRANSACTION_STATUS_IN_PROGRESS  = 0x00`
- `TRANSACTION_STATUS_COMMITTED    = 0x01`
- `TRANSACTION_STATUS_ABORTED      = 0x02`
- `TRANSACTION_STATUS_SUB_COMMITTED = 0x03`

`postgres/src/backend/access/transam/varsup.c` — XID assignment; every
allocated XID starts as IN_PROGRESS and is written COMMITTED or ABORTED at
finish time.

## Design

### File format

goopg uses a simpler flat-byte layout:

```
$PGDATA/global/pg_xact   (one file, no page framing)
```

One byte per XID, indexed directly by transaction ID:

| Value | Meaning |
|-------|---------|
| 0     | Unknown / in-progress at crash — treated as aborted on restart |
| 1     | Committed |
| 2     | Aborted |

The file grows on demand. Reading an offset past the end of the file returns
`TxnStatusUnknown` (0).

### Thread safety

All reads and writes acquire a `sync.RWMutex`.  The `SetXactMarkerLogger` hook
runs under the MVCC manager's own mutex, so no additional lock ordering is
needed there.

### Durability

`setStatus` calls `os.WriteFile` to atomically overwrite the clog on every
commit/abort.  For typical test workloads (hundreds to thousands of
transactions), the file is a few KB, so a full rewrite per transaction is
acceptable.  The WAL `XactCommit` / `XactAbort` record remains the primary
durability mechanism; the clog is a secondary index that enables fast startup
queries without WAL replay.

### Backward compatibility (upgrade path)

If the clog file does not exist when `Open()` runs (old cluster without clog
support), `Open()` creates it and calls `InitializeAsCommitted(nextXID)` to
mark all XIDs `[1, nextXID)` as COMMITTED.  This is conservative: any table
that was rolled back before the upgrade would still have a `xmax` stamp from
our earlier fix, so those rows are already skipped by the `xmax != 0` check.
Tables rolled back via pre-clog-era crashes (no xmax, no clog) remain visible,
which matches the old behaviour.

### Bootstrap XIDs

At `initdb` time, `bootstrapCLog(dataDir)` creates the clog and marks:

- XID 1 (`BootstrapTransactionID`) → COMMITTED
- XID 2 (`FrozenTransactionID`)    → COMMITTED

These XIDs stamp the system-catalog bootstrap rows; marking them committed
ensures `loadUserTablesFromHeap` never filters out the seeded pg_class /
pg_attribute / pg_type rows.

## Key changes

### `internal/mvcc/clog.go` (new)

```
type TxnStatus byte  // 0=Unknown, 1=Committed, 2=Aborted
type CLog struct { ... }

OpenCLog(path) (*CLog, error)
(c *CLog) GetStatus(xid) TxnStatus
(c *CLog) SetCommitted(xid) error
(c *CLog) SetAborted(xid) error
(c *CLog) InitializeAsCommitted(highXID) error  // upgrade path
```

### `internal/initdb/initdb.go`

`Init()` now calls `bootstrapCLog(abs)` after `bootstrapSystemCatalogs` so the
clog exists on the first `Open()` with XIDs 1 and 2 already COMMITTED.

### `internal/initdb/open.go` — `Open()`

1. After `loadCatalogSnapshot`, open (or create) the clog at
   `<DataDir>/global/pg_xact`.
2. If the clog was just created (no on-disk file), call
   `InitializeAsCommitted(txnMgr.NextXID())` (upgrade path).
3. Extend `SetXactMarkerLogger` to also write `SetCommitted` / `SetAborted` on
   the clog (non-fatal: error logged but commit/abort still proceeds).
4. Pass `clog` to `loadUserTablesFromHeap`.

### `internal/initdb/open.go` — `loadUserTablesFromHeap`

Signature extended to `(mgr, cat, clog *mvcc.CLog)`.  For each candidate
pg_class row, before registration:

```go
if clog != nil {
    if clog.GetStatus(ht.Header.Xmin) != mvcc.TxnStatusCommitted {
        continue // crashed-in-progress (Unknown) or rolled-back (Aborted)
    }
}
```

When `clog == nil` (old cluster without clog, should no longer happen after
upgrade), the old behavior (trust xmax==0) is preserved.

## Verification

### Unit tests (`internal/mvcc/clog_test.go`)

- `TestCLogRoundTrip` — set committed/aborted, re-read same process
- `TestCLogPersistence` — reopen from disk, verify statuses survive
- `TestCLogUnknownForMissingEntry` — GetStatus on fresh XID returns Unknown
- `TestCLogInitializeAsCommitted` — all entries ≤ highXID are COMMITTED after init

### Integration test (`internal/initdb/catalog_heap_crash_test.go`)

- `TestCrashMidTransactionTableNotVisibleAfterRestart` — simulate crash
  mid-transaction by writing pg_class rows directly (bypassing ROLLBACK),
  then re-opening. Without clog: table reappears. With clog: absent.

## What this does NOT fix

- Visibility of **data rows** (non-catalog heap tuples) from crashed
  transactions: the MVCC snapshot on startup has an empty `InProgress` set, so
  old in-progress XIDs look committed to live queries.  Full MVCC clog
  integration (consulting clog in `Snapshot.Visible()`) is deferred.
- DROP TABLE / DROP INDEX rollback (pre-existing limitation).
