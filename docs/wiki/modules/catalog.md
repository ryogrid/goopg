# Module: `internal/catalog`

The system-catalog layer. One process-wide `InMemory` registry holds every
relation/index/role/type/ACL/FDW/publication object; Go builders synthesize each
`pg_catalog.*` / `information_schema.*` row set on demand (`VirtualRows`); a
binary codec encodes the three heap-backed catalogs. It serves four masters at
once: the planner (table/column/index lookup, stats), the executor DDL paths
(create/alter/drop registries), pg_dump fidelity (every PG18 column rendered,
because pg_dump's resolver SQL runs server-side on goopg), and PG-standby
compatibility (canonical on-disk catalog rows a real PG 18.3 can consume).
Consumers by file count: executor 101, optimizer 39, initdb 29, postmaster 7,
replication 5.

## Key Files

| File | LOC | Role |
|---|---|---|
| `catalog.go` | 24,021 | Everything core: structs, `InMemory`, all registries, all virtual row builders |
| `pg_proc_names_generated.go` | 13,621 | Generated OID→(proname,argtypes) reverse map |
| `codec.go` | 2,022 | `PGClassRow`/`PGAttributeRow`/`PGTypeRow` heap encode/decode |
| `routines.go` | 1,057 | User-defined routine (function/procedure) registry |
| `pg_operator_seed_data.go` | 807 | Full PG18 pg_operator seed, 799 rows |
| `pubsub.go` | 746 | Publication/subscription rows |
| `default_acl.go` | 294 | ALTER DEFAULT PRIVILEGES (DEFACLOBJ_* letters) |
| `encoding.go`, `relcache_inval.go`, `pg_node_oid_lookup.go`, `pgstats.go`, `bpchar.go`, `pg_database_schema.go`, `pg_operator_data.go` | 166–27 each | enc-ID table; pg_internal.init sweep; forward opindex for node resolver; pg_stats builder; bpchar padding; pg_database consts; OperatorEntry struct |

## Core data structures

- **`Table`** (`catalog.go:334`) — the central relation record. Notable fields:
  `Schema/Name/Columns/OID`; `RelFileNodeOID` (:342) and `DBOid` (:355) for
  physical per-database routing; **`Virtual bool` + `VirtualRows func()[][]string`**
  (:363–364) marking catalogs served by builders instead of heaps;
  `View *parser.SelectStmt` / `ViewDef` (:376–384); constraints
  (`NamedChecks`, `NotNullConstraints`:476–481); partition fields (:529–567);
  `Unlogged`/`Temp` (:570–571); **`TempOwner string`** (:630) — per-session temp
  ownership token; `Owner` (:642, role name driving VACUUM privilege);
  `Stats *TableStats` (:421); reloptions mirrors (`Fillfactor`:648,
  `ReplicaIdentity`:592, `RowSecurity`:600…).
- **`Column`** (`catalog.go:110`) — `Type Type` (:97; note `Type{Name, Args, IsArray}`
  where `Name` is the *element* type name for arrays), `Ordinal`, identity/
  generated fields (:117–160), `Dropped` (:165, slot retained),
  `MissingValue any` (:141, attmissingval analogue typed `any` to dodge a
  catalog→executor import cycle), and ~15 dump-only fidelity fields
  `Storage/Compression/StatTarget/Options/Collation/FDWOptions` (:177–223).
- **`Index`** (`catalog.go:1778`) — `Unique/Method/Primary/ColExprs/Predicate/
  IncludeColumns/ColDescending…`, plus its own `DBOid`.
- **Constraint & rule types** — `NamedCheckConstraint` (:228; NotValid/NoInherit/
  NotEnforced), `NamedNotNullConstraint` (:251, PG18 contype='n'), `ForeignKey`
  (:1498), `Trigger` (:1342), `PolicyInfo` (:1413), `RuleInfo` (:1444),
  `PartitionBound` (:1556), `TableStats`/`ColumnStats` (:1649/:1693).
- **Type-side registries** — `EnumType`:3069, `Domain`:3105, `CompositeType`:3234,
  `RangeType`:3274, `UserCollation`:3355, `UserOperator`:18712,
  `UserOperatorClass/Family`:19215/18991, `AmOpMember/AmProcMember`:19447/19463,
  `Cast`:18485, `Transform`:18613, `AccessMethod`:17447,
  `ForeignDataWrapper`:17315, `ForeignServer`:18205, `UserMapping`:20333,
  `StatisticsObject`:2993, `RoleMembership`:5643, `RoleAttrs`:15972,
  `BuiltinProc`:19784, `BuiltinOperator`:19931.
- **Manager vs on-disk duality** — `InMemory` (`catalog.go:2296`) is the live
  truth: an `RWMutex` guarding `namespaces map[uint32]*tableNamespace` (:2316,
  where `tableNamespace{tables,indexes,byTable}`:2282) plus ~40 flat maps
  (enums/domains/composites/ranges/roles/ACLs/toast renames/partition edges).
  The on-disk heap catalogs are a **persistence projection**: written by the
  executor at DDL time, reloaded at startup into the same `InMemory`. Code
  programs against the `Catalog` interface (:1990); `SearchPathCatalog` (:23876)
  wraps it adding search_path/temp-owner/dboid resolution.

## Virtual vs heap-backed catalogs

The split is deliberate and asymmetric:

- **pg_class (1259) is virtual** — registered in `(*InMemory).registerSystemTables()`
  (`catalog.go:8133`) with `Virtual: true` and a PG18-canonical 34-column
  tupdesc (:8140–8175); rows come from `PGClassRowsForDBOid` (:7168). The
  executor's values operator special-cases it (`internal/executor/operators.go:165`
  swaps in `ctx.PgClassRows()`; hook declared `executor/context.go:689–694`).
