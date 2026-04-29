# WAL Direct-I/O Write Path (M0010)

- status: accepted
- date: 2026-04-29
- supersedes: —

## Goal

Make WAL writes on the primary (and the corresponding walreceiver
WAL-persist path on the standby) bypass the OS page cache when an
operator opts in via `wal_direct_io=on`. Newly-written WAL bytes no
longer warm the page cache, freeing it for relation-file working set —
the cache-pressure pathology M0010 documents on write-heavy primaries.

This document spans both phases of the rollout. Phase 1 (landed in
this loop) delivers GUC + probe + plumbing + fallback observability;
Phase 2 (the next sibling slice in `.ralph/fix_plan.md`) lights up
the actual O_DIRECT segment open + alignment-safe writes.

## Non-goals

- **Direct-I/O for heap / index files.** That's an explicit
  out-of-scope item in the M0010 milestone (`docs/milestones/0010-...md`
  §"Out of Scope"). The relation-file `O_DIRECT|O_DSYNC` toggle
  already exists in `internal/storage/direct_io_linux.go` and is
  controlled separately by `OpenOptions.AlignedIO`.
- **Async durability redesign.** `flushUpTo` still calls `dataSync`
  (fdatasync on Linux per M0007-0002) regardless of the I/O mode.
  O_DIRECT does NOT replace fdatasync — `O_DIRECT|O_DSYNC` would,
  but goopg keeps the explicit barrier for symmetry with the
  buffered path and so a future asynchronous-commit slice can
  reason about durability without conditional-fsync paths.
- **Windows / macOS parity.** Linux-only this milestone (M0010
  scope §"Out of Scope").
- **Logical replication direct-I/O.** Logical decoding reads from
  segments via `RecordIterator`; if the iterator misses the page
  cache it pays a real read syscall, but that's the same shape as
  upstream. Walsender's in-memory handoff (M0010-0002) is the
  designated mitigation.

## Phasing

| Phase | Scope | Status |
| --- | --- | --- |
| 1 | GUC, FS probe, fallback reporting, startup log line. `wal_direct_io=on` is plumbed end-to-end but does NOT yet flip `O_DIRECT` on segment opens — buffered writes regardless. | landed (this loop) |
| 2 | OR `O_DIRECT` into `openSegment`'s flags when probe ok. Per-write read-modify-write for partial trailing blocks. Aligned scratch buffer allocation via `unix.Mmap`. Walreceiver inherits via the shared writer fd. | next slice (M0010-0001b in fix_plan) |

The split is not arbitrary: Phase 2 requires non-trivial changes to
`state.writeAt` (alignment-safe per-chunk RMW, scratch buffer
lifecycle, interaction with the AIO submit path). Landing Phase 1
first lets operators verify their filesystem honours O_DIRECT
(probe outcome surfaces in startup logs) before Phase 2 changes
the syscall shape.

## Phase 1 file map

| File | Role |
| --- | --- |
| `internal/wal/direct_io_linux.go` | `probeDirectIO(walDir)` — opens a sentinel file with `O_RDWR\|O_CREAT\|O_DIRECT`, observes EINVAL / EOPNOTSUPP, removes the sentinel. Returns the human-readable fallback reason or empty on success. |
| `internal/wal/direct_io_other.go` | Non-Linux stub returning a fixed fallback reason. Mirrors the M0009 io_uring stub shape (`internal/aio/method_iouring_other.go`). |
| `internal/wal/writer.go` | New `Config.DirectIO bool`, new `state.directIORequested` / `state.directIOFallbackReason`, `loadState` runs the probe when `DirectIO=true`, new `Writer.DirectIORequested()` / `Writer.DirectIOFallbackReason()` accessors. |
| `internal/config/defaults.go` | Registers the `wal_direct_io` GUC (`TypeBool`, default `off`, `ContextPostmaster`, `ScopeServer`). |
| `internal/initdb/open.go` | `OpenOptions.WALDirectIO` plumbs to `wal.Config.DirectIO`. |
| `cmd/goopg/main.go` | Reads the GUC into `OpenOptions.WALDirectIO`; emits `event=wal_direct_io_active` on probe success or `event=wal_direct_io_fallback` on failure. |
| `internal/wal/direct_io_test.go` | `TestDirectIODisabledByDefault` (no probe runs when GUC off), `TestDirectIOEnabledProbesFilesystem` (probe runs, outcome plumbed correctly per GOOS), `TestDirectIOFallbackReasonStable` (idempotent reads). |

## The probe

`probeDirectIO(walDir)` runs once at writer construction. The probe
opens a sentinel file `<walDir>/.wal_direct_io_probe` with
`O_RDWR|O_CREAT|O_DIRECT`. Three outcomes:

1. **Success** → close + remove the sentinel, return `("", nil)`.
   Phase 2 may flip `O_DIRECT` on segment opens.
2. **EINVAL** → filesystem doesn't honour O_DIRECT (tmpfs,
   overlayfs are the common cases). Return
   `("filesystem does not support O_DIRECT (open returned
   EINVAL)", nil)`. Writer continues in buffered mode.
3. **EOPNOTSUPP** → some FUSE filesystems return this instead.
   Same fallback treatment.
4. **Other errno** → unexpected (permission denied, mkdir race);
   returned as an error so `NewWriter` fails fast and the operator
   sees the misconfiguration.

