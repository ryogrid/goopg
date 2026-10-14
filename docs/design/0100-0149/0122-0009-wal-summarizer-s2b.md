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
- **goopg-native records with no XLogRecord header** (`Record.XLog == nil`) are passed over. This is S6a (design
  §4.4): HeapLock, HeapVisible and HeapVacuum payloads carry modifications the summary must see.

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