- **pg_type (1247) and pg_attribute (1249) are heap-backed** — when
  `base/<dbOid>/1247|1249` exist, startup registers them via
  `RegisterRealTable` (`catalog.go:12147`, rejects Virtual tables, requires a
  preset OID < FirstUserOID) so a SeqScan reads the relfile directly
  (`internal/initdb/open.go:2795 loadSystemCatalogsIfPresent`).
- **…but pg_class also gets a real heap image** for PG-standby/dump parity:
  `syncTableToCatalogHeap` (`internal/executor/operators_ddl.go:18036`) emits
  PG18-canonical 34-column pg_class + 25-column pg_attribute rows, the three
  pg_class/pg_attribute btree index files, pg_attrdef rows (adbin as canonical
  node text via `internal/nodes`), and pg_inherits rows. Called from CREATE TABLE
  and ~12 ALTER sites (operators_ddl.go:3775, 4490, 4908, 5177, 6159, 9162, 9499,
  9546, 9677, 9701, 9828, 10114). Startup reload scans these heaps
  (`loadUserTablesFromHeap`, open.go:2899+, per-database pass open.go:1540).
- The 34-column alignment exists precisely so `scanMatching` can decode physical
  pg_class heap tuples for statements like `UPDATE pg_class SET reltuples…`
  (`catalog.go:8138–8142`).
- Everything else — pg_database, pg_namespace, pg_constraint, pg_depend,
  pg_stat_*, information_schema.* — is a virtual builder either here
  (`registerSystemTables` catalog.go:8133–11812,
  `registerInformationSchemaTables`:11812, the `PG*RowsForDBOid` family
  :6567–15654) or in `internal/executor/sys_pg_*.go`.

## Lifecycle

- **CREATE/ALTER/DROP** — the executor calls interface methods (`CreateTable`:12355,
  `CreateIndex`:12414, `AddColumn`:12446, `RenameTable`:12469, `CreateView`:12592,
  `DropTable`:20506, `DropIndex`:20585…) which mutate the in-memory namespace
  under `c.mu`; the same executor statement then persists via
  `syncTableToCatalogHeap`. `RegisterRealTable`:12147 /
  `RegisterVirtualTable`:12183 are the two install paths.
- **OID allocation** — one cluster-wide `nextOID` counter: `AllocOID`:6468,
  `allocOIDLocked`:6475; `AdvanceNextOIDPast`:6457 is fed at startup from the
  heap scan and the checkpointed counter embedded in WAL/pg_control.
  `FirstUserOID = 16384` floors dynamic OIDs; `BootstrapSuperuserOID = 10`.
  Roles share the counter (`RegisterRole`:15944); `RegisterRoleWithOID`:15989
  preserves role OIDs across restarts (consumed by the pg_authid heap loader /
  WAL replay so `pg_policy.polroles` stay valid). Predefined `pg_*` roles:
  `newPredefinedRoleMap`:15932.
