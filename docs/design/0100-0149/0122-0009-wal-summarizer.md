# WAL summarizer (M0122-0009)

status: in progress — agent-reviewed 2026-10-13; S1 (blkreftable package) landed 2026-10-13; S2–S7 open
date: 2026-10-13

Slice "WAL summarizer" of M0122-0009 (WAL / recovery / crash-consistency infra).
Design only. Nothing here is implemented yet. Upstream paths are relative to `postgres/src/`.

## 1. Source

- Backlog: `unimplemented_feat.json` entry "WAL summarization not running;
  pg_available_wal_summaries() always returns empty rows" (task_id M0095-0002,
  status open, first deferred 26cb3148). `.ralph/fix_plan.md` (M0122-0009
  block, "Still open, with their owners") names it the next slice.
- Current stubs:
  - `internal/executor/operators_pg_available_wal_summaries.go`: `Next()` returns `EOF` immediately.
  - `summarize_wal` is registered with BootVal `off` and ContextSigHup
    (`internal/utils/misc/defaults.go:1521-1530`).
  - `wal_summary_keep_time` exists only in the name/context table
    `internal/utils/misc/pg_gucs_gen.go:415`. It is **not registered** as a variable.
- Tests that want it:
  - `internal/testport/client_tools_port_test.go:279` `TestPort_PgWalsummary002Blocks`
    (inventory row WS-002, `docs/test-port/postgres-oracle-target-inventory.csv:541`). The rationale
    says "pg_available_wal_summaries() / summarize_wal / pg_walsummary -i deferred". The test currently
    asserts 0 summaries and only cluster-init + SQL.
  - WS-001 (`:540`, `TestPort_PgWalsummary001Basic`) already passes. It only exercises the binary's CLI.
  - Upstream `bin/pg_walsummary/t/002_blocks.pl` needs:
    - `summarize_wal = on`;
    - `pg_available_wal_summaries()` with `end_lsn >= pg_current_wal_insert_lsn()`;
    - `pg_stat_io` `walsummarizer`/`wal` `reads > 0`;
    - "exactly one new WAL summary" after `UPDATE …; CHECKPOINT`;
    - the file at `pg_wal/summaries/%08s×5.summary`;
    - `pg_walsummary -i` printing exactly 2 lines, one of them `FORK main: block 0`.
  - `pg_combinebackup` TAP rows 001–011 (`postgres-oracle-target-inventory.csv:485-495`) are all `not-tried`.
    They need `pg_basebackup --incremental`. goopg accepts and **ignores** `INCREMENTAL`
    (`internal/backup/basebackup.go:1315-1321`) and has no `UPLOAD_MANIFEST`.

## 2. Upstream reference (PG 18.3)

### 2a. Summarizer loop: `backend/postmaster/walsummarizer.c`

- **Start and stop.**
  - The postmaster starts the summarizer when `summarize_wal && pmState ∈ {PM_RUN, PM_HOT_STANDBY}`
    (`postmaster.c:3390-3393`).
  - Startup FATALs if `summarize_wal` is on with `wal_level=minimal` (`postmaster.c:854`).
  - The process exits when `!summarize_wal` after a SIGHUP reload
    (`ProcessWalSummarizerInterrupts`, walsummarizer.c:861). Both GUCs are PGC_SIGHUP
    (`guc_tables.c:1885`, `:3516`).
- **Start position.** `GetOldestUnsummarizedLSN` (:508):
  - Starts at the oldest segment that still exists on the oldest timeline in the history
    (`XLogGetOldestSegno` → segment start, `lsn_is_exact=false`).
  - Then raises that position to the max `end_lsn` of existing summary files on that TLI
    (`exact=true`).
  - Nothing persists progress except the summary files themselves.
  - When the position is not exact, `SummarizeWAL` uses `XLogFindNextRecord` (:960-992).
- **Main loop** (`WalSummarizerMain`, :213, loop :379-447):
  1. `MaybeRemoveOldWalSummaries`.
  2. `latest = GetLatestLSN()`. On a primary this is `GetFlushRecPtr`, so **only flushed WAL is
     summarized**. In recovery it is the max of the walreceiver flush and the replay pointer (:803).
  3. If `current_tli != latest_tli`, compute the `switch_lsn` from the timeline history
     (`tliSwitchPoint`). Once `current_lsn >= switch_lsn`, move to the next TLI at exactly
     `switch_lsn` (:403-421).
  4. `SummarizeWAL(tli, start, exact, switch_lsn, latest)`.
  5. Publish `summarized_lsn = pending_lsn = end` and broadcast `summary_file_cv`.
- **How a summary ends** (`SummarizeWAL`, :909):
  - `fast_forward` starts **true** (:920).
  - For every `RM_XLOG_ID` record, `SummarizeXlogRecord` (:1427) returns true for
    `XLOG_CHECKPOINT_REDO`, `XLOG_CHECKPOINT_SHUTDOWN`, `XLOG_PARAMETER_CHANGE` and
    `XLOG_END_OF_RECOVERY`. It extracts that record's `wal_level`.
  - If that record is not the first record of the file, the summary ends at its **ReadRecPtr**
    (:1112-1116). The record is then the first record of the next file.
  - If it is the first record, it sets `fast_forward = (wal_level == minimal)`.
  - RM_XLOG records go through `SummarizeXlogRecord` even while fast-forwarding. The
    RM_DBASE/RM_SMGR/RM_XACT special cases and the block-ref loop run only when `!fast_forward`
    (:1123-1166).
  - Consequence: summaries run redo-point to redo-point. **Every** `SummarizeWAL` call starts in
    fast-forward. If its first record is not a boundary record, WAL up to the next boundary is
    skipped without writing a file. In steady state this only happens after an inexact start,
    because every later call starts exactly at the previous boundary's ReadRecPtr. The skipped
    range still advances `summarized_lsn`/`pending_lsn` (main loop :425-439), so it shows up as a
    gap to `WalSummariesAreComplete`.
  - Other end conditions:
    - a record that *starts* at or after `switch_lsn` → end = `switch_lsn` and the record is not
      consumed (:1061-1086);
    - a record that *ends* at or after `switch_lsn` → end = that record's `EndRecPtr`, which may be
      past `switch_lsn` (:1171, :1183-1185). The main loop then rewinds to exactly `switch_lsn` on
      the next TLI (:403-421);
    - end of a historic TLI (end = `read_upto`, :1042).
  - On the current TLI a summary never ends at "latest flushed LSN". The page reader just waits
    (:1524-1585). A file therefore appears only after a boundary record has been flushed.
  - Otherwise `summary_end_lsn = EndRecPtr` after each record, and that value is published as
    `pending_lsn` (:1171-1177).
  - A file is written only if `end > start && !fast_forward` (:1198).
- **Waiting for WAL.** `summarizer_read_local_xlog_page` (:1500) sleeps via `summarizer_wait_for_wal`
  (:1614).
  - The sleep is `sleep_quanta × 200 ms`, with `sleep_quanta` in 1..150.
  - The quanta double when no page was read. They stay the same after exactly one page. After
    more than one page they shrink by the number of pages read, with a floor of 1 (:1614-1645).
  - The latch is also set by `WakeupWalSummarizer()` after every checkpoint (`xlog.c:7337`).

### 2b. Records inspected and their BlockRefTable effect

Records are ignored while `fast_forward` is true. Otherwise:

| Record | Function | Effect |
|---|---|---|
| any record, every block ref (`XLogRecGetBlockTagExtended`) | main loop :1148-1166 | `MarkBlockModified(rlocator, fork, blk)` unless `fork == FSM` |
| `RM_SMGR_ID / XLOG_SMGR_CREATE` | `SummarizeSmgrRecord` :1318 | `SetLimitBlock(xlrec.rlocator, xlrec.forkNum, 0)` unless FSM |
| `RM_SMGR_ID / XLOG_SMGR_TRUNCATE` | same | `flags & SMGR_TRUNCATE_HEAP` → `SetLimitBlock(main, blkno)`; `& SMGR_TRUNCATE_VM` → `SetLimitBlock(vm, blkno)` (sic: heap blkno); FSM ignored |
| `RM_DBASE_ID / CREATE_FILE_COPY`, `CREATE_WAL_LOG` | `SummarizeDbaseRecord` :1249 | `SetLimitBlock({spc, db, relNumber=0}, MAIN, 0)` |
| `RM_DBASE_ID / XLOG_DBASE_DROP` | same | the same, for each `tablespace_ids[i]` |
| `RM_XACT_ID / COMMIT, COMMIT_PREPARED, ABORT, ABORT_PREPARED` | `SummarizeXactRecord` :1367 | `ParseCommitRecord` / `ParseAbortRecord`; for each dropped `xlocators[i]`, `SetLimitBlock(fork, 0)` for all forks except FSM |
| `RM_XLOG_ID / REDO, SHUTDOWN, PARAMETER_CHANGE, END_OF_RECOVERY` | `SummarizeXlogRecord` :1427 | file boundary + fast-forward decision. Payload `wal_level` is an int at offset 0 for REDO, 20 for `CheckPoint` (SHUTDOWN), 20 for `xl_parameter_change` and 16 for `xl_end_of_recovery` (`pg_control.h:35-43`, `xlog_internal.h:273-306`) |

The `{spc, db, 0}` sentinel makes `basebackup_incremental.c` back up the whole database fully
(`GetFileBackupMethod`, :740-754).

A VM *clear* is not a block reference upstream:
- `log_heap_update` registers only the heap buffers (`heapam.c:8975-8977`).
- Only `XLOG_HEAP2_VISIBLE` registers the VM buffer as block 0 and the heap buffer as block 1
  (`heapam.c:8830-8835`).

### 2c. On-disk format (must be byte-identical for `pg_walsummary`)

**File name.** The file is `pg_wal/summaries/%08X%08X%08X%08X%08X.summary` (walsummarizer.c:1204).
- The fields are TLI, start_lsn hi, start_lsn lo, end_lsn hi, end_lsn lo.
- They are uppercase hex, 40 characters total, then `.summary`.
- The parser (`IsWalSummaryFilename`, walsummary.c:263) accepts exactly 40 `[0-9A-F]` characters
  followed by `.summary`. Anything else in the directory is ignored.
- The file is written to `pg_wal/summaries/temp.summary` and then moved into place with
  `durable_rename` (:1202, :1231).
- The two list filters use **different** overlap predicates, and both must be ported literally:
  - `GetWalSummaries` (walsummary.c:43) drops a file when `start_lsn >= file.end` or
    `end_lsn <= file.start`.
  - `FilterWalSummaries` (:100) drops a file only when `start_lsn > ws.end` or `end_lsn < ws.start`.
  - `WalSummariesAreComplete` (:138) sorts by start and walks forward. It tolerates overlap, and
    reports the first gap in `*missing_lsn`, or `InvalidXLogRecPtr` when the list is empty.

**Serialized BlockRefTable** (`common/blkreftable.c`, `WriteBlockRefTable` :474). All integers are
host-endian. goopg only targets little-endian.

1. `uint32 magic = 0x652b137b`, bytes `7b 13 2b 65` (`blkreftable.h:30`).
2. Entries in ascending order of `(spcOid, dbOid, relNumber, forknum)`
   (`BlockRefTableComparator` :1152).
   - Tablespace sorts first. The comment at `blkreftable.h:96-97` (on the incremental-writer API)
     says "database, then tablespace". It contradicts both `blkreftable.c:811-813` and the
     comparator. Both goopg writers (in-memory and incremental) must sort spc-first.
   - `forknum` compares as signed `int`.
3. Each entry is a 24-byte `BlockRefTableSerializedEntry` (:155) with no padding:
   `spcOid u32, dbOid u32, relNumber u32, forknum i32 (enum), limit_block u32, nchunks u32`.
   - `nchunks` counts chunks after trimming **trailing** chunks whose `usage == 0` (:511-514).
     Zero-usage chunks in the middle are kept.
   - Then `nchunks × uint16 chunk_usage[]`, written only if `nchunks > 0`.
   - Then, for every chunk `j < nchunks` with `usage > 0`, `usage × uint16` of chunk data.
4. Chunk geometry: `BLOCKS_PER_CHUNK = 65536` (:78), `MAX_ENTRIES_PER_CHUNK = 4096` (:80).
   - **Array chunk** (`usage < 4096`):
     - It holds 16-bit offsets of `blk % 65536`.
     - Offsets are distinct and kept in **insertion order**, not sorted (:1057-1114).
     - Adding the 4096th distinct offset to an array of 4095 converts the chunk to a bitmap (:1067).
   - **Bitmap chunk** (`usage == 4096`):
     - It is 4096 words (8192 bytes). Bit `(o % 16)` of word `o / 16` is the bit for offset `o`.
     - Once a bitmap, a chunk stays a bitmap even if `SetLimitBlock` clears all its bits. In that
       case it is still written as 8192 zero-filled bytes.
5. Terminator: a 24-byte all-zero entry (`BlockRefTableFileTerminate` :1292).
6. `uint32 CRC32C` over every preceding byte, from the magic through the terminator.
   - It uses standard init and final XOR. This equals Go `crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))`.
   - The reader checks it when it sees the zero entry (:645-656).

**limit_block semantics:**
- An entry created by `MarkBlockModified` starts with `limit_block = InvalidBlockNumber` (`0xFFFFFFFF`, :319).
- An entry created by `SetLimitBlock` starts at the given value.
- `SetLimitBlock` only ever *lowers* the limit (:903).
- On lowering, it drops every chunk above the limit chunk by setting usage to 0. Inside the limit
  chunk it filters array offsets (keeping order) or clears bits (:923-958).
- A later mark on a dropped chunk restarts it as an array.

### 2d. SQL functions (`backend/backup/walsummaryfuncs.c`, `pg_proc.dat` oids 6321-6323)

- `pg_available_wal_summaries()`: SRF returning `(tli int8, start_lsn pg_lsn, end_lsn pg_lsn)`.
  - One row per file returned by `GetWalSummaries(0, 0, 0)`, in directory order (:32).
  - It does **not** check `summarize_wal`.
- `pg_wal_summary_contents(tli int8, start_lsn pg_lsn, end_lsn pg_lsn)`: SRF returning
  `(relfilenode oid, reltablespace oid, reldatabase oid, relforknumber int2, relblocknumber int8, is_limit_block bool)` (:69).
  - Errors with `invalid timeline %lld` (22023) when `tli < 1 || tli > INT32_MAX` (:92).
  - Opening a missing file is an ERROR.
  - Per relation fork, it emits a `limit_block` row with `is_limit_block = true` when the limit is
    valid, then one `false` row per modified block in file order.
- `pg_get_wal_summarizer_state()`: one record
  `(summarized_tli int8, summarized_lsn pg_lsn, pending_lsn pg_lsn, summarizer_pid int4)` (:177).
  - Before initialization it returns `(0, 0/0, 0/0, NULL)`.
  - When no summarizer is running, `pending_lsn = summarized_lsn` and pid is NULL (:450-490).

### 2e. `wal_summary_keep_time` removal (`MaybeRemoveOldWalSummaries`, :1660)

