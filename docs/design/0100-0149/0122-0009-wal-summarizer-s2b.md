# M0122-0009 — WAL summarizer S2b: record classifier and offline SummarizeWAL

Status: landed 2026-10-14. This continues [0122-0009-wal-summarizer.md](0122-0009-wal-summarizer.md), which holds
the design (§4.3 classifier, §5 slice plan) and the S1 and S2a notes.

## Change

`internal/postmaster/walsummarizer` (`summarize.go`) ports `walsummarizer.c`'s record handling and the
`SummarizeWAL` loop. It runs offline over a list of decoded `xlog.Record`s.

- **Boundaries** (`SummarizeXlogRecord`): CHECKPOINT\_REDO, CHECKPOINT\_SHUTDOWN, PARAMETER\_CHANGE and
  END\_OF\_RECOVERY end a summary at the record's ReadRecPtr, unless it is the summary's first record.
  - In that case the record sets fast-forward when its `wal_level` (at offset 0, 20, 20 or 16) is minimal.
  - Fast-forwarded ranges write no file.
- **SMGR:** CREATE sets limit 0, except for the FSM. TRUNCATE sets the heap limit and/or the VM limit, both at the
  heap block, as PG does.
- **DBASE:** CREATE\_FILE\_COPY, CREATE\_WAL\_LOG and DROP set limit 0 on the `{spc, db, 0}` sentinel.
- **XACT:** a commit or abort, including the prepared variants, sets limit 0 on every fork except the FSM of each
  dropped relation.
  - `xlog.ParseXactRecord` now decodes the HAS\_RELFILELOCATORS chunk into `DroppedRels`.
  - The chunk follows the subxacts chunk in both record shapes.
- **Block references:** every non-FSM block reference marks its block, unless fast-forwarding.
  - goopg's decoder folds the built-in tablespaces to TblOid 0. `blockLocator` rebuilds 1664 when dbOid is 0, and
    1663 otherwise.
- **LSN translation:** in one place. goopg's StartLSN is ReadRecPtr+1, and EndLSN is EndRecPtr.
- **Ranges:** `Summarize` chains exact-start ranges. Records after the last boundary stay unsummarized, because
  the live summarizer would wait for more WAL.

## Evidence

**Oracle A** (`TestSummarizeMatchesPostgres`) starts a private PG 18.3 from `postgres/local_install` with
`summarize_wal=on` and runs a workload that reaches every arm:

- a user tablespace;
- INSERT / UPDATE / DELETE;
- a truncating VACUUM;
- a dropped table and a rolled-back CREATE;
- CREATE / DROP DATABASE … STRATEGY FILE\_COPY.

goopg decodes that WAL and summarizes it from PG's first summary. All 14 of PG's files come out with the same
names and the same bytes. The test skips when PG is not installed or under `-short`. It takes about 1 s.

The WAL is generated at test time rather than committed: initdb's own WAL makes the segment about 1.6 MB even
gzipped.

**Other tests:**
- `TestClassifierArms` covers each arm on hand-built records, including tablespace folding and the FSM exclusion.
- `TestFastForwardAndOpenRange` covers `wal_level=minimal` fast-forward and the open trailing range.

**Non-vacuity:**
- Dropping the `-1` from `readRecPtr` fails all three tests.
- Disabling the xact arm fails the classifier test and Oracle A.

## Not yet ported (ledgered)

- **Reading from an arbitrary point** (`exact=false` / `XLogFindNextRecord`), the timeline `switch_lsn`, the
  `maximum_lsn` flush bound and the historic end-of-WAL cases. These belong to the live loop (S3).
- **goopg-native records** were passed over here. S6a (below) classifies them.

## S4a landed (2026-10-14): `pg_available_wal_summaries` and `pg_wal_summary_contents`

These are ports of `backup/walsummaryfuncs.c` over `internal/backup/walsummary`.

- **`pg_available_wal_summaries()`** used to return no rows. It now lists `pg_wal/summaries`
  (`GetWalSummaries(0, Invalid, Invalid)`).
  - The analyzer had no column arm for it, so a reference to one of its columns (`ORDER BY start_lsn`) failed
    with 42703. The empty stub hid this.
