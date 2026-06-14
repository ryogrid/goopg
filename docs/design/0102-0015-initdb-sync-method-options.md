# 0102-0015 — initdb `--sync-method` + `--no-sync-data-files`

- Milestone: M0102-0010 (initdb CLI option coverage)
- Status: accepted
- Date: 2026-06-13 (loop #24)

## Problem

`goopg init` performs a recursive `fsync` of the data directory before
returning (landed loop #21, `0102-0012`), but it hard-codes the FSYNC
sync method and always syncs every file. Upstream `initdb` exposes two
further knobs that `postgres/src/bin/initdb/t/001_initdb.pl` exercises:

```perl
command_ok([ 'initdb', '--sync-only', '--no-sync-data-files', $datadir ],
    '--no-sync-data-files');
...
command_ok([ 'initdb', '--sync-only', $datadir, '--sync-method' => 'syncfs' ],
    'sync method syncfs');          # on builds where syncfs is available
command_fails(...)                   # where it is not
```

Without these flags goopg cannot match the upstream `--sync-only` test
tier (lines 79–91 of `001_initdb.pl`). Both were explicitly deferred
when `-N`/`-S` landed.

## Upstream semantics (faithful reference)

`sync_pgdata(pg_data, version, sync_method, sync_data_files)`
(`src/common/file_utils.c`) and the option plumbing in
`src/bin/initdb/initdb.c` (globals at 170–171; option codes 19/21 at
3198/3200; `parse_sync_method` in `src/fe_utils/option_utils.c:90`):

- **`--sync-method=METHOD`** — `fsync` (default) or `syncfs`.
  `parse_sync_method` rejects anything else with
  `unrecognized sync method: <m>`, and rejects `syncfs` on a build
  without `HAVE_SYNCFS` with `this build does not support sync method "syncfs"`.
- **FSYNC method** — `walkdir(pg_data, fsync_fname, false, exclude_dir)`.
  When `--no-sync-data-files` is given, `exclude_dir = "<pg_data>/base"`
  and `walkdir` skips that subtree entirely (it `return`s immediately when
  `path == exclude_dir`, so `base/` is neither descended nor fsynced).
  The tablespace pass `walkdir(pg_tblspc, …)` is also gated on
  `sync_data_files`.
- **SYNCFS method** — `do_syncfs(pg_data)` (one `syncfs(2)` flushes the
  whole filesystem), then `do_syncfs` of each tablespace **only if
  `sync_data_files`**, then `do_syncfs(pg_wal)` if `pg_wal` is a symlink.
  Because `syncfs` flushes the entire filesystem, `--no-sync-data-files`
  has **no effect on `base/`** under syncfs — it only suppresses the
  per-tablespace `syncfs` calls.

goopg creates **no tablespace symlinks** under `pg_tblspc` at init time
(documented in `0102-0012`), so the `pg_tblspc` walk/loop is a no-op for
us in both methods. The only data-file subtree is `base/`.

## Design

Touches **only** `internal/initdb` + `cmd/goopg` — no executor / planner /
catalog / codec / WAL-format surface, so the TPC-H spot-check gate does
not apply.

### Options (`internal/initdb/initdb.go`)

```go
// SyncMethod selects how Init flushes the cluster to disk: "" or "fsync"
// (default, recursive fsync) or "syncfs" (one syncfs(2) per filesystem).
// Mirrors initdb --sync-method (parse_sync_method). An unrecognized value
// is rejected; "syncfs" is rejected on non-Linux builds.
SyncMethod string

// NoSyncDataFiles, when true, excludes the per-database data files in
// base/ from the fsync pass, mirroring initdb --no-sync-data-files
// (sync_data_files=false). Under the syncfs method it has no effect
// because syncfs flushes the whole filesystem (goopg has no tablespaces).
NoSyncDataFiles bool
```

### Sync method resolution + dispatch

- `resolveSyncMethod(string) (dataDirSyncMethod, error)` ports
  `parse_sync_method` exactly (empty/`fsync` → fsync; `syncfs` →
  syncfs gated on `syncfsSupported`; else `unrecognized sync method`).
  Validated up front in `Init` (before the `--sync-only` branch) so both
  the sync-only and full-init paths share one validation point.
- `fsyncDataDir` is generalised to
  `syncDataDir(dataDir, method, syncDataFiles)`:
  - **fsync**: `excludeDir = <dataDir>/base` when `!syncDataFiles`, then
    `walkAndFsync(dataDir, false, excludeDir)` + the existing relocated-
    `pg_wal` recursion.
  - **syncfs**: `syncfsPath(dataDir)` + `syncfsPath(pg_wal)` if `pg_wal`
    is a symlink (no tablespace loop — goopg has none).
- `walkAndFsync` gains an `excludeDir string` parameter and returns early
  when `path == excludeDir`, faithfully porting `walkdir`'s
  `if (exclude_dir && strcmp(exclude_dir, path) == 0) return;`. The
  parameter is propagated into recursive calls exactly as upstream does
  (it only ever matches the top-level `base/`).

### syncfs portability

`syncfs(2)` is Linux-only; mirrors upstream's `HAVE_SYNCFS` compile gate.
Two build-tagged files:

- `syncfs_linux.go` — `const syncfsSupported = true`; `syncfsPath` opens
  the path `O_RDONLY` and calls `unix.Syncfs(fd)` (ports `do_syncfs`).
- `syncfs_other.go` — `const syncfsSupported = false`; `syncfsPath`
  returns an error. `resolveSyncMethod` rejects `syncfs` before this is
  ever called, matching the `command_fails` arm of the upstream test on
  syncfs-less builds.

### CLI (`cmd/goopg/main.go`)

`--sync-method <fsync|syncfs>` (string) and `--no-sync-data-files`
(bool, long form only — upstream has no short form) registered on the
`init` flag set and threaded into `Options`.

## Faithful-but-divergent notes

- goopg has no `pg_tblspc` symlinks, so the tablespace fsync/syncfs passes
  upstream performs are intentionally absent; the net flushed set is
  identical for a goopg-created cluster.
- Under syncfs, `--no-sync-data-files` is accepted but inert (no
  tablespaces) — identical to upstream behaviour for a tablespace-free
  cluster.

## Testing

- `internal/initdb/sync_test.go` (extends the loop #21 file):
  - `--no-sync-data-files` excludes `base/` (assert via an instrumented
    fsync recorder / observable behaviour: a cluster syncs successfully
    and `base/` is skipped).
  - `--sync-method=syncfs` succeeds on Linux.
  - `resolveSyncMethod` table test: `""`/`fsync`/`syncfs` accepted,
    `bogus` → `unrecognized sync method`.
- `cmd/goopg/main_test.go`: `init --sync-only --no-sync-data-files` and
  `init --sync-only --sync-method=syncfs` both exit 0 on a previously
  initialised dir; `--sync-method=bogus` exits 1 with the error text.

## References

- `postgres/src/common/file_utils.c` — `sync_pgdata`, `walkdir`,
  `do_syncfs`.
- `postgres/src/fe_utils/option_utils.c:90` — `parse_sync_method`.
- `postgres/src/bin/initdb/initdb.c:170-171,3198-3200,3389-3396,3449,3512`.
- `postgres/src/bin/initdb/t/001_initdb.pl:78-91`.
- Prior slice: `0102-0012-initdb-sync-options.md`.