- GUC: int, minutes, default 14400 (10 days), range 0 .. INT_MAX/60, PGC_SIGHUP. A value of 0
  disables removal (`guc_tables.c:3516-3526`).
- Removal runs at most once per redo-pointer advance (:1676).
- For each TLI that has summaries, a file is removed if both of these hold:
  - its `end_lsn <= start of the oldest segment still on disk`, or no WAL is left for that TLI;
  - its mtime is older than `now - keep_time*60` (`RemoveWalSummaryIfOlderThan`, walsummary.c:230).

### 2f. Shared state and waiters

- `WalSummarizerData` (:56):
  - `initialized`, `summarized_tli`, `summarized_lsn`, `lsn_is_exact`, `summarizer_pgprocno`,
    `pending_lsn`, protected by `WALSummarizerLock`;
  - the condition variable `summary_file_cv`.
- `summarized_lsn` is the LSN where the next summary file will start. Normally that is the end of
  the last file written. It also advances over fast-forwarded ranges that wrote no file, and it is
  rewound to `switch_lsn` at a timeline switch (:56-95, :397-439). `pending_lsn` is the end of the
  last record read into the in-memory table.
- `GetOldestUnsummarizedLSN` returns `InvalidXLogRecPtr` when `summarize_wal` is off (:521-522).
  For a non-summarizer caller it reads shared memory, and it computes and initializes shared memory
  only if that has not happened yet.
- `WaitForWalSummarization(lsn)` (:663):
  - Loops until `summarized_lsn >= lsn`, sleeping on the CV.
  - Every 10 s it emits WARNING `still waiting for WAL summarization through %X/%X after %ld second(s)`.
  - After 6 consecutive 10 s cycles without `pending_lsn` progress it raises ERROR 55000
    `WAL summarization is not progressing`.
  - It returns immediately if `summarize_wal` turns off.
- Callers:
  - Incremental `BASE_BACKUP` waits for `backup_state->startpoint` (`basebackup_incremental.c:459`).
    It then needs `GetWalSummaries` + `WalSummariesAreComplete` to cover the manifest's WAL range.
  - At promotion, the `.partial` rename waits for `EndOfLog` (`xlog.c:5396`).
  - `KeepLogSeg` never removes WAL ≥ `GetOldestUnsummarizedLSN()` (`xlog.c:8033`).

## 3. goopg today

- **WAL reader for a background consumer.** `xlog.RecordIterator` (`internal/access/transam/xlog/iterator.go:37`):
  - `NewRecordIterator(w, walDir, segSize, startLSN)` (:101);
  - `Next(ctx)` (:154) blocks at the tail on the writer subscription (`Writer.Subscribe`, `writer.go:834`).
  - Each `Record` (`reader.go:16`) has `XLog *XLogDecodedRecord`, which holds:
    - the `Header` (`Rmid`, `Info`);
    - `Blocks []XLogBlockRef{Rel, Block, …}`;
    - `MainData`.
    These are produced by `decodeRecordXLogDetailed` / `parseXLogRecordData`
    (`pg_xlog_decode.go:332`, `:359`) and `decodeXLogBlockRefHeader` (:507).
  - LSN conventions differ from PG (iterator.go:475-476):
    - `StartLSN = pos+1`, which is 1-based: PG `ReadRecPtr = StartLSN-1`.
    - `EndLSN = pos+advance`, which equals PG `EndRecPtr` (the 0-based offset of the next byte).
- **Reader limitations to fix:**
  - The iterator reads up to `WrittenLSN` (`writer.go:799`), not the flush LSN. `flushedLSNAtomic`
    (`writer.go:296`) has no exported accessor.
  - It has no `XLogFindNextRecord` equivalent: it cannot start at a segment start whose first page
    carries `XLP_FIRST_IS_CONTRECORD`. `readAllPageAware` (`reader.go:103`) handles this case at
    `reader.go:140-162`. (It is not `readAllUncached`, which starts at :33.)
  - Records ending exactly at a page end: `readRecordBytesAt` (`iterator.go:490-514`) skips a page
    header only while bytes are still owed. `EndLSN` therefore stops before the next page's header,
    which matches PG's `EndRecPtr`. The S3 golden test still pins this.
  - `decodeXLogBlockRefHeader` loses information about the built-in tablespaces: 1663 and 1664 map to
    `TblOid=0` (`pg_xlog_decode.go:539-551`). The emitter rule in `EncodeSmgrCreatePG`
    (`pg_assembled_emit.go:1151-1162`) is `DBOid==0` → 1664 (whatever TblOid is), else
    `TblOid==0` → 1663, else `TblOid`. If the summarizer rebuilds the locator, it must use that
    rule.
    - Preferred: have the summarizer read the raw `spcOid` from the block header. Add a raw field to
      `XLogBlockRef`, or use a header-only walk. Then there is no reconstruction at all, and
      PG-produced WAL (Oracle A) round-trips exactly.
  - The decoder materializes every FPI (`decodeXLogBlockImage`). That is wasted work for a summarizer.
  - It rejects compressed FPIs (`pg_xlog_decode.go:521-529`). goopg never writes them.
- **PG-format block refs.** heap insert/delete/update/hot-update/prune/freeze/vacuum-cleanup, all
  btree records, `XLOG_FPI`, and `XLOG_SEQ_LOG` are assembled with real `XLogRecordBlockHeader`s
  (`pg_assembled_emit.go` emit sites :159-1134; `seq_log.go:44-53`). `rmgr_map.go:34-89` maps the
  rest.
- **Create, truncate, drop:**
  - `XLOG_SMGR_CREATE`: `EncodeSmgrCreatePG` (`pg_assembled_emit.go:1151`), emitted by
    `Pool.pinNewXID` when **block 0** is extended (`internal/storage/bufpool.go:1836`). The bulk
    path `Pool.ExtendRelationBatchWithXID` emits it too, when `first == 0` (`bufpool.go:1919-1929`;
    callers `executor/bulk_insert.go:208` and `heap_insert_select.go:53`). It also fires when an
    in-place-truncated file is re-extended. B-tree (re)builds truncate in place and then `PinNew`
    block 0 (`nbtree/btree.go:1963`, `bulkload.go:203`), so they get limit 0.
  - `XLOG_DBASE_CREATE_WAL_LOG` / `XLOG_DBASE_DROP` (`pg_assembled_emit.go:1202`, `:1214`;
    emitted at `internal/postmaster/database_ddl.go:1686`, `:1890`). CREATE DATABASE also writes one
    FPI per block (`walLogRelfileFPIs`, :959).
  - `XLOG_SMGR_TRUNCATE` is **never emitted** (`pg_xlog_decode.go:177-181`).
    - TRUNCATE truncates the relfile in place with no truncate record
      (`internal/executor/operators_ddl.go:17190`, `storage/smgr.go:640`).
    - Runtime partial truncation **does exist**. It is VACUUM's tail truncation:
      `commands/vacuum/vacuum.go:368` → `Pool.TruncateRelationTail` (`bufpool.go:1433-1450`) →
      `Manager.TruncateRelationTo`.
      - It is WAL-first only when the `LogSmgrTruncateTo` hook is set. Production never sets it:
        there is no `LogSmgrTruncateTo:` in `initdb/open.go`, and only `vacuum_test.go` calls
        `SetLogSmgrTruncateTo`.
      - So the tail truncation is **unlogged**. The vacuum.go comment that a missing hook makes it
        "a no-op" is wrong: the physical truncate still runs.
      - Replay-side `TruncateRelationTo` calls are at `recovery.go:1239` and `:6579-6593`.
  - Commit and abort carry **no `XACT_XINFO_HAS_RELFILELOCATORS`** (`pg_assembled_emit.go:357-407`).
    DROP unlinks files immediately (`operators_ddl.go:6942` etc.).
  - The goopg xact parser stops before the relfilelocator chunk (`pg_xact_parse.go:115`).