- **Dependency recording** — there is deliberately **no general pg_depend
  store**: `PGDependRowsForDBOid`:14990 synthesizes only sequence-ownership ('a')
  rows through the executor-injected `SequenceParamsFunc` hook (catalog.go:80,
  import-cycle breaker). The one first-class edge type is
  `constraintViewDeps`:2489 (`RegisterViewConstraintDep`:20952 /
  `ViewsDependingOnConstraint`:20996) enforcing DROP CONSTRAINT RESTRICT for
  functional-dependency views.
- **Transactional semantics** — most catalog mutations are immediate, not
  MVCC-versioned; PG-like behaviour is emulated with explicit sidecar markers:
  deferred DROP TABLE visibility (`SetTablePendingDropXID`/`TablePendingDropXID`,
  :21339–21365), `PgClassRowMark`s (:2949/:21379, row-lock marks on pg_class
  tuples), `pendingAttachXID` (:2417, in-flight ATTACH PARTITION blocks FK
  deletes), `DetachPendingEpoch` on children (:550) with
  `VisiblePartitionChildren`:4834 (detach-concurrent snapshot epoch), and
  `pendingInheritanceChangeCount` (:2457, forces plan-cache bypass). Treat
  catalog DDL as autocommit-ish except where a marker above exists.
- **Recovery twins** — nearly every mutator has a `*DuringRecovery` sibling
  (e.g. `RegisterIndexDuringRecovery`:6164, `DropCollationDuringRecovery`:13488,
  `RegisterRangeTypeDuringRecovery`:22837) that skips collision checks/OID mints
  during WAL replay — a second path that must stay in sync with the primary.

## Scoping

- **Per-database namespaces** — `namespaces map[dbOid]*tableNamespace` despite
  one shared process-wide registry; unknown dbs get an `emptyTableNamespace`
  (`ns(dbOid)`:3774); only CreateTable/CreateIndex/RegisterRealTable/
  TryRegisterUserTable create one (`getOrCreateNS`:3789). Every accessor takes
  trailing `dbOid ...uint32`, defaulted via `resolveDBOid`:3811 to
  `DefaultDBOid = 1`. Type-family maps key dbOid into the name
  (`domainKey`:3821, `enumKey`:3829, …).
- **The postgres-is-default quirk** — `NamespaceDBOid(connDBOid)` (:23962) maps
  0 **and `PostgresDBOid = 5`** back to DefaultDBOid(1), because write paths
  still persist under 1; only genuinely new CREATE DATABASE OIDs get their own
  namespace. Per-db heaps `base/<dbOid>/1259|1249` exist since M0122-0007;
  `base/5` is mirrored from `base/1` by
  `internal/executor/sys_catalog_postgres_db_mirror.go`.
- **Schemas/search_path** — schema registry (`RegisterSchema`:12991,
  `SchemaOID`:12708) with heap-TID sidecars (`SchemaHeapTID`:12742 family, one
  getter/setter triple per catalog). `SearchPathCatalog.LookupTable`:23984 tries
  schemas in order for unqualified names; it also carries `TempOwnerToken`,
  `SnapshotPartitionDetachEpoch`, `DisableSeqScan`, `DBOid`.
- **Temp tables** — `Table.TempOwner` records the owning session token;
  per-owner temp namespaces (`EnsureTempNamespace`/`tempNamespaceName`,
  :15823–15884); `DropSessionTempObjects`:20534 cleans up at disconnect;
  `AccessibleInheritanceChildren`:4578 hides other-session temp children
  (RELATION_IS_OTHER_TEMP).

## System views

- **Nailed views** — `cmd/gen-nailed-view-tables/main.go` renders
  `internal/initdb/nailed_view_manifest.tsv` (captured from a throwaway real
  PG18.3 by `scripts/capture-ev-action.sh`) into
  `internal/initdb/nailed_view_seed_data.go`: bootstrap pg_class/pg_attribute
  heap rows for **80 on-disk system views**, plus `nailedViewRewriteEntries()`
  `_RETURN` pg_rewrite seed rows whose ev_action bodies resolve at runtime from
  an embedded blob (`internal/initdb/nailed_view_ev_action.go`). Consumed by
  `initdb/relcache_init.go:789,:2738` and `initdb/pg_rewrite_bootstrap.go:142`;
  OID pins guarded by `system_view_oid_pins.go` + a manifest-drift test. View
  reltype is goopg's RECORDOID (2249), diverging from upstream per-view
  composite types (documented in the generator header).
