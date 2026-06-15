# CLOG Reference Map — PostgreSQL 18.3 → goopg

*Analysis date: 2026-06-14 · Branch: `align-data-structure-with-pg`*

Detailed, cited, side-by-side comparison. Each section is structured as
**PostgreSQL (oracle)** → **goopg** → **Difference**. See
[`clog-goopg-vs-postgres-overview-2026-06-14.md`](clog-goopg-vs-postgres-overview-2026-06-14.md)
for orientation and [`clog-goopg-gaps-and-remediation-2026-06-14.md`](clog-goopg-gaps-and-remediation-2026-06-14.md)
for the prioritized gap list.

All `postgres/...` citations are PG 18.3; all `internal/...` citations are goopg
at branch HEAD.

---

## 1. On-disk format

**PostgreSQL.** Two bits per XID, four transactions per byte, four status codes
(`postgres/src/include/access/clog.h:27-30`):

```c
#define TRANSACTION_STATUS_IN_PROGRESS     0x00
#define TRANSACTION_STATUS_COMMITTED       0x01
#define TRANSACTION_STATUS_ABORTED         0x02
#define TRANSACTION_STATUS_SUB_COMMITTED   0x03   /* committed subxact, parent undecided */
```

Layout constants (`postgres/src/backend/access/transam/clog.c:61-89`):

```c
#define CLOG_BITS_PER_XACT   2
#define CLOG_XACTS_PER_BYTE  4
#define CLOG_XACTS_PER_PAGE  (BLCKSZ * CLOG_XACTS_PER_BYTE)   /* 8192*4 = 32768 */
TransactionIdToPage(xid)    = xid / CLOG_XACTS_PER_PAGE
TransactionIdToPgIndex(xid) = xid % CLOG_XACTS_PER_PAGE
TransactionIdToByte(xid)    = TransactionIdToPgIndex(xid) / CLOG_XACTS_PER_BYTE
TransactionIdToBIndex(xid)  = xid % CLOG_XACTS_PER_BYTE     /* lane; shift = lane*2 */
```

Segment = `SLRU_PAGES_PER_SEGMENT = 32` pages (`postgres/src/include/access/slru.h`),
so 32 × 32768 = **1,048,576 XIDs per segment file**, named by `%04X`/`%06X` of
the segment number under `pg_xact/`.

**goopg.** Two different representations:

- *Primary flat file* (`internal/mvcc/clog.go:14-46`): one **byte** per XID,
  three states only — `TxnStatusUnknown=0`, `TxnStatusCommitted=1`,
  `TxnStatusAborted=2`. Stored at `<DataDir>/global/pg_xact` as a contiguous
  byte array indexed directly by XID. In memory it is split into `clogBank`s of
  `xidsPerBank = 128*1024` XIDs each (`clog.go:30`).
- *SLRU mirror* (`internal/mvcc/clog.go:457-468`) reproduces PG's exact layout:

```go
clogBitsPerXact     = 2
clogXactsPerByte    = 4
clogXactsPerPage    = storage.BlockSize * clogXactsPerByte // 32768
slruPagesPerSegment = 32
clogXactsPerSegment = clogXactsPerPage * slruPagesPerSegment // 1048576
pgClogStatusInProgress = 0x00
pgClogStatusCommitted  = 0x01
pgClogStatusAborted    = 0x02
```

The per-XID byte/page/lane math in `mirrorToSLRUUnlocked`
(`clog.go:712-717`) is identical to PG's macros.

**Difference.** PG uses 2 bits/XID everywhere; goopg's *runtime* store is 8
bits/XID (16× larger) and only the *mirror* is 2 bits/XID. goopg has **no
`SUB_COMMITTED` (0x03) state** — committed subtransactions are not represented
on disk at all. The SLRU mirror is byte-compatible with PG for the three states
it emits; goopg's `loadFromSLRU` defensively reads a stray `0x03` lane as
committed (the `default` case at `clog.go:608-616`).

---

## 2. In-memory caching

**PostgreSQL.** SLRU shared-memory buffer pool
(`postgres/src/backend/access/transam/slru.c`,
`postgres/src/include/access/slru.h`): a fixed set of page slots, grouped into
banks of `SLRU_BANK_SIZE = 16`, with per-page LRU counters, dirty flags, and a
page-replacement victim search (`SlruSelectLRUPage`). The most-recently-zeroed
page (`latest_page_number`) is pinned and never evicted. Buffer count is
auto-tuned (`CLOGShmemInit`) and capped by `transaction_buffers`. Only a working
set of CLOG pages is resident; the rest stays on disk.

**goopg.** No buffer pool and no eviction. The **entire** status array is
resident in per-bank byte slices, grown lazily on first write
(`getOrCreateBank`, `clog.go:64`). The SLRU mirror files are touched with
ordinary `os.OpenFile`/`ReadAt`/`WriteAt` per update — there is no page cache in
front of them.