- **Checkpoint markers:**
  - Online checkpoints append `XLOG_CHECKPOINT_REDO` at the redo point when `PGCompatCheckpoints`,
    which is on in production (`checkpointer.go:830-842`, `initdb/open.go:1991`). Its payload is a
    **hard-coded** `wal_level=1` (`pg_assembled_emit.go:1300-1302`).
  - Every checkpoint record (online and shutdown) carries `CheckPoint.wal_level` from
    `CheckpointerConfig.GUCParams.WalLevel` (`checkpointer.go:920`).
    - **That is not the GUC.** `initdb/open.go:1954,1990` passes `xlog.DefaultGUCParameters()`,
      which hard-codes `WalLevel: 1` (`checkpointer.go:89`).
    - `applyCheckpointGUCs` (`cmd/goopg/main.go:760-809`) never touches it.
    - So SHUTDOWN records also always say `replica`.
  - `XLOG_PARAMETER_CHANGE` and `XLOG_END_OF_RECOVERY` are decoded but **never emitted**.
  - The end-of-recovery checkpoint is an ordinary online `CheckpointNow`
    (`internal/initdb/recovery_state.go:159-167`).
- **Background processes:**
  - The checkpointer goroutine is started in `cmd/goopg/main.go:868-871`.
  - The autovacuum launcher is built in `main.go:601-612` and started in
    `internal/postmaster/server.go:640-647` on `runCtx`.
  - SIGHUP and `goopg reload` go through `Server.reloadConfig` (`server.go:824`), which ends with
    the `ApplyCheckpointGUCs` hook. That is the natural place to start or stop the summarizer.
  - Background activity slots are defined in `internal/utils/activity/registry.go:24-27`.
  - `pg_stat_io` already emits the two `walsummarizer` rows with zero counters
    (`internal/executor/pgstat_io.go:120,138,155-159`). The wait event `WalSummarizer` exists
    (`catalog/catalog.go:10822`).
- **WAL retention.** `SlotAwareRetainer.Retain` (`xlog/retention.go:95`) computes `keepLSN` at :143.
  It has no summarizer clamp.
- **SQL surface:**
  - `pg_available_wal_summaries` is planned by name in `optimizer/planner.go:7061` →
    `planPgAvailableWalSummaries` (:7901). Its node is `optimizer/plan.go:2615`. Executor dispatch is
    at `executor/executor.go:73` and the EXPLAIN label at `operators_explain.go:7345`.
  - `pg_wal_summary_contents`, `pg_get_wal_summarizer_state` and `pg_ls_summariesdir` exist only as
    pg_proc seeds (`initdb/pg_proc_seed_data.go:3302-3303`, `:3376`) and in volatility lists. **They
    have no implementation.**
  - `pg_current_wal_insert_lsn` is also seeded but not executable
    (`testport/pgwaldump_server_tier_test.go:28`). It was re-checked 2026-10-13: the only
    executor hit is the volatility list, `executor/pg_nonimmutable_builtins.go:148`.
- **Directories.** initdb creates `pg_wal/summaries` (`internal/initdb/initdb.go:152`). Base backups
  ship it empty (`backup/basebackup.go:797`).

## 4. Design

### 4.1 Packages (mirror the upstream split; avoid import cycles)

| goopg | upstream | contents |
|---|---|---|
| `internal/access/common/blkreftable` | `common/blkreftable.c` | `Table`, `Entry`, `RelFileLocator{Spc,Db,Rel uint32}`, `SetLimitBlock`, `MarkBlockModified`, `GetEntry`, `Entry.Blocks(start,stop)`, `(*Table).WriteTo(io.Writer)`, streaming `Reader` (`NextRelation`, `GetBlocks`) and `Writer` |
| `internal/backup/walsummary` (leaf) | `backup/walsummary.c` | `File{TLI, Start, End}`, `FileName`/`ParseFileName`, `List(dir, tli, start, end)`, `Filter`, `AreComplete`, `Open`, `RemoveIfOlderThan` |
| `internal/postmaster/walsummarizer` | `postmaster/walsummarizer.c` | the goroutine, shared `State`, `OldestUnsummarizedLSN`, `WaitFor(lsn)`, record classifier |

- The `blkreftable` placement follows the `internal/access/common/pglz` precedent (`src/common`
  code lives there).
- `blkreftable` has its own locator type. It does not reuse `storage.RelFileNode`, whose `TblOid=0`
  is lossy.
- The executor reaches the summarizer through an interface on `executor.Context`, following the
  `executor.Checkpointer` pattern (`executor/context.go:1969`). Retention gets a
  `KeepLSNFn func() uint64` field on `SlotAwareRetainer`.
- **Import graph (open question (c), resolved by reading import blocks).**
  - `internal/postmaster` imports `internal/backup` (`postmaster/server.go`).
  - `internal/backup` does **not** import `internal/postmaster`. Its package comment
    (`backup/basebackup.go:7-12`) forbids it, because the chain is postmaster → replication →
    backup. The only reference is the external test package `backup_test`
    (`basebackup_wire_test.go`), which is not a cycle.
  - `internal/backup` imports `executor`, `initdb`, `libpq`, `xlog` and `utils/misc`
    (`basebackup.go:77-82`).
  - Consequences:
    - `internal/backup/walsummary` and `internal/postmaster/walsummarizer` are separate Go packages
      and need not cycle. The `internal/postmaster/autovacuum` subpackage is the precedent.
    - `walsummarizer` must import only `xlog`, `blkreftable`, `walsummary` and `utils/misc`. It
      must not import `executor`, `initdb`, `backup` or `postmaster`. That way a future incremental
      `BASE_BACKUP` in `internal/backup` can import it for `WaitFor` / `AreComplete`.
    - Checkpointer and redo-pointer access goes through function hooks.

### 4.2 Goroutine lifecycle

- **Start.**
  - Start the goroutine in `Server.Run` next to autovacuum (`server.go:640`) when `summarize_wal` is on.
  - Refuse startup with `WAL cannot be summarized when "wal_level" is "minimal"` (PG text,
    `postmaster.c:854-856`; PG raises it as ERROR, which the postmaster promotes to FATAL).
    - goopg **accepts** `wal_level=minimal` today. It is a legal enum value (`defaults.go:1538-1539`)
      with no startup check. That includes PG's sibling `max_wal_senders > 0` check
      (`postmaster.c:851-853`), which goopg also lacks.
    - The setting changes no WAL. So this check is the only thing that keeps `minimal` and the
      summarizer apart.
  - On reload, `reloadConfig` calls `Summarizer.Reconcile()`, which starts or cancels the goroutine
    (PG: the process exits on `!summarize_wal` and the postmaster restarts it).
  - Not started during crash or archive recovery, which runs before `Run`.
  - Standby summarization (PM_HOT_STANDBY) is deferred.
- **Position.** Mirror `GetOldestUnsummarizedLSN` exactly:
  1. Start at the oldest segment on disk for the oldest TLI in the history (`firstAvailableSegment`,
     `reader.go:486`, plus `ReadHistory`, `timeline_history.go:58`), with `exact=false`.
  2. Raise it to the max `End` of `walsummary.List(tli)`, which sets `exact=true`.
  Nothing else is persisted.