- **pg_stat_\*** — Go builders, not SQL views: `PGStatTablesRowsForDBOid`:7704,
  xact/indexes/statio variants :7797–8076, scoped by `StatTableScope{All,User,Sys}`:7671;
  counters are faithful zeros/NULLs (no PgStat_StatTabEntry store),
  n_live_tup comes from ANALYZE reltuples.
- **information_schema** — virtual tables registered in-package
  (`registerInformationSchemaTables`:11812) plus generated view seed data
  (`cmd/gen-information-schema-views`, `cmd/gen-information-schema-procs`).

## Public API

Grouped highlights (all in `catalog.go` unless noted):

```go
// Lookup
LookupTable / LookupColumn / LookupIndex          // interface :1990; impls :12224/:12299/:12311
LookupTableByOID / LookupTableByOIDAllDBs         // :6346/:6360
LookupIndexByOID :4886 · TablesInSchema :20472 · AllTables :21195
UserTableHandles :21220 · AllUserViews/MatViews :21457/:21475

// DDL
CreateTable/CreateIndex/CreateView/AddColumn      // :12355–12592
DropTable/DropView/DropIndex/RenameTable/RenameIndex/RenameSchema // :20506–13139
TryRegisterUserTable :12112 · AllocOID :6468

// Row builders (virtual catalogs)
PGClassRowsForDBOid :7168   PGAttrdefRowsForDBOid :6966
PGConstraintRowsForDBOid :6645    PGDependRowsForDBOid :14990
PGInheritsRowsForDBOid :15293     PGIndexesRows… :6567
PGStatTables/Xact/Indexes… :7704–8129

// Types
RegisterEnum/AddEnumValueResult/DropEnum :21613–22060
RegisterDomain/DropDomain :22929/:23662
RegisterCompositeTypeWithFields :22070  RegisterRangeType :22654
ResolveColumnType :23829

// Privileges / roles
GrantTablePrivilege[WithGrantOption][As] :16236–16258
RevokeTablePrivilege :16350  HasTablePrivilege :16558
RelaclText :16956  IsSuperuser :5914  RoleIsMemberOf :5883
RoleOID :16114  RoleNameForOID :16181

// Codec (codec.go + small files)
PGClassRow/PGAttributeRow/PGTypeRow encode+decode · PadBpchar
EncodingIDToName (encoding.go) · SerializeTSDictOptions :14055

// Cross-package hooks (import-cycle breakers)
SequenceParamsFunc / FindSequenceOwnedByFunc :80/:91
SetRelationSizer :21133  RegprocName/RegprocedureNameParts :20023/:20190
```

## Dependencies

- **Used by** — executor (101 files), optimizer (39), initdb (29), postmaster (7),
  replication (5), backup, nbtree.
- **Uses** — `internal/parser` (view ASTs, expression types), `internal/utils/*`,
  and injected function hooks instead of imports wherever executor behaviour
  would be needed (`SequenceParamsFunc`, `PgClassRows` wiring) — the layering
  rule is that catalog must stay below executor.

## Notable patterns / gotchas

- **Sibling renderers** — the virtual pg_class builder and the heap-written
  pg_class row are two renderers of the same object; changes must land in both
  or pg_dump/PG-standby and `\d` disagree.
- **Do not "fix" the db 5→1 mapping** — making postgres connections use their
  own namespace makes every existing table vanish (`catalog.go:23951–23961`).
- The live "postgres" database's displayed OID must stay the 16384 placeholder —
  CREATE SUBSCRIPTION subdbid and datacl heap resync depend on it (:2361–2368).
- `RoleAttrs.ConnLimit == 0` is a *valid PG setting* ("no new connections");
  "no attributes" constructions must set −1 explicitly (:15986–15989).
- ~15 `Table`/`Column` fields exist for pg_dump round-trip fidelity only
  (Compression, StatTarget, Collation, ReplicaIdentity, RowSecurity, Policies,
  Rules, ParallelWorkers, Tablespace…) — goopg does not act on them.
- `GeneratedVirtual` does not change storage: every generated column is
  materialized STORED (:120–127).
- `SmallDimension` is retired (M0125-0043): nothing sets it; the planner derives
  smallness itself (:440–451).
- Empty `TempOwner` on a temp relation means visible-to-all-sessions
  (single-session legacy compat, :626–629).
