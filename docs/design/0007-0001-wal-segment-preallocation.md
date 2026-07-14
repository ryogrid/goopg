# WAL Segment Preallocation (Milestone 0007)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0007 — WAL Segment Preallocation & `fdatasync`         |
| Refines    | [root-0008-wal-and-recovery.md](root-0008-wal-and-recovery.md) |
| Supersedes | —                                                      |

## Problem

`internal/wal/writer.go` creates segment files lazily
(`os.OpenFile(O_CREATE|O_RDWR)`) and grows them implicitly as records
are appended. Two consequences:

- The first record in a fresh segment forces a filesystem allocation
  during the commit path, occasionally causing latency spikes and
  letting the filesystem allocate non-contiguous extents.
- Every commit-path `f.Sync()` is a full `fsync(2)` that also persists
  inode metadata (mtime, size). Upstream uses `fdatasync` because
  preallocated segments do not change size between commits.

Upstream's solution is segment **preallocation**: create the file at
the configured `SegmentSize` (default 16 MiB), zero-fill it once, and
`fsync` the data + the directory entry. After that, every append into
the segment is an in-place overwrite — the inode never changes.

This loop lands the preallocation primitive plus the recovery-side
machinery needed to make it safe. The `fdatasync` switch is a
separate seam (`0007-0002-fdatasync-commit-path.md`).

## Decision

### `Config.Preallocate bool`, GUC-driven

`wal.Config` grows `Preallocate bool`. When true, every newly created
segment is zero-filled to `SegmentSize` and `fsync`-ed before any
record write hits it. The directory entry is also `fsync`-ed so the
new file is durable (mirrors upstream's `durable_rename` /
`fsync_fname`).

The setting is driven by the upstream-named `wal_init_zero` GUC
(already discussed in the milestone). `internal/config/defaults.go`
registers it `ContextSigHup`, default `on`. `cmd/goopg start` reads
it at server boot and sets `Config.Preallocate` accordingly.

### Trailing-zero EOS sentinel

`encodeRecord`'s 8-byte header is `[len:u32 LE][crc:u32 LE]`. A
zero-filled segment tail looks like an infinite stream of records
with `len=0` and `crc=0`. Recovery would mistakenly emit thousands of
empty records.

The fix has two parts:

1. **Reject empty Appends.** `Writer.Append([]byte{})` now returns an
   error. No production caller emits empty records — every existing
   call site (`internal/initdb/open.go`, the buffer pool's FPI hook,
   the walreceiver) passes a non-empty encoded payload. This frees
   the zero header to be the EOS sentinel.
2. **Stop on the first zero header.** `decodeRecord` (and the
   recovery-side write-position scanner) treat `len==0 && crc==0` as
   end-of-stream rather than as a valid empty record.

### `detectWritePos` — scan-based for full-size segments

Today `detectWritePos` reconstructs the write position by summing
on-disk segment sizes (the last segment is the only one allowed to be
short). With preallocation, every segment is full-size from creation,
so the size-based formula always returns `numSegments * SegmentSize`
— wrong by however many bytes the tail of the last segment is empty
of records.

The new logic:

- All-but-last segments still must be at the configured size (any
  other configuration is a corruption).
- The **last** segment is scanned record-by-record. The write
  position is the offset just past the last valid record — which is
  also the offset of the first zero header.

This logic works whether the last segment is short (legacy lazy
mode) or full-size (preallocated mode). On a server upgraded from
the legacy mode mid-stream, both shapes coexist cleanly: any new
segment created post-upgrade is preallocated; the legacy short
segments are walked once and left as-is.

### `readStream` — read-to-ENOENT, terminate on EOS

`ReadAll`'s segment-iterator now reads every existing segment in
order until ENOENT. The previous "stop when a segment is shorter
than `segSize`" rule is replaced by the EOS-sentinel rule applied to
the decoded byte stream: `decodeRecord` returns `(_, _, ErrEOS)`
when it sees a zero header, and the iterator breaks.

### What this loop *doesn't* deliver

- **`fdatasync` on the commit path.** Separate seam:
  `0007-0002-fdatasync-commit-path.md`.
- ~~**Eager next-segment lookahead.**~~ Done — see "Follow-up
  (2026-07-09): eager next-segment lookahead" below.
- **`wal_recycle` (segment renaming).** Future loop. The recycler
  reuses an old segment file as the next zero-filled one rather than
  unlinking + zero-filling. Requires interaction with retention.
- **`posix_fallocate`.** Zero-write loop is the v0 implementation;
  faster preallocation primitives are out of scope.

### Follow-up (2026-07-08): preallocation counters