- **`pg_wal_summary_contents(tli, start_lsn, end_lsn)`** is a new FROM-clause SRF.
  - It is a `PgWalSummaryContents` plan node. Its arguments may be correlated, so the lateral binding follows
    `PgGetSequenceData`.
  - Per relation fork it emits the limit-block row (`is_limit_block = true`) first, then the modified blocks in
    the file's stored order.
  - It is strict.
  - **Errors, with PG's text and position:**
    - `invalid timeline N` (22023);
    - the missing file, with PG's relative path (58P01);
    - corruption (XX001);
    - bad `pg_lsn` / bigint input (22P02), with the caret on the argument.

**Evidence:**
- The nine committed PG summary files were installed in a goopg cluster, and the same script ran against the
  scratch PG 18.3 that wrote them. The output is byte-identical, error messages and SQLSTATEs included.
  - The script covers the ordered listing, two files' contents in natural order, a lateral per-file count, and the
    error cases.
- `TestWalSummaryFunctionsMatchPostgres` (`internal/executor`) pins the same comparison against
  `testdata/wal_summary_funcs.golden`. PG captured that file with `summarize_wal=off`, so it saw exactly the
  committed files.
- Non-vacuity: dropping the limit-block row fails the test.
- The fire set shows no changed plan at SF0.25 or SF1, and the TPC-H acceptance arm is value-identical.

**Not ported:** `pg_get_wal_summarizer_state()` (S4b) needs the live summarizer's state (S3).

## S3a landed (2026-10-14): the live summarizer core

S3 is split. S3a is the summarizer that runs over a live WAL stream. S3b wires it into the server.

