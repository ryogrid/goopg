# WAL summarizer — design review (M0122-0009)

status: accepted
date: 2026-10-13

Review of [0122-0009-wal-summarizer.md](0122-0009-wal-summarizer.md), split out to keep the design doc under the D3 length limit. Corrections it lists were applied to that doc in place.

## Findings

This was the agent review (milestone 0122 Workflow step 4). It was read-only against
`postgres/src` (PG 18.3) and goopg HEAD `833acdff8`. Nothing was built or run.

**Verified, no change needed:**
- **BlockRefTable format** (`common/blkreftable.c`):
  - magic `0x652b137b` (`blkreftable.h:30`);
  - the 24-byte unpadded `BlockRefTableSerializedEntry` `{spcOid, dbOid, relNumber, forknum(int),
    limit_block, nchunks}` (:155-161);
  - spc → db → rel → fork qsort (`BlockRefTableComparator` :1152);
  - trailing zero-usage chunks trimmed and middle ones kept (:511-514, :829-831);
  - `chunk_usage[nchunks]` followed by `usage × uint16` per non-empty chunk;
  - array offsets kept in insertion order, conversion to bitmap on the 4096th distinct offset
    (:1067), and a bitmap stays a bitmap (all-zero bitmaps are still written);
  - a 24-byte all-zero terminator, then CRC-32C (standard init/final) over magic..terminator,
    checked by the reader on the zero entry (:645-656, :1292-1309);
  - `SetLimitBlock` only lowers the limit (:903) and filters arrays in place (:949-957).
  - The incremental writer `BlockRefTableWriteEntry` (:817-849) produces identical bytes per entry.
- **File naming and directory:** `%08X×5.summary` and `temp.summary` + `durable_rename`
  (walsummarizer.c:1198-1231); `IsWalSummaryFilename` (walsummary.c:263).
- **SQL output columns:** OIDs 6321-6323 and their column lists match `pg_proc.dat:12493-12512`.
- **Summarizer behavior:**
  - `fast_forward` starts true (:920);
  - the four boundary records and their `wal_level` sources (:1427-1483);
  - the SMGR, DBASE and XACT limit rules (:1249-1415), including SMGR_TRUNCATE_VM using the heap
    `blkno`;
  - `GetLatestLSN` = flush pointer on a primary (:803-810);
  - `WakeupWalSummarizer` after every checkpoint (`xlog.c:7337`); `KeepLogSeg` clamp
    (`xlog.c:8033`); `WaitForWalSummarization` constants (:663-760);
  - `wal_summary_keep_time` range (`guc_tables.c:3516-3526`; Max = 35791394).
- **Upstream VM behavior:** VISIBLE registers VM block 0 + heap block 1 (`heapam.c:8829-8834`), and
  an update registers heap buffers only (`heapam.c:8975-8977`).
- **goopg citations:** 29 groups were spot-checked. All match except the corrections below.

**Corrected in the text:**
1. 2a: every `SummarizeWAL` call starts in fast-forward, not only after an inexact start. Skipped
   ranges still advance `summarized_lsn`.
2. 2a: a record ending past `switch_lsn` ends the file at its `EndRecPtr`, not at `switch_lsn`; the
   main loop then rewinds.
3. 2a: on the current TLI no file is ever cut at the flush pointer.
4. 2a/2b/4.3: the sleep-quanta rule, and the `wal_level` offsets for PARAMETER_CHANGE (20) and
   END_OF_RECOVERY (16).
5. 2c: the "database, then tablespace" comment lives at `blkreftable.h:96-97` (incremental-writer
   API) and is wrong for **both** writers.
6. 2c: `GetWalSummaries` and `FilterWalSummaries` use different overlap predicates.
7. 2f: `summarized_lsn` is the start of the next file, not "end of the last file written".
8. 4.5: SQLSTATEs. The reader errors are XX001 via `ReportWalSummaryError`; "ends unexpectedly" was
   added; open uses `errcode_for_file_access`; `invalid timeline` is 22023. The "22P02-class"
   wording was wrong.
9. 3: CONTRECORD handling is in `readAllPageAware`, not `readAllUncached`.
10. 3: the tablespace rebuild rule. The emitter uses `DBOid==0` → 1664, not
    `TblOid==0 && DBOid==0`. The text now recommends reading the raw `spcOid`.
11. 3: shutdown (and all) `CheckPoint.wal_level` comes from `xlog.DefaultGUCParameters()`
    (hard-coded 1), **not** the GUC.
12. 3: runtime partial truncation exists (VACUUM tail, unlogged because the `LogSmgrTruncateTo`
    hook is unwired). `ExtendRelationBatchWithXID` also emits SMGR_CREATE.
13. 4.2: `fetchIOStatRows` is at `pgstat_io.go:253`; the `WriteHistory` range; `postmaster.c`
    severity.
14. 4.4 guard: it gated "S7 (`pg_basebackup --incremental`)", but S7 is WS-002. The gate now
    applies to the incremental-backup follow-on.
15. 5: "each independently landable" was not true (S5 needs S3, S4b needs S3, S6 mixed
    summarizer-local and WAL-producer work). Dependency order added and S6 split into S6a..S6f.

**Open questions resolved:**
- (a) HeapVisible (kind 29) is not in `recordKindToRmgrInfo` and is stamped `RmgrGoopgCatalog`
  (128) by `classifyXLogRecord`. It is live-emitted on every VM change (`open.go:2757`,
  `vm.go:85-96`). The `rmgr_map.go:80-88` comment is stale.
- (b) `Pool.LogHeapVacuum()` is dead. There is no non-test caller and the field is unread
  elsewhere.
- (c) There is no cycle. postmaster imports backup, and backup does not import postmaster (only
  the external `backup_test` package does). `walsummarizer` must stay free of `executor`,
  `initdb`, `backup` and `postmaster` imports.
- (d) goopg accepts `wal_level=minimal`: it is a legal enum value with no startup check (PG's
  `max_wal_senders` check is missing too). It changes no WAL, so summaries stay complete, but the
  `minimal` + `summarize_wal` refusal must be added in S3.
- (e) Writes outside the pool or without a first-touch FPI, listed in 4.4:
  - `MarkDirtyWithLSN*` (covered by its own block refs);
  - `VMSaveForks` (item 2, **not** fpw-mitigated);
  - **new** item 10, REINDEX CONCURRENTLY `os.Rename` swap: an incremental-backup corruption
    risk;
  - **new** item 11, SET TABLESPACE byte copy: unsafe when moved away and back;
  - **new** item 12, unlogged VACUUM tail truncation: parity only;
  - **new** item 13, ROLLBACK-of-TRUNCATE direct rewrite: low risk;
  - the remaining sites, checked and found safe.

**Remaining open questions:**
- `recovery.go:834,886` (the SHUTDOWN `wal_level` offset) was not re-read. Offset 20 is confirmed
  from `pg_control.h`.
- Whether Oracle B's `pg_waldump` run tolerates goopg's RmgrGoopgCatalog (128) records, or needs
  `--rmgr` filtering.
- Whether WS-002's "exactly 2 blocks" holds on goopg. Any goopg-only side write between the UPDATE
  and the REDO record breaks it, and it must be measured at S7.
- Owners and tasks for 4.4 items 10-12 (REINDEX CONCURRENTLY, SET TABLESPACE, VACUUM truncate WAL).
  They are WAL/crash-recovery gaps independent of the summarizer and should be filed against their
  areas.

Verdict: approved for S1