**Difference.** PG bounds CLOG memory to a tunable buffer count and pages in/out
on demand; goopg trades memory for simplicity by keeping all status bytes
resident (≈1 byte/XID). At PG-scale XID counts this is a memory-scaling concern
(see remediation doc) but is a non-issue at current goopg workloads.

---

## 3. Setting status (commit / abort)

**PostgreSQL.** `TransactionIdSetTreeStatus`
(`postgres/src/backend/access/transam/clog.c`) sets the status of a transaction
*and its subtransaction tree* atomically with respect to readers. When the tree
spans multiple CLOG pages it uses a 3-phase protocol: off-page subxids are first
marked `SUB_COMMITTED`, then the page holding the top XID is flipped to
`COMMITTED`, then the off-page subxids are finalized — so a reader never sees a
subxact committed before its parent. Under contention, **group commit**
(`TransactionGroupUpdateXidStatus`) batches many backends' updates behind a
single bank-lock acquisition via the lock-free `clogGroupFirst` queue in shared
memory. The group-update optimization is applied only when a transaction has
fewer than `THRESHOLD_SUBTRANS_CLOG_OPT = 5` subxids (`clog.c:103`) — this
bounds which transactions opt in, not the queue depth.

**goopg.** `setStatus` (`internal/mvcc/clog.go:373`) writes a single byte under
the relevant bank's write lock, returns early if unchanged (idempotent), then
rewrites the whole flat file via `flush` (`clog.go:398`) and updates the SLRU
mirror lane via `mirrorToSLRUUnlocked` (`clog.go:686`, fsync per call). Public
entry points are `SetCommitted`/`SetAborted` (`clog.go:190-197`). There is no
tree/subxact atomicity and no group commit — each XID is stamped independently.

**Difference.** goopg has no `SUB_COMMITTED` phase and no multi-page tree
atomicity (it does not record subxact commit status on disk at all), and no
group-commit batching. Its commit path also rewrites the *entire* flat file on
each status change (`os.WriteFile` of the whole array) and does a per-commit
fsync of the mirror segment — simpler but O(file size) per commit and
single-flush per backend.

---

## 4. Reading status

**PostgreSQL.** `TransactionIdGetStatus(xid, *lsn)`
(`postgres/src/backend/access/transam/clog.c`) reads the 2-bit status under a
shared bank lock and returns the group's async-commit LSN.
`TransactionLogFetch` (`postgres/src/backend/access/transam/transam.c`) wraps it
with a single-XID cache and special-cases `BootstrapTransactionId`/
`FrozenTransactionId` as always-committed. This is consulted **directly in the
visibility path** (`HeapTupleSatisfies*` → `TransactionIdDidCommit`).

**goopg.** `GetStatus` (`internal/mvcc/clog.go:174`) is an O(1) byte read under
the bank's read lock, returning `TxnStatusUnknown` for out-of-range XIDs. **The
MVCC visibility path does not call it at runtime.** `TupleVisible`
(`internal/mvcc/visibility.go:14`) and `Snapshot.SeesCommittedXID`
(`internal/mvcc/snapshot.go`) decide commit status from the snapshot alone:

- XID `< Xmin` is assumed committed;
- XID `>= Xmax` is in the future;
- otherwise visible unless listed in the snapshot's in-memory `InProgress`
  (`snapshot.go:72`) or `Aborted` (`snapshot.go:73`) arrays.

A `FrozenTransactionID` fast path skips the check entirely
(`visibility.go:39-42`), and hint bits (`HeapXminCommitted`) short-circuit it
when cached (`visibility.go:48-56`). CLOG is consulted only at **load/recovery
time** — e.g. the heap loader filters tuples whose xmin status is Aborted, and
recovery stamps/reads CLOG.

**Difference.** This is the single most important behavioral divergence: in PG
the CLOG *is* the runtime commit oracle; in goopg the CLOG is a
persistence/recovery artifact and runtime visibility is driven by snapshot
arrays. goopg also has **no async-commit LSN** concept (see §5).

---

## 5. WAL integration & async commit

**PostgreSQL.** CLOG has its own WAL record types,
`CLOG_ZEROPAGE = 0x00` and `CLOG_TRUNCATE = 0x10`
(`postgres/src/include/access/clog.h:55-56`), replayed by `clog_redo`. For
async commit, `TransactionIdGetStatus` returns a per-group LSN
(`CLOG_XACTS_PER_LSN_GROUP = 32`, `CLOG_LSNS_PER_PAGE`,
`clog.c:91-96`) so that a committed status is not honored until the WAL is
flushed to that LSN — this is what makes hint bits safe under asynchronous
commit.

