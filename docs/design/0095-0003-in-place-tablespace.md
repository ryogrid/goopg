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
  remained unparsed** (not even accepted syntax at the time) — separate,
  additive grammar work, not covered by this slice. **Closed 2026-07-08, see
  below.**

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

## CREATE INDEX / ALTER TABLE/INDEX ... SET TABLESPACE (landed 2026-07-08, M0122-0007 follow-up)

Closed the resume point named just above. Three grammar gaps, all catalog-
metadata-only (same scope boundary as `CREATE TABLE ... TABLESPACE`: no
physical relocation, no restart durability):

1. **`CREATE INDEX ... TABLESPACE name`.** Real PG grammar places the clause
   after `WITH (storage_parameter)` and before `WHERE` (`gram.y`'s `IndexStmt`:
   `OptTableSpace` precedes `where_clause`) — `parseCreateIndexTail`
   (`internal/parser/ddl.go`) now accepts it in that position onto the new
   `CreateIndexStmt.Tablespace` field. `execCreateIndex`
   (`internal/executor/operators_ddl.go`) resolves it via the same
   `resolveTablespaceClause` helper the CREATE TABLE path uses (renamed from
   `resolveCreateTableTablespace` — the resolution logic has nothing
   table-specific about it) once, before branching into either the
   gist/spgist/gin/brin catalog-only path or the real btree
   `createBTreeIndex` path, since an unknown name / `pg_global` must reject the
   statement regardless of access method. The resolved OID lands on the new
   `catalog.Index.Tablespace` field.
2. **`ALTER TABLE name SET TABLESPACE name`.** Unlike `SET SCHEMA`/`SET
   LOGGED` (whole-statement fields the caller intercepts before the
   per-action parser runs), real PG's `SET TABLESPACE` is an ordinary
   `alter_table_cmd` (`AT_SetTableSpace`, `gram.y:2932-2937`) — combinable
   with other comma-separated actions. `parseAlterTableAction`
   (`internal/parser/ddl.go`) recognizes it (checked before the pre-existing
   `SET (reloptions)` branch, which unconditionally expects a `(`) and emits
   the new `AlterTableSetTablespace` action kind carrying the name in the new
   `AlterTableAction.TablespaceName` field. `execAlterTable`
   (`internal/executor/operators_ddl.go`) resolves and stores it on
   `tbl.Tablespace`.
3. **`ALTER INDEX name SET TABLESPACE name`.** Same grammar reused for the
   other relkind. The dedicated ALTER INDEX parse block checks for it ahead
   of the pre-existing `ALTER INDEX name SET (param = value, …)` reloptions
   branch (same ordering reason as #2), producing the identical
   `AlterTableSetTablespace` action wrapped in an `AlterTableStmt` with
   `TagOverride: "ALTER INDEX"`. `execAlterTable`'s index branch (the
   `LookupIndex` fallback reached when the name doesn't resolve as a table)
   resolves and stores it on `idx.Tablespace`.

Both `catalog.Index.Tablespace` and `AlterTableAction.TablespaceName` render
as `pg_class.reltablespace` via the same index row-builder loop in
`catalog.go`'s `registerSystemTables` that previously hardcoded `"0"` for
every index. Deliberately **not** carried through `btreeIndexProps`/the
CREATE INDEX WAL record — like `Table.Tablespace`, `Index.Tablespace` is
catalog-metadata-only for the life of the session and resets to `0` (default)
after a restart (ledger row, M0122-0007).

Tests: `parser.TestParseCreateIndexTablespace` (plain / after WITH options /
before WHERE / absent), `TestParseAlterTableSetTablespace`,
`TestParseAlterIndexSetTablespace` (also pins the sibling reloptions `SET (…)`
branch stays unaffected) — all in `internal/parser/create_tablespace_test.go`.
`executor`'s `TestCreateIndexTablespaceResolvesAndStores` (real btree path),
`TestCreateIndexTablespaceCatalogOnlyMethod` (gist path),
`TestCreateIndexTablespaceUnknownErrors42704`,
`TestAlterTableSetTablespaceUpdatesTable`,
`TestAlterIndexSetTablespaceUpdatesIndex`,
`TestPGClassRendersIndexReltablespace` (live `SELECT reltablespace FROM
pg_catalog.pg_class`) — `internal/executor/create_index_tablespace_test.go`.
Confirmed non-vacuous: the new parser fields/action kind and catalog field
don't exist without the fix (compile failure on `git stash` of the
parser/catalog/executor diffs). Gates: `go build ./...`/`go vet ./...` clean;
`go test ./internal/parser/... ./internal/catalog/... ./internal/executor/...`
PASS; `go test ./internal/planner/... ./internal/initdb/...` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
txns, all 3 workloads).

**Still open** (M0122-0007, this bucket): CREATE/DROP DATABASE full DDL,
REINDEX physical rebuild, tablespace physical relocation + restart
durability (both table and index).

## Restart durability (2026-07-08 follow-up)

