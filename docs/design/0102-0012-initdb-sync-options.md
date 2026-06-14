# 0102-0012 — initdb `-N`/`--no-sync` and `-S`/`--sync-only` fsync control

**Milestone:** M0102-0010 (initdb CLI option coverage)
**Status:** accepted
**Author:** Ralph (loop #21, 2026-06-13)

## Problem

`goopg init` wrote a fresh data directory and returned **without ever
fsyncing it**. Upstream `initdb`, by default, recursively flushes the whole
cluster to disk before exiting (`sync_pgdata`, `initdb.c:3512`) so a host
crash immediately after `initdb` cannot leave a torn cluster. Two CLI options
control that behavior, and `postgres/src/bin/initdb/t/001_initdb.pl` exercises
both:

- `-N`/`--no-sync` — skip the trailing fsync (faster, non-durable). Used
  throughout the TAP suite to keep test runs cheap.
- `-S`/`--sync-only` — fsync an **already-initialized** data directory and
  exit *without* creating a cluster. Used by `pg_upgrade`. The TAP suite
  asserts it **fails** on a missing directory and **succeeds** on a valid one:
  ```
  command_fails([ 'initdb', '--sync-only', "$tempdir/nonexistent" ], ...);
  command_ok(   [ 'initdb', '--sync-only', $datadir ],              ...);
  ```

goopg supported neither, and (more importantly) did no fsync at all, so a
default `goopg init` was silently less durable than upstream.

## Change

### `internal/initdb`

- `Options.NoSync bool` — when true, skip the trailing fsync.
- `Options.SyncOnly bool` — when true, fsync an existing data directory and
  return without laying out a cluster.
- `Init` gained two touch points:
  1. **Early sync-only branch** (right after resolving the absolute data-dir
     path, before any layout/validation): `os.Stat` the directory; if it is
     missing or not a directory, return
     `could not access directory %q` — mirroring `initdb.c:3444`'s
     `pg_check_dir(pg_data) <= 0 → pg_fatal("could not access directory")`.
     Otherwise call `fsyncDataDir` and return. This branch never mutates the
     filesystem.
  2. **Trailing fsync** (after `writePgControl`): unless `NoSync` is set, call
     `fsyncDataDir(abs)`, mirroring the default `sync_pgdata` at the end of
     `initdb` main.
- New helpers, mirroring `src/common/file_utils.c` with the default **FSYNC**
  sync method:
  - `fsyncDataDir(dataDir)` — walk the tree fsyncing every regular file and
    directory. The top-level walk ignores symlinks (exactly as `sync_pgdata`
    does), then, if `<dataDir>/pg_wal` is a symlink to a relocated WAL
    directory, recurses through that link separately (the `xlog_is_symlink`
    pass). goopg init creates no tablespace symlinks under `pg_tblspc`, so
    there is no second `process_symlinks` pass.
  - `walkAndFsync(path, followTopSymlink)` — the `walkdir` analogue: recurse
    into children (never following symlinks in subdirectories, matching
    upstream's deliberate choice), then fsync the directory itself after its
    children.
  - `fsyncPath(path, isDir)` — the `fsync_fname_ext` analogue: open
    read-only and `Sync()`. Tolerates the same benign errors upstream does —
    `EACCES` on open (and `EISDIR` for directories), and `EBADF`/`EINVAL` on
    the fsync of a directory opened `O_RDONLY` (some filesystems reject it;
    that is not a durability failure).

### `cmd/goopg/main.go`

`runInit` registers `-N`/`--no-sync` and `-S`/`--sync-only` (both short and
long forms bind to the same variable) and threads them into `Options`. The
success line is mode-aware: `synced data directory` for sync-only,
`created data directory` otherwise.

## Oracle mirror

| goopg | upstream |
|-------|----------|
| trailing `fsyncDataDir` unless `NoSync` | `initdb.c:3512` `sync_pgdata(...)`, gated on `!noclean`/`do_sync` |
| sync-only early branch | `initdb.c:3439` `if (sync_only) { ... pg_check_dir(...) <= 0 → pg_fatal; sync_pgdata; return; }` |
| `fsyncDataDir` / `walkAndFsync` / `fsyncPath` | `src/common/file_utils.c` `sync_pgdata` / `walkdir` / `fsync_fname_ext` (FSYNC method) |

## Scope / deferrals

`--sync-method` (the `syncfs` Linux syscall path) and `--no-sync-data-files`
(exclude `base/` from the sync) are **deferred** — `syncfs` is a distinct raw
syscall subsystem and `--no-sync-data-files` is a sub-knob of the sync method.
goopg always uses the per-file FSYNC method, which is correct and portable.
These remain tracked under M0102-0010's remaining-options list and in the
deferral ledger.

## Tests

`internal/initdb/sync_test.go`:

- `TestInitSyncOnlyRejectsMissingDir` — `--sync-only` on a nonexistent path
  fails with `could not access directory` and creates nothing.
- `TestInitSyncOnlyRejectsFile` — a regular-file path is likewise rejected.
- `TestInitSyncOnlyExistingDir` — `--sync-only` on a real cluster succeeds and
  leaves `pg_control` byte-for-byte unchanged (flush, not rewrite).
- `TestInitNoSyncStillCreatesCluster` — `--no-sync` still lays out the full
  tree (PG_VERSION, global/pg_control, base, pg_wal).
- `TestInitDefaultSyncsCleanly` — the default fsync walk tolerates everything
  initdb writes.
- `TestInitSyncOnlyFollowsExternalWALSymlink` — the sync walk descends into a
  relocated `pg_wal` symlink (external WAL dir) without error.

## Risk

Low and confined to `internal/initdb` + `cmd/goopg`. No executor / planner /
catalog / codec / on-disk-format change, so the TPC-H spot-check gate does not
apply. The one behavioral change for existing callers is that a default
`goopg init` now fsyncs (slower, more durable) — callers that want the old
speed pass `NoSync: true`.
