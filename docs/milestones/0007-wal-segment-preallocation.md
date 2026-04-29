# Milestone 0007 — WAL Segment Preallocation and `fdatasync`-Based Durability

**Status:** planned
**Depends on:** Milestone 0001 (foundational server with WAL append / flush
machinery in `internal/wal`), Milestone 0002 (production-grade checkpointing
and full-page-write semantics).
**Drives:** Lower commit-path latency under sustained write workloads, more
PG-faithful WAL on-disk semantics, and a stable foundation for the
replication milestones (0005 and 0008) that depend on durable, contiguous
WAL segments.

## Context

Today `internal/wal/writer.go` creates WAL segment files lazily on first
write via `os.OpenFile(..., os.O_CREATE|os.O_RDWR, 0o600)` and grows them
implicitly as records are appended (`openSegment` at
`internal/wal/writer.go:485`). The flush path calls `f.Sync()` per dirty
segment in `flushUpTo` (`internal/wal/writer.go:380`), and the surrounding
comment claims "fdatasync semantics" even though `os.File.Sync` issues a
full `fsync(2)` on Linux. Two things follow from this:

- Every commit-path flush pays for an `fsync` that also persists inode
  metadata (mtime, size). PostgreSQL upstream uses `fdatasync` on Linux for
  this exact reason: WAL segments are preallocated to a fixed size, so
  inode metadata does not change between flushes and only the data blocks
  need to reach the platter.
- Because segments grow implicitly, the first record written into a fresh
  segment forces the filesystem to allocate space and update file size
  during the commit path, occasionally causing latency spikes and giving
  the filesystem freedom to allocate non-contiguous extents — both of
  which upstream sidesteps by zero-filling each new segment ahead of time.

This milestone closes both gaps: WAL segments are preallocated by writing
zeroed bytes for the full configured segment size and `fsync`-ed once at
creation, and the steady-state commit path switches to `fdatasync` (via the
`syscall.Fdatasync` path on Linux, with a documented fallback to `fsync` on
platforms that do not expose it).

## In Scope

### Segment Preallocation

- A preallocator that, given a target segment number, creates the segment
  file at the configured `SegmentSize` (default 16 MiB per
  `internal/wal/writer.go:14`) by writing zero bytes for the entire length
  and then issuing a single `fsync` so both the data blocks and the
  directory entry are durable before the segment is offered to the
  append path.
- Directory `fsync` after a new segment file appears so the rename /
  creation is itself crash-safe (mirrors upstream's
  `durable_rename` / `fsync_fname` behaviour).
- Eager preallocation of the *next* segment while the *current* segment is
  still being written into, so that when the writer rolls over, the next
  segment is already zero-filled and `fsync`-ed. The lookahead depth must
  be configurable and default to 1.
- Crash-safe behaviour for partially preallocated segments: a zero-filled
  tail is indistinguishable from "no records here yet" to the recovery
  reader, which already treats trailing zeros as the end of the WAL
  stream. Recovery must continue to terminate cleanly on a zeroed page
  rather than mistaking it for a torn record.

### `fdatasync` on the Commit Path

- Replace the per-segment `f.Sync()` call in `flushUpTo`
  (`internal/wal/writer.go:384`) with a path that issues `fdatasync` on
  Linux and degrades gracefully to `fsync` on other platforms.
- Preserve the existing `flushedLSN` advance / `dirty` set semantics
  exactly: a flush is still credited only after the durable-sync syscall
  returns successfully.
- Keep `fsync` (full metadata flush) at three specific moments where
  metadata correctness matters and `fdatasync` is insufficient:
  segment-file creation (preallocation), the directory-entry flush after
  segment creation, and segment removal.
- Update the misleading comment at `internal/wal/writer.go:187` /
  `:408` so it accurately describes the new behaviour.

### Configuration and Compatibility

