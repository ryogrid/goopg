# 03 — First WAL Segment and Runtime Rotation

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility) — bootstrap
WAL slice
**Audience:** Claude Code working on `internal/initdb/` and
`internal/wal/`.

---

## Scope

The single WAL segment file `pg_wal/000000010000000000000001` that
initdb writes, the 40-byte `XLogLongPageHeaderData` that opens it,
the inaugural `XLOG_CHECKPOINT_SHUTDOWN` record body sitting
immediately after that long header, and the runtime rules
(`XLogFileInit`, recycle vs unlink, `archive_status/<seg>.ready`,
`pg_wal/summaries/`, timeline-history files on promotion) that keep
`pg_wal/` legible to a vanilla PG18 standby attaching at any moment
during goopg's lifetime.

**In scope.** First segment filename and on-disk page header layout;
sysid / `xlp_seg_size` / `xlp_xlog_blcksz` stamping; the one
`XLOG_CHECKPOINT_SHUTDOWN` record body; the ordering invariant
"WAL flushed before `global/pg_control` is finalised"; runtime
segment pre-zero, recycle, unlink and archive-status emission;
`pg_wal/summaries/` lifecycle; timeline-history files on promotion.

**Out of scope.** `ControlFileData` field layout, CRC, and the
checkpoint-LSN backlink — see `02-pg-control-and-checkpoint.md`.
DDL-emitted catalog / heap WAL records — see `05-` and `11-`.
Streaming replication protocol, `walreceiver`, and basebackup of
`pg_wal/` — see `09-streaming-replication-readiness.md`.

---

## Upstream references

| Path | Used for |
|------|----------|
| `src/include/access/xlog_internal.h:36-67` | `XLogPageHeaderData` / `XLogLongPageHeaderData` C layout. |
| `src/include/access/xlog_internal.h:34` | `XLOG_PAGE_MAGIC = 0xD118`. |
| `src/include/access/xlog_internal.h:76` | `XLP_LONG_HEADER` flag bit `0x0002`. |
| `src/include/access/xlog_internal.h:165-171` | `XLogFileName` snprintf format `%08X%08X%08X`. |
| `src/include/access/xlog_internal.h:149` | `XLOGDIR = "pg_wal"`. |
| `src/include/access/xlogrecord.h:41-55` | `XLogRecord` struct, `SizeOfXLogRecord`. |
| `src/include/access/xlogrecord.h:243` | `XLR_BLOCK_ID_DATA_SHORT = 255`. |
| `src/include/catalog/pg_control.h:35-65` | `CheckPoint` body struct. |
| `src/include/catalog/pg_control.h:68` | `XLOG_CHECKPOINT_SHUTDOWN = 0x00`. |
| `src/include/access/rmgrlist.h:28` | `RM_XLOG_ID` rmgr id for the inaugural record. |
| `src/backend/access/transam/xlog.c:5071-5234` | `BootStrapXLOG` — emits the first segment + first record. |
| `src/backend/access/transam/xlog.c:3376-3396` | `XLogFileInit` — pre-zeroed segment allocation. |
| `src/backend/access/transam/xlog.c:3635-3648` | `XLogFileClose` — `POSIX_FADV_DONTNEED` hint. |
| `src/backend/access/transam/xlog.c:3559-3608` | `InstallXLogFileSegment` — atomic rename, recycle entry point. |
| `src/backend/access/transam/xlog.c:3861-3917` | `RemoveOldXlogFiles` — segment removal driver. |
| `src/backend/access/transam/xlog.c:4004-4035` | `RemoveXlogFile` — recycle-vs-unlink branch (`wal_recycle`). |
| `src/backend/access/transam/xlog.c:4235` | `WriteControlFile` — called *after* `BootStrapXLOG` returns. |
| `src/backend/access/transam/xlog.c:5562` | `ValidateXLOGDirectoryStructure` — checks `pg_wal/`, `archive_status/`, `summaries/`. |
| `src/backend/access/transam/xlog.c:5996-6021` | `StartupXLOG` — `XLogInitNewTimeline` then `writeTimeLineHistory` at promotion. |
| `src/backend/access/transam/xlogarchive.c:443-486` | `XLogArchiveNotify` — emits `<seg>.ready`. |
| `src/backend/access/transam/timeline.c:303-395` | `writeTimeLineHistory` — `<TLI>.history` writer. |
| `src/backend/postmaster/walsummarizer.c:213-1697` | WAL summary file producer / pruner. |
| `src/bin/initdb/initdb.c:234` | initdb's `pg_wal/summaries` directory creation. |