The shared stats sink this loop's "Counters / observability" bullet
was waiting on now exists (`internal/stats.Counter`, a per-P sharded
additive counter landed for the M0013-0003 WAL-buffer drain counters
and the M0122-0003 fsync counters). `walBufferCounters` grows two more
fields, `segmentsPreallocated` and `preallocatedBytes`, bumped once in
`state.openSegment`'s `wasNew` branch — the same branch that calls
`preallocateSegment` — so a re-open of an already-preallocated segment
never double-counts. `Writer.SegmentsPreallocated()` /
`Writer.PreallocatedBytes()` expose lifetime sums, surfaced as two new
`pg_stat_wal_io` columns: `wal_segments_preallocated_total` and
`wal_init_zero_bytes_total`. Both are `0` when `Preallocate` is off.
See `internal/wal/wal_test.go`'s `TestPreallocationCounters` and
`internal/initdb/wal_io_views_test.go`'s
`TestStatWALIOPreallocationCounters`.

### Follow-up (2026-07-09): eager next-segment lookahead

Closes the "Eager next-segment lookahead" gap named above (M0122-0007
backlog entry). `state.openSegment(segNo)` now calls
`state.eagerPreallocSegment(segNo+1)` right after it finishes handling
`segNo` itself (whether `segNo` was freshly created or already
existed) — so by the time the writer's own write head rolls over into
`segNo+1`, that segment's file is (usually) already fully zero-filled
and `openSegment`'s `os.Stat`-based `wasNew` check finds it in place,
skipping the synchronous `preallocateSegment` zero-fill on the
commit-path hot path. Purely a latency optimization: `openSegment`'s
existing "does the file already exist" check is the only interface
between the two paths, so a slow, aborted, or lost-race eager job
changes nothing about correctness — the synchronous path always still
preallocates `segNo` itself exactly as before if the eager job hasn't
gotten there first.

**Race-safe by construction.** `eagerPreallocWorker` never writes to
the final segment path directly. It zero-fills a `<segfile>.eager
<pid>.tmp` file to `SegmentSize`, fsyncs it, then durably links it
into place with `os.Link(tmp, final)` — mirroring upstream
`XLogFileInit`'s create-as-temp-then-link-no-clobber pattern
(`xlog.c`). `os.Link` returning `EEXIST` (the synchronous path, or a
second eager attempt, won the race) is treated as success, not an
error — the file is there either way. `state.eagerInFlight` (guarded
by `eagerMu`) prevents two concurrent eager jobs for the same segment
number; `state.eagerWG` lets `close()` wait for any still-running
eager goroutine before the writer tears down `s.files`, so `Close()`
never returns while a background preallocation could still be
touching the WAL directory.

**A new correctness hazard this creates, and its fix.** Before this
change, the highest-numbered segment file on disk was always exactly
the segment currently receiving real writes — `detectWritePos`
(consulted only when a writer reopens, e.g. after a restart) exploited
this by trusting every *non-last* segment as "fully used" via a cheap
file-size check, and content-scanning only the literal highest-numbered
file for the true end-of-WAL. Eager lookahead breaks that assumption:
a crash between eagerly preallocating `segNo+1` and the writer ever
really reaching it leaves a fully zero, never-written `segNo+1` file
sitting *above* the genuinely partially-written `segNo` — the old
logic would then blindly trust `segNo` as "fully used" (wrong: it's
only partially written) while content-scanning the empty phantom
`segNo+1` (right size, wrong file). `detectWritePos` now walks
backward from the highest segNo, dropping any segment that is *both*
full-size (`segSize`) *and* scans as entirely empty (0 bytes before
the first EOS sentinel), until it finds one that isn't — that becomes
the "real last" segment for the existing (unchanged) scan-the-last-
segment logic. The full-size guard specifically distinguishes an eager
phantom from a legitimate legacy short/empty last segment (already
handled correctly by the pre-existing logic, unchanged) — a
genuinely-active, preallocated-but-partially-written segment can never
be mistaken for a phantom because a real record stream never starts
with a zero header (`Writer.Append` rejects empty payloads, so the
very first byte of genuine content is never part of the EOS sentinel
pattern). At most one phantom can exist at a time in practice (eager
lookahead only ever preallocates one segment ahead of the one
currently in real use), but the trim loop tolerates more defensively.

