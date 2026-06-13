# 0102-0014 — initdb `-g`/`--allow-group-access` (M0102-0010)

Status: accepted

## Context

`goopg init` lays out the data directory with owner-only permissions: every
directory is created at `0o700` and every file at `0o600` (the goopg analogue
of upstream's `pg_dir_create_mode = PG_DIR_MODE_OWNER` / `pg_file_create_mode =
PG_FILE_MODE_OWNER`, `postgres/src/common/file_perm.c`). Upstream `initdb`
exposes `-g`/`--allow-group-access`, which relaxes the cluster so the owning
group may read and traverse it — directories become `0o750`
(`PG_DIR_MODE_GROUP`) and files `0o640` (`PG_FILE_MODE_GROUP`,
`src/include/common/file_perm.h:35,41`). PostgreSQL itself then refuses to
start a cluster whose permissions are neither owner-only nor exactly group mode,
so the on-disk modes are a compatibility surface, not a cosmetic detail.

`postgres/src/bin/initdb/t/001_initdb.pl` exercises this directly (lines
102-110):

```perl
command_ok([ 'initdb', '--allow-group-access', $datadir_group ],
    'successful creation with group access');
ok(check_mode_recursive($datadir_group, 0750, 0640),
    'check PGDATA permissions');
```

`check_mode_recursive` (`src/test/perl/PostgreSQL/Test/Utils.pm:599`) walks the
whole tree with `follow_fast => 1` and asserts **every** directory is `0750`
and **every** regular file is `0640` — including, via the followed `pg_wal`
symlink, a relocated external WAL directory.

## What upstream does

Two mechanisms, both in `initdb.c`:

1. **Permissions.** When `-g` is parsed, `SetDataDirectoryCreatePerm(
   PG_DIR_MODE_GROUP)` (`initdb.c:3360`, `file_perm.c:34`) sets the two global
   `pg_*_create_mode` variables to the group modes. Every subsequent `mkdir` /
   file create in the run uses those globals, so the tree is built at the group
   mode from the start.
2. **`log_file_mode`.** `setup_config` (`initdb.c:1421-1425`) seeds
   `log_file_mode = 0640` into `postgresql.conf` when group access is enabled,
   "so a backup from a user in the group would [not] fail if the log files were
   not relocated." It is written **unquoted** (`replace_guc_value(..., false)`).

## Decision

Add `Options.AllowGroupAccess bool` and an `init` CLI flag `-g` /
`--allow-group-access`, wired into both mechanisms:

### 1. `log_file_mode` seeding — `internal/initdb/config_seed.go`

`seedPostgresqlConf` gains an `allowGroupAccess bool` parameter. The seeding
order now mirrors `setup_config` exactly:

```
default_text_search_config   (-T,  initdb.c:1343-1346)
log_file_mode = 0640         (-g,  initdb.c:1421-1425)   ← new
<each -c/--set override>      (-c,  initdb.c:1430-1436)
```

Placing `log_file_mode` **before** the `-c`/`--set` loop matches upstream's
line order, so an explicit `-c log_file_mode=...` still overrides the
group-access default. The value `0640` reuses the existing `replaceGUCValue`
port and is written unquoted (`gucValueRequiresQuotes("0640")` is false: digits
+ no unit letters).

### 2. Permission relaxation — `internal/initdb/initdb.go`

goopg's layout is spread across ~40 hard-coded `0o700`/`0o600` literals in many
free helper functions (no shared builder to thread a mode through). Rather than
churn every site or introduce mutable package globals (which would race under
parallel `Init` calls in tests, unlike PG's single-process globals), Init lays
the tree out at owner mode as before and then, when `AllowGroupAccess` is set,
runs a single recursive relaxation pass `relaxToGroupAccess(abs)` **after** the
full layout but **before** the trailing fsync — so the relaxed modes are
flushed durably. The net on-disk result is identical to upstream's
create-at-group-mode: every directory `0o750`, every file `0o640`, which is
exactly the invariant `check_mode_recursive` validates.

`relaxToGroupAccess` / `chmodTreeGroup` mirror the traversal of the existing
`fsyncDataDir` / `walkAndFsync` pair: the top-level walk ignores symlinks, and a
relocated `pg_wal` (`-X`/`--waldir`) is descended through separately so its
external target and contents are relaxed too — necessary because
`check_mode_recursive` follows that symlink. The two passes agree on which
entries they touch.

## Alternatives considered

- **Mutable package globals (`dirCreateMode`/`fileCreateMode`)**, the closest
  structural mirror of `pg_*_create_mode`. Rejected: package-level mutable state
  set per-`Init`-call races under parallel tests and is a footgun for any future
  concurrent caller; PG gets away with it only because each `initdb` is its own
  process.
- **Thread a mode pair through every helper.** Rejected: ~40 call sites across
  many functions, high churn and merge-conflict surface for a behavior the
  single post-pass captures exactly.

## Faithfulness note

The only divergence from upstream is *when* the mode is applied (one trailing
pass vs. at each create). The observable result — the durable on-disk
permission tree and the seeded `log_file_mode` — is byte-for-byte what
`001_initdb.pl` asserts. No on-disk format change; the default (no `-g`) path is
untouched and remains owner-only.

## Testing

- `internal/initdb/group_access_test.go`:
  - `TestInitAllowGroupAccess` — Go port of `check_mode_recursive(0750, 0640)`
    plus the `log_file_mode = 0640` seeding assertion.
  - `TestInitDefaultIsOwnerOnly` — the default cluster is `0700`/`0600` and does
    not seed `log_file_mode` (guards against accidental relaxation).
  - `TestInitAllowGroupAccessWithWALDir` — `-g` + `-X` relaxes the external WAL
    directory reached through the `pg_wal` symlink.
- `cmd/goopg/main_test.go`: `TestInitCommandAllowGroupAccess` drives the full
  `init -D <dir> --allow-group-access` CLI and re-validates the recursive modes
  + seeded GUC.

## Scope

Touches only `internal/initdb` + `cmd/goopg`; no executor/planner/catalog/codec
path, so the TPC-H silent-regression spot-check gate does not apply. Fifth
initdb-option gap closed under M0102-0010.