- **Reading.** Read through a flush-bounded iterator.
  - Option 1: add `Writer.FlushedLSN()` and an iterator read limit, so records ending past the flush
    point are not consumed.
  - Option 2: add a separate page reader over segment files.
  - For `exact=false`, implement `FindNextRecord` by honouring `xlp_rem_len` and
    `XLP_FIRST_IS_CONTRECORD` (reuse the `readAllPageAware` logic, `reader.go:140-162`).
  - Wake on `Writer.Subscribe` and on an explicit post-checkpoint `Wakeup()`, called from the
    checkpointer after the checkpoint record is flushed. Keep PG's quanta backoff as the fallback.
- **LSN translation (one place only).** PG `ReadRecPtr = rec.StartLSN-1` and PG `EndRecPtr = rec.EndLSN`.
  - File names, `State` and the SQL functions use PG LSNs only.
  - A golden test pins this translation on records that end exactly at page and segment boundaries.
- **Loop body** (port of `SummarizeWAL`):
  - `fast_forward` starts true.
  - A boundary record that is not first ends the summary at `ReadRecPtr`.
  - Timeline switch handling is the same as upstream.
  - Write `temp.summary`, fsync it, rename it, then fsync the directory (as `WriteHistory` does,
    `timeline_history.go:121-150`, whose directory fsync at :145-150 is best-effort; the
    summarizer's must not be, matching `durable_rename`). Then publish state and broadcast.
- **State.** A mutex-guarded struct with `initialized`, `summarizedTLI`, `summarizedLSN`,
  `lsnIsExact`, `pendingLSN` and `running`, plus a broadcast channel replaced on every publish.
  - `WaitFor(lsn)` is a verbatim port of `WaitForWalSummarization`, including its WARNING/ERROR text
    and 10 s / 6-cycle constants.
  - `pg_get_wal_summarizer_state.summarizer_pid` is the pid goopg reports for a new background slot
    (`WalSummarizerIdx` after `BgwriterIdx`, `registry.go:27`). Its `pg_stat_activity` row has
    backend_type `walsummarizer`.
- **I/O stats.** Count WAL page reads into the `walsummarizer/wal/normal` cell of
  `fetchIOStatRows` (`pgstat_io.go:253`). WS-002 polls `sum(reads) > 0`.
- **Retention.** `SlotAwareRetainer.Retain` clamps `keepLSN` to `KeepLSNFn()` (the
  `GetOldestUnsummarizedLSN` port; 0 when off).

### 4.3 Record classifier

The input is `xlog.Record`. Record kinds are dispatched on `Header.Rmid` / `Header.Info&0xF0`, as
upstream does:
- **Block refs:** for every `XLog.Blocks[i]` with `Rel.Fork != FSMFork`, call `MarkBlockModified`
  with the rebuilt locator. RM_XLOG FPI records are included.
- **RM_SMGR CREATE / TRUNCATE:** decode `xl_smgr_create` / `xl_smgr_truncate` from `MainData` as
  in 2b. TRUNCATE is needed for PG-produced WAL fixtures.
- **RM_DBASE:** `CREATE_FILE_COPY`, `CREATE_WAL_LOG`, `DROP` as in 2b.
- **RM_XACT:** extend `pg_xact_parse.go` past subxacts to the `HAS_RELFILELOCATORS` chunk
  (`nrels i32` + `RelFileLocator[nrels]`). For each locator, `SetLimitBlock(fork, 0)` for forks
  0, 2 and 3.
- **RM_XLOG boundaries:** REDO (int `wal_level` at offset 0), SHUTDOWN (`CheckPoint.wal_level` at
  offset 20, `recovery.go:834,886`), PARAMETER_CHANGE (`wal_level` at offset 20),
  END_OF_RECOVERY (`wal_level` at offset 16). All four are processed even in fast-forward. The
  RM_SMGR/RM_DBASE/RM_XACT arms and the block-ref loop run only when `!fast_forward`.
- **goopg-native records** (`Record.Payload != nil`, no blocks): see 4.4.
  - Native records stamped with a real PG rmgr (HeapLock → RM_HEAP, HeapVacuum → RM_HEAP2) must
    not be fed to a PG-struct decoder.
  - Records under `RmgrGoopgCatalog` (128) are discriminated on `payload[0]`. This includes the
    live `RecordKindHeapVisible`, see 4.4 item 2.

### 4.4 Where goopg's WAL would make a summary miss a modification (main correctness risk)

**Baseline mitigation.** Every page mutation funnels through `markDirtyCore` → `maybeEmitFPI`
(`bufpool.go:2306-2350`, `:2598`) or `MarkDirtyChangeRecord` (:2485). Both emit a block-ref'd
`XLOG_FPI` on the first touch after each published redo (`needsImage`, :1142), provided
`full_page_writes` is on.
- The redo barrier ensures that FPI lands after the `CHECKPOINT_REDO` record that opens the summary
  (`checkpointer.go:814-842`).
- So with `full_page_writes=on`, every heap or index page dirtied in a cycle has at least one block
  ref in that cycle's summary. This holds even when later records in the cycle have no block refs.
- The other `MarkDirty*` variants were checked (open question (e)):
  - `MarkDirtyLogicalChange` (:2548) and `MarkDirtyCoveredByRecordLocked` (:2428) also make the
    first-touch FPI decision.
  - `MarkDirtyWithLSN` / `MarkDirtyWithLSNLocked` (:2370, :2379) skip `maybeEmitFPI`. All 8
    production callers stamp a page that is a block ref of the record they just emitted:
    - btree split, `nbtree/btree.go:3184-3186`;
    - btree new root, `btree.go:3478-3479` and `btree_vacuum.go:1475-1476`;
    - btree mark-half-dead and unlink target, `btree_vacuum.go:644`, `:743`.
    So they are covered.
  - `MarkDirtyHint` / `MarkDirtyHintBit` (:2254, :2359) are hint-only and unlogged. That is parity.
  - `MarkDirtyChangeRecord` with `needFPI && full_page_writes` writes **only** the FPI and drops the
    change record. That is still a block ref.
- Limits:
  - An FPI emit failure is only logged (`bufpool.go:2615`), so the block is silently missed.
  - With `full_page_writes=off` (`main.go:796-803`), items 1 and 9 below become real gaps.
  - Pages written **outside the buffer pool** get no FPI at all, whatever `full_page_writes` says.
    That covers items 2 and 10-13.

Concrete divergences:

1. **`RecordKindHeapLock`.**
   - The native body is header-stamped `RM_HEAP/XLOG_HEAP_LOCK` with **no block ref**
     (`rmgr_map.go:45-46`, `format.go:165-215`).
   - It is emitted at runtime by `markHeapLockDirty` (`executor/operators_lockrows.go:2626`) via the
     `logHeapLock` hook (`initdb/open.go:683`).
   - Fix in the classifier: decode the native payload (`DecodeHeapLock`) and mark `(rel, blk)`.
     Better, convert the producer to a PG `xl_heap_lock` with a block ref.
2. **`RecordKindHeapVisible`** (kind 29).
   - Emitted on every VM change by the VM WAL hook (`initdb/open.go:2757-2764`, `recovery.go:1803`).
     The hook is installed unconditionally after the VM load. `VisibilityMap.logChange`
     (`storage/vm.go:85-96`) calls it under `mu` for every set, clear and drop.
   - **Resolved (open question (a)):** kind 29 is not in `recordKindToRmgrInfo`
     (`rmgr_map.go:34-89`). `encodeRecordXLog` → `classifyXLogRecord` (`format.go:155`) therefore
     stamps it `RmgrGoopgCatalog` (128) with info 0. It is live-emitted, which contradicts the "no
     live-emitted record reaches this arm" comment (`rmgr_map.go:80-88`). That comment is stale.
     Replay finds it by `payload[0]` (`recovery.go:2307`). The `xlogHeap2Visible` arm
     (`recovery.go:2900-2910`) is reached only by real-PG WAL.
   - The VM fork files are rewritten wholesale at every checkpoint by `VMSaveForks`
     (`storage/vm_fork.go:200`). They do not go through the buffer pool, so **VM blocks never get
     FPIs or block refs**. This gap is therefore **not** closed by `full_page_writes=on`.
     - Failure mode: a restored incremental backup gets a stale `_vm` fork whenever the prior
       backup's copy differs.
     - With the `visibility_map_wal_logged` capability, a crash start loads that fork
       (`open.go:2738-2752`). Stale all-visible bits then corrupt index-only scans.
   - For PG parity (2b), SET records (`Flags & (AllVisible|AllFrozen)`) mark:
     - VM block `HeapBlk / 32672` (`vmMaxHeapPagesPerPage = (8192-24)*4`, `vm_fork.go:36`);
     - the heap block.
   - A clear does nothing, as upstream. `HeapVisibleDropRelation` (0x08) → `SetLimitBlock(vm, 0)`.
   - The payload has no tablespace OID (`recovery.go:1803-1813`). Relations in user tablespaces
     cannot be located without a catalog lookup, so the payload needs a new `spcOid` field.
     - `DecodeHeapVisible` checks `len == heapVisibleSize` exactly. The new field therefore needs a
       length-versioned decode, so that pre-change WAL still replays.
     - Converting the producer to PG `XLOG_HEAP2_VISIBLE` (VM block 0 + heap block 1) is the
       parity fix. It removes the classifier special case.
3. **`RecordKindHeapVacuum`.** It maps to `RM_HEAP2/PRUNE_VACUUM_SCAN` with no block ref
   (`rmgr_map.go:48-49`). The hook is wired (`open.go:571`, `:793`).
   - **Resolved (open question (b)): dead.**
     - The accessor `Pool.LogHeapVacuum()` (`bufpool.go:944`) has no caller in non-test code.
     - The field `p.logHeapVacuum` is read only by that accessor.
     - `xlog.EncodeHeapVacuum` is called only from the hook closure at `open.go:572`.
     - VACUUM prunes through the `LogHeapPruneOpt` and `LogHeapVacuumCleanup` hooks instead
       (`vacuum.go:147`, `dead_items_pass.go:33`).
   - Classify it defensively anyway, for old WAL, and file the dead hook for removal.
4. **Dropped relations.** Commit records carry no relfilelocators (3).
   - Upstream would set `limit 0` for every fork. goopg gets no entry.
   - If the relfilenumber is reused and block 0 is re-extended, the `pinNewXID` SMGR_CREATE sets
     `limit 0` (`bufpool.go:1836`), so the incremental-backup outcome stays safe. The summary bytes
     still differ from PG's for the same workload.
   - Fix: emit `HAS_RELFILELOCATORS` in `EncodeXactCommitPGWithSubxacts` from the transaction's
     pending-delete list.
5. **In-place TRUNCATE** with no `XLOG_SMGR_TRUNCATE` (3).
   - PG assigns a new relfilenode and logs SMGR_CREATE (limit 0). goopg keeps the relfilenumber.
   - A limit 0 appears only when block 0 is re-extended.
   - A truncated-and-left-empty relation has size 0, and `GetFileBackupMethod` backs up zero-length
     files fully, so this case is safe.
   - The file-level parity fix (emit `SMGR_TRUNCATE` with `blkno=0`) belongs to the TRUNCATE owner.
     The summarizer only needs to classify the record.
6. **Hard-coded `wal_level=1` in REDO *and* in every `CheckPoint` record** (see 3).
   - **Resolved (open question (d)):** goopg accepts `wal_level=minimal` (see 4.2), but WAL content
     does not change. goopg has no PG-style skip-WAL-for-new-relfilenode optimisation, so a
     summary over "minimal" WAL is still complete. Claiming `replica` is safe today, only
     unfaithful.
   - The startup refusal in 4.2 keeps `summarize_wal` and `minimal` apart.
   - If goopg ever implements minimal's WAL skipping, the REDO and checkpoint payloads must carry
     the live GUC, and goopg must emit `XLOG_PARAMETER_CHANGE` on a `wal_level` change at restart.
     Otherwise `fast_forward` never engages.
7. **No `XLOG_END_OF_RECOVERY` / end-of-recovery shutdown checkpoint at promotion.**
   - Upstream, the new timeline's first summary starts at `switch_lsn` with a boundary record.
   - goopg's first boundary on the new TLI is the first `CHECKPOINT_REDO`. Any record before it is
     fast-forwarded and **no summary is written** for `[switch_lsn, first redo)`.
   - Incremental backups spanning the promotion then fail `WalSummariesAreComplete`.
   - Fix: emit `XLOG_END_OF_RECOVERY` as the first record on the new TLI.
8. **FSM** is ignored, as upstream. **Hint-bit-only** changes are unlogged upstream too. Both are
   parity, not risks.
9. **`MarkDirtyUnlogged` sites** (MultiXact lock stamps, in-place `pg_database` writes,
   `bufpool.go:2316-2329`; `MarkDirtyUnlogged` at :2327) still pass through `maybeEmitFPI`. They are covered only when
   `full_page_writes=on`.
10. **REINDEX CONCURRENTLY swaps the index file with no WAL** (added in review).
    - `rebuildIndexConcurrently` (`executor/operators_reindex.go:323-350`, also :405) builds a
      shadow relfile. It then calls `swapRelationPhysicalFile` (`operators_ddl.go:692-714`), which
      does `os.Rename(shadowPath, oldPath)` and keeps the index's relfilenode.
    - The shadow's SMGR_CREATE and FPIs name the **shadow** locator. Nothing in WAL names the old
      index's locator, so its summary entry gets no `limit 0`.
    - An incremental backup then sends only the blocks touched afterwards. `pg_combinebackup`
      takes every other block from the prior backup's **pre-rebuild** index, which gives a corrupt
      index after restore.
    - PG builds a new relfilenode and swaps catalog entries, so the new file is
      SMGR_CREATE'd (limit 0).
    - Fix: emit `XLOG_SMGR_CREATE` for the old index's locator at the swap. Better, swap
      relfilenodes in the catalog as upstream does.
11. **ALTER TABLE/INDEX … SET TABLESPACE copies with no WAL** (added in review).
    - `relocateRelationPhysicalFile` (`operators_ddl.go:609-650`; callers :9127, :11089)
      byte-copies the main fork. `newRel` differs from `oldRel` only in `TblOid`, so the relfilenode
      is unchanged. No SMGR_CREATE and no FPIs are written.
    - A single move is safe: the new path is absent from the prior manifest, so it is backed up in
      full (`basebackup_incremental.c:703-733`).
    - A move away and back to the original tablespace inside one incremental window is not.
      Modifications made while the relation lived in tablespace B are recorded under B's locator.
      The file back at A's path is sent incrementally against A's stale prior copy.
    - PG assigns a new relfilenode, logs SMGR_CREATE, and FPIs every copied block.
    - Fix: SMGR_CREATE for `newRel` at relocation. Per-block FPIs are also needed for standby
      parity.
12. **Unlogged VACUUM tail truncation** (see 3; `vacuum.go:368` → `bufpool.go:1433-1450`; the
    `LogSmgrTruncateTo` hook is unwired in production) (added in review).
    - Incremental-backup outcome: safe. The shorter file sets `truncation_block_length`, and later
      re-extension gets first-touch FPIs or block-ref'd inserts.
    - Byte parity: PG would `SetLimitBlock(main, keep)` and `SetLimitBlock(vm, keep)`.
    - It is also a crash-recovery / standby divergence in its own right.
    - Fix belongs to the VACUUM owner: wire the hook to a PG `xl_smgr_truncate`.
13. **ROLLBACK of TRUNCATE rewrites the file directly** (added in review).
    - `restoreTruncateUndo` (`operators_ddl.go:17102-17121`) does `mgr.TruncateRelation` and then
      `mgr.Extend` with the pre-TRUNCATE page snapshots, all with no WAL.
    - The restored bytes equal pool content that earlier records already covered, so the summary
      is not missing anything. Low risk, listed for completeness.
    - It is unlogged for crash recovery (TRUNCATE owner).
- Checked and safe (open question (e)):
  - Bulk extension (`ExtendRelationBatchWithXID`; the pages are only touched later through
    `Pin` + `MarkDirty*`).
  - `pinNewXID`'s direct `mgr.Extend` (block 0 → SMGR_CREATE; later blocks re-arm the first-touch
    image).
  - CREATE DATABASE file copies (covered by the `{spc, db, 0}` entry).
  - `seq_log.go:104` direct `WriteBlock` (XLOG_SEQ_LOG carries the block ref).
  - B-tree build/REINDEX (in-place truncate, then `PinNew` block 0 → SMGR_CREATE).
  - `initdb/open.go:3244` `appendCatalogRows` (it has no caller).

