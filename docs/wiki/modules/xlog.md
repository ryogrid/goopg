# Module: `internal/access/transam/xlog`

The **write-ahead log** — WAL record encoding, the append/emit path, segment
management, crash recovery / replay, and both physical and logical
(pgoutput) decoding. It is the Go analogue of PG's `src/backend/access/transam/`
(`xlog.c`, `xloginsert.c`, `xlogrecovery.c`, `xlogreader.c`) plus
`src/backend/replication/pgoutput`.

This is the durability spine: every page mutation is encoded as a WAL record,
appended to an in-memory ring buffer, and flushed to `pg_wal` segments ahead of
the data write (WAL-before-data). On restart, the same records are replayed to
reconstruct the cluster state. The same WAL is what a vanilla PG 18.3 standby
consumes for physical replication, and `pgoutput` re-encodes it for logical
replication.

## Key Files

- `writer.go` (2,794) — the `Writer`: in-memory WAL ring buffer (`MemRing`),
  `Append`/`AppendRaw`, commit LSN, `FlushUpTo`, segment preallocation/recycling,
  `detectWritePos`/`scanLastSegmentEnd`, `RemoveOldSegments` retention.
- `recovery.go` (6,682) — the record codec (`Encode*`/`Decode*` per record kind)
  and the redo engine: `ApplyRecord`, `ReplayRecords(From)`,
  `ReplayFromDirWithMgr`, per-rmgr `replay*` functions (heap, btree, xact,
  smgr, clog, tblspc, dbase), checkpoint discovery.
- `pg_assembled_emit.go` (1,456) — encoding PG-canonical records (heap
  insert/update/delete, xact commit/abort) that a real PG 18.3 can consume.
- `checkpointer.go` (1,122) — the checkpointer: periodic `CHECKPOINT` records,
  `CheckPointFields`, dirty-page flush orchestration, FPI watermark.
- `reader.go` (627) — segment reading: `readStream(From)`, `Record` iterator,
  page-aware decode, `endOfWAL` handling, `durableUnknownRecord`.
- `iterator.go` (587) — `RecordIterator` over a WAL stream
  (`readOneAt`, page-header decode).
- `pgoutput.go` (696) — the `PgOutput` logical-decoding plugin: `Begin`/
  `Commit`/`Relation`/`Insert`/`Update`/`Delete`/`Truncate` messages,
  `encodePgoTuple` (row tuple → pgoutput text/bytea), `RelationFilter`.
- `pgoutput_decoder.go` (399) — the inbound side: decode pgoutput messages for
  the apply worker.
- `pg_xlog_decode.go` (610) — generic PG WAL-record decode (rmgr dispatch,
  block-ref decode, FPI handling) used for PG-authored WAL.
- `slots.go` (523) — replication slots: physical/logical slot persistence
  (`pg_replslot/`), `AdvanceConfirmedFlushLSN`, `MinRestartLSN`.
- `syncrep.go` (416) — synchronous replication: quorum/priority wait for
  standby acknowledgement.
- `mem_ring.go` / `mem_ring_concurrent.go` — the shared WAL ring buffer
  (append window + drained pages).
- `xlog_page.go` (368) — `xlogPageValidator`: page-header validation
  (`xlp_pageaddr`, TLI, segment size) — the "don't trust recycled segments"
  check.
- `stripe_append.go` (362) — batched stripe append of records into segments.
- `format.go` — WAL page/record header structs, `encodeRecordXLog`,
  `XLogRecord` decode.
- `retention.go` — keep-horizon / segment retention computation.
- `archive_recovery.go` / `archive_restore.go` — `recovery.signal` archive
  mode: `restore_command` fetch → replay → promote.
- `timeline_history.go` — timeline history file handling.
- `seq_log.go`, `replmon.go`, `subscriber_mon.go` — sequencing, replication
  monitoring.
- `slot_decoder.go` / `slots_pg.go` — slot-backed decoding (logical slots).
- `relmap.go` — relation-map WAL records.

## Public API

```go
// Writer (writer.go)
func NewWriter(cfg Config) (*Writer, error)
func (w *Writer) Append(payload []byte) (lsn uint64, used uint64, err error)
func (w *Writer) AppendRaw(stream []byte) (lsn uint64, used uint64, err error)
func (w *Writer) FlushUpTo(lsn uint64) error
func (w *Writer) WrittenLSN() uint64 / DrainedLSN() uint64
func (w *Writer) Close() error

// Recovery (recovery.go)
func ReplayRecords(mgr *storage.Manager, records []Record) (ReplayStats, error)
func ReplayRecordsFrom(mgr *storage.Manager, records []Record, redoLSN uint64) (ReplayStats, error)
func ReplayFromDirWithMgr(mgr *storage.Manager, walDir string, segmentSize int64) (ReplayStats, error)
func ApplyRecord(mgr *storage.Manager, r Record) (bool, error)   // redo one record
func DiscoverLastCheckpointLSN(...) (uint64, error)

// Reader (reader.go)
func readStream(segDir string, startLSN uint64, ...) ([]Record, error)
func readStreamFrom(segDir string, startLSN uint64, w *Writer, ...) (*RecordIterator, error)

// Logical decoding (pgoutput.go)
func NewPgOutput(snap *CatalogSnapshot, w io.Writer) *PgOutput
func (p *PgOutput) Begin(xid) / Commit(lsn) / Insert(rel, row) / ...
```