Closed the "restart durability (both table and index)" resume point named
above. `pg_class.reltablespace` for both `Table.Tablespace` and
`Index.Tablespace` was already correctly *written* into the live heap row
(`buildUserPGClassRow` / `buildUserPGClassRowForIndex`, the latter fixed this
loop — it had hardcoded `0` even for an index explicitly created `...
TABLESPACE ts1`) but never *read back* on the heap-driven restart path:

- `catalog.PGClassRow` gained `RelTablespace uint32`, decoded by
  `DecodePGClassPhysicalRow` (`internal/catalog/codec.go`) at the real PG18
  `FormData_pg_class` byte offset 92 — verified against `pgClassColDefs`'s own
  offset comments (`internal/initdb/initdb.go`): sits between
  `relfilenode`(88) and `relpages`(96).
- `internal/initdb/open.go`'s `loadUserTablesFromHeap` sets `tbl.Tablespace =
  tr.RelTablespace` when reconstructing a table from the pg_class heap.
- `loadUserIndexesFromHeap` decodes the same column for `relkind='i'` rows and
  threads it through a new `tablespace uint32` parameter on
  `catalog.Catalog`'s `RegisterIndexDuringRecovery` /
  `internal/initdb.indexRegistryRecovery` — mirrors the existing
  `fillfactor`/`deduplicateItems` parameters, which take the same "pass 0/nil
  here, apply the real value via a separate reloptions-decode step" shape for
  reloptions but, unlike those, tablespace needs no string parsing so it goes
  straight through the constructor argument.

For the WAL-only replay fallback (`internal/initdb.replayIndexDDLRecords`,
exercised when no pg_index heap row exists yet — e.g. an uncheckpointed crash
between `syncIndexToCatalogHeap` and the next checkpoint), `wal.
CreateIndexPayload` gained a `Tablespace uint32` field inside its existing
self-describing "extension" block (new `ciExtHasTablespace` flag bit,
`internal/wal/recovery.go`), encoded/decoded exactly like `Fillfactor`.
`btreeIndexProps` (`internal/executor/operators_ddl.go`) gained a matching
`Tablespace` field, and `createBTreeIndex` now sets `idx.Tablespace =
xp.Tablespace` in the same block that already applies
`Fillfactor`/`DeduplicateItems` from `xp` — **before** the CREATE INDEX WAL
record is built, following the same "before WAL emission, not a post-call
assignment" discipline every other M0122-0006-follow-up index property
already established (a post-call assignment survives a graceful shutdown
checkpoint but reverts to 0 across a genuine uncheckpointed crash restart).
This let `execCreateIndex`'s old post-call `bidx.Tablespace =
idxTablespaceOID` block (added when the CREATE INDEX TABLESPACE clause first
landed, deliberately deferred at the time) be deleted outright — the value
now flows through `btreeIndexProps` like every sibling property.

`ALTER TABLE/INDEX ... SET TABLESPACE` was previously catalog-metadata-only
for the life of the session (explicitly documented as such when it first
landed) — it now resyncs the heap-persisted pg_class row immediately after
the mutation: the table action reuses the established
`deleteCatalogRowsForOID`(xmax-stamp)+`syncTableToCatalogHeap` idiom every
other column-mutating ALTER action in `execAlterTable` already follows; the
index action calls `resyncIndexClassHeapRow`, which already stamps the old
row's xmax internally. An uncheckpointed crash immediately after either
statement no longer loses the change.

Tests: `TestEncodeCreateIndexExtensionRoundTrip`'s new "tablespace only" case
plus the gen1 backward-compat zero-value assertion
(`internal/wal/index_ddl_test.go`);
`TestTableTablespaceSurvivesRestartViaCatalogHeap`,
`TestIndexTablespaceSurvivesRestartViaCatalogHeap`,
`TestAlterTableSetTablespaceSurvivesRestartViaCatalogHeap`
(`internal/initdb/tablespace_restart_test.go`) — full `Init`→`Open`→DDL→
`Close`→`Open` cycles against a real on-disk data directory (no JSON
snapshot), all confirmed non-vacuous via `git stash` on the fix files (fail
with `Tablespace = 0, want <oid>` pre-fix). Also live-verified against the
real `cmd/goopg` binary: `CREATE TABLESPACE ts1 LOCATION ''`, `CREATE TABLE
ts_live (id int4) TABLESPACE ts1`, `CREATE INDEX ts_live_idx ON ts_live (id)
TABLESPACE ts1`, then a real `goopg stop` / `goopg start` — `pg_class.
reltablespace` for both rows unchanged across the restart; likewise for
`ALTER TABLE ... SET TABLESPACE`. Gates: `go build ./...`/`go vet ./...`
clean; `go test ./internal/catalog/... ./internal/wal/...
./internal/initdb/... ./internal/executor/...` PASS; `scripts/
tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed txns, all 3 workloads).

