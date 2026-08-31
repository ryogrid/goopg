# M0131-S30.6 — the two WAL append paths must agree on segment-boundary layout

Status: LANDED 2026-08-12 (loop #151)
Task: `.ralph/fix_plan.md` M0131-S30.6
Siblings: `0131-0022` (reader: sub-header segment tail), `0131-0028` (writer:
pad clobbers a page header), `0131-0029` (an early end of WAL refuses the start)

## The defect

`state.appendPGCompat` has two append paths and they produced **different WAL
byte layouts for the same append sequence**:

- **Path B** (stripe, `Config.WALBuffers > 0` — the production path) reserves
  through `insertPosTracker.reserveEmittedAndPublish`. When a reservation would
  straddle a segment boundary it **re-lands the record at the boundary** and
  fills `[curr, boundary)` with an `XLOG_NOOP` pad via `emitSegmentPad`.
- **Path A** (`Config.WALBuffers == 0`, an oversized record, or a ring drain)
  encoded at the tracker cursor and emitted straight through
  `emitWithPageHeaders` with **no crossing check at all**, so it wrote records
  that straddle a segment boundary.

goopg's on-disk WAL layout was therefore writer-path-dependent, and every
consumer had to tolerate both shapes. That is not an abstract tidiness problem:
it is why the `0131-0022` reader fix had to make its segment-tail skip
**CRC-conditional** rather than positional (the first, offset-only version of
that fix dropped Path A's straddling records and everything behind them), and it
is one more axis along which a recovery bug can hide.

Upstream has a single rule for every inserter: `ReserveXLogInsertLocation`
reserves in *usable-byte* space and `XLogInsertRecord` applies the same boundary
handling regardless of how the record got there
(`postgres/src/backend/access/transam/xlog.c`).

## The fix

### 1. Path A applies the same crossing rule (`internal/wal/writer.go`)

Before encoding, Path A now predicts the emitted size at the cursor
(`predictEmittedSize(paddedLen, …)` — the same prediction Path B reserves with).
If the emission would cross the segment boundary it:

1. composes the gap `[curr, boundary)` exactly as `emitSegmentPad` does — a pad
   RECORD sized to the gap **minus** `pageHeaderBytesIn(curr, boundary, segSize)`
   (the `0131-0028` rule), emitted through `emitWithPageHeaders`;
2. stamps the re-landed record's `xl_prev` with the pad's start LSN, matching
   `reserveEmittedAndPublish`'s `t.prev = startCandidate`;
3. writes the pad and the record as **one contiguous `writeAt`** at the gap
   start, so a crash cannot leave the record durable without its pad.

A record whose emission cannot fit in a whole segment is left where it is —
re-landing would only make it straddle the *next* boundary; this is the same
bound Path B asserts with its "emitted size exceeds segSize" panic.

### 2. An unusable gap is ZERO-FILLED, not skipped (both paths)

The pre-existing rule for a gap too small to hold a record (`gapLen <
xlogMinimumRecordSize`, and the 25-byte case `buildSegmentPadRecord` cannot
encode) was "leave the bytes untouched; they are zeros in a preallocated
segment". Porting that to Path A exposed the assumption: **with
`Config.Preallocate` off, an unwritten gap leaves the segment file SHORT**, and
replay stops at the short read, discarding every record behind it. Measured
while porting: `TestReopenAfterSegmentStraddlingRecord/payload72` read 628 of
2068 records.

`emitSegmentPad` now writes explicit **zeros** over such a gap and returns
`padded=false`; `insertPosTracker.onCrossSegment` gained that boolean return so
`reserveEmittedAndPublish` advances `prev` **only for a real pad record** (a
zero-filled gap holds no record to link to, so the record at the boundary links
straight to the last record before the gap — the previous behaviour, now stated
explicitly). Path A mirrors both halves.

Zero-filling is the PG-faithful choice, not merely the convenient one:
`AdvanceXLInsertBuffer` zeroes each WAL page before it is inserted into, so an
unusable page tail in PG is durable zeros and a segment file never has a hole.

## Guard

`internal/wal/writer_segment_cross_parity_test.go`
`TestAppendPathsAgreeOnSegmentBoundaryLayout` appends the SAME payload sequence
through both paths (`Config.WALBuffers` 0 vs 1 MiB) and requires the resulting
segment files to be **byte-identical**, then requires both to replay completely.
Four leads before the boundary: 8 and 16 (sub-header gaps — zero-filled), 64 (a
real pad) and 12288 (a pad spanning a page boundary — the `0131-0028` shape).

Negative control: with the writer change reverted, all four subtests fail with
the first differing byte at the boundary gap (e.g. `lead12288`: "Path A and Path
B disagree at byte 20480").

## What this changes for consumers

Streams written by Path A now contain one `XLOG_NOOP` pad per padded crossing
where they previously contained a straddling record. Tests that counted records
across a segment boundary were updated to exclude pads
(`withoutSegmentPads`), and `TestReplayFromDirEndToEndPageHeaders` now expects 4
records instead of 3.

The reader keeps its tolerance for the old straddling shape — streams written by
earlier goopg versions have it on disk — but no writer path produces it any
more, so `TestReadAllKeepsRecordStraddlingSegmentBoundary`'s fixture is now
built by hand (emit at the cursor, no re-land) instead of by configuring Path A.

## Gates

- `go test ./internal/wal/` and `go test -race ./internal/wal/` PASS
- `go test ./internal/initdb/ ./internal/storage/` PASS
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
- `RUNS=2 bash analysis/crashprobe30.sh` → **OVERALL: PASS (2 runs)**, both runs
  exact on the atomicity invariant (`sum(abalance) == sum(history.delta)`)
- pgbench smoke via the commit hook

## Residual

The `xl_prev` of a pad that itself starts on a page boundary is 24 bytes low
(`reserve_emitted.go`, noted since `0131-0028`); Path A reproduces that quirk
deliberately so the two paths stay byte-identical. Fixing it is a single change
in `reserveEmittedAndPublish` plus the mirror in `appendPGCompat` — deferral
ledger 2026-08-12.
