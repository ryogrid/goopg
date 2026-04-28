# Data Directory and Server Bootstrap (v0)

- Status: accepted
- Date: 2026-04-28
- Supersedes: —

## Problem

`.ralph/specs/GOAL_AND_REQUIREMENTS.md` §6.1 commits goopg to a data
directory layout that "mirrors PostgreSQL's at the directory level
(`base/`, `global/`, `pg_wal/`, `pg_xact/`, etc.) closely enough
that an operator familiar with PostgreSQL can navigate it." §10
makes `goopg init` and `goopg start` load-bearing for the initial
milestone (DoD #2: "`goopg init` creates a data directory; `goopg
start` brings up a server"). This document pins the v0 shape of
that layout, the `goopg init` and `goopg start -D` flows, and which
subsystems consume what from the on-disk tree.

## Upstream reference

- `postgres/src/bin/initdb/initdb.c` — the upstream tool. v0 borrows
  the directory list and the `PG_VERSION` sentinel; everything else
  (catalog bootstrap, default `template1`/`postgres`/`template0`,
  `pg_control`, `pg_filenode.map`, locale/encoding wiring) is
  deferred.
- `postgres/src/include/storage/relfilenode.h` — defines the
  `(spcNode, dbNode, relNode)` triple goopg's
  `internal/storage.RelFileNode` mirrors. v0 always uses the
  default tablespace, so `spcNode` is implicit; `dbNode` defaults
  to `catalog.DefaultDBOid` (1) and `relNode` is the in-memory
  catalog OID handed out from `FirstUserOID = 16384`.
- `postgres/src/include/catalog/catversion.h` — `CATALOG_VERSION_NO`.
  Goopg's `PG_VERSION` only carries the major version (`"18"`), not
  the catalog-version-no, because the on-disk catalog is not yet
  implemented. The major version is the same string the wire layer
  reports in `server_version` ParameterStatus — so a binary that
  advertises wire version 18.x can only open a directory that says
  "18".

## Layout

`goopg init -D <dir>` produces this tree (mode 0700 on every
directory; 0600 on the regular files):

```
<dir>/
├── PG_VERSION              # "18\n" — major version sentinel
├── postgresql.conf         # sample, all settings commented out
├── pg_hba.conf             # sample: loopback trust, reject everything else
├── base/
│   └── 1/                  # catalog.DefaultDBOid; per-relation files land here as <relOid>
├── global/                 # reserved for shared catalogs (empty in v0)
├── pg_wal/                 # WAL segment files (managed by internal/wal)
└── pg_xact/                # commit-log SLRU pages (reserved; empty in v0)
```

The file list is exposed as `initdb.Subdirs` and `initdb.SampleFiles()`
so tests and follow-up admin tooling can introspect the layout
without re-deriving it.

### Why these dirs and not more

The four subdirectories are the ones the server actually touches
in v0:

- `base/<dbOid>/<relOid>` — heap and index files. The path is
  produced by `internal/storage.smgr.relPath`; see
  `internal/storage/smgr.go:194`.
- `pg_wal/` — WAL segments. `internal/wal.OpenWriter` defaults to
  `<DataDir>/pg_wal` when `WALDir` is unset
  (`internal/wal/writer.go:29`).
- `global/` and `pg_xact/` — reserved so an operator who already
  thinks in upstream-PG terms doesn't get surprised by missing
  paths. They're empty in v0 because the shared catalog and the
  commit-log SLRU haven't landed.

Upstream initdb creates many more (`pg_commit_ts`, `pg_logical`,
`pg_multixact`, `pg_notify`, `pg_replslot`, `pg_serial`,
`pg_snapshots`, `pg_stat`, `pg_stat_tmp`, `pg_subtrans`,
`pg_tblspc`, `pg_twophase`). They'll be added one milestone at a
time, alongside the subsystem that fills them.

### Refusal contract

Init refuses to clobber a non-empty target directory (matching
`postgres/src/bin/initdb/initdb.c:check_data_directory()`'s "is
not empty" guard). An existing-but-empty directory is accepted so
operators can pre-create the mountpoint with the right ownership
before running init.

## Server bootstrap

`internal/initdb.Open(opts)` is the single seam between a path on
disk and the four runtime handles a goopg server needs. It:

1. Resolves `opts.DataDir` to an absolute path so subsequent
   logging and diagnostics use the canonical form.
2. Verifies the target is initialized — directory exists, is a
   directory, and `PG_VERSION` matches `CatalogVersion` exactly.
   The version match is intentionally strict so a binary upgrade
   can't silently corrupt a stale directory.
3. Constructs `storage.Manager` rooted at the data directory,
   `storage.Pool` of `PoolSlots` buffers (default 1024), an
   `mvcc.Manager`, and an empty `catalog.NewInMemory()`.
4. Returns a `Runtime` bundle whose `Close()` releases pool +
   smgr in order and is idempotent.

`cmd/goopg start -D <dir>` calls `initdb.Open` and forwards the
four handles into `Server.Config.{Catalog,Pool,TxnMgr}`. With
those set, the wire layer routes COPY (and, in future loops, every
other table-touching statement) through parser→planner→executor.
Without `-D`, the server still runs in the protocol-only fallback
mode — useful for the pure-protocol smoke tests and for the early
authentication/configuration loops that don't need storage.

## What's deliberately deferred

- **On-disk catalog.** Schema declared via SQL during a session
  (`CREATE TABLE`, `CREATE INDEX`, `ALTER TABLE`) currently
  vanishes when the process exits. The next loop will write a
  pg_class/pg_attribute encoder under `global/` so a restart can
  rebuild the in-memory catalog from disk. Heap data files
  themselves already persist via the storage manager — only the
  schema metadata is volatile.
- **`pg_control`.** Upstream's master control file holds the
  cluster-wide LSN, last-checkpoint info, and database-state
  enum. v0 has none of those concepts piped through to a single
  file yet; the WAL writer maintains its own LSN cursor in
  memory.
- **Tablespaces.** Every relation lives under
  `base/<DefaultDBOid>/`. `pg_tblspc/` will arrive when (if) we
  add `CREATE TABLESPACE`.
- **Multi-database.** The catalog hands out OIDs against
  `DefaultDBOid = 1`. `CREATE DATABASE` and the per-database
  subdirectory under `base/` land alongside the on-disk catalog.
- **Locale and encoding.** `LC_COLLATE`, `LC_CTYPE`, the encoding
  argument upstream takes — all hardcoded to UTF-8 / `C` collation
  via the wire-layer ParameterStatus block. A real `goopg init
  --locale` lands when collation matters.

## Test surface

- `internal/initdb/initdb_test.go` — directory layout, mode 0700,
  PG_VERSION contents, non-empty refusal, empty-existing
  acceptance, sample HBA defaults.
- `internal/initdb/open_test.go` — happy-path Open, missing-dir
  diagnostic, uninitialized-dir diagnostic, version-mismatch
  guard, idempotent Close.
- `cmd/goopg/main_test.go` — CLI integration: `goopg init -D
  <tmp>` materialises the layout; `goopg init` without `-D`
  exits 2 with a `-D` diagnostic.

## Migration path

Future loops that mutate the on-disk layout (system catalog files,
pg_control, additional reserved subdirectories) bump
`CatalogVersion` and update this doc. The strict
`PG_VERSION`-equality check in `Open` then becomes the migration
gate: an old data directory either gets explicitly upgraded or
fails to open, never silently misinterpreted.