All citations below take `src/...` as relative to `postgres/`.

---

## Initdb-time output

### Filename + path

Exactly one regular file is created under `pg_wal/`:

```
pg_wal/000000010000000000000001
```

The 24-hex name decomposes as `<TLI=00000001><logid=00000000><seg=00000001>`
per `XLogFileName` (`src/include/access/xlog_internal.h:165-171`). The
initial timeline is the constant `BootstrapTimeLineID = 1`
(`src/backend/access/transam/xlog.c:112`); segment 0 is intentionally
skipped so LSN `0/0` retains its "invalid pointer" meaning, hence
the first usable segment is `…00000001` and the first usable LSN is
`wal_segment_size` (16 MiB by default — see
`src/backend/access/transam/xlog.c:5115`).

File size equals `wal_segment_size` (16 MiB). The file is allocated
zero-filled by `XLogFileInit` and the first 8 KiB page is overwritten
in-place by `BootStrapXLOG` before `fsync` and `close`
(`src/backend/access/transam/xlog.c:5175-5210`).

### Long page header (first 40 bytes of the segment)

`XLogLongPageHeaderData = XLogPageHeaderData + 24 extra bytes`
(`src/include/access/xlog_internal.h:61-67`). Initdb stamps:

| Field | C type | Initdb value | Citation |
|-------|--------|--------------|----------|
| `xlp_magic` | `uint16` | `XLOG_PAGE_MAGIC` = `0xD118` | `xlog_internal.h:34`, `xlog.c:5144` |
| `xlp_info` | `uint16` | `XLP_LONG_HEADER` = `0x0002` | `xlog_internal.h:76`, `xlog.c:5145` |
| `xlp_tli` | `TimeLineID` (`uint32`) | `BootstrapTimeLineID` = `1` | `xlog.c:5146` |
| `xlp_pageaddr` | `XLogRecPtr` (`uint64`) | `wal_segment_size` = `0x01000000` (16 MiB) | `xlog.c:5147` |
| `xlp_rem_len` | `uint32` | `0` | implicit via `memset(page, 0, …)` `xlog.c:5106` |
| `xlp_sysid` | `uint64` | identical to `ControlFileData.system_identifier` | `xlog.c:5099-5101`, `xlog.c:5149` |
| `xlp_seg_size` | `uint32` | `wal_segment_size` | `xlog.c:5150` |
| `xlp_xlog_blcksz` | `uint32` | `XLOG_BLCKSZ` = `8192` | `xlog.c:5151` |

`SizeOfXLogLongPHD` is `MAXALIGN(40) = 40` on 64-bit
(`xlog_internal.h:69`). The record area starts at offset `40` of
page 0.

### First WAL record — `XLOG_CHECKPOINT_SHUTDOWN`

Emitted inline by `BootStrapXLOG` at
`src/backend/access/transam/xlog.c:5153-5173`. Layout (all
little-endian on x86_64):

| Section | Bytes | Value |
|---------|-------|-------|
| `XLogRecord` header | `SizeOfXLogRecord` = 24 | see below |
| `XLogRecordDataHeaderShort` | 2 | `id = XLR_BLOCK_ID_DATA_SHORT (255)`, `data_length = sizeof(CheckPoint) = 88` |
| `CheckPoint` body | 88 | see body table below |
| Total `xl_tot_len` | `SizeOfXLogRecord + 2 + sizeof(CheckPoint)` = `24 + 2 + 88` = `114` | `xlog.c:5158` |

`XLogRecord` fields (`xlogrecord.h:41-53`):