**goopg.** No CLOG-specific WAL records and no async-commit LSN tracking. The
CLOG is **derived from the transaction WAL records** instead. Commit/abort are
logged as `RecordKindXactCommit = 8` / `RecordKindXactAbort = 9`
(`internal/wal/recovery.go:73,78`); the xact-marker hook in
`internal/initdb/open.go` writes the WAL record and fsyncs it *before* stamping
CLOG, so the WAL is authoritative and CLOG is a write-behind cache. On restart,
`replayCLogFromWAL` (`internal/initdb/xact_recovery.go`) re-derives CLOG from
these records (it also handles `RecordKindXactCommitInval = 32`, emitted for
nailed-catalog writes, identically to `XactCommit` when re-deriving CLOG —
`internal/initdb/xact_recovery.go`, `open.go`). Note the WAL fsync-before-stamp
ordering applies to the **commit** path only: the xact-marker hook flushes the
WAL up to the commit LSN before `SetCommitted`, whereas the abort path stamps
`SetAborted` without a preceding flush (`internal/initdb/open.go`) — safe
because an un-flushed abort is simply re-derived as Unknown→Aborted on replay.
(There is no `CLOG_ZEROPAGE`/`CLOG_TRUNCATE` analogue because
goopg never truncates — see §6.)

**Difference.** PG durably WAL-logs CLOG page zeroing and truncation and tracks
per-group commit LSNs for async commit; goopg does neither — it reconstructs
CLOG from the xact records and has no async-commit fast path.

---

## 6. Truncation, vacuum, and wraparound

**PostgreSQL.** `TruncateCLOG(oldestXact, oldestxid_datoid)`
(`postgres/src/backend/access/transam/clog.c`) deletes CLOG segments older than
the cluster-wide `datfrozenxid`, gated by wraparound-safe page comparison
(`CLOGPagePrecedes`) and coordinated with `AdvanceOldestClogXid`. Frozen tuples
(xmin = `FrozenTransactionId`) need no CLOG entry, so aggressive freezing by
VACUUM is what makes truncation safe. This bounds `pg_xact/` size and prevents
XID wraparound.

**goopg.** **No truncation path exists.** The flat file and the in-memory banks
grow monotonically with `NextXID`; there is no `TruncateCLOG`,
`CLOGPagePrecedes`, or `AdvanceOldestClogXid` analogue, and no VACUUM-driven
freezing that would write `FrozenTransactionID` into tuple headers. Wraparound
is only *guarded* — `ErrXIDWraparound` (`internal/mvcc/manager.go`) refuses to
allocate once XIDs are exhausted — not *recovered* from. A `FrozenTransactionID`
fast path exists in visibility (`visibility.go:39-42`) for compatibility, but
nothing populates it.

**Difference.** PG reclaims CLOG space and survives indefinitely via
freeze+truncate; goopg accumulates CLOG forever and stops at wraparound. This is
the most consequential durability/longevity gap.

---

## 7. Subtransactions

**PostgreSQL.** A separate persistent SLRU, `pg_subtrans`
(`postgres/src/backend/access/transam/subtrans.c`), stores 4 bytes per XID
(the parent XID), 2048 XIDs per page. `SubTransGetTopmostTransaction` walks the
parent chain to the top-level XID. Combined with `SUB_COMMITTED` in CLOG, this
gives correct cross-restart subxact visibility.

**goopg.** In-memory only. `SubxactMap` (`internal/mvcc/subxact_visibility.go:14`)
holds `parents` and `aborted` maps for the lifetime of the owning transaction;
`Register` (`:30`), `MarkAborted` (`:39`), `TopLevelXid` (`:65`, with a cycle
guard), `IsSubxact` (`:79`). The Manager mirrors this state
(`subxact_visibility.go:221-282`). Visibility uses
`SeesCommittedXIDWithSubxacts` (`:111`): individually-rolled-back subxacts are
invisible, otherwise the subxact resolves to its top-level XID and the normal
snapshot check applies. Subxact lifecycle is WAL-logged
(`RecordKindXactAssignment = 15`, `RecordKindXactRollbackTo = 16`,
`RecordKindXactSubAbort` — `internal/wal/recovery.go:122-133`) and replayed into
the in-memory maps, but **nothing is written to an on-disk `pg_subtrans`**.

