# `fdatasync` on the WAL Commit Path (Milestone 0007)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0007 — WAL Segment Preallocation & `fdatasync`         |
| Refines    | [root-0008-wal-and-recovery.md](root-0008-wal-and-recovery.md), [0007-0001-wal-segment-preallocation.md](0007-0001-wal-segment-preallocation.md) |
| Supersedes | —                                                      |

## Problem

`flushUpTo` issues `f.Sync()` per dirty segment. On Linux,
`os.File.Sync` calls the `fsync(2)` syscall, which persists both the
data blocks and the file's inode metadata (mtime, size, …). With
preallocation in place (`0007-0001`), the inode never changes between
commits — the segment is full-size from creation, every append is an
in-place overwrite. Every `fsync` on the commit path therefore flushes
inode metadata that has not changed, paying a syscall for nothing.

Upstream uses `fdatasync` for exactly this case (see
`postgres/src/backend/storage/file/fd.c::pg_fdatasync` and the
`wal_sync_method` selector in xlog.c).

## Decision

Replace the per-segment `f.Sync()` call in `flushUpTo` with a
platform-aware helper:

- **Linux**: `unix.Fdatasync(int(f.Fd()))`. Works on all files opened
  via the standard library; doesn't allocate; matches upstream's
  syscall.
- **Other platforms** (macOS, BSDs, Windows): fall back to `f.Sync()`.
  macOS has `F_FULLFSYNC` but no `fdatasync(2)`; BSDs vary; Windows
  has no equivalent. The fallback is documented and visible in the
  build tags.

The full `fsync` is *kept* at three specific moments where metadata
correctness matters and `fdatasync` would lose information:

1. **Segment file creation (preallocation).** The whole point is to
   make the inode durable so the commit path doesn't have to.
2. **Directory fsync after segment creation.** A directory entry is a
   data structure inside the parent directory's inode; `fdatasync` on
   a directory is at best `fsync` and at worst an error.
3. **Segment removal.** Unlinking a segment changes the parent
   directory's contents. `fsync` on the directory is the right tool;
   we don't yet do this in `removeOldSegments` and that gap is
   explicitly *not* closed in this loop (it's a parallel correctness
   bug, tracked separately).

The misleading comment at `flushUpTo` ("`wal: fdatasync %s`" error
prefix) was already aspirational text; this loop makes it accurate.

### Build-tag layout

Two new files in `internal/wal`:

- `sync_linux.go` (build tag `//go:build linux`): exports
  `dataSync(f *os.File) error` that calls `unix.Fdatasync`.
- `sync_other.go` (build tag `//go:build !linux`): exports the same
  symbol that calls `f.Sync()`.

`flushUpTo` calls `dataSync(f)` instead of `f.Sync()`. The choice is
compile-time, no runtime dispatch overhead.

`golang.org/x/sys/unix` is the canonical home for `Fdatasync` (the
standard library's `syscall` package only exposes it on a subset of
GOOS values and is officially in maintenance mode). It's already a
transitive dependency through `lib/pq` and other modules in the
go.mod.

### What this doesn't change

- **Append → flush ordering.** A flush is still credited only after
  the durable-sync syscall returns successfully; the `flushedLSN`
  advance is unchanged.
- **The set of segments synced per `flushUpTo`.** Every dirty
  segment up to `targetSeg` is still synced individually. Group-
  commit batching is out of scope.
- **`synchronous_commit = off` semantics.** The default is still
  "successful flush means bytes are durable." Async-commit modes are
  separate work.
- **The platform fallback's behaviour.** `f.Sync()` on macOS still
  invokes `fsync(2)`, not `F_FULLFSYNC`. Upgrading the macOS path to
  full-sync semantics is a sibling concern that doesn't affect Linux
  performance and is out of scope.

## Verification

`internal/wal/wal_test.go` grows
`TestFlushUpToSyncsThroughDataSync`: a writer that appends a
record, calls `FlushUpTo`, and confirms `dataSync` was invoked
exactly once for the dirty segment by indirection through a test-
only sink (counts calls without committing to syscall introspection
that would be platform-specific).

The existing flush tests (`TestAppendFlushAndReadAll`,
`TestFlushUpToRejectsUnwrittenLSN`) continue to pass — the contract
they pin doesn't depend on which syscall does the persisting.