## Internal structure

- **Append path** — an executor/storage mutation calls the record encoder
  (`recovery.go`'s `Encode*`), producing a `Record`; `Writer.Append` copies it
  into the shared ring buffer (`MemRing`), stamps the LSN, and notifies
  subscribers. `FlushUpTo` drains the ring, `xlogWrite`/`doSync` write the
  segments and fsync; `commit_delay`/`commit_siblings` throttle the flush.
- **Record layout** — each record has a header (`RecordKind` byte + flags +
  payload length + `xl_prev` backpointer) and an optional body; `format.go`
  keeps the wire layout PG-compatible (page headers `SizeOfXLogLongPHD`,
  `XLOGBlockSize=8192`, segment `XLogSegSize`).
- **Recovery** — `ReplayRecords` walks the segment stream, `ApplyRecord`
  dispatches per `RecordKind` to `replay*` functions that re-run the mutation
  against `storage.Manager` pages (heap insert/delete/update/multi-insert,
  btree split/insert/dedup/delete, xact commit/abort, smgr create/truncate,
  clog zero-page/truncate, tblspc/dbase create/drop, page images). PG-authored
  WAL goes through `pg_xlog_decode`'s rmgr dispatch with block-ref/FPI handling.
- **Checkpointer** — the checkpointer goroutine periodically emits a
  `CHECKPOINT` record embedding `CheckPointFields` (redo LSN, nextXID/nextOid,
  timeline) and flushes dirty buffers, advancing the recovery horizon.
- **Slots** — `Slots` persists physical/logical replication slots to
  `pg_replslot/`; logical slots track `ConfirmedFlushLSN` and `MinRestartLSN`
  so WAL is retained while a consumer lags.
- **Segment lifecycle** — `detectWritePos`/`scanLastSegmentEnd` find the real
  end of WAL (not trusting recycled segments, `xlog_page.go`); segments are
  preallocated and recycled by `preallocateSegment`/`recycleSegmentFile`;
  `RemoveOldSegments` applies the retention horizon.

## Dependencies

- **Used by** — `internal/storage` (WAL flush barrier, `WALFlusher`),
  `internal/access/transam` (commit records), `internal/access/nbtree`
  (index WAL), `internal/executor` (DDL/DML WAL via storage),
  `internal/initdb` (WAL bootstrap, recovery), `internal/replication`
  (walsender), `internal/backup`, `internal/postmaster`.
- **Uses** — `internal/storage` (pages, `Manager` for redo), `internal/port/runtimeshim`
  (nanotime), `internal/utils/activity/stats` (counters),
  `internal/utils/misc` (GUCs like `wal_level`, `synchronous_commit`,
  `wal_compression`), `internal/access/transam` (xid visibility during replay).

## Notable patterns / gotchas

- **WAL-before-data** — a page mutation must be WAL-logged (and flushed to the
  record's LSN) before the data page write is made durable; `storage.flushSlot`
  flushes to `max(pd_lsn, hintFlushBarrier)`.
- **`xl_prev` chain** — every record back-points to the previous one; the
  reader/`pg_waldump` validate it. A gap from a recycled segment breaks the
  chain, which `xlog_page.go`'s validator prevents.
- **Record-kind registry** — `RecordKind*` constants in `recovery.go` are the
  goopg-native encoding; `IsGoopgNativeRecord` distinguishes them from
  PG-canonical records. The two codecs must agree with `headerMatchesEmittedKind`.
- **Unsupported PG records** — `unsupportedDecodedXLogOpcode` refuses
  PG-authored records goopg cannot replay (2PC, `HEAP2_REWRITE`, unknown
  opcodes) with `ErrUnsupportedRecord`, so a crash tail never silently "ends"
  early (`durableUnknownRecord`).
- **`endOfWAL` ambiguity** — a torn/zero/CRC-failed tail is end-of-WAL; a
  decodable-but-unhandled record is an error the caller must see (the two were
  once conflated — the S16 data-loss class).
- **PG-identical emission** — `pg_assembled_emit.go` and `pgoutput.go` must
  produce byte-identical output to a real PG 18.3 (a vanilla standby replays
  goopg WAL; a real publisher's pgoutput is decoded by the apply worker).
- **Retention** — never delete a segment still reachable from a standby or
  logical slot (`MinRestartLSN`); `RemoveOldSegments` respects the max
  `MinRestartLSN` across slots.