| Field | C type | Initdb value | Citation |
|-------|--------|--------------|----------|
| `xl_tot_len` | `uint32` | `114` | `xlog.c:5158` |
| `xl_xid` | `TransactionId` | `InvalidTransactionId` (0) | `xlog.c:5157` |
| `xl_prev` | `XLogRecPtr` | `0` | `xlog.c:5156` |
| `xl_info` | `uint8` | `XLOG_CHECKPOINT_SHUTDOWN` = `0x00` | `xlog.c:5159`, `pg_control.h:68` |
| `xl_rmid` | `RmgrId` | `RM_XLOG_ID` = `0` | `xlog.c:5160`, `rmgrlist.h:28` |
| 2 bytes padding | — | zero | `xlogrecord.h:48` |
| `xl_crc` | `pg_crc32c` | CRC32C over body then header (excl. `xl_crc`) | `xlog.c:5169-5173` |

`CheckPoint` body fields (`pg_control.h:35-65`), values from
`xlog.c:5115-5132`:

| Field | C type | Initdb value | Citation |
|-------|--------|--------------|----------|
| `redo` | `XLogRecPtr` | `wal_segment_size + SizeOfXLogLongPHD` = `0x01000028` | `xlog.c:5115` |
| `ThisTimeLineID` | `TimeLineID` | `1` | `xlog.c:5116` |
| `PrevTimeLineID` | `TimeLineID` | `1` | `xlog.c:5117` |
| `fullPageWrites` | `bool` | GUC `full_page_writes` (true by default) | `xlog.c:5118` |
| `wal_level` | `int` | GUC `wal_level` (`replica` = 1) | `xlog.c:5119` |
| `nextXid` | `FullTransactionId` | `(0, FirstNormalTransactionId=3)` | `xlog.c:5120-5121` |
| `nextOid` | `Oid` | `FirstGenbkiObjectId` (10000) | `xlog.c:5122` |
| `nextMulti` | `MultiXactId` | `FirstMultiXactId` = 1 | `xlog.c:5123`, `multixact.h:25` |
| `nextMultiOffset` | `MultiXactOffset` | `0` | `xlog.c:5124` |
| `oldestXid` | `TransactionId` | `FirstNormalTransactionId` = 3 | `xlog.c:5125` |
| `oldestXidDB` | `Oid` | `Template1DbOid` = 1 | `xlog.c:5126`, `pg_database_d.h:58` |
| `oldestMulti` | `MultiXactId` | `1` | `xlog.c:5127` |
| `oldestMultiDB` | `Oid` | `Template1DbOid` = 1 | `xlog.c:5128` |
| `time` | `pg_time_t` | `time(NULL)` | `xlog.c:5131` |
| `oldestCommitTsXid` | `TransactionId` | `InvalidTransactionId` (0) | `xlog.c:5129` |
| `newestCommitTsXid` | `TransactionId` | `InvalidTransactionId` (0) | `xlog.c:5130` |
| `oldestActiveXid` | `TransactionId` | `InvalidTransactionId` (0) | `xlog.c:5132` |

The CRC32C is computed twice (body first, then header up to
`offsetof(xl_crc)`) and finalised — see `xlog.c:5169-5173`.

### Ordering invariant vs `WriteControlFile`

Inside `BootStrapXLOG` the order is fixed and load-bearing
(`src/backend/access/transam/xlog.c:5175-5219`):

1. `XLogFileInit(1, BootstrapTimeLineID)` allocates the segment.
2. `write(openLogFile, page, XLOG_BLCKSZ)` writes page 0.
3. `pg_fsync(openLogFile)` durably flushes the segment.
4. `close(openLogFile)`.
5. `InitControlFile(sysidentifier, …)` populates the in-memory
   `ControlFile`, including `checkPoint = checkPoint.redo` and
   `checkPointCopy = checkPoint`.
6. `WriteControlFile()` writes `global/pg_control`.

If steps 1–4 fail, no `pg_control` is ever produced, leaving the
cluster in an "unfinished initdb" state that operators recognise.
The reverse ordering (control file before WAL) would let a crash
mid-initdb publish a control file whose `checkPointCopy.redo`
points into a segment that does not yet exist on disk — the next
backend would `PANIC: could not read XLOG checkpoint record`.

---

## Continuous maintenance

