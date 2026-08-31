# 0102-0011 — initdb `-X`/`--waldir` external WAL directory option

**Milestone:** M0102-0010 (remaining initdb options; follow-up to M0102-0008)
**Status:** accepted
**Date:** 2026-06-13

## Problem

`goopg init` accepted only `-D` and (since the prior loop) `-U`/`--username`.
Upstream `initdb`'s `-X`/`--waldir` option — which relocates the
write-ahead-log directory outside the data directory — was unsupported, so the
corresponding subtests of `postgres/src/bin/initdb/t/001_initdb.pl` could not be
matched:

```perl
command_fails([ 'initdb', '--waldir' => $xlogdir, $datadir ],
    'existing nonempty xlog directory');
command_fails([ 'initdb', '--waldir' => 'pgxlog', $datadir ],
    'relative xlog directory not allowed');
```

(and the success path, where `--waldir => $xlogdir` participates in the larger
"successful creation" `command_ok` block alongside `--no-sync`,
`--text-search-config`, and `--set`.)

## Upstream behaviour mirrored

`postgres/src/bin/initdb/initdb.c` `create_xlog_or_symlink()` (the
`if (xlog_dir)` block, ~initdb.c:2955–3017):

1. **Absolute path required.** `canonicalize_path` then
   `if (!is_absolute_path(xlog_dir)) pg_fatal("WAL directory location must be an
   absolute path")`.
2. **`pg_check_dir` switch:**
   - case 0 (absent) → create it (`pg_mkdir_p`, `pg_dir_create_mode` = 0700).
   - case 1 (present, empty) → fix permissions (`chmod 0700`) and reuse.
   - cases 2/3/4 (present, not empty — case 4 is a mount-point with only
     `lost+found`) → `pg_log_error("directory \"%s\" exists but is not empty")`,
     `exit(1)`.
3. **Symlink.** `symlink(xlog_dir, subdirloc)` where `subdirloc` is
   `<PGDATA>/pg_wal`.

## Implementation

All changes are in `internal/initdb/initdb.go` and `cmd/goopg/main.go`; no
on-disk format change, and the default (no `WALDir`) layout is byte-identical to
before.

- **`Options.WALDir string`** — new field; empty means pg_wal lives under `-D` as
  a plain subdirectory (all existing callers unchanged).
- **Early absolute-path validation** in `Init`, placed right after the superuser
  `pg_` prefix check and *before* `ensureEmptyDir`/`MkdirAll`, so a relative
  `--waldir` fails fast without leaving a half-built data directory (mirrors the
  fail-fast posture of the `-U` `pg_` guard).
- **`setupWALDir(abs, walDir)`** helper mirrors the `pg_check_dir` switch:
  `os.ReadDir` → non-empty rejected (`"exists but is not empty"`), empty reused
  (`chmod 0700`), `ErrNotExist` created (`MkdirAll 0700`); then
  `os.Symlink(walDir, <abs>/pg_wal)`.
- **Subdir loop skip.** When `WALDir != ""`, the loop skips the literal `pg_wal`
  entry (now a symlink) but still creates `pg_wal/archive_status` and
  `pg_wal/summaries`; `os.Mkdir` follows the symlink so those land physically
  inside `walDir`, reproducing upstream's on-disk shape.
- **CLI wiring** (`runInit`): `-X` and `--waldir` both bind to one variable,
  passed as `Options.WALDir`.

## Tests

`internal/initdb/waldir_test.go`:

- `TestInitRejectsRelativeWALDir` — relative path rejected, no data dir created.
- `TestInitRejectsNonEmptyWALDir` — `walDir` holding a `lost+found` marker (as
  001_initdb.pl sets up) is rejected.
- `TestInitRelocatesWALDir` — non-existent `walDir`: `pg_wal` is a symlink to it
  and `archive_status`/`summaries` live inside it.
- `TestInitDefaultWALDirIsPlainSubdir` — default path: `pg_wal` is a plain dir,
  not a symlink.

CLI smoke-tested: `goopg init -D d -X /abs/extwal` creates `d/pg_wal ->
/abs/extwal` (exit 0); `--waldir relwal` exits 1 with the absolute-path message.

## Scope / deferral

This closes one of the remaining-initdb-options items enumerated under
M0102-0010. The full "successful creation" subtest of 001_initdb.pl additionally
needs `--no-sync`, `--text-search-config`, and `--set`, each a distinct
subsystem; they remain deferred (one option per loop, design doc first) per the
M0102-0010 deferral ledger. See `0102-0010-initdb-superuser-name-option.md` for
the `-U` predecessor.