**Guard until items 2, 7, 10 and 11 land** (plus 1 and 9 when `full_page_writes=off`):
- Log a WARNING at summarizer start when `full_page_writes=off`.
- Gate the incremental-backup follow-on (`pg_basebackup --incremental`, a non-goal of this task,
  see 6) on items 2, 7, 10 and 11, and on 1 and 9 unless `full_page_writes=off` is refused for
  incremental backup.
- S7 (WS-002) is not gated on them: WS-002 only reads summaries.

### 4.5 SQL functions

- `pg_available_wal_summaries`: replace the EOF stub with `walsummary.List(0,0,0)` (directory order).
  Rows are listed whether or not `summarize_wal` is on.
- `pg_wal_summary_contents(tli, start, end)`:
  - Add a planner arm next to `planner.go:7061` and a plan node and operator, following
    `PgAvailableWalSummaries`.
  - It streams `blkreftable.Reader` and emits the limit row first, then block rows.
  - It uses PG's error texts and SQLSTATEs:
    - `invalid timeline %lld` is 22023 (walsummaryfuncs.c:92-95);
    - `could not open file "%s": %m` uses `errcode_for_file_access`, so 58P01 for ENOENT
      (walsummary.c:217-220);
    - the reader's malformed-file errors all go through `ReportWalSummaryError` (walsummary.c:322)
      as `ERRCODE_DATA_CORRUPTED` XX001: `file "%s" has wrong magic number: expected %u, found %u`,
      `file "%s" has wrong checksum: expected %08X, found %08X`, and `file "%s" ends unexpectedly`
      (blkreftable.c:597-601, :652-655, :1233-1236, :1248-1251).
