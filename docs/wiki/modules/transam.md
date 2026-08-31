# Module: `internal/access/transam`

The **transaction manager** — MVCC snapshots, transaction-commit log (clog),
sub-transactions, multixact tuple locks, and Serializable Snapshot Isolation
(SSI). It is the Go analogue of PG's `src/backend/access/transam/`
(`clog.c`, `subtrans.c`, `multixact.c`, `predicate.c`, `procarray.c`,
`snapshot.c`, `xact.c`).

The package is where a transaction's XID is assigned, its commit/abort is
recorded durably in clog, and its visibility is computed for every tuple read.
`internal/executor` drives the SQL-level transaction verbs (`BEGIN`/`COMMIT`/
`ROLLBACK`/savepoints) through the `Manager` interface; the WAL layer emits and
replays `XACT_COMMIT`/`XACT_ABORT` records through the same manager.

## Key Files

- `manager.go` (1,401) — `Manager`/`Transaction`: XID allocation, snapshot
  capture/rotation, commit/abort, sub-xid allocation, waiting on other XIDs,
  `OldestXmin` computation, proc-array-like active-XID tracking.
- `clog.go` (885) — the commit log (`pg_xact`): per-XID 2-bit commit/abort
  status, async commit batching, `EnablePGSLRUMirror`.
- `clog_bufferpool.go` (511) — the clog SLRU buffer pool (in-memory page cache
  over the on-disk `pg_xact/` segments).
- `clog_statuscache.go` — per-XID status cache (committed/aborted/in-progress).
- `subxact.go` / `subxact_slru.go` / `subxact_visibility.go` (595) —
  sub-transaction XID tracking, the `pg_subtrans` SLRU, and visibility that
  walks parent XIDs.
- `multixact/` — the MultiXactId engine (a MultiXactId is a set of locker XIDs
  on one tuple): `multixact.go` (in-memory sets + membership) and `store.go`
  (the durable `pg_multixact/{offsets,members}` SLRU, `NewStore`/`NewStoreAt`).
- `ssi.go` / `ssi_conflict.go` / `ssi_precommit.go` — Serializable Snapshot
  Isolation: rw-antidependency edges, predicate locks, pre-commit dangerous
  structure detection, conflict-out.
- `snapshot.go` — `Snapshot` struct (xmin/xmax/active-xid lists) + capture.
- `visibility.go` — the core `TupleVisible`/visibility rules (xmin/xmax against
  snapshot, hint-bit fast paths).
- `xidgen.go` — XID counter and wrap-around management.
- `combocid.go` — command-id / combo-cid (cmin/cmax) tracking.
- `predlock.go` (586) — predicate-lock substrate (hash-bucket SIREAD locks).
- `procarray.go` — active-snapshot/active-XID array for cross-backend visibility.
- `partition_detach_epoch.go` — snapshot epoch counter for concurrent
  `DETACH PARTITION CONCURRENTLY`.
- `control/` — pg_control decode/encode (`pgcontrol.go`, `control.go`): the
  control file that records DB state, checkpoint LSN, nextXID/nextOid, TLI.

## Public API

```go
func NewManager() *Manager
func (m *Manager) Begin(...) *Transaction
func (t *Transaction) AssignXID()                  // materialize an XID lazily
func (t *Transaction) FreshSnapshot() *Snapshot    // per-statement snapshot
func (t *Transaction) SnapshotFor(...) *Snapshot   // statement-level snapshot
func (m *Manager) Commit(...) / CommitAsync(...) / Rollback(...)
func (m *Manager) AllocateSubXid()                 // sub-transaction XID
func (m *Manager) WaitForXID(xid, ...)             // wait for another txn
func (m *Manager) OldestXmin() TransactionID       // vacuum horizon
func (m *Manager) NextXID() TransactionID / SetNextXID(xid)
func (m *Manager) ReplayXactCommit(...) / ReplayXactAbort(...) // WAL recovery

// CLog
func (c *CLog) DidCommit(xid, parentOf) bool
func (c *CLog) GetStatus(xid) TxnStatus
func (c *CLog) SetCommitted(xid) / SetAborted(xid)

// Multixact
func (s *Store) Members(mxid) ([]Member, bool)
func (s *Store) Create(m1, m2 Member) (MultiXactId, error)
func (s *Store) CreateFromMembers(members []Member) (MultiXactId, error)
func (s *Store) Expand(mxid, add, live) (MultiXactId, error)
```

## Internal structure

- **XID lifecycle** — `Manager.Begin` creates a `Transaction` with no XID;
  `AssignXID` materializes one only at first write (read-only transactions never
  take an XID — the M0093 read-only commit fast path). `NextXID`/`SetNextXID`
  are seeded from pg_control at startup and advanced by WAL replay.
- **Snapshot** — `FreshSnapshot`/`SnapshotFor` capture
  `{xmin, xmax, activeXIDs[]}` from the proc array; the read-committed
  per-statement refresh vs RR/Serializable pinned snapshot is decided here.
- **Clog** — a 2-bit status per XID across 32 pages/segment; the buffer pool
  caches pages, with an async-commit LSN watermark so `CommitAsync` can defer
  the durable write. `TransactionIdDidCommit/Abort` are the visibility backend.
- **Multixact** — a MultiXactId is an array of locker XIDs; `store.go`
  persists `{offsets, members}` to SLRU so a multi-xmax on disk survives
  restart. `NewStoreAt` seeds from pg_control's `nextMulti`.
- **SSI** — predicate locks (hash-bucket + tuple-grain), rw-antidependency
  edges between in-flight transactions, and pre-commit conflict detection
  produce 40001 serialization failures on dangerous structures.
- **Visibility** — `visibility.go` computes, for a tuple header and a snapshot,
  whether the row is visible, walking sub-xids and multixacts.

## Dependencies

- **Used by** — `internal/executor` (every DML/DDL path: snapshot lookup,
  xid stamping, commit/rollback), `internal/storage` (HOT-chain visibility),
  `internal/initdb` (recovery, pg_control), `internal/access/nbtree`
  (visibility during index scans), `internal/postmaster` (transaction verbs).
- **Uses** — `internal/storage` (pages, WAL redone), `internal/access/transam/xlog`
  (WAL emission/replay of commit records), `internal/utils/activity`,
  `internal/utils/misc`, `internal/port/runtimeshim`.

## Notable patterns / gotchas

- **XID wraparound** — XID comparison uses signed arithmetic (`xid - y < 0`
  means "older"), never bare `<`; wrap-around guards (`xidWarnAge`/`xidStopAge`)
  must not be "simplified".
- **Lazy XID** — a read-only transaction has no XID until it writes; every
  "who is the writer" site must tolerate `InvalidTransactionID`.
- **Clog fault-in** — the buffer pool zero-fills a missing/short `pg_xact`
  segment on read (a real PG's `SimpleLruReadPage` hard-errors); used by
  recovery and by a goopg-owned cluster restart.
- **Async commit** — `CommitAsync` returns before the clog segment is durable;
  the LSN watermark + wal-writer flush close the window, and `ReplayXactCommit`
  reconstructs the commit from WAL.
- **Sub-xid overflow** — beyond a threshold (`SubXactArraySize`), sub-xids
  overflow and visibility must fall back to parent-XID checks
  (`subxact_visibility.go`).
- **SSI rw-antidependency discipline** — a tuple read in SERIALIZABLE that a
  concurrent writer later commits against forms an rw-edge; forgetting the
  conflict-out produces a write skew that PG detects (`ssi_precommit.go`).