- **One implementation.** `rangeSummarizer` is a streaming port of one `SummarizeWAL` call: `add` consumes a record
  and reports when a boundary ends the range; `finish` writes the file.
  - The offline `Summarize` and the live `Summarizer.Run` both drive it.
  - Oracle A (PG's own summaries) therefore checks the code the live loop runs.
- **`Summarizer.Run(ctx, src, start)`** consumes a `Source` from an exact start and writes a summary per range.
  - It publishes `State` (`SummarizedTLI` / `SummarizedLSN` / `LSNIsExact` / `PendingLSN` / `Running`, as in
    `WalSummarizerData`) and closes a `Changed` channel per summary, for a future `WaitFor`.
  - It returns nil when its context is cancelled.
- **`ResumePoint`** is the exact-start half of `GetOldestUnsummarizedLSN`: the highest end among the summaries on
  disk for the timeline.
- **Flush bound.**
  - `xlog.Writer.FlushedLSN()` is new.
  - `RecordIterator.SetReadLimit` caps `Next`, so no record ending past the limit is returned, even when written.
  - With the limit set to `FlushedLSN`, the summarizer never summarizes WAL a crash could still rewrite. That was
    the design's "summarizing unflushed WAL" risk.
  - The writer wakes subscribers only on append, so the S3b loop must re-poll on a timer while flushes catch up.

**Evidence:**
- `TestSummarizeMatchesPostgres` now also runs the live `Summarizer` over PG's WAL. It stops halfway, restarts
  from `ResumePoint`, and runs to the end.
  - The files equal the offline ones, which equal PG's, byte for byte.
  - `AreComplete` finds no gap across the restart.
- `TestRecordIteratorReadLimit` checks that a written record past the limit, even by one byte, is held back and
  then released when the limit passes it.

**Next, S3b:**
- start and stop on `summarize_wal` in `Server.Run`, with reload reconcile;
- the iterator source with the flush bound and a timed re-poll;
- the no-summaries start (PG's `XLogFindNextRecord` from the oldest segment);
- the retention clamp, the `wal_level=minimal` startup error, and the `pg_stat_io` reads counter.

## S3b landed (2026-10-14): the summarizer runs in the server

- **`walsummarizer.Service`** is the `WalSummarizerMain` analog.
  - `Server.Run` reconciles it with `summarize_wal` at start, `reloadConfig` reconciles it on every reload (the
    GUC is PGC\_SIGHUP), and it stops at shutdown.
  - After an error it waits 10 s and resumes from the files, as `WalSummarizerMain` does.
- **Start position:**
  - `ResumePoint` (exact) when summaries exist.
  - Otherwise the latest redo point: the checkpointer's (stored 1-based) or, before this run's first
    checkpoint, pg\_control's `CheckPointCopyRedo`.
  - On a fresh cluster the first summary starts at 0/1000028, where PG's starts.
- **Reading:** an `xlog.RecordIterator` with `SetReadLimit(Writer.FlushedLSN)`. A `pollSource` re-polls every
  200 ms, PG's sleep quantum, because the writer wakes readers only on append.
- **Retention:** `SlotAwareRetainer.KeepLSNFn = Service.KeepLSN`, the summarized LSN. No segment is removed before
  it is summarized (`KeepLogSeg`'s summarize\_wal clamp).
- **`wal_level=minimal`:** with `summarize_wal` on, `goopg start` refuses with PG's text, `WAL cannot be summarized
  when "wal_level" is "minimal"` (postmaster.c).
- goopg's checkpointer already writes `XLOG_CHECKPOINT_REDO` (wal\_level replica) on online checkpoints, so live
  summaries close at every CHECKPOINT, as in PG.

**Evidence:** `TestServerSummarizesWAL` (a real goopg server via `testutil/cluster`):
- a summary lists the new table's blocks;
- `summarize_wal = off` plus a reload writes nothing across a CHECKPOINT;
- `on` resumes;
- a restart continues;
- `AreComplete` finds no gap over the whole set.

**Not yet ported (ledgered):**
- With no summaries, PG starts at the oldest WAL on disk and searches forward (`XLogFindNextRecord`). goopg starts
  at the latest redo point.
- goopg opens its runtime before refusing `wal_level=minimal`, so one checkpoint is written; PG refuses before
  startup.
- `pg_stat_io`'s walsummarizer reads, the walsummarizer `pg_stat_activity` row, and `pg_get_wal_summarizer_state`
  (S4b) remain.
- goopg logs no `XLOG_SMGR_CREATE` for a new table, so its summary carries blocks but no limit row. This is a
  producer gap (design §4.4, S6).

## S4b landed (2026-10-14): `pg_get_wal_summarizer_state()`

- **Planner:** the zero-argument `PgAvailableWalSummaries` node gains a `Func` field, which also plans
  `pg_get_wal_summarizer_state()` as a one-row FROM-clause SRF. goopg has no record-valued builtins, so PG's TAP
  form `SELECT ... FROM pg_get_wal_summarizer_state()` is the supported shape.
- **Executor:** it reads `executor.Context.WalSummarizerState`, set at both dispatch sites from
  `Server.walSummarizerStatus`, a view of `walsummarizer.Service.State`.
- **Values:** these follow `GetWalSummarizerState`, as checked on a fresh scratch PG 18.3:

  | State | Row |
  |---|---|
  | Never ran | `0, 0/0, 0/0, NULL` |
  | Running | the timeline, both LSNs, a pid |
  | Stopped | `pending_lsn = summarized_lsn` and a NULL pid |

  - goopg runs the summarizer as a goroutine, so the pid is the server's.

**Evidence:**
- `TestServerSummarizerState` drives the same off → on → off sequence on a real goopg server.
- The fire set shows no changed plan at either scale. The TPC-H arm matches the m0190 values.

## S5 landed (2026-10-14): `wal_summary_keep_time`

- **The GUC** is registered as `guc_tables.c` defines it: minutes, default 14400 (SHOW `10d`), range 0..INT\_MAX/60,
  PGC\_SIGHUP. Checked against the scratch PG's `pg_settings`.
- **`walsummary.RemoveOld`** ports `MaybeRemoveOldWalSummaries`. Per timeline, a summary is removed only when both
  hold:
  - its WAL is gone: its end is at or before the start of the oldest segment on disk (`XLogGetOldestSegno`), or no
    segment is left for that timeline;
  - its mtime precedes now − keep\_time.

  keep\_time 0 disables removal. `walsummary` stays a leaf: the oldest-segment lookup is passed in, and the
  `pg_wal` scan, `oldestSegmentLSN`, lives in `walsummarizer`.
- **When it runs:** the `Service` calls it after each summarized range, through `Summarizer.AfterSummary`.
  - It runs at most once per redo-pointer value (`redo_pointer_at_last_summary_removal`), as in PG.
  - `cmd/goopg` reads `wal_summary_keep_time` from the live registry, so a reload applies.

**Tests:**
- `TestRemoveOld` over PG's nine files: removes only what both conditions allow, removes nothing when keep is 0,
  and removes every old-enough file when no WAL is left.
- `TestOldestSegmentLSN`.
- `TestWalSummaryKeepTimeGUC`.

## Activity row and `pg_stat_io` reads (2026-10-14)

- **`pg_stat_activity`:** while the summarizer goroutine runs, it holds the background slot
  `activity.WalSummarizerIdx` (4), with backend\_type `walsummarizer`. The hooks are
  `ServiceConfig.OnStart`/`OnStop`, wired in `cmd/goopg` as the bgwriter's slot is.
  - Like goopg's other background rows, its pid prints NULL.
- **`pg_stat_io`:** the `(walsummarizer, wal, normal)` reads / read\_bytes cells count the 8 kB WAL pages the
  record stream enters (`IOCounters.countPages`).
  - PG counts each XLOG\_BLCKSZ page `WALRead` fetches.
  - The counters live on the `Service`, so they stay cumulative across summarizer restarts, like pg\_stat
    counters.
  - They reach the executor through `WalSummarizerStatus.WALReads` / `WALReadBytes`.
  - WS-002 polls `sum(reads) > 0` here before it trusts the summaries.

**Tests:**
- `TestServerSummarizerState` now checks one `walsummarizer` activity row while running and none after stopping,
  and `reads > 0` with `read_bytes = reads * 8192`.
- `TestIOCountersCountEachPageOnce`.

## S6a landed (2026-10-14): classifier arms for goopg-native records

- **Correction to the plan.** A native record is not header-less. The writer stamps it with its PG analogue's
  `(rmid, info)` (`recordKindToRmgrInfo`), or `RmgrGoopgCatalog`/0 when it has none. Its main data is the native
  payload, and it has **no block reference**. `Record.XLog == nil` occurs only in legacy bare-record streams.
- **`summarizeNative`** runs for a record with no block reference whose header matches its kind byte
  (`xlog.IsGoopgNativeRecord`). It also runs for a header-less legacy record. It marks what PG's block-ref'd
  equivalent would:

  | native kind | PG equivalent | marked |
  |---|---|---|
  | HeapLock (RM\_HEAP / LOCK) | `xl_heap_lock`, block 0 = heap page | the heap page |
  | HeapVacuum (RM\_HEAP2 / PRUNE\_VACUUM\_SCAN) | prune record, block 0 = heap page | the heap page (no live producer; old WAL) |
  | HeapVisible set (rmgr 128) | `XLOG_HEAP2_VISIBLE`, block 0 = VM page, block 1 = heap page | VM page `HeapBlk / 32672` and the heap page |
  | HeapVisible clear | none: PG clears the map inside the heap record, without a VM block reference | nothing |
  | HeapVisible drop relation | the VM fork's `SMGR_TRUNCATE` / drop | VM fork limit block 0 |

- A real PG record of the same `(rmid, info)` always has a block reference, so it is never read as a native
  payload, even when its first main-data byte equals a native kind.
- **Tablespace.** The payloads carry `TblOid` 0, so the locator is pg\_default, or pg\_global for a shared
  relation. Every VM fork is there: `VMSaveForks` writes only under `base/` and `global/`. A heap-page mark for a
  relation in a user tablespace lands under the pg\_default locator. That page also has a first-touch FPI when
  `full_page_writes` is on. The `spcOid` field, or conversion to `XLOG_HEAP2_VISIBLE`, stays a producer slice
  (S6b).

**Tests:**
- `TestNativeRecordArms` writes the native payloads through the real `xlog.Writer` and reads them back with
  `ReadAll`, so the on-disk header stamping is exercised. It checks the entries above, and that a PG heap-lock
  record whose main data starts with the HeapLock kind byte is classified by its block reference alone.
- `TestServerSummarizesWAL` now runs `VACUUM ws` and expects VM block 0 of `ws` in a summary.
- Negative control: with the arm disabled, both tests fail. The live one reports "no summary lists VM block 0 of
  ws after VACUUM".