- `pg_get_wal_summarizer_state()`: composite non-SRF from `State`, usable in both FROM and the
  target list.
- Optional and cheap: `pg_ls_summariesdir()`.

### 4.6 `wal_summary_keep_time`

- Register the GUC in `defaults.go`: TypeInt, UnitMin, BootVal 14400, Min 0, Max 35791394,
  ContextSigHup.
- Port `MaybeRemoveOldWalSummaries` exactly: once per redo advance (`Checkpointer.LastCheckpointRedoLSN`,
  `checkpointer.go:392`), with the per-TLI oldest-segment rule and the mtime cutoff.

## 5. Slices (landable one at a time in the order S1 → S2 → {S3, S4a} → {S4b, S5} → S6a → S7; S6b..S6f are independent producer changes)

1. **S1 `blkreftable` package.** In-memory table, writer, streaming reader and incremental writer.
   - Golden tests with hand-derived bytes:
     - empty table: magic + 24 zero bytes + CRC;
     - one entry created only by a limit;
     - array chunk keeping insertion order with dedup;
     - conversion to bitmap at the 4096th distinct offset;
     - limit inside a bitmap chunk (an all-zero bitmap is still written);
     - limit trimming trailing chunks but keeping middle zero chunks;
     - limit inside an array chunk (filtered in place, order kept);
     - a chunk dropped by a limit and then re-marked, which restarts as an array even if it was a
       bitmap;
     - `SetLimitBlock` with a value ≥ the current limit is a no-op;
     - sort order across spc, db, rel and fork.
   - Cross-check: write files and run PG's `pg_walsummary` and `pg_walsummary -i` via the testport
     `clientToolBin`. It validates magic and CRC and prints the expected lines.
2. **S2 `walsummary` files plus the offline summarizer core.**
   - Covers name format/parse, list/filter/complete, temp+rename, classifier (4.3) and the
     `SummarizeWAL` port over a record source.
   - Oracle A: run a scratch PG 18.3 (private port, `summarize_wal=on`, never a reference cluster).
     Capture `pg_wal/` and `pg_wal/summaries/`. Run goopg's offline summarizer over that WAL and
     require **byte-identical** summary files and names.
     - Seed the offline run with PG's first file's `start_lsn` as an exact start, or compare only
       the files after the first boundary.
     - Pin `wal_compression=off` on the scratch PG: goopg's decoder rejects compressed FPIs.
     - Include a user-tablespace relation, so the raw-`spcOid` path is exercised.
   - Oracle B: over goopg-produced WAL, every block ref shown by `pg_waldump` within a summary range
     appears in that summary.
3. **S3 live summarizer.**
   - Adds the goroutine, start/stop/SIGHUP reconcile, flush-bounded reading, `FindNextRecord`, the
     restart position from files, `State`/`WaitFor`, the retention clamp, the `wal_level=minimal`
     startup error, and the `pg_stat_io` reads counter.
   - Test: cluster with `summarize_wal=on`, `CHECKPOINT` ×2. Expect contiguous files (end of one =
     start of next), each boundary equal to a `CHECKPOINT_REDO` ReadRecPtr. Restart and expect
     continuation with no gap. A reload with `off` stops the summarizer.
4. **S4 SQL functions.** `pg_available_wal_summaries` (real), `pg_wal_summary_contents`,
   `pg_get_wal_summarizer_state`.
   - **S4a** is the first two functions. They depend only on S1 + S2 and may land before S3.
   - **S4b** is `pg_get_wal_summarizer_state`, which needs S3's `State`.
   - Tests: output matches `pg_walsummary` for the same file; the error texts; the `(0,0/0,0/0,NULL)`
     state when off.
5. **S5 `wal_summary_keep_time`.** GUC registration (SHOW/SET parity) and removal. Removal runs in
   the summarizer loop, so this slice depends on S3. The GUC registration alone could land
   earlier.
   - Test: backdate a file's mtime, remove the WAL beneath it, run a checkpoint, and expect the file
     to be gone. `keep_time=0` keeps it.
6. **S6 divergence closure (4.4).** This is not one landable slice. Split it:
   - **S6a** (summarizer-local, after S2): classifier arms for the native HeapLock, HeapVisible and
     HeapVacuum payloads.
   - **S6b..S6f** (each a WAL-producer change with its own replay/compat impact and owner, landable
     independently):
     - HeapVisible `spcOid` with a length-versioned decode, or conversion to PG `XLOG_HEAP2_VISIBLE`;
     - `HAS_RELFILELOCATORS` in commit/abort;
     - `END_OF_RECOVERY` at promotion;
     - SMGR_CREATE (or a relfilenode swap) at the REINDEX CONCURRENTLY swap (item 10);
     - SMGR_CREATE at SET TABLESPACE (item 11).
   - Test: workload of SELECT FOR UPDATE + VACUUM (VM set) + DROP + REINDEX CONCURRENTLY + SET
     TABLESPACE away and back + promotion, with `full_page_writes=off`. The summary must include
     every `(rel, fork, blk)` whose on-disk bytes changed between two checkpoints (page-diff
     oracle).
     - The oracle must mask hint-bit-only differences and skip the FSM, because both are unlogged
       upstream too.
     - A relation whose limit block ≤ blk counts as covered.