- A `wal_init_zero` GUC (matching upstream's name) that controls whether
  preallocation actively writes zeros or relies on filesystem
  hole-punching. Default `on`, matching upstream defaults.
- A `wal_recycle` GUC (also matching upstream's name) that controls
  whether old segments are renamed and reused versus removed and
  reallocated. Default `on`. The recycler must reuse the existing
  zero-filled, `fsync`-ed file rather than re-zeroing it.
- The on-disk layout remains compatible with both pre-milestone and
  post-milestone runs: a server that finds short, lazily-grown segments
  from a previous version must still recover correctly. New segments
  appearing after first start under this milestone are full-size.

### Observability

- Counters for `wal_segments_preallocated_total`,
  `wal_segments_recycled_total`, and `wal_init_zero_bytes_total` (or
  whatever the closest upstream-shaped counters are at the time of
  implementation), surfaced through the existing stats infrastructure.
- A startup log line indicating whether preallocation and recycling are
  enabled, the segment size in use, and the lookahead depth, so operators
  can confirm the new path is active.
- The replication-event logging from M0005 must continue to work: a
  preallocated-but-empty segment that has not yet received records must
  not be advertised to standbys as containing data.

## Out of Scope

- `posix_fallocate` or `fallocate(FALLOC_FL_*)` fast paths. The first
  implementation uses a simple zero-write loop because it works
  identically across filesystems; faster preallocation primitives are a
  follow-up.
- O_DIRECT / direct-IO write paths.
- Group-commit batching changes. The commit-path latency win in this
  milestone comes purely from the syscall switch and the elimination of
  cold-allocation stalls; group-commit policy is unchanged.
- WAL compression and any change to record framing.
- Asynchronous-commit (`synchronous_commit = off`) tuning. This
  milestone keeps the existing semantics: a successful flush still means
  the bytes are durable.
- Cross-platform parity beyond Linux + macOS. Windows preallocation
  semantics are deferred.

## Required Design Docs

Place under `docs/design/` with sequential numbering at creation time:

- `0007-0001-wal-segment-preallocation.md` — preallocator design,
  zero-fill loop, directory `fsync`, lookahead policy, recycle path,
  interaction with `wal_init_zero` / `wal_recycle`, and the startup-time
  detection of pre-milestone partial segments.
- `0007-0002-fdatasync-commit-path.md` — exact mapping of commit-path
  `Sync` calls to `fdatasync` versus `fsync`, the platform fallback, the
  set of moments that still require full `fsync`, and the rationale for
  each.

These design docs should cross-link to `docs/design/root-0008-wal-and-recovery.md`
and refine, rather than supersede, its existing description of WAL append /
flush.

## Reference

Upstream sources to consult:

- `postgres/src/backend/access/transam/xlog.c` —
  `XLogFileInit` / `XLogFileInitInternal` for the zero-fill +
  `pg_fsync` + `durable_rename` sequence, and the `wal_init_zero` /
  `wal_recycle` GUC plumbing.
- `postgres/src/backend/storage/file/fd.c` — `pg_fsync_writethrough` /
  `pg_fdatasync` and the platform-detection logic that picks
  `fdatasync` versus `fsync` for the commit path.
- `postgres/src/backend/access/transam/xlog.c` — `issue_xlog_fsync`
  for the per-segment commit-path flush and the `wal_sync_method`
  selection.
- `postgres/src/include/access/xlog_internal.h` — segment-size and
  segment-file-naming invariants.

## Definition of Done

1. New WAL segments observed on disk (under `pg_wal/` or the configured
   WAL directory) are exactly `SegmentSize` bytes long from the moment
   they appear, without requiring any record to have been written.
2. The writer's commit-path flush issues `fdatasync` on Linux (verified
   by syscall trace under `strace -e fdatasync,fsync`) and `fsync` only
   at segment creation, the post-creation directory flush, and segment
   removal.
3. `wal_init_zero = off` is honoured: segments are still allocated to
   full size but zero-write traffic is skipped (matching upstream's
   filesystem-trusts-the-hole behaviour).
4. `wal_recycle = on` causes superseded segments to be renamed into
   place as new segments rather than re-zeroed; observable via the
   `wal_segments_recycled_total` counter advancing.
5. Crash-recovery tests (kill `-9` mid-workload, restart) still pass
   bit-for-bit. Recovery terminates cleanly at the first zeroed page
   inside a preallocated-but-not-yet-written segment, exactly as it
   did when segments grew implicitly.
6. A `pgbench` write-heavy run shows reduced commit-path tail latency
   versus pre-milestone baseline, with no regression on throughput, on
   at least one Linux filesystem (ext4 or xfs). The improvement is
   documented with measurements in the implementation PR.
7. Both required design docs (`0007-0001`, `0007-0002`) are merged with
   status `accepted`, and `root-0008-wal-and-recovery.md` carries a
   forward-link to them.