### `XLogFileInit` rule

`src/backend/access/transam/xlog.c:3376-3396` is the only sanctioned
way to materialise a new segment file. Internally it (a) opens a
temp file `xlogtemp.<pid>`, (b) writes `wal_segment_size` zero
bytes, (c) `fsync`s, (d) calls `InstallXLogFileSegment` which
`durable_rename`s the temp file into place under the
`ControlFileLock` (`xlog.c:3559-3608`). The two invariants:

- The file appearing at `pg_wal/<name>` must be a full
  `wal_segment_size` of zeros (or recycled-but-unused garbage from
  a prior segment that the next page-header write will overwrite),
  never a partial file. `durable_rename` makes the appearance atomic.
- WAL writer must not advance past the segment boundary until the
  next segment exists on disk; the rename completes before
  `XLogFileInitInternal` returns the fd.

### Segment-rotation matrix

| Caller | Trigger | Effect on `pg_wal/` | Effect on `archive_status/` |
|--------|---------|---------------------|-----------------------------|
| `BootStrapXLOG` (`xlog.c:5071`) | initdb | Creates `…00000001`. | None. |
| `XLogFileInit` via `AdvanceXLInsertBuffer` | Writer's insert position crosses a segment boundary | New zero-filled segment `<TLI>00000000<seg+1>`. | None until segment fills. |
| `RequestXLogSwitch` (`pg_switch_wal`) | Manual / synchronous-commit boundary | Pads the current segment with NOOPs, advances. | `.ready` for the closed segment (if `archive_mode=on`). |
| `XLogWrite` filling a segment | Page write reaches segment end | Closes current fd, asks `XLogFileInit` to mint next. | `.ready` on the closed segment when archiving is on. |
| `RemoveOldXlogFiles` (`xlog.c:3861`) | After checkpoint | Recycles via rename or unlinks superseded segments. | `.done` files removed by `RemoveXlogFile`. |
| `XLogInitNewTimeline` (`xlog.c:5252-5321`) | End of recovery / promotion | Either copies last segment to new TLI or `XLogFileInit`s next segment on new TLI. | Calls `XLogArchiveCleanup` on the new segment. |
| `RemoveNonParentXlogFiles` (`xlog.c:3936-3989`) | Switch to new timeline | Removes future-tli segments that don't belong to new history. | Skips files already `.ready`. |

### Recycle vs unlink policy

`RemoveXlogFile` (`xlog.c:4004-4035`) consults the GUC `wal_recycle`
and the `XLogCtl->InstallXLogFileSegmentActive` flag. The branch:

| Condition | Action |
|-----------|--------|
| `wal_recycle = on`, segment ≤ `XLOGfileslop(lastredo)`, `InstallXLogFileSegmentActive`, regular file (not symlink), `InstallXLogFileSegment(..find_free=true)` succeeds | Rename the old file to `<TLI><logid><nextSeg>`; counts as `ckpt_segs_recycled++`. |
| Any of the above fails | `durable_unlink` the file. |

The "rename for recycle" path is the on-disk equivalent of
preallocation: the file's bytes are not zeroed, the page-header
writer will overwrite them lazily. A standby reading a recycled-but-
not-yet-written page sees stale bytes whose `xlp_magic` no longer
matches the expected segment LSN; this is how the standby detects
end-of-WAL on a primary at idle.

### `archive_status/` files

`XLogArchiveNotify` (`src/backend/access/transam/xlogarchive.c:443-486`)
creates `pg_wal/archive_status/<seg>.ready` when a segment is closed
and `archive_mode != off`. Once `archive_command` succeeds the
archiver renames the file to `<seg>.done`. `RemoveOldXlogFiles`
honours the marker via `XLogArchiveCheckDone(xlde->d_name)`
(`xlog.c:3906`): a segment with `.ready` but no `.done` is *not*
removed even if checkpoint has superseded it. Timeline-history files
get archive priority — `XLogArchiveNotify` wakes the archiver
forcibly via `PgArchForceDirScan()` for `<TLI>.history`
(`xlogarchive.c:480-481`).

### WAL summary files