End-to-end correctness is exercised by the existing crash-recovery
tests; no Linux-vs-other-platform divergence is introduced because
both code paths return the same error type.

## Cross-references

- Sibling: `0007-0001-wal-segment-preallocation.md` (motivates this
  change — preallocation is what makes `fdatasync` correct, since the
  inode no longer changes between commits).
- Milestone: `docs/milestones/0007-wal-segment-preallocation.md`.
- Upstream:
  - `postgres/src/backend/storage/file/fd.c::pg_fdatasync` — the
    upstream wrapper this implementation mirrors.
  - `postgres/src/backend/access/transam/xlog.c::issue_xlog_fsync` —
    the per-segment commit-path flush.
  - `postgres/src/backend/utils/misc/guc_tables.c::wal_sync_method` —
    GUC selector for the sync method (deferred; v0 picks fdatasync on
    Linux statically).

## Follow-up (2026-07-08): `wal_sync_method` GUC

The "deferred" selector above is now registered and live for the two
methods that map onto a single syscall call:

- `internal/config/defaults.go` registers `wal_sync_method` as a
  `TypeEnum` GUC (`ContextSigHup`, matching upstream's `PGC_SIGHUP` in
  `guc_tables.c`), `EnumOptions: []string{"fsync", "fdatasync",
  "open_sync", "open_datasync"}` (`fsync_writethrough` excluded —
  Windows/macOS-only in upstream too, gated behind
  `HAVE_FSYNC_WRITETHROUGH`), `BootVal: "fdatasync"` (matches
  upstream's `PLATFORM_DEFAULT_WAL_SYNC_METHOD` on Linux).
- `wal.Config.SyncMethod` carries the value into the writer;
  `withDefaults` normalizes empty to `"fdatasync"` so every existing
  caller that doesn't set it explicitly keeps today's behaviour
  byte-for-byte.
- `flushUpTo`'s per-segment sync call is now `state.doSync(f)`, a
  closed two-way switch: `"fsync"` calls the new `fullSync` helper
  (`unix.Fsync` on Linux, `f.Sync()` elsewhere — identical to
  `dataSync` on non-Linux, since there's no separate fdatasync
  primitive to skip there either); everything else runs the existing
  `dataSync` (`fdatasync`) path.
- `cmd/goopg/main.go` reads the GUC (`stringGUC(registry,
  "wal_sync_method", "fdatasync")`) and threads it through
  `initdb.OpenOptions.WALSyncMethod` → `wal.Config.SyncMethod`,
  mirroring the `wal_init_zero`/`wal_buffers` wiring already in place.

**Deliberately not implemented in this loop**: `open_sync` and
`open_datasync`. Upstream applies these via `O_SYNC`/`O_DSYNC` at
segment *open* time, not at flush time — every WAL segment-open site
(`openSegment`, preallocation, and potentially the AIO submission
path in `internal/aio` if segment I/O routes through it) would need
to become sync-method-aware, not just `flushUpTo`. `NewWriter` accepts
these two values at the GUC layer (so `SHOW wal_sync_method` /
`pg_settings` stay upstream-shaped) but rejects them at Writer
construction with `wal.ErrUnsupportedSyncMethod`, mirroring
`internal/aio`'s `io_method=io_uring` → `ErrUnsupportedMethod`
precedent (`internal/aio/aio.go`). See
`.ralph/deferral_ledger.md`'s 2026-07-08 row for the resume point.

Also out of scope: live reconfiguration on `SIGHUP`/`reload`. The GUC
is registered `ContextSigHup` for upstream-accurate metadata, but
goopg's `reload` control-socket command is still a documented no-op
stub (separate open item), and the WAL `Writer` reads `SyncMethod`
once at construction — same limitation every other
`ContextPostmaster`/`ContextSigHup` WAL GUC already has here.

Verification: `internal/wal/sync_method_test.go` — default resolves
to `fdatasync`; both `fsync` and `fdatasync` round-trip a real
Append→FlushUpTo→Close→ReadAll cycle; `open_sync`/`open_datasync`/an
arbitrary bogus value all fail `NewWriter` with
`errors.Is(err, ErrUnsupportedSyncMethod)`. Confirmed non-vacuous via
`git stash` on the implementation files alone (test file fails to
compile without them, not just fails an assertion).
