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
| 1 | GUC, FS probe, fallback reporting, startup log line. | landed |
| 2 | `enableDirectIO(f)` toggles O_DIRECT on segment fds (post-preallocation, via fcntl `F_SETFL`). Per-write RMW alignment in `state.writeAtDirectIO`. Aligned scratch via `unix.Mmap(MAP_PRIVATE\|MAP_ANONYMOUS)`. Walreceiver inherits via the shared writer fd. | landed |
| 2.b | Write-buffering fast path: accumulate user bytes in scratch until block-aligned, drain at flushUpTo. Amortises the per-write RMW pread cost. | deferred (perf-only optimisation) |

The split is not arbitrary: Phase 2 requires non-trivial changes to
`state.writeAt` (alignment-safe per-chunk RMW, scratch buffer
lifecycle, interaction with the AIO submit path). Landing Phase 1
first lets operators verify their filesystem honours O_DIRECT
(probe outcome surfaces in startup logs) before Phase 2 changes
the syscall shape.

## File map (Phases 1 + 2)

| File | Role |
| --- | --- |
| `internal/wal/direct_io_linux.go` | `probeDirectIO(walDir)` — opens a sentinel file with `O_RDWR\|O_CREAT\|O_DIRECT`, observes EINVAL / EOPNOTSUPP, removes the sentinel. `enableDirectIO(f)` — fcntl `F_SETFL` to flip `O_DIRECT` on a live fd post-preallocation. `allocAlignedScratch(size)` / `freeAlignedScratch(b)` — page-aligned RMW buffer via `unix.Mmap(MAP_PRIVATE\|MAP_ANONYMOUS)`. |
| `internal/wal/direct_io_other.go` | Non-Linux stub: probe always falls back; `enableDirectIO` returns an error (never reached because `directIOActive` is always false on non-Linux); scratch helpers are unsupported. |
| `internal/wal/writer.go` | New `Config.DirectIO bool`. `state` grows `directIORequested`, `directIOFallbackReason`, `directIOActive`, `directIOBlockSize`, `directIOScratch`. `loadState` runs the probe + sets `directIOActive`. `openSegment` calls `enableDirectIO` after preallocation when active. `state.writeAt` dispatches to new `state.writeAtDirectIO` (RMW: pread aligned region → overlay user bytes → pwrite aligned region back, looping if region exceeds `directIOScratchCap` = 1 MiB). `state.close` munmaps the scratch. New `Writer.DirectIORequested()` / `Writer.DirectIOFallbackReason()` accessors. |
| `internal/config/defaults.go` | Registers the `wal_direct_io` GUC (`TypeBool`, default `off`, `ContextPostmaster`, `ScopeServer`). |
| `internal/initdb/open.go` | `OpenOptions.WALDirectIO` plumbs to `wal.Config.DirectIO`. |
| `cmd/goopg/main.go` | Reads the GUC into `OpenOptions.WALDirectIO`; emits `event=wal_direct_io_active` on probe success or `event=wal_direct_io_fallback` on failure. |
| `internal/wal/direct_io_test.go` | `TestDirectIODisabledByDefault`, `TestDirectIOEnabledProbesFilesystem`, `TestDirectIOFallbackReasonStable` (Phase 1); `TestDirectIORoundTripWithPreallocation` (records round-trip via the RMW path), `TestDirectIORecordSpanningBlocks` (12 KiB payload across ~3 block boundaries) (Phase 2). Phase 2 tests `t.Skip` on probe fallback. |

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

## Phase 2 details (landed)

1. **O_DIRECT segment open** (`state.openSegment`): the standard
   `os.OpenFile(O_RDWR|O_CREATE)` happens first. When the segment
   is new and preallocation is on, `preallocateSegment` lays the
   16 MiB body down via 64-KiB chunked pwrites — these can't
   satisfy O_DIRECT alignment because `make([]byte, 64KiB)`
   isn't page-aligned. After preallocation completes (and the
   directory fsync) we call `enableDirectIO(f)` which uses
   `fcntl(F_SETFL, flags | unix.O_DIRECT)` to flip the flag on
   the live fd. From this point on every read / write through
   `f` must be block-aligned. The same fd serves the wal writer
   AND the walreceiver path (walreceiver delegates to
   `wal.Writer.Append`), so walreceiver inherits the alignment
   contract for free.
2. **Per-write alignment** (`state.writeAtDirectIO`): three steps
   per region.
   1. Compute the aligned region `[alignDown(segOff),
      alignUp(segOff+len(user bytes)))`. Capped at
      `directIOScratchCap = 1 MiB` per iteration; outsized writes
      loop through the scratch.
   2. `pread` the existing aligned region into the per-state
      scratch. Past-EOF bytes (legacy lazy-grow without
      `wal_init_zero=on`) are zero-padded — the bytes-written-
      so-far invariant is preserved because no caller reads past
      `state.writeLSN` before flushUpTo blesses it.
   3. Overlay user bytes onto `scratch[userStart-regionStart:]`,
      `pwrite` the full aligned region back. O_DIRECT honours
      this because (offset, length, buffer) are all block-aligned
      by construction (regionStart = alignDown, regionEnd =
      alignUp, scratch is `unix.Mmap`'d so 4 KiB-aligned).
3. **Aligned scratch lifecycle**: lazy-allocated on the first
   `writeAtDirectIO` call (cost paid only on direct-I/O writers,
   not buffered ones), freed in `state.close`. Backed by
   `unix.Mmap(MAP_PRIVATE|MAP_ANONYMOUS)` — same pattern as
   `internal/storage/arena.go`'s buffer pool slab. Capacity 1
   MiB sized to handle a typical commit-batch in one syscall pair
   while keeping memory overhead bounded.
4. **AIO + DirectIO interaction**: when `state.directIOActive`
   AND `state.aio != nil`, `writeAt` bypasses the AIO engine
   and uses the synchronous RMW path. Reason: `aio.Op.Buffer`
   holds the user's slice pointer, which isn't page-aligned, so
   AIO Submit would need its own aligned-copy substrate. That's
   a perf-only follow-up (heap/index O_DIRECT writes already
   pay this cost via the page-aligned arena slot path); WAL
   correctness lands via the synchronous RMW first.
5. **Block-size**: hard-coded at 4 KiB for now
   (`state.directIOBlockSize`). Every modern Linux DIO-capable
   filesystem (ext4, XFS) satisfies this boundary. A
   `STATX_DIOALIGN`-based probe (kernel ≥ 6.1) is a future
   tightening — wrong-but-larger-than-actual alignment never
   causes EINVAL, just wastes a few extra RMW bytes.

Phase 2 tests cover (a) round-trip with the probe honoured (`Test
DirectIORoundTripWithPreallocation` — three appends + flush +
ReadAll), (b) RMW correctness across multiple block boundaries
(`TestDirectIORecordSpanningBlocks` — 12 KiB payload spanning ~3
block boundaries; the test asserts every byte round-trips). The
probe-failure path is the same buffered code Phase 1 already
exercised. Crash-restart correctness rides on the existing
`TestPreallocatedSegmentRecoversCleanly` test in `wal_test.go`:
the byte stream written under O_DIRECT is identical to the
buffered stream (the RMW writes the exact same bytes), so the
EOS-sentinel decoder sees no difference.

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