**New gap found live-verifying this fix, not fixed here (deferral ledger
row, one task per loop):** the tablespace *registry* itself (`CREATE
TABLESPACE`, `catalog.InMemory.tablespaces`) has no restart durability at
all — `CreateTablespace`/`DropTablespace` mutate a purely in-memory map with
no WAL record and no backing heap relation (`pg_tablespace` is a shared
catalog with no `RelFileNode` resolver, per the "Catalog-visibility scope
decision" section above), so a tablespace vanishes from `pg_tablespace`
entirely after a restart even though its `pg_tblspc/<oid>/` directory stays
on disk — confirmed live: a fresh server session's `CREATE TABLE ... 
TABLESPACE ts1` fails with `42704 tablespace "ts1" does not exist` right
after a restart that had `ts1` live moments before. This orphans any
table/index whose now-durable `reltablespace` OID points at the vanished
tablespace. Needs the same class of fix `CREATE DATABASE`/`CREATE SCHEMA`
already got: a new `RecordKindCreateTablespace`/`RecordKindDropTablespace`
WAL record pair plus a `replayTablespaceDDLRecords` driver in
`internal/initdb`, mirroring `replayDatabaseDDLRecords`/
`replaySchemaDDLRecords`.

**Still open** (M0122-0007, this bucket): CREATE/DROP DATABASE full DDL,
REINDEX physical rebuild, tablespace physical relocation, tablespace-registry
restart durability (new, this follow-up).

## Tablespace-registry restart durability (2026-07-09 follow-up)

Closed the "tablespace-registry restart durability" gap named in the section
above. `catalog.InMemory.tablespaces` (`CreateTablespace`/`DropTablespace`)
now gets the same durability mechanism `CREATE DATABASE`/`CREATE SCHEMA`
already have — a goopg-private WAL record pair plus a post-physical-replay
recovery driver, since `pg_tablespace` is a shared catalog with no backing
heap relation to sync into (per the "Catalog-visibility scope decision"
section above).

- `wal.RecordKindCreateTablespace` (124) / `RecordKindDropTablespace` (125)
  (`internal/wal/recovery.go`). Create carries `oid | name | owner |
  location` (mirrors `RecordKindCreateSchema`'s OID-preserving shape); drop
  carries only `name`. Both are no-op for physical replay (`ApplyRecord`'s
  `ApplyRecordKindPhysicalOnly`-style switch) — there is no page-level state
  to reconstruct, exactly like CREATE/DROP SCHEMA.
- `catalog.InMemory.RegisterTablespaceDuringRecovery(name, owner, location,
  oid)` / `UnregisterTablespaceDuringRecovery(name)` — re-populate/remove the
  `tablespaces` map entry with the original OID (bumping `nextOID` past it,
  mirroring `RegisterSchemaDuringRecovery`).
- `internal/initdb/tablespace_ddl_recovery.go`'s `replayTablespaceDDLRecords`
  — a new post-physical-replay pass, byte-for-byte structured like
  `replaySchemaDDLRecords`: scans `pg_wal` for these two record kinds via a
  local `tablespaceRegistryRecovery` interface type-asserted against the
  `catalog.Catalog` argument, applying each in WAL order. Wired into
  `internal/initdb/open.go`'s `Open` right after `replaySchemaDDLRecords`
  (order doesn't matter relative to schema replay — tablespaces aren't
  schema-scoped — but it must run before `loadUserTablesFromHeap`/
  `loadUserIndexesFromHeap` reconstruct their durable `reltablespace` OIDs,
  so a table/index pointing at a user tablespace doesn't transiently look
  orphaned during the same `Open` call).
- `execCreateTablespace`/`execDropTablespace` (`internal/executor/
  operators_ddl.go`) now append the corresponding WAL record right after the
  in-memory mutation succeeds (create: after the `pg_tblspc/<oid>` directory
  is created; drop: before the directory is removed) — same "mutate, then
  WAL" ordering CREATE/DROP SCHEMA already use.

Tests: `TestTablespaceDDLRecoveryReplaysCreate` (OID preserved across a real
`Init`→`Open`→WAL-append→`Close`→`Open` cycle),
`TestTablespaceDDLRecoveryReplaysDropAfterCreate` (CREATE+DROP cancels out),
`TestReplayTablespaceDDLRecordsHandlesMissingWalDir` (no-op on a fresh
initdb) — all in `internal/initdb/tablespace_ddl_recovery_test.go`, confirmed
non-vacuous via `git stash` on the four impl files (fails to build without
them: `undefined: wal.EncodeCreateTablespace` etc.). Also live-verified
against the real `cmd/goopg` binary: `SET allow_in_place_tablespaces = on;
CREATE TABLESPACE ts1 LOCATION ''; CREATE TABLE t1(a int) TABLESPACE ts1;`,
then a real `goopg stop`/`goopg start` — `pg_tablespace` still lists `ts1`,
`t1.reltablespace` unchanged, and a fresh `CREATE TABLE t2(a int) TABLESPACE
ts1` in the post-restart session succeeds (previously `42704`). Repeated with
`DROP TABLESPACE ts1` before a restart — confirmed it stays gone. Gates: `go
build ./...` clean; `go test ./internal/catalog/... ./internal/wal/...
./internal/initdb/... ./internal/executor/...` PASS; `scripts/
tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` PASS (0 failed txns, all 3 workloads).

**Still open** (M0122-0007, this bucket): CREATE/DROP DATABASE full DDL,
REINDEX physical rebuild, tablespace physical relocation.

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