**Difference.** No persistent `pg_subtrans` and no `SUB_COMMITTED` state.
Acceptable today because subtransactions cannot span a restart in goopg, but it
blocks any feature that needs durable subxact parentage (e.g. a real PG standby
resolving subxacts from goopg's basebackup, or two-phase commit).

---

## 8. Concurrency

**PostgreSQL.** SLRU **bank locks** (shared for reads, exclusive for page
selection/update) plus per-buffer **I/O locks**, with the lock-free
`clogGroupFirst` queue for group commit so many backends update behind one bank
lock acquisition.

**goopg.** A `sync.RWMutex` per `clogBank` (128K XIDs each) — `GetStatus` takes
`RLock`, `setStatus` takes `Lock` (`clog.go:174,378`). `banksMu` guards growth
of the banks slice only (`clog.go:64-86`). Concurrent commits on XIDs in
different banks proceed in parallel; there is no group commit. The per-bank lock
geometry is documented in `docs/design/0107-0004-procarray-xidgen-clog-bank-locks.md`.

**Difference.** Both shard locking by bank, but PG additionally has page-level
I/O locks and a lock-free group-update fast path; goopg's banks are far coarser
(128K vs 16×32768 page slots) and it has no group commit. goopg's whole-file
`flush` on every commit also serializes writers on the file write itself.

---

## 9. Startup, recovery, and standby attach

**PostgreSQL.** `BootStrapCLOG` (initdb), `StartupCLOG` (read `nextXid`,
init `latest_page_number`), `TrimCLOG` (zero the tail of the current page after
redo). A standby boots from basebackup and replays `XLOG_XACT_COMMIT`/`ABORT`
records into CLOG via `SimpleLruReadPage_ReadOnly`.

**goopg** (`internal/initdb/open.go`, `xact_recovery.go`,
`internal/mvcc/clog.go`):

The actual ordering in `internal/initdb/open.go` is:

1. `OpenCLog(path)` loads the flat file if present (`func OpenCLog` at
   `clog.go:99`).
2. `EnablePGSLRUMirror(<DataDir>/pg_xact)` (`clog.go:478`) loads the SLRU
   directory via `loadFromSLRU` (`clog.go:550`), treating it as **authoritative**
   (it is the only fsynced copy, M0106-0013), then backfills any
   in-memory-only entries.
3. `InitializeAsCommitted(highXID)` (`clog.go:203`) — upgrade path: when the
   flat file was absent, all pre-CLOG XIDs `[1,highXID)` are assumed committed.
4. `MarkUnknownAsAborted(highXID)` (`clog.go:242`) — the crash-recovery
   implicit-abort sweep: any still-`Unknown` XID is stamped `Aborted` (PG's
   recovery treatment of `IN_PROGRESS` slots), using
   `mirrorTerminalRangeBatchedUnlocked` (`clog.go:290`) to do one fsync per
   segment instead of ~1M.
5. `HighestKnownXID()` (`clog.go:643`) advances `NextXID` past every terminal
   XID so snapshots see all pre-crash committed rows.
6. **Then** WAL replay re-derives/stamps CLOG from the xact records
   (`replayCLogFromWAL`, `internal/initdb/xact_recovery.go`), after which
   `HighestKnownXID()` is called **again** to re-advance `NextXID` past any XIDs
   recovered from the WAL. (So WAL replay runs *last*, not between steps 2 and 3.)

**Standby-attach caveat** (`clog.go:237-241`): a basebackup-attached cluster
**must** call `InitializeAsCommitted(upstream_nextXid)` *before* the
implicit-abort sweep, or upstream XIDs (absent from the local CLOG) would be
wrongly stamped Aborted. This ordering is documented in code but not yet
exercised by active standby tests.

**Difference.** goopg's recovery is built around re-deriving CLOG from the WAL
and an explicit implicit-abort sweep, rather than PG's SLRU page replay; the
SLRU mirror exists specifically so a real PG standby's
`SimpleLruReadPage_ReadOnly` finds a byte-compatible `pg_xact/`.

---

## Citation index

| Topic | PostgreSQL | goopg |
|---|---|---|
| Status codes / layout | `clog.h:27-30`, `clog.c:61-96` | `clog.go:14-46`, `clog.go:457-468` |
| Set status / tree / group commit | `clog.c` (`TransactionIdSetTreeStatus`, `TransactionGroupUpdateXidStatus`) | `clog.go:373` (`setStatus`), `clog.go:190-197` |
| Get status / visibility | `clog.c` (`TransactionIdGetStatus`), `transam.c` | `clog.go:174`, `visibility.go:14`, `snapshot.go:62-73` |
| WAL / async commit | `clog.h:55-56`, `clog.c:91-96`, `clog_redo` | `wal/recovery.go:73-78`, `initdb/xact_recovery.go` |
| Truncation / wraparound | `clog.c` (`TruncateCLOG`, `CLOGPagePrecedes`) | none; `manager.go` `ErrXIDWraparound` |
| Subtransactions | `subtrans.c`, `subtrans.h` | `subxact_visibility.go:14-130`, `wal/recovery.go:122-133` |
| Startup / recovery | `clog.c` (`BootStrapCLOG`/`StartupCLOG`/`TrimCLOG`) | `initdb/open.go`, `clog.go:203,242,290,478,550,643` |
