# 01 — Data Directory Layout

**Status:** draft
**Date:** 2026-05-19
**Audience:** goopg `internal/initdb/` implementers.

---

## Scope

This file fully specifies the **directory and flat-file skeleton** of
`$PGDATA`:

- Every directory laid down by upstream `initdb` (the 23-entry
  `subdirs[]` array plus `$PGDATA` itself, `pg_wal/`, and `base/1/`).
- Every flat text file written at the `$PGDATA` root or in
  `base/<dboid>/` (`PG_VERSION`, `postgresql.conf`,
  `postgresql.auto.conf`, `pg_hba.conf`, `pg_ident.conf`).
- The runtime mutations that add or remove directory entries after
  `initdb` finishes — `CREATE DATABASE`, `CREATE TABLESPACE`, slot
  lifecycle, WAL archiving, WAL summarization, logical decoding.

The byte-level contents of files inside these directories are covered
in sibling docs:

| Artefact | Doc |
|----------|-----|
| `global/pg_control` | [`02-pg-control-and-checkpoint.md`](02-pg-control-and-checkpoint.md) |
| `pg_wal/000000010000000000000001` (first WAL segment) | [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md) |
| `global/*` catalog heaps + critical indexes | [`04-shared-catalog-bootstrap.md`](04-shared-catalog-bootstrap.md) |
| `base/<dboid>/*` catalog heaps + critical indexes | [`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md) |
| `pg_rewrite`, `pg_stat_wal_receiver`, system views | [`07-system-views-and-pg-rewrite.md`](07-system-views-and-pg-rewrite.md) |
| `pg_internal.init` byte layout | [`08-relcache-init-and-version-files.md`](08-relcache-init-and-version-files.md) |
| `standby.signal`, `recovery.signal`, timeline history, slot state files | [`09-streaming-replication-readiness.md`](09-streaming-replication-readiness.md) |

## Upstream references

All `src/...` paths are relative to `postgres/` in the goopg repo.
Line numbers are against the vendored PG 18.3 tree.

- `src/bin/initdb/initdb.c` — `subdirs[]:231`, `create_data_directory:2890`,
  `create_xlog_or_symlink:2948`, `initialize_data_directory:3044`,
  `write_version_file:1024`, `set_null_conf:1047`, `setup_config:1283`.
- `src/backend/backup/basebackup.c` — `excludeDirContents[]:151`,
  `excludeFiles[]:191`.
- `src/backend/commands/dbcommands.c` — `createdb:684`,
  `XLOG_DBASE_CREATE_WAL_LOG:536`, `XLOG_DBASE_CREATE_FILE_COPY:633`.
- `src/backend/commands/tablespace.c` — `CreateTableSpace:208`,
  `create_tablespace_directories:572`, `XLOG_TBLSPC_CREATE:370`.
- `src/backend/replication/slot.c` — `CreateSlotOnDisk:2258`,
  `ReplicationSlotDropPtr:976`.
- `src/backend/access/transam/xlogarchive.c` — `XLogArchiveNotify:444`.
- `src/backend/postmaster/walsummarizer.c` — `XLOGDIR "/summaries":1202`;
  `src/backend/backup/walsummary.c:49`.
- `src/backend/replication/logical/snapbuild.c` —
  `PG_LOGICAL_SNAPSHOTS_DIR` uses at `:1533, :1577, :1751`.
- `src/backend/replication/logical/reorderbuffer.c` —
  `PG_LOGICAL_MAPPINGS_DIR` use at `:5341`.
- `src/include/replication/reorderbuffer.h:22-24` — `PG_LOGICAL_DIR`,
  `PG_LOGICAL_MAPPINGS_DIR`, `PG_LOGICAL_SNAPSHOTS_DIR`.
- `src/include/pgstat.h:35` — `PG_STAT_TMP_DIR`.
- `src/include/storage/dsm_impl.h:51` — `PG_DYNSHMEM_DIR`.
- `src/include/replication/slot.h:21` — `PG_REPLSLOT_DIR`.

## Initdb-time output

`initialize_data_directory()` (`initdb.c:3044`) is the single entry
point. It runs, in order:

1. `create_data_directory()` (`initdb.c:2890`) — `mkdir -p $PGDATA`.
2. `create_xlog_or_symlink()` (`initdb.c:2948`) — creates `$PGDATA/pg_wal`
   either as a plain directory or as a symlink to the `-X xlog_dir`
   location (in the symlink case the target directory is created
   first).
3. A single loop over `subdirs[]` at `initdb.c:3068` that calls bare
   `mkdir(2)` (not `mkdir -p`) on each entry — every parent has already
   been created by an earlier element of the array, which is why the
   element order is significant (e.g. `pg_multixact` precedes
   `pg_multixact/members`).
4. `write_version_file(NULL)` (`initdb.c:1024, 3087`) — writes
   `$PGDATA/PG_VERSION` containing `PG_MAJORVERSION\n` (`"18\n"` for
   PG 18.x).
5. `set_null_conf()` (`initdb.c:1047, 3090`) — touches an empty
   `postgresql.conf` so the bootstrap backend has a file to read.
6. `setup_config()` (`initdb.c:1283, 3094`) — overwrites
   `postgresql.conf` with templated values and writes the three other
   flat files: `postgresql.auto.conf` (`initdb.c:1446`), `pg_hba.conf`
   (`initdb.c:1520`), `pg_ident.conf` (`initdb.c:1531`).
7. `bootstrap_template1()` then runs the BKI bootstrap backend (out of
   scope here, see `04-`/`05-`) and finally
   `write_version_file("base/1")` (`initdb.c:1024, 3102`) is invoked.

### Directory inventory

| # | Path | Created by | Purpose | basebackup-excluded? | Citation |
|---|------|------------|---------|----------------------|----------|
|  0 | `$PGDATA/` | `create_data_directory` | Cluster root | n/a | `initdb.c:2890` |
|  1 | `global/` | `subdirs[0]` loop | Cluster-wide catalogs + `pg_control` | no | `initdb.c:232` |
|  2 | `pg_wal/` | `create_xlog_or_symlink` (dir or symlink) | WAL segments | no | `initdb.c:2948` |
|  3 | `pg_wal/archive_status/` | `subdirs[1]` | `*.ready` / `*.done` flag files emitted by archiver | no | `initdb.c:233`; `xlogarchive.c:444` |
|  4 | `pg_wal/summaries/` | `subdirs[2]` | WAL-summary files for incremental backup (PG18) | no | `initdb.c:234`; `walsummary.c:49` |
|  5 | `pg_commit_ts/` | `subdirs[3]` | SLRU: commit-timestamp segments | no | `initdb.c:235` |
|  6 | `pg_dynshmem/` | `subdirs[4]` | mmap-backed DSM segments | **yes** (contents) | `initdb.c:236`; `basebackup.c:167` |
|  7 | `pg_notify/` | `subdirs[5]` | LISTEN/NOTIFY queue (SLRU) | **yes** | `initdb.c:237`; `basebackup.c:170` |
|  8 | `pg_serial/` | `subdirs[6]` | SLRU: serializable-snapshot conflict tracking | **yes** | `initdb.c:238`; `basebackup.c:176` |
|  9 | `pg_snapshots/` | `subdirs[7]` | Exported snapshot files | **yes** | `initdb.c:239`; `basebackup.c:179` |
| 10 | `pg_subtrans/` | `subdirs[8]` | SLRU: subtransaction parent table | **yes** | `initdb.c:240`; `basebackup.c:182` |
| 11 | `pg_twophase/` | `subdirs[9]` | Two-phase commit state files | no | `initdb.c:241` |
| 12 | `pg_multixact/` | `subdirs[10]` | SLRU group root | no | `initdb.c:242` |
| 13 | `pg_multixact/members/` | `subdirs[11]` | SLRU: multixact member array | no | `initdb.c:243` |
| 14 | `pg_multixact/offsets/` | `subdirs[12]` | SLRU: multixact offset index | no | `initdb.c:244` |
| 15 | `base/` | `subdirs[13]` | Per-database directory root | no | `initdb.c:245` |
| 16 | `base/1/` | `subdirs[14]` | `template1` heap+index files | no | `initdb.c:246` |
| 17 | `pg_replslot/` | `subdirs[15]` | Replication-slot state-file directories | **yes** (contents) | `initdb.c:247`; `basebackup.c:164` |
| 18 | `pg_tblspc/` | `subdirs[16]` | Symlinks to non-default tablespace locations | no | `initdb.c:248` |
| 19 | `pg_stat/` | `subdirs[17]` | Persistent cumulative statistics files | no | `initdb.c:249` |
| 20 | `pg_stat_tmp/` | `subdirs[18]` | Transient stats / `pg_stat_statements` working files | **yes** | `initdb.c:250`; `basebackup.c:157` |
| 21 | `pg_xact/` | `subdirs[19]` | SLRU: clog (commit log) | no | `initdb.c:251` |
| 22 | `pg_logical/` | `subdirs[20]` | Logical-decoding root | no | `initdb.c:252` |
| 23 | `pg_logical/snapshots/` | `subdirs[21]` | Serialized SnapBuild snapshots | no | `initdb.c:253` |
| 24 | `pg_logical/mappings/` | `subdirs[22]` | rewrite-mapping files for catalog DDL replay | no | `initdb.c:254` |

"basebackup-excluded? **yes**" means `pg_basebackup` keeps the empty
directory but elides its contents (`excludeDirContents[]` in
`basebackup.c:151`).

### Flat-file inventory

| Path | Written by | Content | basebackup-excluded? | Citation |
|------|------------|---------|----------------------|----------|
| `$PGDATA/PG_VERSION` | `write_version_file(NULL)` | `"18\n"` (`PG_MAJORVERSION`) | no | `initdb.c:1024, 3087` |
| `$PGDATA/postgresql.conf` | `set_null_conf` then `setup_config` | Templated GUC defaults from `postgresql.conf.sample` | no | `initdb.c:1047, 1296-1441` |
| `$PGDATA/postgresql.auto.conf` | `setup_config` | Two-line header (`ALTER SYSTEM` target) | no | `initdb.c:1446-1457` |
| `$PGDATA/pg_hba.conf` | `setup_config` | Templated from `pg_hba.conf.sample`; auth methods + IPv6 lines substituted | no | `initdb.c:1460-1524` |
| `$PGDATA/pg_ident.conf` | `setup_config` | Verbatim copy of `pg_ident.conf.sample` | no | `initdb.c:1527-1535` |
| `$PGDATA/base/1/PG_VERSION` | `write_version_file("base/1")` | `"18\n"` | no | `initdb.c:1024, 3102` |

`basebackup.c:191` (`excludeFiles[]`) additionally elides files that
*may* exist in a running cluster but are never part of the canonical
on-disk skeleton: `postgresql.auto.conf.tmp`, `current_logfiles.tmp`,
any `pg_internal.init*`, `backup_label`, `tablespace_map`,
`backup_manifest`, `postmaster.pid`, `postmaster.opts`.

### Per-database scaffolding

`initdb` creates exactly one per-database directory: `base/1/`
(template1). Both `postgres` (OID 5) and `template0` (OID 4) come
later, in the SQL phase of `initialize_data_directory`:

- `make_template0(cmdfd)` (`initdb.c:3146`) executes `CREATE DATABASE
  template0 IS_TEMPLATE = true ALLOW_CONNECTIONS = false`.
- `make_postgres(cmdfd)` (`initdb.c:3148`) executes `CREATE DATABASE
  postgres`.

Both routes go through the SQL-level `createdb()` function in
`dbcommands.c:684`, which has two strategies controlled by the
`STRATEGY` clause:

- `CREATEDB_FILE_COPY` (`dbcommands.c:84`) — `cp -r base/<src>/
  base/<new>/`, emit one `XLOG_DBASE_CREATE_FILE_COPY` per tablespace
  (`dbcommands.c:633`). This is the strategy used implicitly by
  `make_template0` / `make_postgres` because each runs inside its own
  utility statement and `template1` is fully built before either runs.
- `CREATEDB_WAL_LOG` (`dbcommands.c:84, 736`) — block-level copy with
  full-page WAL logging via `XLOG_DBASE_CREATE_WAL_LOG`
  (`dbcommands.c:536`). Default for user-driven `CREATE DATABASE`.

Either strategy ends with the new `base/<dboid>/` populated with every
file from the source directory (heap, indexes, `PG_VERSION`,
`pg_filenode.map`, the eventual `pg_internal.init`).

## Continuous maintenance

`$PGDATA` mutates after `initdb` mostly via local-disk-only operations
plus a handful of WAL-logged ones. The table is keyed by the operation
goopg may originate or replay; rules apply equally on primary and
standby (replay paths cited where they differ).

| Event | Path change | WAL? | Citation |
|-------|-------------|------|----------|
| `CREATE DATABASE <name>` (FILE_COPY) | `mkdir base/<dboid>/`; file-copy template | `XLOG_DBASE_CREATE_FILE_COPY` | `dbcommands.c:633, 2216` |
| `CREATE DATABASE <name>` (WAL_LOG; default) | `mkdir base/<dboid>/`; per-block WAL | `XLOG_DBASE_CREATE_WAL_LOG` | `dbcommands.c:536` |
| `DROP DATABASE` | `rmtree base/<dboid>/` | `XLOG_DBASE_DROP` | `dbcommands.c` (`dropdb`) |
| `CREATE TABLESPACE` | `symlink pg_tblspc/<oid> -> <location>`; `mkdir <location>/PG_18_<catver>_<dboid>` lazily | `XLOG_TBLSPC_CREATE` | `tablespace.c:357, 370` |
| `DROP TABLESPACE` | `unlink pg_tblspc/<oid>` | `XLOG_TBLSPC_DROP` | `tablespace.c` (`DropTableSpace`) |
| `pg_create_physical_replication_slot` / `pg_create_logical_replication_slot` | `mkdir pg_replslot/<name>.tmp`, `state` file, atomic rename → `pg_replslot/<name>/` | no (local only) | `slot.c:2258` |
| Slot drop | `rmtree pg_replslot/<name>/` | no | `slot.c:976` |
| WAL archiving enabled (`archive_mode != off`) | Archiver writes `pg_wal/archive_status/<seg>.ready` then `.done` | no | `xlogarchive.c:444` |
| WAL summarizer enabled (`summarize_wal = on`, PG18) | `pg_wal/summaries/<TLI><start><end>.summary` written via `temp.summary` rename | no | `walsummarizer.c:1202` |
| Logical-decoding consistent point reached | `pg_logical/snapshots/<lsn>.snap` serialized | no | `snapbuild.c:1533, 1577` |
| Catalog DDL during logical decoding | `pg_logical/mappings/map-...` files emitted | no | `reorderbuffer.c:5341` |
| Checkpoint | Cleans stale `pg_logical/snapshots/` entries; rewrites `global/pg_control` (see `02-`) | no for the dir, yes for `pg_control` | `snapbuild.c:1976-1993` |
| Promotion (standby → primary) | Adds new TLI history file `pg_wal/<newtli>.history`; the old `standby.signal` is removed | TLI WAL record | see `09-` |

Notes:

- Slot creation is intentionally local-disk-only — the on-disk slot
  directory exists so the slot survives crash but slot creation is
  *not* WAL-logged, which is why standbys never see slot directories
  appear during replay.
- `pg_dynshmem/`, `pg_notify/`, `pg_serial/`, `pg_snapshots/`,
  `pg_subtrans/`, `pg_stat_tmp/` are all rebuilt-on-startup directories.
  goopg must keep them present but may safely truncate their contents
  on every boot (this matches `dsm_cleanup_for_mmap`, `AsyncShmemInit`,
  `SerialInit`, `DeleteAllExportedSnapshotFiles`, `StartupSUBTRANS`).

## What goopg must produce

Diff against the current `internal/initdb/initdb.go::Subdirs` (line 83)
and `SampleFiles()` (line 125). Status uses the README legend.

### Directories

| Path | goopg status | Owner function | Notes |
|------|--------------|----------------|-------|
| `global/`, `base/`, `base/1/` | `done` | `Init` (line 159) and the `defaultDB` block (line 170) | `defaultDB` currently uses `catalog.DefaultDBOid=1`. |
| `base/5/` (postgres DB) | `partial` | `bootstrapSharedCatalogPlaceholders` (`initdb.go:594`) — implicit | Created as a side-effect of placeholder writes; should become an explicit `mkdir` in a `createPerDatabaseScaffolding(dbOid)` helper modelled on `make_postgres`. |
| `base/4/` (template0) | `missing` | new `createPerDatabaseScaffolding(4, "template0")` | Required for PG `pg_database` consistency; even an empty directory is enough until `09-` mounts the relcache file. |
| `pg_wal/`, `pg_wal/archive_status/`, `pg_wal/summaries/` | `done` | `Subdirs[2..4]` | Symlink form (`initdb -X`) not yet supported. |
| `pg_xact/`, `pg_subtrans/`, `pg_multixact/{,members,offsets}`, `pg_commit_ts/`, `pg_twophase/` | `done` | `Subdirs` | |
| `pg_dynshmem/`, `pg_notify/`, `pg_serial/`, `pg_snapshots/`, `pg_stat/`, `pg_stat_tmp/`, `pg_replslot/`, `pg_tblspc/` | `done` | `Subdirs` | |
| `pg_logical/`, `pg_logical/snapshots/`, `pg_logical/mappings/` | `done` | `Subdirs[16..18]` | |
| `pg_wal/` symlink form | `missing` | extend `Init` with optional `-X xlog_dir` | Mirrors `create_xlog_or_symlink` (`initdb.c:2948`). Tracked as low priority. |

### Flat files

| Path | goopg status | Owner | Notes |
|------|--------------|-------|-------|
| `PG_VERSION` (root) | `done` | `SampleFiles()[0]` (`initdb.go:127`) | Content `"18\n"` matches. |
| `base/<dboid>/PG_VERSION` | `missing` | extend `createPerDatabaseScaffolding` | Currently only the root `PG_VERSION` is written; PG's `ReadMyDatabaseInfo` reads the per-DB file. Add for OIDs `1, 4, 5`. |
| `postgresql.conf` | `done` | `defaultPostgresqlConf` | |
| `postgresql.auto.conf` | `missing` | new builder `defaultPostgresqlAutoConf` returning the two-line `ALTER SYSTEM` header | Required so `ALTER SYSTEM` works against a goopg-initialised cluster and so a vanilla `pg_basebackup` clone is well-formed. |
| `pg_hba.conf`, `pg_ident.conf` | `done` | `defaultPgHBAConf`, `defaultPgIdentConf` | |
| `global/pg_filenode.map` | `done` (out of scope here) | `defaultRelMapFile` | Byte layout covered by `04-`. |

### Continuous-maintenance hooks

| Operation | goopg status | Owner package |
|-----------|--------------|---------------|
| `CREATE DATABASE` → `mkdir base/<dboid>/` + `PG_VERSION` + relfile copy + WAL record | `missing` | future `internal/commands/dbcommands` |
| `CREATE TABLESPACE` → `pg_tblspc/<oid>` symlink + WAL record | `missing` | future `internal/commands/tablespace` |
| Slot create / drop → `pg_replslot/<name>/` | `missing` | future `internal/replication/slot` |
| `pg_wal/archive_status/*.ready` writer | `missing` | future archiver in `internal/wal` |
| `pg_wal/summaries/*.summary` writer (PG18) | `missing` | future `internal/walsummarizer` |
| `pg_logical/snapshots`, `pg_logical/mappings` | `missing` | future `internal/replication/logical` |

For the M0106 `TestE2E_FailoverGoopgToPG/async` test the only
"continuous" requirement is that *empty* directories already exist and
that `pg_basebackup` cleanly copies them — which is satisfied today by
`Subdirs`. The runtime writers in the table above only become required
once the test matrix grows beyond async-failover smoke tests; the
M0106 spec set merely names the owners.

## Verification

Two cheap, deterministic checks.

1. **Directory-set diff against upstream initdb.** From a fresh
   environment:

   ```bash
   initdb -D /tmp/pgref >/dev/null
   goopg init -D /tmp/pggoo >/dev/null
   diff \
     <(cd /tmp/pgref && find . -type d -o -type l | sort) \
     <(cd /tmp/pggoo && find . -type d -o -type l | sort)
   ```

   Expected diff: empty modulo the lines noted under "What goopg must
   produce" (`base/4/`, optionally `base/5/`) and any tablespace
   symlinks the user added.

2. **Flat-file presence + content sanity.**

   ```bash
   for f in PG_VERSION postgresql.conf postgresql.auto.conf \
            pg_hba.conf pg_ident.conf base/1/PG_VERSION; do
     test -s /tmp/pggoo/$f || echo "MISSING: $f"
   done
   test "$(cat /tmp/pggoo/PG_VERSION)" = "18" || echo "PG_VERSION mismatch"
   ```

3. **`pg_basebackup` smoke test against a goopg primary.** After
   `goopg start` and a single dummy transaction:

   ```bash
   pg_basebackup -h 127.0.0.1 -p 5433 -D /tmp/pgclone -X stream
   diff \
     <(cd /tmp/pggoo && find . -type d | sort) \
     <(cd /tmp/pgclone && find . -type d | sort) \
     | grep -vE '^(<|>).*(pg_stat_tmp|pg_replslot|pg_dynshmem|pg_notify|pg_serial|pg_snapshots|pg_subtrans)/'
   ```

   The grep mask drops the seven `excludeDirContents[]` entries from
   `basebackup.c:151`. The remaining diff must be empty; any output
   indicates a layout mismatch that streaming replication will
   eventually trip over.

4. **Standby attach.** Once `02-` and `03-` are also implemented, a
   vanilla PG18 standby pointed at a goopg primary via `primary_conninfo`
   plus `standby.signal` must reach `consistent recovery state` without
   `could not open directory ...` errors. Any such error indicates a
   directory listed in the inventory above is missing on the primary
   and was therefore not streamed to the standby.