`pg_wal/summaries/` holds per-LSN-range `.summary` files emitted by
the walsummarizer background worker
(`src/backend/postmaster/walsummarizer.c:213` entry,
`walsummarizer.c:1660 MaybeRemoveOldWalSummaries` for pruning).
`StartupXLOG`/`ValidateXLOGDirectoryStructure` (`xlog.c:5562`)
PANICs if the directory is missing, even when
`summarize_wal = off`. initdb pre-creates the directory
(`src/bin/initdb/initdb.c:234`); goopg's
`internal/initdb/initdb.go:88` already does the same.

### Timeline switch on promotion

`StartupXLOG` at end of recovery (`xlog.c:5996-6021`) drives a
TLI bump exactly once:

1. Pick `newTLI = findNewestTimeLine(recoveryTargetTLI) + 1`.
2. `XLogInitNewTimeline(EndOfLogTLI, EndOfLog, newTLI)` either
   `XLogFileCopy`s the trailing segment or `XLogFileInit`s the
   next segment on `newTLI` (`xlog.c:5281-5313`).
3. Delete `standby.signal` / `recovery.signal`.
4. `writeTimeLineHistory(newTLI, recoveryTargetTLI, EndOfLog,
   reason)` writes `pg_wal/<newTLI>.history`
   (`src/backend/access/transam/timeline.c:303-395`). The history
   file is one line per parent-TLI switch, then promoted via
   `XLogArchiveNotify` for archival.

A standby attaching after a promotion follows the chain by reading
`<newTLI>.history` before it knows which segments to stream — a
missing or partial history file is FATAL during
`validateRecoveryParameters`.

---

## What goopg must produce

Diff against `internal/initdb/` and `internal/wal/` as of branch
`physical-rep-goopg-to-pg-and-control-file-refactor`.

| Artefact | Status | Existing site | Gap |
|----------|--------|---------------|-----|
| `pg_wal/` directory tree (incl. `archive_status/`, `summaries/`) | `done` | `internal/initdb/initdb.go:86-88` | — |
| First segment file `pg_wal/000000010000000000000001` | `missing` | none | initdb never creates the file. No helper analogous to `BootStrapXLOG` exists; tests prove this via `firstAvailableSegment` returning zero entries. |
| Long page header (40 bytes, sysid-stamped) at offset 0 | `partial` | encoder in `internal/wal/xlog_page.go:95-201`, emitted at runtime by `internal/wal/xlog_emit.go:131-145` | The encoder runs only when the writer crosses a segment boundary at runtime; nothing emits the header during initdb. Sysid plumbing exists (`Writer.sysID` from `Config.SystemID`) but is not wired to `Init`. |
| `XLOG_CHECKPOINT_SHUTDOWN` record body | `missing` | `internal/wal/format.go:35` knows the constant `xlogCheckpointShutdown = 0x00` but no encoder emits the `CheckPoint` body during initdb. | Add a builder mirroring `BootStrapXLOG` lines 5108-5173: assemble `CheckPoint`, prepend `XLogRecordDataHeaderShort`, prepend `XLogRecord` with CRC32C. |
| Ordering: WAL segment flushed before `global/pg_control` | `partial` | `internal/initdb/initdb.go:430-444` writes pg_control last but never writes WAL. | Insert a `writeBootstrapWAL(abs, sysID)` call between `bootstrapRelcacheInitFiles` and `writePgControl`; have it `fsync`. |
| Sysid stamping consistent across header + pg_control | `partial` | `internal/initdb/initdb.go:435` passes `sysID` to `writePgControl`; `internal/initdb/pgcontrol.go:152` writes it as `system_identifier`. | Same `sysID` must flow into the first-segment long header's `xlp_sysid` field — currently no consumer reads it for WAL. |
| `archive_status/<seg>.ready` emission at segment close | `missing` | directory exists; no producer. | Wire a hook into `internal/wal/writer.go` when a segment is closed (and a future `archive_mode` GUC is on). Out of scope for `Init`; the directory existence at initdb time is sufficient for PG-side validation. |
| Recycle policy (rename old → new) | `partial` | `internal/wal/writer.go:705-715 RemoveOldSegments` only unlinks. | Add a `wal_recycle` path that calls `InstallXLogFileSegment`-equivalent: rename the old segment to the next free segno under a lock, leaving bytes intact. |
| Unlink policy when archive `.ready` outstanding | `missing` | `internal/wal/retention.go:75-188` ignores `archive_status/`. | Before unlinking a segment, check that no `<seg>.ready` is pending. |
| `pg_wal/summaries/` directory present | `done` | `internal/initdb/initdb.go:88` | — |
| Summary file producer (walsummarizer) | `missing` | no equivalent worker. | Outside the bootstrap slice; track separately. PG `validateRecoveryParameters` does not require summary files to start. |
| Timeline-history file write on promotion | `partial` | `internal/wal/timeline_history.go:121 WriteHistory` exists; promotion-time caller does not. | Wire `WriteHistory` into the promotion path in `internal/wal/` (cross-ref `09-`). |