Tests: `internal/wal/writer_detect_test.go`'s
`TestDetectWritePos_IgnoresEagerPhantomFutureSegment` (writes a real,
partially-filled segment 0 via the writer, waits for its own eager job
to finish creating segment 1, closes, and asserts `detectWritePos`
returns segment 0's true end rather than segment 0's full size);
confirmed non-vacuous by reverting the trim loop locally (fails with
the exact predicted `writePos` overshoot). `internal/wal/wal_test.go`'s
`TestPreallocationCounters` updated to `w.stateRef.eagerWG.Wait()`
before each count assertion and re-derive the expected totals (one
segment ahead of the pre-eager-lookahead numbers) instead of
implicitly relying on the background goroutine losing a race it had
no guaranteed way to lose. `internal/wal/pg_waldump_compat_test.go`'s
`TestPGWaldumpParsesEmittedWAL` now names an explicit STARTSEG
argument rather than bare `-p walDir` — with a second real segment
file now present, pg_waldump's own directory-only auto-detect mode
(`identify_target_directory(waldir, NULL)`, `pg_waldump.c`) picks
"any WAL-looking file" via unordered `readdir()`, which can hand it
the all-zero segment 1 and misread its zeroed long-page-header as
`xlp_seg_size=0`; naming the exact start segment is the standard,
unambiguous way to invoke pg_waldump against a known LSN range and
sidesteps this pre-existing upstream tool quirk entirely (real
PostgreSQL WAL directories have the same kind of preallocated future
segment during normal operation).

**Review finding, fixed same loop:** an independent review of this
change caught a genuine goroutine/leak race in the first cut of
`close()` — `eagerWG.Wait()` originally ran *before* `flushUpTo`, but
with `Config.WALBuffers > 0` (the default), `Append` only touches the
in-memory buffer; nothing reaches `openSegment` until something drains
it. If nothing had drained the buffer before `Close()`, `close()`'s own
`flushUpTo` call became the *first* caller of `openSegment` for one or
more segments — and `openSegment` unconditionally kicks off a
brand-new eager job for the segment after whichever one it just
opened, with zero chance to have started before the earlier
`Wait()` already returned. `Close()` could then return while that
fresh goroutine was still writing into the WAL directory. Fixed by
moving `eagerWG.Wait()` to run *after* `flushUpTo`, not before it — the
only remaining caller of `openSegment` inside `close()`. New test
`internal/wal/writer_detect_test.go`'s
`TestClose_WaitsForEagerJobTriggeredByItsOwnFlush` (buffers everything
via a large `WALBuffers`, appends across two segment boundaries so
`flushUpTo` opens both for the first time, then asserts the
eager-triggered next segment is already fully sized the instant
`Close()` returns, no explicit wait) — confirmed non-vacuous by
reverting the ordering locally (fails ~95% of runs, a genuine race,
not a rare corner case).

Gates: `go build ./...` clean;
`go test ./internal/wal/...` and `go test -race ./internal/wal/...`
PASS; `go test ./internal/initdb/...` PASS (no regression in the
Init+Open+restart recovery suites); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
workloads).

**Deliberately out of scope:** no new GUC gates this — it only ever
activates when `Config.Preallocate` (`wal_init_zero`) is already on,
matching that GUC's own default-on posture, so a dedicated on/off
toggle for the lookahead specifically wasn't judged worth a second
knob. `posix_fallocate`-based zero-fill (both the synchronous and
eager paths still do a 64 KiB `WriteAt` loop) remains a separate,
already-named follow-up.

### Recovery semantics

The milestone DoD requires "Recovery terminates cleanly at the first
zeroed page inside a preallocated-but-not-yet-written segment". The
EOS-sentinel rule satisfies this: the recovery reader stops at the
first `len==0 && crc==0` header. No partial-record / torn-write
heuristic is needed because every record carries a CRC; a torn
record fails CRC validation and produces `ErrCorruptRecord`, which
is a hard error rather than a quiet truncation. (Upstream uses the
same model.)

## Verification

`internal/wal/wal_test.go` grows two new tests:

- `TestPreallocatedSegmentIsFullSize`: open a writer with
  `Preallocate: true`, append one short record, close. The on-disk
  segment 0 file is exactly `SegmentSize` bytes.
- `TestPreallocatedSegmentRecoversCleanly`: open a writer with
  preallocation, append three records, close. Open again with
  `Preallocate: true`; the writer's `WrittenLSN()` matches the third
  record's end LSN, and `ReadAll` returns exactly the three records
  (no spurious empty records from the zero-fill tail).

Plus a guard test `TestAppendRejectsEmptyPayload` to pin the new
invariant.

The existing legacy (non-preallocated) tests stay green by default
because `Preallocate` defaults to `false`.

## Cross-references

- Milestone: `docs/milestones/0007-wal-segment-preallocation.md`.
- WAL writer / recovery seam: `root-0008-wal-and-recovery.md`.
- Sibling design: `0007-0002-fdatasync-commit-path.md` (next loop).
- Upstream: `postgres/src/backend/access/transam/xlog.c`
  `XLogFileInit`, `XLogFileInitInternal`, `wal_init_zero`,
  `wal_recycle`.
