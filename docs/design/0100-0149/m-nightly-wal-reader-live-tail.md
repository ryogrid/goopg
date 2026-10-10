# M-NIGHTLY tpcds/stage-startup-20261007 — the WAL reader stops at pg_wal's preallocated tail

Status: landed 2026-10-10 (fd694e4f2).

## Problem

The 2026-10-07 nightly TPC-DS stage failed with `server not ready in 120 s`.

- **Where startup spent its time.** The goroutine dump showed startup
  recovery in `xlog/recovery_cache.go` → `xlog/reader.go`, inside
  `os.ReadFile` of WAL segments.
- **How long it took.** The listener bound five minutes after start. The
  window overlapped FORCE=1 gates that were copying multi-GB clones.
- **Normal timing.** On a quiet host the same start takes 7–9 s. The stage
  passed in the three nightlies since.

## Cause

`readStreamFrom` (xlog/reader.go) read every segment file from the first
retained one until ENOENT.

- **What pg_wal holds.** The SF1 cluster's pg_wal has one live segment
  (`…02DF`, holding the shutdown checkpoint) and 70 zero-filled
  preallocated segments (`…02E0`–`…0325`).
- **The cost.** Every start read 1.1 GB of zeros into memory. The
  memoized startup decode then walked it, before the page walk stopped at
  the first zero header.
- **Under contention,** that read alone took minutes.

## PG behaviour

The WAL reader validates each page header against the address it is read
from (`XLogReaderValidatePageHeader`, xlogreader.c:1250-1370). It checks:

- the magic;
- a long header at a segment start;
- `xlp_seg_size`;
- `xlp_pageaddr`.

A failure ends the read, so PG never touches the preallocated or recycled
tail of pg_wal.

## Change

- **`lastLiveSegment`.** It probes only the 40-byte first-page header of
  each file from `firstSegNo` onwards, using the existing
  `xlogPageValidator` (M0131-S19). It returns the last segment whose
  header is valid for its own address.
- **`readStreamFrom`** reads through that segment and stops.
- **Holes are still read.** Invalid segments before a live one are read in
  full, so `durableWALAfter`'s check for lost committed WAL behind a hole
  sees exactly the stream it saw before. Only the trailing tail is
  dropped.
- **Where the old behaviour remains.** Reading until ENOENT still happens
  when the probe cannot judge:
  - segments smaller than a long header (unit fixtures);
  - a first segment whose own header is invalid.

## Verification

- **Tests.**
  - `TestReadStreamStopsAtPreallocatedTail`: two live segments, then a
    zero-filled tail and a recycled segment holding stale WAL. Only the two
    live segments are read, and every payload replays.
  - `TestReadStreamReadsThroughAMidStreamHole`: a zero-filled middle segment
    followed by a live one is read through.
- **Private SF1 clone,** two clean starts per binary:

  | binary | time to listener |
  |---|---|
  | HEAD | 9.4 s, 7.1 s |
  | candidate | 1.2 s, 1.2 s |

  Row counts check out. A table created after one candidate start survives
  the next restart.
- **Gates.**
  - units and the xlog package;
  - the testport restart/crash/recovery subset (`…SurvivesRestart`,
    `P0E4…`, crash tests);
  - TPC-H spotcheck and acceptance arm (values);
  - TPC-DS fire set (no fires) and SF0.25 sweep (PASS=99, plans
    unchanged).

## Not covered (ledgered)

- **Where reading starts.** PG starts at the checkpoint's REDO pointer,
  not at the first retained segment. goopg's ~30 catalog-recovery passes
  still decode every retained segment from the first one onwards. On SF1
  that is only the redo segment itself, but a cluster that retains
  pre-redo segments still decodes them on every start.
- **`xlp_sysid`** is still not checked (already ledgered under M0131-S19).