Probe cost is one open + close + unlink — cheap enough to do
unconditionally when the operator opted in. The probe runs only
when `Config.DirectIO=true`; with the default `off` setting the
probe is skipped entirely so server start pays nothing.

Why probe rather than fall back at first segment open: a probe
failure at startup is loud and grep-able
(`event=wal_direct_io_fallback`); a runtime EINVAL on the first
commit-path pwrite would confuse operators and require
intricate per-segment fallback state. Upstream's `pgaio_uring`
takes the same probe-at-init approach (mirrored in goopg's
M0009-0006 io_uring method).

## Fallback observability

Three log-line shapes, all under the `wal_direct_io` event family
(mirrors M0009's `aio_engine_attached` / `aio_method_fallback`
vocabulary):

- `event=wal_direct_io_active requested=true` — operator asked,
  probe succeeded. Phase 2 will treat this as the green light to
  flip the segment-open flag.
- `event=wal_direct_io_fallback requested=true reason=<...>` —
  operator asked, probe rejected. Writer is in buffered mode; the
  reason tells the operator what to fix (typically: move pg_wal
  off tmpfs).
- (no log line) — operator did not request direct I/O. Default
  rollout. Probe doesn't run, no startup noise.

`Writer.DirectIORequested()` / `Writer.DirectIOFallbackReason()`
are the public read-side. `cmd/goopg start` is the only caller in
Phase 1; future consumers (a `pg_stat_wal_io` view, an
`event=...` counter export) will read the same accessors.

## Phase 2 sketch (sibling slice — not landed in this loop)

Phase 2 (`M0010-0001b` in `.ralph/fix_plan.md`) adds:

1. **`O_DIRECT` segment open**: when `state.directIORequested &&
   state.directIOFallbackReason == ""`, `openSegment` ORs in
   `unix.O_DIRECT`. The same fd serves both writer and
   walreceiver paths (walreceiver writes through
   `wal.Writer.Append`).
2. **Per-write alignment**: `writeAt` routes through a new
   `writeAtDirectIO(f, segOff, buf, blockSize)`:
   - Compute the aligned region `[alignDown(segOff),
     alignUp(segOff+len(buf)))`.
   - Allocate aligned scratch via `unix.Mmap(MAP_PRIVATE|
     MAP_ANONYMOUS)` — same trick as
     `internal/storage/arena.go`.
   - `pread` the existing region into scratch. Past-EOF bytes
     (legacy lazy-grow case without `wal_init_zero=on`) are
     padded with zeros.
   - Overlay `buf` onto `scratch[segOff-regionStart:]`.
   - `pwrite` the full region back. O_DIRECT honours this
     because (offset, length, buffer) are all block-aligned.
3. **Block-size detection**: probe via `unix.Statx` with
   `STATX_DIOALIGN` (kernel ≥ 6.1) or fall back to 4 KiB. The
   block size is stored on `state` and passed to
   `writeAtDirectIO`.
4. **AIO submit path**: when `Config.AIO != nil` and `DirectIO`
   is active, `state.writeAt` still routes through the engine —
   but the buffer must be aligned. Either the engine's worker
   method copies into a scratch arena before pwrite, or the
   writer pre-aligns at the wal layer. Phase 2 design picks the
   former (engine-side) so heap/index O_DIRECT writes (which
   already use page-aligned arena slots) don't pay for a
   redundant copy.
5. **Walreceiver coverage**: walreceiver's WAL-persist path
   delegates to `wal.Writer.Append` — it inherits Phase 2's
   alignment automatically, no separate code path.

Phase 2 tests must cover (a) round-trip on ext4 with the probe
honoured, (b) probe-failure path identical to Phase 1 buffered
behaviour, (c) RMW correctness for arbitrary chunk lengths /
offsets, (d) crash-restart with `wal_init_zero=on` (the EOS
sentinel rule from M0007-0001 must still terminate recovery
cleanly when the trailing block is RMW'd back unchanged).

## Cross-references

- `docs/design/root-0008-wal-and-recovery.md` — overall WAL
  architecture; this doc adds the "WAL writer can run with or
  without page cache" axis.
- `docs/design/0007-0001-wal-segment-preallocation.md` — the
  zero-fill + EOS-sentinel substrate Phase 2 builds on. With
  `wal_init_zero=on` the segment body is fully allocated so
  every RMW can succeed (no past-EOF reads).
- `docs/design/0007-0002-fdatasync-commit-path.md` — the
  durability barrier that runs unchanged under O_DIRECT.
- `docs/design/0009-0001-aio-core.md` — the AIO submit
  surface Phase 2 must keep alignment-safe.
- `docs/design/0005-0001-streaming-replication-architecture.md` —
  walreceiver's Append-driven persistence, which inherits Phase 2's
  alignment without a separate code path.

## Upstream references

- `postgres/src/backend/access/transam/xlog.c` — `XLogFileInit`
  and the buffered-write loop. Upstream's WAL writer uses an
  in-memory `XLogCtl->pages` buffer with explicit `pg_pwrite_zeros`
  for the tail; goopg's Phase 2 RMW achieves the same alignment
  guarantee without the central buffer pool.
- `postgres/src/backend/access/transam/xlogrecovery.c` — the
  recovery path that consumes WAL written via either mode (the
  byte stream is identical regardless of how the kernel reached
  the disk).
- `linux/Documentation/filesystems/ext4.rst` — ext4 O_DIRECT
  semantics: 512-byte alignment minimum, 4 KiB practical for
  modern devices.
