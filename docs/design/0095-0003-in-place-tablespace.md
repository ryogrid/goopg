# 0095-0003 — In-place tablespace foundation (CREATE/DROP TABLESPACE)

**Status:** complete for `011_in_place_tablespace.pl` (the test now PASSES).
GUC + DDL + in-place version directory + runtime registry + BASE_BACKUP
per-tablespace `<oid>.tar` emission all landed; `pg_tablespace` query
visibility landed separately (M0110-0001 DU-002, virtual overlay); `CREATE
TABLE ... TABLESPACE` validation/storage/rendering landed 2026-07-08
(M0122-0007, see below). Physical relocation of a table's storage into its
tablespace's directory remains out of scope (see Deferral).
**Milestone:** M0095-0003 (`011_in_place_tablespace.pl`) / M0122-0007 (CREATE
TABLE ... TABLESPACE).
**Related:** [0095-0003-pg-basebackup-execution.md](0095-0003-pg-basebackup-execution.md)
(physical BASE_BACKUP streaming, already accepted).

## Why this doc exists

`011_in_place_tablespace.pl` was long blocked behind a (now-corrected) note that
blamed BASE_BACKUP. The deferral ledger (2026-06-14, loop #28) re-scoped it: the
real gap is the **in-place tablespace feature**, which decomposes into three
independent pieces:

1. the `allow_in_place_tablespaces` GUC,
2. `CREATE TABLESPACE name LOCATION ''` DDL (statement + directory + catalog),
3. BASE_BACKUP emitting each non-default tablespace as a separate `<oid>.tar`.

This slice lands (1) and the executable core of (2). It is the first
committable, fully-unit-testable layer, mirroring the engine-first pattern used
throughout M0095/M0102/M0110.

## What landed

| layer | change | file |
|-------|--------|------|
| GUC | `allow_in_place_tablespaces` (TypeBool, boot off, `ContextSuset`, session/txn scope), plus a commented `postgresql.conf.sample` entry under a new DEVELOPER OPTIONS section. Mirrors `guc_tables.c:1949` (PGC_SUSET, GUC_NOT_IN_SAMPLE). | `internal/config/defaults.go`, `internal/config/postgresql.conf.sample` |
| AST | `CreateTablespaceStmt{Name, Owner, Location, Options}`, `DropTablespaceStmt{IfExists, Name}` | `internal/parser/ast.go` |
| Parser | `CREATE TABLESPACE name [OWNER [=] role] LOCATION 'dir' [WITH (opts)]` (`parseCreateTablespaceTail`) + `DROP TABLESPACE [IF EXISTS] name`. `tablespace` is an unreserved keyword (`KwTablespace`), so dispatch uses `acceptKeyword`, not `acceptIdentKeyword`. | `internal/parser/ddl.go` |
| Planner | both statements added to the `DDL` passthrough case list | `internal/planner/planner.go` |
| Executor | `execCreateTablespace` / `execDropTablespace`: validate, allocate OID via the registry, create/remove `pg_tblspc/<oid>` | `internal/executor/operators_ddl.go` |
| Catalog | runtime in-place tablespace registry (`tablespaces map[string]*tablespaceRow`, `CreateTablespace`/`DropTablespace`) | `internal/catalog/catalog.go` |
| Wire | `CREATE TABLESPACE` / `DROP TABLESPACE` command tags | `internal/server/dispatch.go` |

## Semantics (faithful to `commands/tablespace.c`)

`CreateTableSpace` (`tablespace.c:207`) and `create_tablespace_directories`
(`:572`) define the behavior goopg mirrors:

- `in_place = allow_in_place_tablespaces && len(location) == 0`.
- A location containing `'` → `42602` "tablespace location cannot contain single
  quotes" (CREATE-DATABASE safety).
- `!in_place && !is_absolute_path(location)` → `42P17` "tablespace location must
  be an absolute path". This is also the path an **empty** `LOCATION` takes when
  the GUC is off — exactly as upstream.
- A reserved `pg_`-prefixed name → `42939` "unacceptable tablespace name …" with
  detail "The prefix \"pg_\" is reserved for system tablespaces."
- A duplicate name → `42710` "tablespace … already exists".
- For an in-place tablespace, `create_tablespace_directories` makes
  `pg_tblspc/<oid>` a **real directory** (not a symlink). goopg creates exactly
  that under `Context.DataDir`.

**Intentional divergence:** an *absolute external* `LOCATION` is valid in PG but
goopg cannot relocate relation files into an arbitrary directory, so it raises
`0A000` "tablespaces with an external location are not supported" with a hint to
use the in-place form. Only the in-place form is meaningful in goopg today.

`DROP TABLESPACE` removes the registry entry and `RemoveAll`s the in-place
directory; a missing name without `IF EXISTS` raises `42704` "tablespace … does
not exist".

When `Context.DataDir` is empty (embedded/test contexts with no cluster on
disk), the registry entry stands alone and no filesystem effect occurs —
matching how other DDL operators skip cluster-filesystem side effects.

## Catalog-visibility scope decision

**Superseded 2026-07-08 (M0122-0007) — see "CREATE TABLE ... TABLESPACE" below.**
The paragraph below described the state as of this slice's original landing;
`pg_tablespace` was subsequently exposed as a `VirtualRows` table
(`internal/catalog/catalog.go`'s `tablespaceVirtualRows`, M0110-0001 DU-002) that
already includes every runtime-registered tablespace alongside the two
bootstrap rows (pg_default/pg_global), so a `CREATE TABLESPACE` **is** visible
to `SELECT ... FROM pg_tablespace` / pg_dump's `getTablespaces` today. The
original concern (below) about needing a shared on-disk heap write no longer
applies to *reading* tablespaces — only a genuinely on-disk-heap-backed
`pg_tablespace` (matching upstream's storage, not just its query surface)
remains out of scope.

`pg_tablespace` in goopg is a **bootstrapped on-disk heap relation**
(`internal/initdb/pg_tablespace_bootstrap.go`), not a virtual table with a
runtime override hook like `pg_extension`. Making a runtime-created tablespace
appear in `pg_tablespace` therefore requires either a new per-relation virtual
overlay or a write into a *shared* on-disk catalog — the latter is a separate
hard capability goopg lacks (no runtime shared-catalog `RelFileNode` resolver;
see the `goopg_no_runtime_shared_catalog_inplace_update` lesson). This slice
deliberately scopes that out: the runtime registry tracks created tablespaces
(for duplicate rejection and OID→directory mapping), and the verifiable artifact
is the in-place directory.

## CREATE TABLE ... TABLESPACE (landed 2026-07-08, M0122-0007)

Closed the resume point named above ("`CREATE TABLE … TABLESPACE foo` already
ignores the TABLESPACE clause"). The clause was parsed and silently discarded
at all three sites that accept it: the main column-list path
(`parseCreateTableTail`, `internal/parser/ddl.go`), the empty-column-list /
typed-table `OF type` path (`consumeCreateTableSuffix`), and the `PARTITION OF`
child path. All three now capture the name onto the new
`CreateTableStmt.Tablespace` field.

The executor resolves the name via a new `catalog.InMemory.LookupTablespaceOID`
(added to the `Catalog` interface), covering the two bootstrap tablespaces
(`pg_default`=1663, `pg_global`=1664, new `catalog.DefaultTablespaceOID` /
`GlobalTablespaceOID` constants) plus the runtime registry. `resolveCreateTableTablespace`
(`internal/executor/operators_ddl.go`) mirrors `DefineRelation`'s tablespace
handling (`tablecmds.c`):

- an unknown name raises `42704` (`ERRCODE_UNDEFINED_OBJECT`, matching
  `get_tablespace_oid`'s `missing_ok=false` error, `tablespace.c:1420`);
- `pg_global` is rejected for an ordinary relation with `22023`
  (`ERRCODE_INVALID_PARAMETER_VALUE`, "only shared relations can be placed in
  pg_global tablespace", `tablecmds.c:920-924`);
- a name resolving to the database's default tablespace (always `pg_default` —
  goopg has no per-database tablespace override) normalizes to `0`
  (`InvalidOid`), mirroring `heap_create`'s
  `reltablespaceOid == MyDatabaseTableSpace ? InvalidOid : reltablespaceOid`.

The resolved OID lands on the new `catalog.Table.Tablespace` field and is
rendered as `pg_class.reltablespace` by both sibling row builders that must
stay in sync (`catalog.go`'s live `VirtualRows` builder and
`internal/executor/pg18_user_catalog_rows.go`'s `buildUserPGClassRow`, the
latter also being the exact function `syncTableToCatalogHeap` writes into the
pg_class heap — a single funnel, no separate wiring needed there). Wired into
the three executor paths that build a `catalog.Table` from a
`CreateTableStmt`: `execCreateTable` (plain/typed/partition-parent tables),
`execCreatePartitionChild`, and `execCreateTableAs` (CTAS — dormant today
since the CTAS grammar doesn't parse a `TABLESPACE` clause yet, wired for
symmetry so a future CTAS-tablespace fix needs no executor change).

**Scope boundaries (deliberate, matching the pre-existing CREATE TABLESPACE
decision above):**

- **No physical relocation.** goopg does not move the table's relation file
  into the tablespace's `pg_tblspc/<oid>/...` directory — this is
  catalog-metadata fidelity only (correct `reltablespace`/`\d+`/pg_dump
  output), not a storage-engine feature. Tracked as a further, larger,
  independent capability (deferral ledger).
- **No restart durability for `Table.Tablespace` itself.** Like the
  pre-existing `Table.Unlogged` flag (also set only at CREATE TABLE time with
  no reload-path reconstruction), a table's tablespace is not restored from
  the pg_class heap on server restart today — `internal/initdb/open.go`'s
  `loadUserTablesFromHeap` reads `catalog.PGClassRow` back but that struct has
  no `RelTablespace` field yet. The value written into the pg_class heap row
  IS correct (so a fresh read of pg_class within the same server lifetime, or
  by an external tool like `pg_dump`/`psql` connecting to the running server,
  sees it) — only the in-process catalog rebuild after a restart drops it,
  matching the existing `Unlogged` precedent exactly.
- **`CREATE INDEX ... TABLESPACE`, `ALTER TABLE/INDEX ... SET TABLESPACE`
  remain unparsed** (not even accepted syntax today) — separate, additive
  grammar work, not covered by this slice.

Tests: `parser.TestParseCreateTableTablespace` (all three parse sites, table
+ empty-column-list + partition child); `executor`'s
`TestCreateTableTablespaceResolvesAndStores`,
`TestCreateTableTablespaceDefaultNormalizesToZero`,
`TestCreateTableTablespaceUnknownErrors42704`,
`TestCreateTableTablespacePgGlobalErrors22023`,
`TestCreateTablePartitionOfChildTablespace`,
`TestPGClassRendersReltablespace` (`internal/executor/create_table_tablespace_test.go`).
Confirmed non-vacuous via `git stash` on the parser and catalog/executor diffs
separately (both fail to compile without the fix — the new field/method don't
exist). Gates: `go build ./...` clean; `go test ./internal/parser/...
./internal/catalog/... ./internal/executor/...` PASS (excluding the
pre-existing, unrelated `TestSeqScanFiresPrefetchesAcrossBlocks` hang already
tracked in the deferral ledger); `go test ./internal/initdb/...` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
txns, all 3 workloads).

## Tests

- `parser.TestParseCreateTablespace` / `TestParseCreateTablespaceMissingLocation`
  / `TestParseDropTablespace`.
- `catalog.TestCreateTablespaceRegistry` (OID allocation, case-insensitive
  duplicate, drop returns OID, name reuse after drop).
- `executor.TestCreateInPlaceTablespace` (real temp data dir: one `pg_tblspc/<oid>`
  dir created, removed on DROP), `…Duplicate` (42710), `…DropTablespaceMissing`
  (42704 / IF EXISTS), `…GUCOff` (42P17), `…ReservedName` (42939),
  `…ExternalLocation` (0A000), `…QuoteInLocation` (42602).
- `config.TestAllowInPlaceTablespacesGUC` (boot off, `ContextSuset`, settable);
  `config.TestSampleConfigCoversRegistry` covers the new sample entry.
- `TestPort_PgBasebackup011InPlaceTablespace` (the upstream scenario end-to-end:
  create an in-place tablespace, `pg_basebackup --format=tar --wal-method=none`,
  assert `base.tar` + exactly one `<oid>.tar`).

## BASE_BACKUP per-tablespace tier (landed loop #13, 2026-06-15)

The remaining two pieces from the original Deferral both landed, making `011`
pass:

1. **Version directory.** `execCreateTablespace` now creates
   `pg_tblspc/<oid>/PG_<major>_<catversion>` (not just `pg_tblspc/<oid>`),
   faithful to `create_tablespace_directories`. The directory name is
   `config.TablespaceVersionDirectory` = `"PG_" + MajorVersion + "_" +
   CatalogVersionNo`. These version constants moved to the **leaf `config`
   package** (`internal/config/version.go`) as the single source of truth:
   `internal/initdb` (which `executor` cannot import — it would cycle, since
   `initdb` imports `executor`) now references `config.MajorVersion` /
   `config.CatalogVersionNo`, and `pgcontrol.go`'s `pgCatalogVersionNo` aliases
   `config.CatalogVersionNo`.

2. **Per-tablespace `<oid>.tar`.** `internal/server/basebackup.go`:
   - `collectInPlaceTablespaces(dataDir)` scans `pg_tblspc` for numeric
     subdirectories (goopg supports only in-place tablespaces, so every numeric
     dir is one), sorted by OID. Mirrors the `pg_tblspc` scan in
     `do_pg_backup_start` (`xlog.c:9040-9130`), whose in-place branch sets
     `ti->path`/`ti->rpath` to the relative `pg_tblspc/<oid>`.
   - `writeTablespaceList` now emits one `(oid, "pg_tblspc/<oid>", NULL)` row per
     tablespace before the default NULL row (result-set 2).
   - The base-tar walk (`emitBaseBackupTar`) ships the `pg_tblspc/<oid>`
     directory entry but does **not** recurse into it — equivalent to
     `sendDir`'s `skip_this_dir` for a tablespace whose `rpath` is inside PGDATA.
   - After `base.tar`, for each tablespace, an `'n'` new-archive frame names
     `"<oid>.tar"` with spclocation `pg_tblspc/<oid>`, then `emitTablespaceTar`
     streams the version-dir tar (paths relative to `pg_tblspc/<oid>`, i.e.
     `PG_<major>_<catversion>/…`), faithful to `sendTablespace`
     (`basebackup.c:1136`). Tablespace files are added to the same backup
     manifest with their PGDATA-relative path.

   pg_basebackup writes each archive to `basedir/<archive_name>` in tar format
   (`pg_basebackup.c:1187`), so the server's archive name directly produces the
   `<oid>.tar` file the upstream test globs for.

## Deferral

On-disk `pg_tablespace` heap visibility remains a further, independent
capability (shared-catalog runtime write — no `RelFileNode` resolver for the
shared `pg_tablespace` catalog) tracked separately; it is **not** needed by
`011`, which exercises only the filesystem + BASE_BACKUP behavior.
