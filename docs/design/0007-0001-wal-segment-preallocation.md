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
- **Eager next-segment lookahead.** When the writer rolls over, the
  next segment's preallocation happens lazily on first open. Eager
  lookahead is a follow-up — gives lower commit-path tail latency at
  rollover but adds a background goroutine.
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