Concrete file to add: `internal/initdb/wal_bootstrap.go` exporting

```go
// WriteBootstrapWAL writes pg_wal/000000010000000000000001 with the
// long page header and the XLOG_CHECKPOINT_SHUTDOWN record. Must be
// called BEFORE writePgControl so the ordering invariant matches
// BootStrapXLOG (xlog.c:5175-5219).
func WriteBootstrapWAL(dataDir string, sysID uint64, now time.Time) error
```

It should reuse `internal/wal/xlog_page.go::EncodeXLogLongPageHeader`
and add a `CheckPoint` encoder. The remaining `wal_segment_size -
XLOG_BLCKSZ` bytes of the file must be zero-filled.

---

## Verification

1. **Byte-level header parity.** Run

   ```bash
   /usr/lib/postgresql/18/bin/initdb -D /tmp/pg-vanilla --no-locale
   goopg init -D /tmp/pg-goopg
   xxd -l 40 /tmp/pg-vanilla/pg_wal/000000010000000000000001 > /tmp/pg-vanilla.long
   xxd -l 40 /tmp/pg-goopg/pg_wal/000000010000000000000001  > /tmp/pg-goopg.long
   diff /tmp/pg-vanilla.long /tmp/pg-goopg.long
   ```

   Expected differences: bytes 8..15 (`xlp_pageaddr` is fixed at
   `0x01000000`, identical) — *no* diff there; bytes 16..23
   (`xlp_sysid`) and the `time` field inside the `CheckPoint` body
   differ because both are wall-clock-derived. All other bytes in
   the first 40 must match.

2. **`pg_waldump` parity.** Run

   ```bash
   pg_waldump pg_wal/000000010000000000000001 -s 0/01000000 -e 0/01000072
   ```

   on both clusters. The single emitted line must read

   ```
   rmgr: XLOG  len (rec/tot): 114/   114, tx:  0, lsn: 0/01000028,
     prev 0/00000000, desc: CHECKPOINT_SHUTDOWN redo 0/1000028 tli 1 prev tli 1 …
   ```

   identical modulo the `time` field and sysid-derived hex inside
   the checkpoint body.

3. **Standby attach E2E.** `TestE2E_FailoverGoopgToPG/async` (the
   M0106 driver test) starts a vanilla PG18 backend with
   `standby.signal` pointing at the goopg primary. The `walreceiver`
   parses every page header it streams; any mismatch in
   `xlp_magic`, `xlp_seg_size`, or `xlp_xlog_blcksz` raises a FATAL
   inside `XLogPageRead` and aborts the standby. A clean attach +
   replay-to-end of the recorded LSN proves the long header is
   PG-compatible.

4. **Ordering invariant.** Inject a `panic()` between the WAL write
   and `writePgControl` in a `go test -tags=fault_inject` build,
   then assert that `/tmp/pg-goopg/global/pg_control` does not
   exist on the next `goopg init` retry, while
   `pg_wal/000000010000000000000001` does (and is exactly
   `wal_segment_size` bytes).

5. **`xlp_sysid` cross-check.** `pg_controldata /tmp/pg-goopg | grep
   'system identifier'` must report the same `uint64` that
   `xxd -s 16 -l 8 pg_wal/000000010000000000000001` shows in
   little-endian order.