7. **S7 WS-002 un-defer.**
   - Remove the deferral in `TestPort_PgWalsummary002Blocks` and port `002_blocks.pl` faithfully,
     including the 2-line `pg_walsummary -i` assertion.
   - Prerequisites owned elsewhere: `pg_current_wal_insert_lsn` (still unimplemented, see 3),
     `generate_series`, `VACUUM FREEZE` parse. In this task it depends on S3 (the `pg_stat_io`
     counter) and S4.
   - Risk to the "exactly 2 blocks" assertion: any goopg-only side write between the UPDATE and
     the CHECKPOINT's REDO adds lines to that summary. Examples are a catalog heap re-sync, a
     stats/relallvisible write, or autovacuum. In PG only `mytable` block 0 and the new-tuple
     block appear; the VM clear is not a block ref and HeapVisible clears must not be marked
     (4.4 item 2). Run the port with autovacuum off in the test's own conf only if upstream's
     cluster does the same; otherwise investigate any extra block as a parity bug.
   - Flip WS-002's inventory rationale.

## 6. Non-goals, deferred work and risks

**Non-goals and deferred work:**
- `pg_basebackup --incremental`, `UPLOAD_MANIFEST` and `pg_combinebackup` TAP rows 001–011. These are
  a follow-on task, gated on S6 (4.4 items 2, 7, 10 and 11 at minimum). `WaitFor` and
  `walsummary.AreComplete` are their API.
- Summarization on a hot standby, and the `.partial`-rename wait at promotion.
- `wal_level=minimal` support.

**Risks:**
- **Silent incompleteness.** A missed block makes an incremental backup restore stale pages with no
  error. 4.4 is the risk register. Do not advertise incremental backup before S6.
  - Coverage of everything except items 2 and 10-13 rests on first-touch FPIs. Any future
    optimisation that suppresses FPIs (for example a hint-only or "known-clean" fast path) or that
    writes pages outside the pool silently re-opens the gap.
  - Oracle B plus the S6 page-diff test should run in the nightly lane.
- **LSN off-by-one.** goopg's StartLSN is 1-based. An error shifts every file name and breaks
  `AreComplete`. Mitigation: one translation helper, plus the page-boundary golden tests and Oracle A.
- **Summarizing unflushed WAL.** If the summarizer reads past the flush LSN and the server crashes,
  the files can claim LSN ranges whose WAL is rewritten later. The flush bound is mandatory.
- **WAL removal racing the summarizer.** Without the retention clamp, `RemoveOldSegments` can delete
  segments that have not been summarized. The summarizer then errors "could not find a valid record"
  and loops on the 10 s retry.
- **CPU.** The full FPI decode per record is wasteful. A block-header-only walk in the summarizer is
  an optimization for after S3.
- **Byte-identity details.** Array chunks keep insertion order, all-zero bitmaps are kept, and middle
  zero chunks are kept. A "clean" Go rewrite that sorts or compacts any of these would still be read
  correctly by `pg_walsummary`, but would fail Oracle A's byte equality. Keep the port literal.

## S1 landed (2026-10-13)

`internal/access/common/blkreftable` is a port of `common/blkreftable.c`.

- **Types and operations:**
  - `Table`, `Entry` with `MarkBlockModified` / `SetLimitBlock` / `Blocks`.
  - The whole-table `Write` (`WriteBlockRefTable`).
  - The entry-at-a-time `Writer` (`CreateBlockRefTableWriter` / `BlockRefTableWriteEntry`).
  - The streaming `Reader` (`CreateBlockRefTableReader` / `BlockRefTableReaderNextRelation` /
    `BlockRefTableReaderGetBlocks`), with PG's three error messages wrapped in `ErrCorrupt`.
- **In-memory layout:** chunk usage and data follow PG.
  - `nchunks` is the used length rather than PG's power-of-two allocation. Only the serialized count
    is visible, and both trim trailing zero-usage chunks.
  - Array chunks keep insertion order.
  - A dropped chunk (usage 0) restarts as an array.
- **Oracle:** better than the hand-derived bytes §5 planned. `testdata/brt_harness.c` links PG
  18.3's own `blkreftable.c` from `postgres/local_install/lib/libpgcommon.a`. It replays each
  `testdata/*.script` and writes PG's serialization (`*.brt`). PG's `pg_walsummary` output for
  each file is `*.txt`.
- **Tests:**
  - **`TestWriteMatchesPostgres`:** both writers must be byte-identical to PG on 13 cases. They
    cover:
    - array order and dedup;
    - the bitmap switch at the 4096th offset;
    - limits inside an array, inside a bitmap, and at block 0 (an all-zero bitmap is still
      written);
    - trailing-chunk trimming with a kept middle zero chunk;
    - a dropped bitmap chunk restarting as an array;
    - a limit that may not rise;
    - the tablespace-first sort;
    - a 6000-operation random mix.
  - **`TestReaderMatchesPgWalsummary`:** reading PG's files reproduces `pg_walsummary`'s text.
    The batch size is 7, so the test also resumes reading in the middle of a chunk.
  - **`TestPgWalsummaryReadsGoopgFile`:** PG's binary reads goopg-written files.
  - **`TestReaderRejectsCorruptFiles`:** wrong magic, a bad checksum and a truncated file.
- **Non-vacuity:** two mutations each fail the byte test, sorting array chunks and sorting by
  database before tablespace.
- **Not yet ported:** `BlockRefTableReaderGetBlocks`' fixed-size `chunk_data` has no bound check
  in PG. The Go reader rejects a chunk size above 4096 as corrupt instead of overflowing.

## S2a landed (2026-10-14)

S2 is split. S2a is `internal/backup/walsummary`, a port of `backup/walsummary.c` plus SummarizeWAL's file write.
S2b is the classifier and the offline `SummarizeWAL` core.

- **What S2a contains:**
  - `File.Name` / `ParseFilename` / `IsFilename` (exactly 40 uppercase hex digits);
  - `List` (GetWalSummaries' exclusive bounds) and `Filter` (FilterWalSummaries' inclusive bounds);
  - `AreComplete`, `Open`/`Read`, and `RemoveIfOlderThan` (whole-second mtime);
  - `Write`: temp.summary, then durable\_rename's fsync / rename / fsync / directory fsync.
  - `List` returns name order where PG returns readdir order; no caller relies on the order.
- **Oracle:** nine summary files written by a scratch PG 18.3 with `summarize_wal=on`, in `testdata/pg18-summaries`.
  - `List` reproduces that server's `pg_available_wal_summaries()`.
  - Rebuilding each table and writing it back with `Write` is byte-identical to PG's file, under PG's name.
  - PG's `pg_walsummary` prints the same output for goopg's file and PG's.
- **Non-vacuity:** giving `Filter` List's predicate fails the boundary test.
- **Next:** S2b, the record classifier (§4.3) and the `SummarizeWAL` port over a record source. The scratch PG data
  in `tmp/walsum-pg` (port 5547) provides its WAL oracle.

## Review

The agent review (milestone 0122 Workflow step 4) is in
[0122-0009-wal-summarizer-review.md](0122-0009-wal-summarizer-review.md). Verdict: approved for S1.
