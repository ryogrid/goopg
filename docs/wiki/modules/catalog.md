# Module: `internal/catalog`

The system-catalog layer. One process-wide `InMemory` registry holds every
relation/index/role/type/ACL/FDW/publication/extension/event-trigger object;
Go builders synthesize each `pg_catalog.*` / `information_schema.*` row set on
demand (`VirtualRows`); a binary codec encodes the three heap-backed catalogs
(pg_class, pg_attribute, pg_type). It serves five masters at once: the planner
(table/column/index lookup, stats), the executor DDL paths (create/alter/drop
registries), pg_dump fidelity (every PG18 column rendered, because pg_dump's
resolver SQL runs server-side on goopg), PG-standby compatibility (canonical
on-disk catalog rows a real PG 18.3 can consume), and WAL-replay (the
`*DuringRecovery` twin methods). Consumers by file count: executor 101,
optimizer 39, initdb 29, postmaster 7, replication 5.

```mermaid
flowchart LR
    subgraph Consumers
        EXEC[executor]
        OPT[optimizer]
        INIT[initdb]
        PM[postmaster]
        REP[replication]
    end
    subgraph catalog["internal/catalog"]
        IM[InMemory]
        VIRT[VirtualRows]
        CODEC[Codec]
        PUBSUB[PubSub]
        ROUT[Routines]
    end
    subgraph Storage
        HEAP[(heap files)]
    end
    EXEC --> IM --> VIRT
    EXEC --> CODEC --> HEAP
    OPT --> IM
    INIT --> CODEC
    INIT --> IM
    PM --> IM --> PUBSUB
    REP --> PUBSUB
```

## Key Files

| File | LOC | Role |
|---|---|---|
| `catalog.go` | 24,969 | Everything core: structs, `InMemory`, all registries, all virtual row builders, ACL store, reloptions, partition routing, OID allocation, the `Catalog` interface, `SearchPathCatalog` wrapper |
| `pg_proc_names_generated.go` | 13,623 | Generated OID→(proname,argtypes) reverse map; `pgProcNamesByOID`, `pgProcArgTypeNamesByOID`, `pgProcRetTypeByOID` — the full PG18 built-in-procedure index |
| `codec.go` | 2,049 | `PGClassRow`/`PGAttributeRow`/`PGTypeRow` heap encode/decode; fixed-offset layout constants (144-byte pg_class rows, 100-byte pg_attribute rows); all built-in type OID constants (16–4072) |
| `routines.go` | 1,081 | User-defined routine (function/procedure) registry: `Routine` struct (35 fields), `Routines` process-wide registry, `Create`/`Lookup`/`Drop`/`ResolveByName`/`ResolveBySig`, per-db scoping, overload resolution, pg_proc heap TID cache |
| `pg_operator_seed_data.go` | 807 | Full PG18 pg_operator seed, 799 rows, returned by `PGOperatorAllEntries()` |
| `pubsub.go` | 746 | Publication/subscription registry: `Publication` (9 fields), `Subscription` (8 fields), `SubscriptionRel`, `PubSub` struct, `CreatePublication`/`CreateSubscription`/`DropPublication`/`DropSubscription`, recovered-via-`*DuringRecovery` twins, per-db scoping |
| `default_acl.go` | 294 | ALTER DEFAULT PRIVILEGES: DEFACLOBJ_* letter constants (`'r'`/`'S'`/`'f'`/`'T'`/`'n'`/`'L'`), `DefaultACLEntry`, `DefaultACLNormalizePriv`, `GrantDefaultACLPrivilege`, `RevokeDefaultACLPrivilege`, `DefaultACLText` |
| `encoding.go` | 166 | Encoding-ID↔name mapping: `EncodingIDToName` (pg_encoding_to_char), `EncodingNameToID` (pg_char_to_encoding), `ValidServerEncodingName`, 43 canonical encoding names, ~60 cleaned aliases |
| `relcache_inval.go` | 151 | `pg_internal.init` sweep: `RelcacheInitFileUnlink` (single-db), `RelcacheInitFileRemoveAll` (cluster-wide), `WithRelCacheInitLock`, plus `allDigits`/`removeInitFilesInDir` helpers |
| `pgstats.go` | 117 | `PGStatsRowsForDBOid` — Go builder replicating the `pg_stats` SQL view projection (MCV, histogram, null_frac, n_distinct, avg_width) |
| `pg_node_oid_lookup.go` | 120 | Forward OID indexes for canonical pg_node_tree resolver: `LookupOperatorForNode` (name+leftOID+rightOID → OperatorEntry), `LookupProcForNode` (name+argOIDs → funcid), `ProcResultType` (funcid → prorettype) |
| `bpchar.go` | 55 | `PadBpchar` — blank-pads bpchar values to declared width for DataRow/COPY/pgoutput boundaries |
| `pg_database_schema.go` | 54 | `PgDatabaseColumnsPG18` canonical column schema, `SharedCatalogRelFileNode` for cluster-wide catalogs, `PgDatabaseRelationOID` (1262) |
| `pg_operator_data.go` | 27 | `OperatorEntry` struct (15 fields) — single pg_operator row representation |

## Core data structures

### `Table` (`catalog.go:418`)

The central relation record with ~60 fields:

- **Identity**: `Schema`, `Name`, `Columns []Column`, `OID`, `RelFileNodeOID`, `DBOid`
- **Virtual dispatch**: `Virtual bool` + `VirtualRows func()[][]string` — catalogs served by builders instead of heaps
- **View support**: `View *parser.SelectStmt` / `ViewColumnAliases` / `ViewDef` (raw SQL), `CheckOption` ("cascaded"/"local"/""), `SecurityBarrier`/`SecurityInvoker`, `OfTypeOID` (composite-typed tables)
- **Constraints**: `NamedChecks []NamedCheckConstraint`, `NotNullConstraints []NamedNotNullConstraint`, `CheckConstraints []string`, `ForeignKeys []ForeignKey`
- **Partition support**: `PartitionKey []string`, `PartitionMethod` ("LIST"/"RANGE"/"HASH"), `PartitionParentOID`, `PartitionBounds []PartitionBound`, `DetachPendingEpoch uint64`, `PartitionKeyOpClasses`, `PartitionKeyExprs`, `PartitionKeyCollations`
- **Storage attributes**: `Unlogged`/`Temp`, `TempOwner string`, `Tablespace uint32`, `ReplicaIdentity string`, `Fillfactor`, `RelFrozenXID storage.TransactionID`
- **Security**: `RowSecurity`, `ForceRowSecurity`, `Policies []PolicyInfo`, `Rules []RuleInfo`, `Owner string`
- **Stats**: `Stats *TableStats` (nil before ANALYZE)
- **Dump-only**: `ForeignServerName`, `ForeignOptions`, `Policies`, `Rules`, `Compression`, `StatTarget`, `Collation`, `ParallelWorkers` — goopg does not act on these

### `Column` (`catalog.go:194`)

- `Type` (`Type{Name, Args, IsArray}` where `Name` is the *element* type name for arrays)
- `Ordinal`, `Dropped` (slot retained), `MissingValue any` (attmissingval, typed `any` to dodge import cycle)
- Identity: `IdentityColumn`/`IdentityAlways` (bool, distinguishes ALWAYS vs DEFAULT), `IdentityStart`, `IdentityIncrement`, `IdentityMin`, `IdentityMax`, `IdentityCache`, `IdentityCycle`
- `GeneratedExpr string` (STORED generated columns), `GeneratedVirtual` (note: still materialized STORED)
- ~15 dump-only fidelity fields: `Storage`, `Compression`, `StatTarget`, `Options`, `Collation`, `FDWOptions`, `Local`, `InhCount`, `IsLocal`

### `Index` (`catalog.go:1898`)

- `Schema`, `Name`, `Table *Table`, `Columns []string`, `Unique`, `Method`, `Primary`, `OID`, `DBOid`, `Tablespace`
- Expression indexes: `ColExprs []*parser.Expr`, `ColExprStrings []string`
- Ordering: `ColDescending []bool`, `ColNullsFirst []bool`, `ColOpClasses []string`, `ColCollations []string`
- Partial indexes: `HasPredicate bool`, `Predicate parser.Expr`, `PredicateString string`
- Covering: `IncludeColumns []string`
- Constraint: `IsConstraint`, `IsExclusion`, `ExclusionOp string`, `Deferrable`, `InitiallyDeferred`, `NullsNotDistinct`
- Storage options: `Fillfactor int`, `DeduplicateItems *bool`, `FastUpdate *bool`, `GinPendingListLimit int`
- Partition: `PartitionParentOID`, `DeclaredHash`

### `InMemory` (`catalog.go:2459`)

The live truth: ~40 maps and slices behind an `RWMutex`:

```go
type InMemory struct {
    mu              sync.RWMutex
    relSizer        atomic.Pointer[func(RelFileNode) (int64, bool)]
    namespaces      map[uint32]*tableNamespace   // dbOid → tables/indexes
    routines        *Routines
    databases       map[string]bool
    databaseConnLimit map[string]int32
    databaseEncoding  map[string]int32
    databaseOwner     map[string]uint32
    databaseOid       map[string]uint32          // real distinct OIDs
    dbRoleSettings    map[uint32][]string
    roleSettings      map[roleSettingKey][]string
    roleMembers       map[roleMembershipKey]*RoleMembership
    partitionChildren     map[uint32][]uint32
    pendingAttachXID      map[uint32]uint32
    indexPartitionChildren map[uint32][]uint32
    toastRenames          map[uint32]string
    inheritanceChildren   map[uint32][]uint32
    pendingPartitionDetachCount int
    pendingInheritanceChangeCount int
    enumTypes       map[string]*EnumType
    domains         map[string]*Domain
    compositeTypeNames map[string]bool
    compositeTypeFields map[string][]CompositeField
    compositeTypes  map[string]*CompositeType
    rangeTypes      map[string]*RangeType
    constraintViewDeps map[string][]string
    opClassHashFuncs map[string]string
    opClassSchemas  map[string]string
    userAggregates  map[string]*UserAggregate
    userCollations  []*UserCollation
    userConversions []*UserConversion
    userTSDicts     []*UserTSDict
    userTSConfigs   []*UserTSConfig
    schemas         map[string]uint32
    schemaOwners    map[string]uint32
    schemaHeapTIDs  map[string]SchemaHeapTID
    typeHeapTIDs    map[uint32]SchemaHeapTID
    // ... operatorHeapTIDs, collationHeapTIDs, conversionHeapTIDs, etc.
    tempNamespaces  map[string]uint32
    roles           map[string]uint32
    predefinedRoles map[string]uint32
    roleAttrs       map[string]*RoleAttrs
    tableACLs       map[uint32]map[string]map[string]bool
    tableACLGrantor map[uint32]map[string]string
    tableACLOrder   map[uint32][]string
    roleACLDisplay  map[string]string
    relACLEmptied   map[uint32]bool
    relACLOwnerRevoked map[uint32]bool
    attrACLs        map[attrACLKey]map[string]map[string]bool
    attrACLOrder    map[attrACLKey][]string
    attrACLGrantor  map[attrACLKey]map[string]string
    parameterACLOIDs map[string]uint32
    parameterACLNames map[uint32]string
    defaultACLOIDs   map[defaultACLKey]uint32
    defaultACLKeys   map[uint32]defaultACLKey
    defaultACLGlobal map[uint32]bool
    compatObjects   map[string]map[string]struct{}
    fdws            map[string]*ForeignDataWrapper
    accessMethods   map[string]*AccessMethod
    eventTriggers   map[string]*EventTrigger
    foreignServers  map[string]*ForeignServer
    userMappings    map[string]*UserMapping
    // ... tablespaces, extensions, casts, transforms, userOperators, userOpFamilies, userOpClasses, amOpMembers, amProcMembers, udts
}
```

### `tableNamespace` (`catalog.go:2445`)

```go
type tableNamespace struct {
    tables  map[string]*Table           // key: "schema.name"
    indexes map[string]*Index           // key: "schema.name"
    byTable map[uint32]map[string]*Index // table OID → index name → *Index
}
```

### `Catalog` interface (`catalog.go:2136`)

~85 methods covering lookup, DDL, statistics, extensions, tablespaces, roles, privileges, indexes, routines, enum/domain/composite/range types, operators, and search-path resolution. Every method has a concrete `*InMemory` receiver plus a `SearchPathCatalog` wrapper.

### `SearchPathCatalog` (`catalog.go:24824`)

Wraps a `Catalog` with search-path resolution, temp-owner filtering, per-db OID routing, and partition-detach epoch tracking. `LookupTable` tries schemas in order for unqualified names; `LookupIndex` and `IndexesOnTable` follow the same pattern.

### Type-side registry structs

| Type | Fields | Key |
|---|---|---|
| `EnumType` | `OID, Name, Values []EnumValue, Owner, DBOid` | `enumKey(dbOid, name)` |
| `Domain` | `OID, Name, Base Type, NotNull, Default, Checks []DomainCheck, Owner, DBOid` | `domainKey(dbOid, name)` |
| `CompositeType` | `OID, Name, Fields []CompositeField, Owner, DBOid` | `compositeKey(dbOid, name)` |
| `RangeType` | `OID, Name, Subtype, SubtypeOpclass, Collation, CanonicalFunc, SubtypeDiff, MultirangeName, Owner, DBOid` | `rangeKey(dbOid, name)` |
| `UserOperator` | `OID, Name, LeftType, RightType, NamespaceOID, FuncOID, Owner` | `userOperatorKey(schema, name, left, right)` |
| `UserOperatorFamily` | `OID, Name, Method, NamespaceOID, Owner, DBOid` | `userOpFamilyKey(dbOid, schema, name, method)` |
| `UserOperatorClass` | `OID, Name, Method, FamilyOID, KeyType, InType, IsDefault, NamespaceOID, Owner, DBOid` | `userOpClassKey(dbOid, schema, name, method)` |
| `ForeignDataWrapper` | `OID, Name, Options, Handler, Validator` | — |
| `ForeignServer` | `OID, Name, FDWName, Type, Version, Options, DBOid` | `foreignServerKey(dbOid, name)` |
| `UserMapping` | `OID, User, Server, Options, DBOid` | `userMappingKey(dbOid, user, server)` |
| `Cast` | `OID, Source, Target, Context, Method, FuncOID` | `castKey(source, target)` |
| `Transform` | `OID, TypeName, Lang, FromFuncOID, ToFuncOID` | — |
| `AccessMethod` | `OID, Name, Type, HandlerOID` | `accessMethodKey(dbOid, name)` |
| `EventTrigger` | `OID, Name, Event, Owner, FuncOID, Tags` | — |
| `StatisticsObject` | `OID, Schema, Name, TableOID, Kinds, Columns, Exprs, HasExpr, Owner` | — |
| `UserAggregate` | `OID, Name, ArgTypes, ReturnType, SFunc, SType, InitCond, SortOp, ...` | — |
| `UserCollation` | `OID, Name, Locale, Provider, Deterministic, ...` | — |
| `UserConversion` | `OID, Name, ForEncoding, ToEncoding, FuncOID, Default` | — |
| `UserTSDict` | `OID, Name, Template, InitOption, Options, DBOid` | — |
| `UserTSConfig` | `OID, Name, Parser, Mappings []TSConfigMapping, DBOid` | — |

### `RoleAttrs` (`catalog.go:16491`)

```go
type RoleAttrs struct {
    Super       bool
    Login       bool
    Inherit     bool
    CreateRole  bool
    CreateDB    bool
    Replication bool
    BypassRLS   bool
    ConnLimit   int32  // -1 = unlimited (PG default), 0 = "no new connections"
    Password    string // SCRAM-SHA-256 verifier
    ValidUntil  string
}
```

## Virtual vs heap-backed catalogs

The split is deliberate and asymmetric:

- **pg_class (1259) is virtual** — registered in `(*InMemory).registerSystemTables()`
  (`catalog.go:8418`) with `Virtual: true` and a PG18-canonical 34-column
  tupdesc; rows come from `PGClassRowsForDBOid`. The executor's values operator
  special-cases it (`internal/executor/operators.go:165` swaps in `ctx.PgClassRows()`;
  hook declared `executor/context.go:689–694`).
- **pg_type (1247) and pg_attribute (1249) are heap-backed** — when
  `base/<dbOid>/1247|1249` exist, startup registers them via
  `RegisterRealTable` (`catalog.go:12467`, rejects Virtual tables, requires a
  preset OID < FirstUserOID) so a SeqScan reads the relfile directly
  (`internal/initdb/open.go:2795 loadSystemCatalogsIfPresent`).
- **…but pg_class also gets a real heap image** for PG-standby/dump parity:
  `syncTableToCatalogHeap` (`internal/executor/operators_ddl.go:18036`) emits
  PG18-canonical 34-column pg_class + 25-column pg_attribute rows, the three
  pg_class/pg_attribute btree index files, pg_attrdef rows, and pg_inherits
  rows. Called from CREATE TABLE and ~12 ALTER sites.
- **pg_database (1262) is shared-catalog heap-backed** — stored at
  `global/1262` (not per-database). `SharedCatalogRelFileNode` routes it.
  Startup reload in `loadSystemCatalogsIfPresent` reads it.
- **pg_proc heap** — user-defined routines write physical pg_proc/pg_aggregate
  heap rows via `bootstrapPgProcTuples` (initdb recovery path) and
  `syncRoutineToCatalogHeap` (runtime CREATE/ALTER). The `Routines` registry carries
  `heapTIDs map[uint32]SchemaHeapTID` for O(1) heap UPDATE.
- Everything else — pg_namespace, pg_constraint, pg_depend, pg_stat_*,
  information_schema.* — is a virtual builder.

```mermaid
flowchart TD
    subgraph Virtual["Virtual (Go builders)"]
        pg_class[pg_class 1259]
        pg_constraint[pg_constraint]
        pg_depend[pg_depend]
        pg_stat_tables[pg_stat_*]
        pg_namespace[pg_namespace]
        information_schema[information_schema.*]
        pg_collation[pg_collation]
        pg_proc_virtual[pg_proc virtual rows]
        pg_aggregate[pg_aggregate]
    end
    subgraph HeapBacked["Heap-backed"]
        pg_type[pg_type 1247]
        pg_attribute[pg_attribute 1249]
        pg_class_heap[pg_class heap image]
        pg_database[pg_database 1262]
        pg_proc_heap[pg_proc heap]
        pg_attrdef[pg_attrdef]
        pg_inherits[pg_inherits]
    end
    pg_class -.->|"syncTableToCatalogHeap"| pg_class_heap
    pg_proc_virtual -.->|"syncRoutineToCatalogHeap"| pg_proc_heap
```

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
  `FirstRoutineOID = 1<<17` leaves room for built-in pg_proc rows.
  Roles share the counter (`RegisterRole`:15944); `RegisterRoleWithOID`:15909
  preserves role OIDs across restarts.
- **Recovery twins** — nearly every mutator has a `*DuringRecovery` sibling
  (e.g. `RegisterIndexDuringRecovery`:6164, `DropCollationDuringRecovery`:13488,
  `RegisterRangeTypeDuringRecovery`:22837, `RegisterDomainDuringRecovery`:23908)
  that skips collision checks/OID mints during WAL replay — a second path that
  must stay in sync with the primary.
- **Dependency recording** — deliberately **no general pg_depend store**:
  `PGDependRowsForDBOid`:14990 synthesizes only sequence-ownership ('a') rows.
  The one first-class edge type is `constraintViewDeps`:2489 enforcing DROP
  CONSTRAINT RESTRICT for functional-dependency views.
- **Transactional semantics** — most catalog mutations are immediate, not
  MVCC-versioned. Sidecar markers emulate PG behaviour:
  `SetTablePendingDropXID`/`TablePendingDropXID` (deferred DROP TABLE visibility),
  `PgClassRowMark`s (:2949/:21379, row-lock marks on pg_class tuples),
  `pendingAttachXID` (:2417, in-flight ATTACH PARTITION blocks FK deletes),
  `DetachPendingEpoch` on children with `VisiblePartitionChildren`:4834,
  `pendingInheritanceChangeCount` (:2457, forces plan-cache bypass),
  `PendingPartitionDetachCount` for O(1) DETACH CONCURRENTLY detection.
- **relcache init file invalidation** — `RelcacheInitFileUnlink` removes
  `pg_internal.init` for a single database; `RelcacheInitFileRemoveAll` sweeps
  the entire cluster at startup (including pg_tblspc/ paths). Both are guarded
  by `WithRelCacheInitLock` to prevent TOCTOU races with concurrent readers.

## Scoping

- **Per-database namespaces** — `namespaces map[dbOid]*tableNamespace` despite
  one shared process-wide registry; unknown dbs get an `emptyTableNamespace`
  (`ns(dbOid)`:3774); only CreateTable/CreateIndex/RegisterRealTable/
  TryRegisterUserTable create one (`getOrCreateNS`:3789). Every accessor takes
  trailing `dbOid ...uint32`, defaulted via `resolveDBOid`:3811 to
  `DefaultDBOid = 1`. Type-family maps key dbOid into the name
  (`domainKey`:3821, `enumKey`:3829, `compositeKey`:4007, `rangeKey`:4015).
- **The postgres-is-default quirk** — `NamespaceDBOid(connDBOid)` maps
  0 **and `PostgresDBOid = 5`** back to DefaultDBOid(1), because write paths
  still persist under 1; only genuinely new CREATE DATABASE OIDs get their own
  namespace. Do not "fix" this — making postgres connections use their own
  namespace makes every existing table vanish.
- **Schemas/search_path** — schema registry (`RegisterSchema`:12991,
  `SchemaOID`:12708) with heap-TID sidecars (`SchemaHeapTID`:12742 family).
  `SearchPathCatalog.LookupTable`:23984 tries schemas in order for unqualified
  names; it also carries `TempOwnerToken`, `SnapshotPartitionDetachEpoch`,
  `DisableSeqScan`, `DBOid`.
- **Temp tables** — `Table.TempOwner` records the owning session token;
  per-owner temp namespaces (`EnsureTempNamespace`/`tempNamespaceName`,
  `tempNamespaceOID`); `DropSessionTempObjects`:20534 cleans up at disconnect;
  `AccessibleInheritanceChildren`:4578 hides other-session temp children.
  Empty `TempOwner` on a temp relation means visible-to-all-sessions
  (single-session legacy compat).
- **Per-db registry scoping** — `Publication.DBOid`, `Subscription.DBOid`,
  `Routine.DBOid`, `UserTSDict.DBOid`, `UserTSConfig.DBOid`, `EnumType.DBOid`,
  `Domain.DBOid`, `CompositeType.DBOid`, `RangeType.DBOid` — all carry the
  real physical database OID so two distinct databases may each create a
  same-named object without colliding.
- **Postgres-db mirror** — `base/5` is mirrored from `base/1` by
  `internal/executor/sys_catalog_postgres_db_mirror.go` so a connection to
  `postgres` sees the same catalog heap images.

## Relationship map of the catalogs

```mermaid
erDiagram
    PG_CLASS ||--o{ PG_ATTRIBUTE : "has columns"
    PG_CLASS ||--o{ PG_INDEX : "has indexes"
    PG_CLASS ||--o{ PG_ATTRDEF : "column defaults"
    PG_CLASS ||--o{ PG_CONSTRAINT : "constraints"
    PG_CLASS ||--o{ PG_TRIGGER : "triggers"
    PG_CLASS ||--o{ PG_INHERITS : "inheritance edges"
    PG_CLASS ||--o{ PG_POLICY : "RLS policies"
    PG_CLASS ||--o{ PG_REWRITE : "rules/views"
    PG_CLASS }o--|| PG_NAMESPACE : "schema"
    PG_TYPE ||--o{ PG_ATTRIBUTE : "column type"
    PG_TYPE ||--o{ PG_ENUM : "enum values"
    PG_TYPE ||--o{ PG_RANGE : "range subtype"
    PG_NAMESPACE ||--o{ PG_PROC : "functions in schema"
    PG_PROC ||--o{ PG_AGGREGATE : "aggregate state"
    PG_NAMESPACE ||--o{ PG_COLLATION : "collations"
    PG_NAMESPACE ||--o{ PG_OPERATOR : "operators"
    PG_NAMESPACE ||--o{ PG_OPCLASS : "opclasses"
    PG_CLASS ||--o{ PG_FOREIGN_TABLE : "foreign table options"
    PG_DATABASE ||--o{ PG_CLASS : "per-database heap"
```

## System views

- **Nailed views** — `cmd/gen-nailed-view-tables/main.go` renders
  `internal/initdb/nailed_view_manifest.tsv` (captured from a throwaway real
  PG18.3 by `scripts/capture-ev-action.sh`) into
  `internal/initdb/nailed_view_seed_data.go`: bootstrap pg_class/pg_attribute
  heap rows for **80 on-disk system views**, plus `nailedViewRewriteEntries()`
  `_RETURN` pg_rewrite seed rows. View reltype is goopg's RECORDOID (2249),
  diverging from upstream per-view composite types.
- **pg_stat_*** — Go builders, not SQL views: `PGStatTablesRowsForDBOid`:7704,
  xact/indexes/statio variants–8076, scoped by `StatTableScope{All,User,Sys}`:7671;
  counters are faithful zeros/NULLs; n_live_tup comes from ANALYZE reltuples.
- **pg_stats** — `PGStatsRowsForDBOid` (pgstats.go) replicates the SQL view
  projection directly from `ColumnStats` (MCV/histogram/null_frac/n_distinct).
  Only MCV (kind 1) and equi-depth histogram (kind 2) are collected; correlation,
  most-common-elements, and range-type stats are always NULL.
- **pg_proc/pg_aggregate** — virtual rows built from `Routines` registry, plus
  embedded built-in proc rows from `pg_proc_names_generated.go`.
- **information_schema** — virtual tables registered in-package
  (`registerInformationSchemaTables`:11812) plus generated view seed data
  (`cmd/gen-information-schema-views`, `cmd/gen-information-schema-procs`).

## Public API

Grouped highlights (all in `catalog.go` unless noted):

```go
// ── Lookup ──────────────────────────────────────────────────────────
LookupTable / LookupColumn / LookupIndex          // interface; impls:12299/12311/12631
LookupTableByOID / LookupTableByOIDAllDBs         //:6360/6617
LookupIndexByOID                                  //:5070
TablesInSchema / AllTables / AllUserViews/MatViews //:21375/22127/22389/22407
UserTableHandles / AllIndexes                      //:22152/21865
IndexesOnTable / HasPrimaryKey                     //:21537/22026
SchemaExists / SchemaOID / SchemaNameForOID        //:13020/13028/13041
LookupEnum / LookupEnumByOID / LookupEnumByArrayOID //:22603/22641/22656
LookupDomain / LookupDomainByOID / LookupDomainByArrayOID //:24044/24078/24093
LookupCompositeType / LookupCompositeTypeByOID / LookupCompositeTypeByArrayOID //:23044/23076/23091
LookupRangeType / LookupRangeTypeByOID / LookupRangeTypeByMultirangeOID //:23642/23693/23708
LookupStatistics / StatisticsByOID                  //:4187/4322
LookupBuiltinProc / LookupBuiltinProcByOID          //:20810/20818
LookupBuiltinOperator / LookupBuiltinOperatorByOID  //:20882/20896
LookupUserOperator / LookupUserOperatorByOID        //:19631/19676
LookupUserOperatorFamily / LookupUserOperatorClass  //:19957/20102
LookupForeignDataWrapper / LookupForeignServer      //:18128/18930
LookupUserMapping / LookupTablespaceOID             //:21295/16239
LookupEventTrigger / LookupTrigger / LookupToastRel //:18376/1241
LookupUserAggregateByName / LookupUserAggregateByOID //:4519/4530
Routines().Lookup / Routines().LookupByName         // routines.go:441/480
Routines().LookupByOID / Routines().ResolveBySig    // routines.go:778/674

// ── DDL ─────────────────────────────────────────────────────────────
CreateTable / CreateIndex / CreateView / CreateMatView //:12675/12734/12912
AddColumn / RenameTable / RenameIndex / RenameSchema   //:12766/12789/12850/13378
DropTable / DropView / DropIndex / DropMatView         //:21409/12994/21517
DropSessionTempObjects / SessionTempTableNames         //:21466/21501
TryRegisterUserTable / RegisterRealTable / RegisterVirtualTable //:12432/12467/12503
RegisterSchema / UnregisterSchema / RenameSchema       //:13311/13322/13378
CreateDatabase / DropDatabase / RenameDatabase         //:5349/5365/5388
CreateExtension / DropExtension                        //:13544/13571
CreateTablespace / DropTablespace / RenameTablespace   //:16092/16112/16163
CreateCollation / DropCollation / RenameCollation      //:13617/13665/13689
CreateConversion / DropConversion / RenameConversion   //:13848/13905/13976
CreateTSDict / DropTSDict / RenameTSDict               //:14198/14260/14616
CreateTSConfig / DropTSConfig / RenameTSConfig         //:14690/14754/14844
Routines().Create / Routines().Drop / Routines().DropByName // routines.go:279/508/555
Routines().RenameRoutine / Routines().SetSchema         // routines.go:800/842
RegisterEnum / DropEnum / AddEnumValue / RenameEnum    //:22545/22977/22848/22721
RegisterDomain / DropDomain / RenameDomain             //:23877/24610/24107
RegisterCompositeTypeWithFields / DropCompositeType    //:23018/23859
RegisterRangeType / DropRangeType / RenameRangeType    //:23602/23764/22807
RegisterUserOperator / DropUserOperator                //:19486/19520
RegisterUserOperatorFamily / DropUserOperatorFamily    //:19801/19863
RegisterUserOperatorClass / DropUserOperatorClass      //:20027/20112
RegisterAmOpMember / RegisterAmProcMember              //:20241/20262
RegisterForeignDataWrapper / DropForeignDataWrapper    //:18032/18051
RegisterForeignServer / DropForeignServer              //:18968/18999
RegisterUserMapping / DropUserMapping                  //:21270/21305
RegisterAccessMethod / DropAccessMethod                //:18156/18190
RegisterEventTrigger / DropEventTrigger                //:18319/18343
RegisterStatistics / DropStatistics                    //:4114/4161
RegisterUserAggregate / DropUserAggregate              //:4508/4559
RegisterRole / UnregisterRole / RenameRole             //:16451/16579/16610
RegisterSchemaDuringRecovery / UnregisterSchemaDuringRecovery //:13459/13471

// ── Row builders (virtual catalogs) ─────────────────────────────────
PGClassRowsForDBOid       PGAttrdefRowsForDBOid
PGConstraintRowsForDBOid  PGDependRowsForDBOid
PGInheritsRowsForDBOid    PGIndexesRowsForDBOid
PGIndexRowsForDBOid       PGTablesRowsForDBOid
PGPolicyRowsForDBOid      PGTriggerRowsForDBOid
PGSequenceRowsForDBOid    PGRewriteRowsForDBOid
PGForeignTableRowsForDBOid PGForeignServerRowsForDBOid
PGUserMappingsRowsForDBOid PGConversionRowsForDBOid
PGTSDictRowsForDBOid      PGTSConfigRowsForDBOid
PGStatTables/ Xact/ Indexes / StatioTables / StatioIndexes / StatioSequences
PGStatsRowsForDBOid       PGInitPrivsRowsForDBOid
PGStatDatabaseRows        PGStatDatabaseConflictsRows
PGStatUserFunctionsRows   PGDatabasesRows
PGCollationRowsForDBOid   //:15211

// ── Types ───────────────────────────────────────────────────────────
RegisterEnum / AddEnumValueResult / RemoveEnumValue / DropEnum
RegisterDomain / AddDomainCheck / DropDomain / DropDomainConstraint
RegisterCompositeTypeWithFields / RegisterCompositeType
RegisterRangeType / LookupRangeType / ListRangeTypes
ResolveColumnType / IsSerialTypeName / TablesWithColumnOfType
FindColumnUsingDomainTransitively

// ── Privileges / roles ─────────────────────────────────────────────
GrantTablePrivilege / GrantTablePrivilegeWithGrantOption / GrantTablePrivilegeAs
RevokeTablePrivilege / HasTablePrivilege
GrantColumnPrivilege / GrantColumnPrivilegeAs / RevokeColumnPrivilege / AttrACLText
MaterializeOwnerACL / IsOwnerACLRevoked / HasOwnerPrivilege
DropTableACL / RelaclText / ProcACLText / TypeACLText / NamespaceACLText
ParameterACLText / ForeignServerACLText / ForeignDataWrapperACLText / DatabaseACLText
IsSuperuser / RoleIsMemberOf / IsAdminOfRole / HasPrivsOfRole / SelectBestAdmin
RoleOID / RoleNameForOID / RoleNameAtOID / RoleNameForOIDOrUnknown
GrantRoleMembership / RevokeRoleMembership / LookupRoleMembership
AllRoleStates / AllRoleMemberships / RoleConfigEntries / DatabaseConfigEntries

// ── Default ACLs (default_acl.go) ───────────────────────────────────
DefaultACLOID / HasDefaultACL / GrantDefaultACLPrivilege / RevokeDefaultACLPrivilege
DefaultACLText / DefaultACLEntries
DefaultACLObjTypeFromParserObjType / DefaultACLNormalizePriv

// ── Publications / Subscriptions (pubsub.go) ────────────────────────
CreatePublication / CreatePublicationAsOwner / DropPublication / SetPublicationOwner
LookupPublication / Publications / PublicationsForDBOid
CreateSubscription / CreateSubscriptionAsOwner / DropSubscription / SetSubscriptionOwner
LookupSubscription / Subscriptions / SubscriptionsForDBOid
AddSubscriptionRel / AdvanceSubscriptionRel / LookupSubscriptionRel / SubscriptionRels

// ── Codec (codec.go + small files) ──────────────────────────────────
PGClassRow / PGAttributeRow / PGTypeRow encode+decode
PadBpchar (bpchar.go) / EncodingIDToName (encoding.go)
SerializeTSDictOptions / DeserializeTSDictOptions
FormatExprForAttrdef / BuildTableReloptions / BuildIndexReloptions / ApplyTableReloptions
FormatPartitionBound / ArrayTextLiteral / ReloptionsElements

// ── Partition routing ──────────────────────────────────────────────
FindPartitionForValue       FindRangePartitionForValue
FindRangePartitionForDatums FindHashPartitionForValue
FindHashPartitionByHash     PartitionChildren / IndexPartitionChildren
RegisterPartitionChild / UnregisterPartitionChild
MarkPartitionDetachPending / ClearPartitionDetachPending
VisiblePartitionChildren / InheritanceChildren / AccessibleInheritanceChildren

// ── Cross-package hooks (import-cycle breakers) ────────────────────
SequenceParamsFunc / FindSequenceOwnedByFunc
SetRelationSizer / RelationBlocks / RelAllVisibleFunc
RelNBlocksFunc / RelationLockRowsFunc / AdvisoryLockRowsFunc
VirtualSpecLockRowsFunc / UserTableTriggerStatsFunc
RegprocName / RegprocedureName / RegprocedureNameParts
RegoperatorName / RegoperatorNameAndSchema
IndexRealPages / ResolveIndexColumnOpclassOID / ResolveIndexColumnCollationOID

// ── OID / metadata ─────────────────────────────────────────────────
AllocOID / AdvanceNextOIDPast / NextOID
RelFileNode / IndexRelFileNode / ToastRelFileNode / ToastRelFileNodeByOID
RelAllVisible / DatFrozenXID / SetDatabaseACLChangeXID / TableACLChangeXID
SetTablePendingDropXID / TablePendingDropXID / ClearTablePendingDropXID
AddPgClassRowMark / PgClassRowMarks / ClearPgClassRowMark / ClearPgClassRowMarksForXID
SetComment / GetComment / AllComments
RegisterCompatObject / DropCompatObject / ListCompatObjects

// ── Node-tree forward OID resolution (pg_node_oid_lookup.go) ───────
LookupOperatorForNode / LookupProcForNode / ProcResultType

// ── index AM metadata ──────────────────────────────────────────────
IndexAMCapabilityByName / AccessMethodOIDByName / AccessMethodNameByOID
LanguageNameToOID / LookupOpClassSupportProcOID / DefaultBtreeOpclassForSubtype
```

## Codec format (`codec.go`)

The on-disk binary format matches `executor.EncodeRow` / `executor.DecodeRowInto`
so a standard SeqScan on pg_class / pg_attribute / pg_type sees correct values
without special-casing the system catalogs:

- **1 null-flag byte** per column (0 = present, 1 = NULL)
- **int4** → 4 bytes, big-endian uint32
- **bool** → 1 byte (1 = true, 0 = false)
- **text** → 4-byte big-endian uint32 length + raw UTF-8 bytes

Fixed-size layout constants guarantee that the codec can decode any column
without walking null-bitmaps:

- **pg_class**: 144 bytes fixed part, with offsets `pgClassOffOID=0`,
  `pgClassOffRelName=4`, `pgClassOffRelNamespace=68`, `pgClassOffRelFileNode=88`,
  `pgClassOffRelTablespace=92`, `pgClassOffRelIsShared=117`,
  `pgClassOffRelPersistence=118`, `pgClassOffRelKind=119`,
  `pgClassOffRelNAtts=120`, `pgClassOffRelIsPopulated=129`
- **pg_attribute**: 100 bytes fixed part, with offsets `pgAttributeOffRelID=0`,
  `pgAttributeOffName=4`, `pgAttributeOffTypID=68`, `pgAttributeOffNum=74`,
  `pgAttributeOffTypMod=76`, `pgAttributeOffNotNull=86`,
  `pgAttributeOffIdentity=89`, `pgAttributeOffIsDropped=91`
- `pgNameDataLen = 64` bytes for `name`-type columns (padded with \0)

The 34-column pg_class alignment exists precisely so `scanMatching` can decode
physical pg_class heap tuples for statements like `UPDATE pg_class SET reltuples…`
(`catalog.go:8423–8142`).

## `pg_proc_names_generated.go` structure

This 13,623-line generated file provides three maps:

- **`pgProcNamesByOID map[uint32]string`** — OID → proname (all PG18 built-in procs)
- **`pgProcArgTypeNamesByOID map[uint32][]string`** — OID → argument type names (parallel to proargtypes)
- **`pgProcRetTypeByOID map[uint32]uint32`** — OID → prorettype OID

These are consumed by:
- `pg_node_oid_lookup.go` — builds forward indexes `LookupProcForNode`/`LookupOperatorForNode`
- `regproc.go` — `RegprocName`, `RegprocedureName`, `RegprocedureNameAndSchema`
- `initdb` — pg_proc heap row bootstrap
- pg_proc virtual row builder — `registerPgProcView` / `BuiltinProcs`

## `Routines` registry details (`routines.go`)

The `Routines` struct owns three maps guarded by a single `RWMutex`:

```go
type Routines struct {
    mu      sync.RWMutex
    byKey   map[string]*Routine   // routineKey(dbOid, schema, name, signature) → *Routine
    byName  map[string][]string   // nameKey(dbOid, schema, name) → []overloadKeys
    nextOID uint32
    heapTIDs map[uint32]SchemaHeapTID  // OID → live pg_proc heap TID
}
```

Key format: `routineKey` = `dbPrefix:lower(schema).lower(name)(type1,type2,…)`;
`nameKey` = `dbPrefix:lower(schema).lower(name)`. The `dbPrefix` is `"dbN:"`
where N is the database OID, or `""` for DefaultDBOid.

Overload resolution: `LookupByName` returns ALL routines matching a bare name
(one per signature). `Lookup` with explicit arg types returns exactly one.
`ResolveByName` returns an error if the name is ambiguous (multiple overloads).
`ResolveBySig` resolves by name + arg types, returning an error if not found.

The `Routine` struct carries 35 fields including `ArgTypeSchemas` (for regprocedure
output), `ArgTypeOIDs` (for the `char` ambiguity), `SequenceDeps`/`RoutineCallOIDs`/
`TableDeps`/`ColumnDeps` (dependency tracking), `Config` (proconfig SET clauses),
and `Aggregate *UserAggregate` (prokind='a' routines).

## `PubSub` API details (`pubsub.go`)

The `PubSub` struct wraps a `sync.RWMutex` and three maps:

```go
type PubSub struct {
    mu              sync.RWMutex
    publications    map[string]*Publication   // pubMapKey(dbOid, name) → *Publication
    subscriptions   map[string]*Subscription  // subMapKey(dbOid, name) → *Subscription
    subscriptionRels []*SubscriptionRel       // in creation order
}
```

Key format: `pubMapKey`/`subMapKey` = `"dbN:name"` where N is the database OID.
`PublicationsForDBOid`/`SubscriptionsForDBOid` filter by database.

`SubscriptionRel` tracks per-relation replication progress:
```go
type SubscriptionRel struct {
    SubName     string
    RelOID      uint32
    State       string  // 'i' = init, 'r' = ready, 's' = syncing, ...
    LSN         uint64
}
```

`AdvanceSubscriptionRel` updates the LSN and state of a subscription relation,
used by the apply worker to track which WAL position it has consumed.

## Key flow: CREATE TABLE

```mermaid
sequenceDiagram
    participant E as executor ddlOp
    participant C as catalog InMemory
    participant H as heap files
    participant W as xlog
    E->>E: execCreateTable(parser.CreateStmt)
    E->>C: CreateTable(schema, name, cols, ...)
    C->>C: AllocOID → table OID
    C->>C: getOrCreateNS(dbOid) → namespace
C->>C: store table in tables['schema.name']
    C->>C: RegisterPartitionChild if partition
    C-->>E: *Table
    E->>C: CreateIndex(table, primary key, ...)
    C->>C: AllocOID → index OID
C->>C: store index in indexes['schema.name']
    C-->>E: *Index
    E->>E: syncTableToCatalogHeap(table)
    E->>H: write pg_class heap row (144 bytes)
    E->>H: write pg_attribute heap rows (100 bytes each)
    E->>H: write pg_class btree index rows
    E->>H: write pg_attribute btree index rows
    E->>H: write pg_attrdef rows for column defaults
    E->>H: write pg_inherits rows if partition
    E->>W: EncodeSmgrCreate(rel) + EncodeHeapInsert (catalog WAL)
    E->>E: RelcacheInitFileUnlink(dbOid)
    E->>C: Invalidate() plan cache
```

## Dependencies

- **Used by** — executor (101 files), optimizer (39), initdb (29), postmaster (7),
  replication (5), backup, nbtree, server/grant_ddl.go, server/role_ddl.go.
- **Uses** — `internal/parser` (view ASTs, expression types), `internal/storage`
  (RelFileNode, TransactionID), `internal/utils/*`, and injected function hooks
  instead of imports wherever executor behaviour would be needed
  (`SequenceParamsFunc`, `PgClassRows` wiring, `SetRelationSizer`) — the layering
  rule is that catalog must stay below executor.

## Recovery flow

```mermaid
sequenceDiagram
    participant X as xlog/recovery
    participant C as catalog InMemory
    participant I as initdb
    participant H as heap files

    X->>I: StartupXLOG
    I->>I: RelcacheInitFileRemoveAll
    I->>H: loadSystemCatalogsIfPresent(base/1/1247, 1249, 1262)
    I->>C: RegisterRealTable(pg_type, pg_attribute, pg_database)
    I->>H: loadUserTablesFromHeap (base/1/1259 + indexes)
    I->>C: RegisterTable x N (for each user table)
    I->>H: loadRoutinesFromHeap (pg_proc scan)
    I->>C: routines.CreateDuringRecovery x N
    I->>H: loadUserIndexesFromHeap (pg_index scan)
    I->>C: RegisterIndexDuringRecovery x N
    X->>X: WAL replay begins
    loop For each WAL record
        X->>C: MutatorDuringRecovery call
        alt DDL record
            C->>H: syncTableToCatalogHeap
        end
    end
    X->>I: AdvanceNextOIDPast(scan max)
    X->>C: SetRelationSizer (wire up buffer pool)
    I->>C: DetectCatalogDBOID (resolve db OID)
```

## Notable patterns / gotchas

- **Sibling renderers** — the virtual pg_class builder and the heap-written
  pg_class row are two renderers of the same object; changes must land in both
  or pg_dump/PG-standby and `\d` disagree. Same for pg_proc (virtual rows from
  `Routines` registry + heap rows from `syncRoutineToCatalogHeap`).
- **Do not "fix" the db 5→1 mapping** — making postgres connections use their
  own namespace makes every existing table vanish (`catalog.go:24910–23961`).
- **The live "postgres" database's displayed OID must stay the 16384 placeholder** —
  CREATE SUBSCRIPTION subdbid and datacl heap resync depend on it (:2361–2368).
- **`RoleAttrs.ConnLimit == 0` is a *valid PG setting* ("no new connections")**;
  "no attributes" constructions must set −1 explicitly (:15986–15989).
- **~15 `Table`/`Column` fields exist for pg_dump round-trip fidelity only**
  (Compression, StatTarget, Collation, ReplicaIdentity, RowSecurity, Policies,
  Rules, ParallelWorkers, Tablespace…) — goopg does not act on them.
- **`GeneratedVirtual` does not change storage**: every generated column is
  materialized STORED (:120–127).
- **`SmallDimension` is retired** (M0125-0043): nothing sets it; the planner derives
  smallness at plan time from `internal/planner/small_dimension.go`.
- **Empty `TempOwner` on a temp relation means visible-to-all-sessions**
  (single-session legacy compat, `:629`).
- **Recovery twin drift hazard** — every `*DuringRecovery` method is a second
  path that must stay in sync with the primary. If a new field is added to a
  struct and the primary mutator sets it, the recovery twin must too.
- **`IsImmediate()` on Index** — `Deferrable` alone determines pg_index.indimmediate,
  NOT `InitiallyDeferred`. Use `idx.IsImmediate()` (`!idx.Deferrable`), not
  a manual check of both fields.
- **`Index.DeclaredHash`** — goopg has no native hash access method; a hash index
  is built on the B-tree substrate (Method stays "btree") but the flag gates
  SERIALIZABLE predicate locking at page grain vs relation grain. Not persisted
  to WAL, so it resets after restart.
- **`relSizer` is atomic, not under `c.mu`** — the planner reads it once per
  FROM item and must not contend on the catalog lock (`:2467–2470`).
- **`SchemaHeapTID` family** — ~10 parallel `*HeapTID` maps (schema, type, operator,
  collation, conversion, event_trigger, publication, ts_dict, ts_config) enable
  O(1) heap UPDATE of catalog rows. They are seeded at CREATE and at startup
  reload, refreshed by every ALTER heap UPDATE, and deleted with the object.
  The B1.x/B2.x/B3.x numbering in doc 02a §3.3 tracks which TID-carrying cache
  contract applies to each catalog.
- **`pendingAttachXID` vs `pendingPartitionDetachCount`** — two different
  partition concurrency mechanisms. `pendingAttachXID` defers FK registration
  to COMMIT (avoids deadlock with concurrent RI checks). `DetachPendingEpoch`
  makes a partition invisible to snapshots taken after the detach began. Both
  are zero in the steady state.
- **`tableACLOrder` and `attrACLOrder` preserve grant order** — PostgreSQL
  appends a new grantee's aclitem to the end of the array. goopg must follow
  this list (not alphabetical) or pg_dump emits GRANT lines in the wrong order.
- **`roleACLDisplay` preserves original-case spelling** — PostgreSQL role names
  are case-significant when double-quoted. Without this, a lower-cased store
  would render `mixedcase` and pg_dump would emit `TO mixedcase` (a different,
  nonexistent role).
- **`relACLEmptied` vs `relACLOwnerRevoked`** — `REVOKE ALL FROM <owner>` on a
  table leaves `{}` (non-NULL empty array), which pg_dump emits as `REVOKE ALL
  … FROM <owner>;`. On a function, the same revoke may leave a surviving PUBLIC
  aclitem — `relACLOwnerRevoked` captures that case.
- **`defaultACLGlobal`** — a global default entry (no IN SCHEMA) inherits the
  target role's full acldefault() rights; a schema-scoped entry's baseline is
  empty. Real PostgreSQL seeds `old_acl` from `acldefault()` only when `nspid`
  is invalid.
- **`Publication.Owner` and `Subscription.Owner` must be non-zero** — pg_dump's
  `getRoleName()` calls `pg_fatal()` with "role with OID %u does not exist" if
  the OID doesn't resolve. Both default to `BootstrapSuperuserOID (10)`, but
  `CreatePublicationAsOwner`/`CreateSubscriptionAsOwner` set the session's
  currently-effective role.
- **`PadBpchar` lives in catalog, not executor or WAL** — because both
  `internal/executor` and `internal/wal` must apply the identical rule and
  neither may import the other. The package that owns `Type` is the natural home.
- **`pgConvEncNames` is duplicated from initdb** — initdb cannot be imported
  from catalog without a cycle, and the encoding-ID↔name mapping is an immutable
  PostgreSQL constant.
- **`allDigits` in relcache_inval.go** — the sweep that removes `pg_internal.init`
  from every database directory under `base/`, `pg_tblspc/`, and `global/` must
  not descend into names like `pgsql_tmp`. The `allDigits` guard is what keeps
  it safe.
- **`NamespaceDBOid` maps 0 and 5 back to 1** — but only the DISPLAYED oid.
  Write paths still persist under 1. The `databaseOid` map tracks the REAL
  distinct OID for genuinely new databases, while the bootstrap databases
  keep the legacy 16384 placeholder.
- **`lookupTable` vs `LookupTable`** — the unexported `lookupTable` (lowercase)
  is a direct map access without search-path resolution; the exported
  `LookupTable` on `SearchPathCatalog` tries schemas in order. Using the wrong
  one bypasses temp-table visibility and partition-detach epoch gating.
- **`resolveDBOid` defaulting to 1** — every accessor takes `dbOid ...uint32`
  that defaults to `DefaultDBOid = 1`. A caller that forgets to pass the
  connection's real database OID silently reads/writes database 1's objects.
- **Toast table naming** — `ToastRelName` generates `pg_toast_<relOID>` names;
  `ToastParentTable` resolves the parent relation from a toast OID.
  `ToastRelFileNode` constructs the `RelFileNode` for the toast fork.
  `RenameToastRel` is called during `ALTER TABLE RENAME TO` to keep the toast
  name in sync.
- **`registerSystemTables` seeds all built-in OIDs** — called once at startup,
  it installs pg_type, pg_attribute, pg_class, pg_database, pg_proc, and all
  other bootstrap catalogs with their canonical PG18 OIDs. A new built-in
  catalog must be added here or the OID is available for user allocation.
- **`FirstRoutineOID = 1<<17`** — built-in pg_proc entries occupy OIDs 1 through
  131071 (PG18 has ~4,000 built-in procs). User-defined routines start at 131072.
  The gap is a safety margin so generated pg_proc entries never collide with
  user routines.

## Built-in type OID reference (`codec.go`)

The `codec.go` file defines the built-in type OID constants. The most
frequently referenced:

| OID | Type | Kind |
|---|---:|---|
| 16 | `bool` | boolean |
| 17 | `bytea` | varlena |
| 18 | `char` | char |
| 19 | `name` | name |
| 20 | `int8` | int64 |
| 21 | `int2` | int16 |
| 23 | `int4` | int32 |
| 25 | `text` | varlena |
| 26 | `oid` | uint32 |
| 28 | `xid` | uint32 |
| 29 | `cid` | uint32 |
| 30 | `oidvector` | varlena |
| 114 | `json` | varlena |
| 142 | `xml` | varlena |
| 600 | `point` | by-ref |
| 601 | `lseg` | by-ref |
| 602 | `path` | by-ref |
| 603 | `box` | by-ref |
| 604 | `polygon` | by-ref |
| 628 | `line` | by-ref |
| 650 | `cidr` | varlena |
| 700 | `float4` | float32 |
| 701 | `float8` | float64 |
| 705 | `unknown` | — |
| 718 | `circle` | by-ref |
| 790 | `money` | int64 |
| 829 | `macaddr` | by-ref |
| 869 | `inet` | varlena |
| 1033 | `aclitem` | varlena |
| 1042 | `bpchar` | varlena |
| 1043 | `varchar` | varlena |
| 1082 | `date` | int32 |
| 1083 | `time` | int64 |
| 1114 | `timestamp` | int64 |
| 1184 | `timestamptz` | int64 |
| 1186 | `interval` | by-ref |
| 1266 | `timetz` | int64 |
| 1560 | `bit` | varlena |
| 1562 | `varbit` | varlena |
| 1700 | `numeric` | by-ref |
| 2249 | `record` | — |
| 2278 | `void` | — |
| 2279 | `trigger` | — |
| 2950 | `uuid` | by-ref |
| 3802 | `jsonb` | varlena |
| 3904 | `int4range` | by-ref |
| 4072 | `jsonpath` | varlena |

`REGCLASS` (2205), `REGPROC` (24), `REGOPER` (2203), `REGNAMESPACE` (4089),
`REGROLE` (4096), and `REGTYPE` (2206) are also defined for the reg* family.

## Codec row layouts

**pg_class row** (144-byte fixed part). The column layout with offsets:

| Offset | Column | Size |
|---|---:|---:|
| 0 | `relname` (name) | 64 |
| 64 | `relnamespace` (oid) | 4 |
| 68 | `reltype` (oid) | 4 |
| 72 | `reloftype` (oid) | 4 |
| 76 | `relowner` (oid) | 4 |
| 80 | `relam` (oid) | 4 |
| 84 | `relfilenode` (oid) | 4 |
| 88 | `reltablespace` (oid) | 4 |
| 92 | `relpages` (int4) | 4 |
| 96 | `reltuples` (float4) | 4 |
| 100 | `relallvisible` (int4) | 4 |
| 104 | `reltoastrelid` (oid) | 4 |
| 108 | `relhasindex` (bool) | 1 |
| 109 | `relisshared` (bool) | 1 |
| 110 | `relpersistence` (char) | 1 |
| 111 | `relkind` (char) | 1 |
| 112 | `relnatts` (int2) | 2 |
| 114 | `relchecks` (int2) | 2 |
| 116 | `relhasrules` (bool) | 1 |
| 117 | `relhastriggers` (bool) | 1 |
| 118 | `relhassubclass` (bool) | 1 |
| 119 | `relrowsecurity` (bool) | 1 |
| 120 | `relforcerowsecurity` (bool) | 1 |
| 121 | `relispopulated` (bool) | 1 |
| 122 | `relreplident` (char) | 1 |
| 123 | `relispartition` (bool) | 1 |
| 124 | `relrewrite` (oid) | 4 |
| 128 | `relfrozenxid` (xid) | 4 |
| 132 | `relminmxid` (xid) | 4 |
| 136 | `relacl` (aclitem[]) | 4+ |
| 140 | `reloptions` (text[]) | 4+ |
| 144 | `relpartbound` (pg_node_tree) | 4+ |

The fixed 144-byte prefix means the codec can decode `relname`/`relnamespace`/
`relfilenode`/`relkind`/`relnatts` without walking the null bitmap.

**pg_attribute row** (100-byte fixed part):

| Offset | Column | Size |
|---|---:|---:|
| 0 | `attrelid` (oid) | 4 |
| 4 | `attname` (name) | 64 |
| 68 | `atttypid` (oid) | 4 |
| 72 | `attstattarget` (int4) | 4 |
| 76 | `attlen` (int2) | 2 |
| 78 | `attnum` (int2) | 2 |
| 80 | `attndims` (int4) | 4 |
| 84 | `attcacheoff` (int4) | 4 |
| 88 | `atttypmod` (int4) | 4 |
| 92 | `attbyval` (bool) | 1 |
| 93 | `attstorage` (char) | 1 |
| 94 | `attalign` (char) | 1 |
| 95 | `attnotnull` (bool) | 1 |
| 96 | `atthasdef` (bool) | 1 |
| 97 | `atthasmissing` (bool) | 1 |
| 98 | `attidentity` (char) | 1 |
| 99 | `attgenerated` (char) | 1 |
| 100 | `attisdropped` (bool) | 1 |
| 101 | `attislocal` (bool) | 1 |
| 102 | `attinhcount` (int2) | 2 |
| 104 | `attcollation` (oid) | 4 |
| 108 | `attacl` (aclitem[]) | 4+ |
| 112 | `attoptions` (text[]) | 4+ |
| 116 | `attfdwoptions` (text[]) | 4+ |

## `Catalog` interface method groups

The `Catalog` interface (~85 methods) breaks down into groups:

**Table/index**: `CreateTable`, `CreateIndex`, `DropTable`, `DropIndex`,
`AddColumn`, `RenameTable`, `RenameIndex`, `RegisterRealTable`,
`RegisterVirtualTable`, `AllTables`, `AllIndexes`, `TablesInSchema`,
`UserTableHandles`, `LookupTable`, `LookupIndex`, `LookupTableByOID`,
`LookupIndexByOID`, `IndexesOnTable`, `HasPrimaryKey`, `ToastRelName`,
`ToastRelFileNode`, `ToastRelFileNodeByOID`, `DropSessionTempObjects`,
`SessionTempTableNames`.

**Schema**: `SchemaExists`, `SchemaOID`, `SchemaNameForOID`, `RegisterSchema`,
`UnregisterSchema`, `RenameSchema`.

**Types**: `RegisterEnum`, `DropEnum`, `AddEnumValue`, `RemoveEnumValue`,
`RenameEnum`, `LookupEnum`, `LookupEnumByOID`, `LookupEnumByArrayOID`;
`RegisterDomain`, `DropDomain`, `RenameDomain`, `LookupDomain`,
`LookupDomainByOID`; `RegisterCompositeType`, `RegisterCompositeTypeWithFields`,
`DropCompositeType`, `LookupCompositeType`, `LookupCompositeTypeByOID`;
`RegisterRangeType`, `DropRangeType`, `RenameRangeType`, `LookupRangeType`,
`LookupRangeTypeByOID`.

**Routines**: `Routines().Create`, `Routines().Drop`, `Routines().Lookup`,
`Routines().LookupByName`, `Routines().LookupByOID`, `Routines().ResolveByName`,
`Routines().ResolveBySig`, `Routines().RenameRoutine`, `Routines().SetSchema`.

**Roles/privileges**: `RegisterRole`, `UnregisterRole`, `RenameRole`,
`RoleOID`, `RoleNameForOID`, `IsSuperuser`, `RoleIsMemberOf`,
`IsAdminOfRole`, `HasPrivsOfRole`, `GrantRoleMembership`,
`RevokeRoleMembership`, `GrantTablePrivilege`, `RevokeTablePrivilege`,
`HasTablePrivilege`, `GrantColumnPrivilege`, `RevokeColumnPrivilege`,
`RelaclText`, `ProcACLText`, `TypeACLText`, `NamespaceACLText`.

**Statistics**: `RegisterStatistics`, `DropStatistics`, `LookupStatistics`,
`StatisticsByOID`.

**Extensions/tablespaces/collations**: `CreateExtension`, `DropExtension`,
`CreateTablespace`, `DropTablespace`, `RenameTablespace`, `LookupTablespaceOID`,
`CreateCollation`, `DropCollation`, `RenameCollation`, `CreateConversion`,
`DropConversion`, `CreateTSDict`, `DropTSDict`, `CreateTSConfig`,
`DropTSConfig`.

**Publications/subscriptions**: `CreatePublication`, `DropPublication`,
`LookupPublication`, `Publications`, `PublicationsForDBOid`,
`CreateSubscription`, `DropSubscription`, `LookupSubscription`,
`Subscriptions`, `SubscriptionsForDBOid`.

**Partition routing**: `FindPartitionForValue`, `FindRangePartitionForValue`,
`FindRangePartitionForDatums`, `FindHashPartitionForValue`,
`FindHashPartitionByHash`, `PartitionChildren`, `IndexPartitionChildren`,
`VisiblePartitionChildren`, `InheritanceChildren`,
`AccessibleInheritanceChildren`, `RegisterPartitionChild`,
`UnregisterPartitionChild`, `MarkPartitionDetachPending`,
`ClearPartitionDetachPending`.

**OID/misc**: `AllocOID`, `AdvanceNextOIDPast`, `NextOID`, `DBOID`,
`SetDBOID`, `SetComment`, `GetComment`, `AllComments`, `RegisterCompatObject`,
`DropCompatObject`, `ListCompatObjects`, `SetRelationSizer`, `RelationBlocks`,
`RegprocName`, `RegprocedureName`, `RegoperatorName`, `IndexRealPages`,
`ResolveIndexColumnOpclassOID`, `ResolveIndexColumnCollationOID`.

## `SearchPathCatalog` vs `InMemory` — the wrapper pattern

Every `Catalog` method has two implementations: the concrete `*InMemory`
receiver and the `SearchPathCatalog` wrapper. The wrapper:

1. Delegates the actual work to the wrapped `Catalog`.
2. Applies search-path resolution for unqualified names (`LookupTable`,
   `LookupIndex`).
3. Filters temp tables by `TempOwnerToken`.
4. Routes per-database operations to the right namespace (the wrapper's
   `DBOid`).
5. Applies the partition-detach epoch gate to `VisiblePartitionChildren`.

The executor and optimizer take a `catalog.Catalog` (the interface); the
postmaster constructs a `SearchPathCatalog` per connection with the session's
search path. The `SearchPathCatalog` is stateless — it holds a reference to
the underlying `InMemory` and the session-specific settings.

## `Routine` struct fields (`routines.go`)

The `Routine` struct (35 fields) tracks:

- Identity: `Name`, `Schema`, `DBOid`, `OID`, `Kind` (function/procedure),
  `Language`, `Owner`.
- Signature: `ArgTypes []string`, `ArgTypeOIDs []uint32`,
  `ArgTypeSchemas []string`, `ArgDefaults []string`, `ArgModes []string`,
  `ArgNames []string`, `ReturnType string`, `ReturnTypeSet bool`.
- Behavior: `Volatility`, `Strict`, `SecurityDefiner`, `Leakproof`,
  `ParallelSafe`, `Cost`, `Rows`.
- Body: `Body string` (SQL/PL/pgSQL source), `Bin string` (C library path),
  `Prosrc string` (the SQL body text), `Config []string` (proconfig SET).
- Dependencies: `SequenceDeps`, `RoutineCallOIDs`, `TableDeps`, `ColumnDeps`.
- Aggregate: `Aggregate *UserAggregate` (prokind='a').

`ResolveByName` returns all overloads of a name; `ResolveBySig` resolves by
name + arg types. Ambiguity (multiple overloads with the same name and
arg-types after type coercion) is an error, matching `func_match_argtypes`.

## Virtual catalog registration order

`registerInformationSchemaTables` (catalog.go:11812) registers the
information_schema views. The order matters because some views reference
others in their query definitions. The registration order:

1. `information_schema.tables` (the base view)
2. `information_schema.columns`
3. `information_schema.schemata`
4. `information_schema.views`
5. `information_schema.routines`
6. `information_schema.parameters`
7. `information_schema.referential_constraints`
8. `information_schema.key_column_usage`
9. `information_schema.table_constraints`
10. `information_schema.table_privileges`
11. `information_schema.column_privileges`
12. `information_schema.role_table_grants`
13. `information_schema.element_types`
14. `information_schema.sequences`
15. `information_schema.domain_constraints`
16. `information_schema.domains`
17. `information_schema.domain_udt_usage`
18. `information_schema.role_column_grants`
19. `information_schema.role_routine_grants`
20. `information_schema.check_constraints`

## Type name resolution helpers

`ResolveColumnType` resolves a column type by name, handling `serial`/`bigserial`/
`smallserial` (via `IsSerialTypeName`) and array type names. `TablesWithColumnOfType`
finds all tables with a column of a given type (used by `DROP TYPE` RESTRICT).
`FindColumnUsingDomainTransitively` walks domain base types to find whether a
domain's base uses a given type.

## Operator lookup for node resolution (`pg_node_oid_lookup.go`)

`LookupOperatorForNode(opname, leftOID, rightOID)` returns an `OperatorEntry`
for an operator named `opname` with the given operand type OIDs. The
`pg_operator_seed_data.go` file provides the full 799-row PG18 pg_operator
seed as `PGOperatorAllEntries()`. The forward index maps
`(name, leftOID, rightOID)` → operator OID and metadata.

`LookupProcForNode(funcname, argOIDs)` returns the funcid for a built-in
function. `ProcResultType(funcid)` returns its `prorettype` OID from
`pgProcRetTypeByOID`.

These three are the resolver's sole entry points into the catalog — the
`nodes` package must not import the full catalog (layer rule), so it uses
these narrow forward indexes instead.

## `InMemory` method implementation details

### `ns` / `getOrCreateNS` — namespace resolution

`ns(dbOid)` returns the `tableNamespace` for the given database OID, or
`emptyTableNamespace` if the database is unknown. `getOrCreateNS` creates
one if it doesn't exist (called by `CreateTable`, `CreateIndex`, etc.).

`resolveDBOid` defaults `dbOid=0` to `DefaultDBOid=1` — the `PostgresDBOid=5`
is also mapped to 1 for display purposes only. Write paths always persist
under the real OID.

### `registerSystemTables` — bootstrap catalog registration

Called once at startup, this method (catalog.go:8418) registers every
built-in catalog table with its canonical PG18 OID. The registration order:

1. `pg_type` (1247) — heap-backed, non-virtual.
2. `pg_attribute` (1249) — heap-backed, non-virtual.
3. `pg_class` (1259) — virtual, Go builder.
4. `pg_database` (1262) — shared catalog, heap-backed.
5. `pg_proc` (1255) — virtual, from `pgProcNamesByOID`.
6. `pg_namespace` (2615) — virtual.
7. `pg_constraint` (2662) — virtual.
8. `pg_depend` (2608) — virtual.
9. `pg_index` (2610) — virtual.
10. `pg_trigger` (2620) — virtual.
11. `pg_rewrite` (2618) — virtual.
12. `pg_operator` (2617) — virtual.
13. `pg_opclass` (2616) — virtual.
14. `pg_am` (2601) — virtual.
15. `pg_amop` (2602) — virtual.
16. `pg_amproc` (2603) — virtual.
17. `pg_enum` (3501) — virtual.
18. `pg_range` (3541) — virtual.
19. `pg_attrdef` (2604) — virtual.
20. `pg_inherits` (2611) — virtual.
21. `pg_stat_user_tables` — virtual (Go builder).
22. `pg_stat_all_tables` — virtual.
23. `pg_stat_sys_tables` — virtual.
24. ... + 20+ more pg_stat_* and information_schema views.

### `syncTableToCatalogHeap` — physical catalog persistence

Called by ~12 DDL statements, this function (operators_ddl.go:18036) writes
the pg_class, pg_attribute, pg_attrdef, and pg_inherits heap rows:

1. Compute the pg_class heap row as a 144-byte fixed part using the
   `PGClassRow` codec.
2. Write the row via `PageAddHeapTuple` + `smgr.Extend`/`WriteBlock`.
3. Build the pg_class btree index entry and WAL-log it.
4. For each column, write the pg_attribute heap row (100 bytes).
5. Build the pg_attribute btree index entry.
6. For each column default, write the pg_attrdef heap row (with the
   `adbin` serialized by `nodes.Out`).
7. For partition children, write the pg_inherits row.
8. WAL-log every heap mutation.

The function is the "sibling renderer" to the virtual pg_class builder.
Both must produce the same column values or `\d` and pg_dump disagree.

### `registerSystemTables` bootstrapping flow

The full startup flow for catalog initialization:

1. `initdb/open.go` calls `loadSystemCatalogsIfPresent` which reads the
   heap files for `pg_type` (1247), `pg_attribute` (1249), and `pg_database`
   (1262) from disk.
2. `RegisterRealTable` registers each as a real (non-virtual) table.
3. `loadUserTablesFromHeap` scans `base/1/1259` (pg_class heap) and
   registers every user table as a `Table`.
4. `loadRoutinesFromHeap` scans `pg_proc` for user-defined routines.
5. `loadUserIndexesFromHeap` scans `pg_index` for user-defined indexes.
6. `registerSystemTables` installs the virtual system catalogs on top of
   the heap-backed ones.
7. WAL replay runs `Register*DuringRecovery` for any DDL records.

The `registerSystemTables` call is idempotent — it skips tables whose OID
is already registered by `RegisterRealTable`.

## OID allocation details

`AllocOID` returns the next available OID and advances the counter:

```go
func (c *InMemory) AllocOID() uint32 {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.allocOIDLocked()
}

func (c *InMemory) allocOIDLocked() uint32 {
    oid := c.nextOID
    c.nextOID++
    return oid
}
```

`AdvanceNextOIDPast(maxOID)` bumps the counter past a given OID (used at
startup to establish the floor). `NextOID` returns the current value without
allocating.

`FirstUserOID = 16384` means user tables start at OID 16384. Built-in
catalogs occupy OIDs 1-16383. `BootstrapSuperuserOID = 10` is the
bootstrapped superuser role. `FirstRoutineOID = 131072` leaves room for
built-in pg_proc entries.

## `RoleMembership` graph

`roleMembers` maps `roleMembershipKey{member, role}` → `*RoleMembership`:

```go
type RoleMembership struct {
    Grantor   uint32
    Inherit   bool
    AdminOption bool
}
```

`GrantRoleMembership` adds an entry; `RevokeRoleMembership` removes it.
`RoleIsMemberOf` walks the graph: it follows `member→role` edges
transitively (respecting `Inherit`). `IsAdminOfRole` checks for a direct
edge with `AdminOption=true`.

`AllRoleMemberships` returns all edges for `pg_auth_members`. `AllRoleStates`
returns all roles with their `RoleAttrs` for `pg_roles`.

## Constraint recording

Constraints are stored on the `Table` struct:

- `NamedChecks`: `CHECK (expr)` with a name.
- `NotNullConstraints`: `NOT NULL` with a name.
- `CheckConstraints`: unnamed `CHECK (expr)`.
- `ForeignKeys`: `FOREIGN KEY (col) REFERENCES other(col)`.
- `PrimaryKey`: derived from `HasPrimaryKey`.
- `Unique`: `UNIQUE (col)` — stored as `Index.Unique` on the index.

`PGConstraintRowsForDBOid` renders these as `pg_constraint` rows. The
`contype` is `'p'` (primary key), `'u'` (unique), `'f'` (foreign key),
`'c'` (check), or `'n'` (not null).

`constraintViewDeps` enforces `DROP CONSTRAINT RESTRICT` for functional
dependency views: a view is marked as dependent on a constraint via
`constraintViewDeps[string] = []string{viewName}`. Dropping the constraint
without `CASCADE` fails if any view depends on it.

## `EnumType` lifecycle

`RegisterEnum(dbOid, name, values)` creates an `EnumType` with the given
ordered values. `AddEnumValue` appends a new value (at the end for PG
compatibility — new enum values are always added at the end, not sorted).
`RemoveEnumValue` removes a value (only if unused). `RenameEnum` renames
the type.

`LookupEnum` resolves by name; `LookupEnumByOID` resolves by OID;
`LookupEnumByArrayOID` resolves the array type of an enum (e.g., `_mood`
for `mood`).

The enum values are stored as `[]EnumValue`:

```go
type EnumValue struct {
    OID   uint32
    Label string
}
```

Each value has its own OID (allocated from the global counter). The sort
order is the position in the `Values` slice (0-indexed).

## `Domain` lifecycle

`RegisterDomain` creates a `Domain` with a base type, not-null constraint,
default expression, and check constraints. `AddDomainCheck` appends a new
check. `DropDomain` removes the domain (RESTRICT only — fails if any column
uses the domain). `DropDomainConstraint` removes a specific check constraint.

`FindColumnUsingDomainTransitively` walks the domain's base type chain to
find whether any column ultimately uses a given type through a domain.

## `CompositeType` lifecycle

`RegisterCompositeTypeWithFields` creates a composite type (used for `CREATE
TYPE AS (col1 type1, col2 type2)`). `CompositeField` specifies the field
name, type, and OID. `LookupCompositeType` resolves by name; `DropCompositeType`
removes it.

## `RangeType` lifecycle

`RegisterRangeType` creates a range type with a subtype, opclass, collation,
canonical function, and subtype_diff function. `MultirangeName` is the
associated multirange type name. `LookupRangeTypeByMultirangeOID` resolves
the range type from its multirange's OID.

## `Cast` registry

`Cast` entries map `(source OID, target OID)` → `Cast` with the coercion
method (function, binary-compatible, I/O, etc.). `LookupCast` resolves
the cast. The cast registry is seeded at startup from the built-in cast
list (PG18's `cast.c`).

## `Transform` registry

`Transform` entries map `(type OID, language OID)` → `Transform` with
`FromFuncOID` and `ToFuncOID` for the type's transformation functions
between the SQL type and the language's internal representation.

## `AccessMethod` registry

`AccessMethod` entries map `(name, dbOid)` → `AccessMethod`:

```go
type AccessMethod struct {
    OID        uint32
    Name       string
    Type       string // "Table" or "Index"
    HandlerOID uint32
}
```

`AccessMethodOIDByName` resolves the OID for a known access method.
`IndexAMCapabilityByName` returns a bitmap of capabilities for a named
index AM (used by the planner to decide which index strategies are
available). The known access methods are "btree" (goopg's sole index AM),
"hash" (stored as btree with DeclaredHash flag), and "heap" (the table AM).

## Partition routing internals

The catalog implements partition routing for LIST, RANGE, and HASH methods:

- **LIST partitions**: `FindPartitionForValue` iterates `PartitionBounds` looking
  for a `PartitionBound` whose `BoundValues` contains the input value. The
  comparison uses the partition key's opclass comparator.
- **RANGE partitions**: `FindRangePartitionForValue` binary-searches the bounds
  sorted by `PartitionBound.Index` (which is the CREATE TABLE order). Each bound
  is a `[lower, upper)` interval; `FindRangePartitionForDatums` handles the
  multi-column case by comparing column-by-column with the opclass.
- **HASH partitions**: `FindHashPartitionForValue` hashes the input value via
  `FindHashPartitionByHash`, which computes `hash(key) % nPartitions` and maps
  the remainder to the bound whose `PartitionBound.Index` matches.
  `FindHashPartitionByHash` is the public API for hash-partition routing.

`PartitionChildren` and `IndexPartitionChildren` return the live partition list
for a parent, excluding those with `DetachPendingEpoch <= snapshotEpoch`.
`VisiblePartitionChildren` applies the epoch gate; `InheritanceChildren` bypasses
it (used for DDL that must see all children).

## ACL model details

ACLs are stored in three parallel maps per table (and per attribute):

```
tableACLs[relOID][grantee] → map[privilege]bool
tableACLGrantor[relOID][grantee] → grantorName
tableACLOrder[relOID] → [grantee1, grantee2, ...]  (preserves grant order)
```

`RelaclText` renders the PG `aclitem[]` text format (e.g. `{user=arwd/user}`)
from the three maps. `GrantTablePrivilege` adds a new grantee/privilege pair;
`RevokeTablePrivilege` removes it. `HasTablePrivilege` checks if a role
(including inherited roles) has the privilege.

Column-level ACLs follow the same pattern with `attrACLKey{relOID, attNum}`.

`MaterializeOwnerACL` ensures the table owner has their implicit privileges in
the ACL map; `IsOwnerACLRevoked` detects the case where `REVOKE ALL FROM owner`
leaves an empty-but-not-null array.

## Role membership resolution

`RoleIsMemberOf` checks if `role` is a member of `parentRole` by walking the
role membership graph stored in `roleMembers[roleMembershipKey{member, role}]`.
`IsAdminOfRole` additionally requires `ADMIN OPTION` (the `grantor` field).
`HasPrivsOfRole` returns true if `role` is a member of `parentRole` with
`inherit=true` (the `Inherit` flag on the membership).

`AllRoleStates` returns the full set of known roles with their `RoleAttrs` for
pg_roles. `AllRoleMemberships` returns the grant edges for pg_auth_members.

## Toast table resolution

`ToastRelName` generates `pg_toast_<relOID>` from a relation OID.
`ToastParentTable` resolves the parent relation from a toast OID, searching
all namespaces. `ToastRelFileNode` constructs the `RelFileNode` for the
toast's `_main` fork. `ToastRelFileNodeByOID` resolves by OID alone (used
by the executor when the toast OID is known but not the parent).

`LookupToastRel` resolves a toast relation by schema+name, returning both
the OID and whether it is an index. `toastBearingTables` returns all tables
that have a toast relation (used by VACUUM).

## Search path resolution (`SearchPathCatalog`)

`SearchPathCatalog` wraps a `Catalog` with additional state:

- `schemas []string` — the effective search_path (pulled from the session
  registry at construction time)
- `tempOwnerToken string` — the current session's temp-table owner token
- `snapshotPartitionDetachEpoch uint64` — the snapshot's epoch for partition
  visibility gating
- `disableSeqScan bool` — `SET enable_seqscan = off`
- `dbOid uint32` — the connection's database OID

`LookupTable` tries each schema in order: first the temp namespace (if the
temp owner token matches), then `pg_catalog`, then `pg_temp` (the session's
own temp schema), then each search_path schema. `LookupIndex` follows the same
order. `IndexesOnTable` returns all indexes for a table (including partition
indexes).

`SearchPathCatalog` also carries `SnapshotPartitionDetachEpoch` to implement
the detach-concurrently gate: `VisiblePartitionChildren` omits children whose
`DetachPendingEpoch` is ≤ the snapshot's epoch.

## Virtual row builder reference

Every virtual catalog has a `PG*RowsForDBOid` function. The row builder
receives the database OID and returns `[][]string` ready for the executor's
VALUES operator. Key builders:

- **PGClassRowsForDBOid**: 34-column pg_class row. Renders relkind, relnatts,
  reltuples (from `Stats.Reltuples` or `Table.Stats`), relfrozenxid, relhasindex,
  relisshared, relpersistence, reloptions, etc.
- **PGAttributeRowsForDBOid**: 25-column pg_attribute row. Renders attname,
  atttypid, attlen, attnum, atttypmod, attnotnull, atthasdef, attidentity,
  attgenerated, attisdropped, etc.
- **PGIndexesRowsForDBOid**: pg_indexes view (schemaname, tablename, indexname,
  tablespace, indexdef). The `indexdef` is reconstructed from `Index` fields.
- **PGConstraintRowsForDBOid**: pg_constraint system view. Only renders
  constraints that goopg tracks (NOT NULL, CHECK, PRIMARY KEY, UNIQUE, FOREIGN
  KEY). Renders conname, connamespace, contype, conrelid, confrelid, etc.
- **PGDependRowsForDBOid**: pg_depend with only sequence-ownership ('a') edges.
- **PGTriggerRowsForDBOid**: pg_trigger — always returns zero rows (goopg has
  no trigger support).
- **PGPolicyRowsForDBOid**: pg_policy — returns zero rows (goopg has no RLS).
- **PGRewriteRowsForDBOid**: pg_rewrite — returns the `_RETURN` rule for
  views stored in `Table.Rules`.
- **PGStatTablesRowsForDBOid**: pg_stat_all_tables / pg_stat_user_tables /
  pg_stat_sys_tables. Renders seq_scan, seq_tup_read, n_live_tup (from
  ANALYZE), n_dead_tup, etc. All counters start at zero.
- **PGStatDatabaseRows**: pg_stat_database (xact_commit, xact_rollback, blks_read,
  blks_hit, etc.) — all counters start at zero.
- **PGCollationRowsForDBOid**: pg_collation rows for default and C/POSIX
  collations plus user-defined collations.
- **PGConversionRowsForDBOid**: pg_conversion rows for built-in and
  user-defined encoding conversions.
- **PGSequenceRowsForDBOid**: pg_sequence rows for owned sequences.
- **PGTSDictRowsForDBOid / PGTSConfigRowsForDBOid**: text search dictionary
  and configuration rows.
- **PGForeignTableRowsForDBOid, PGForeignServerRowsForDBOid,
  PGUserMappingsRowsForDBOid**: FDW-related rows.
- **PGDatabasesRows**: pg_database rows (datname, datdba, encoding, datcollate,
  datconnlimit, etc.).
- **PGInitPrivsRowsForDBOid**: pg_init_privs rows for system catalog initial
  privileges.

## `InMemory` method categories

The `InMemory` struct has methods organized by domain:

**Table/index methods**: `CreateTable`, `CreateIndex`, `DropTable`, `DropIndex`,
`RenameTable`, `RenameIndex`, `AddColumn`, `RegisterRealTable`,
`RegisterVirtualTable`, `TryRegisterUserTable`, `AllTables`, `AllIndexes`,
`TablesInSchema`, `UserTableHandles`, `LookupTable`, `LookupIndex`,
`LookupTableByOID`, `LookupIndexByOID`, `IndexesOnTable`, `HasPrimaryKey`,
`ToastRelName`, `ToastParentTable`, `ToastRelFileNode`, `ToastRelFileNodeByOID`.

**Schema methods**: `SchemaExists`, `SchemaOID`, `SchemaNameForOID`,
`RegisterSchema`, `UnregisterSchema`, `RenameSchema`.

**Type methods**: `RegisterEnum`, `DropEnum`, `AddEnumValue`, `RemoveEnumValue`,
`RenameEnum`, `LookupEnum`, `LookupEnumByOID`, `LookupEnumByArrayOID`;
`RegisterDomain`, `DropDomain`, `RenameDomain`, `LookupDomain`,
`LookupDomainByOID`, `LookupDomainByArrayOID`; `RegisterCompositeTypeWithFields`,
`RegisterCompositeType`, `DropCompositeType`, `LookupCompositeType`,
`LookupCompositeTypeByOID`; `RegisterRangeType`, `DropRangeType`,
`RenameRangeType`, `LookupRangeType`, `LookupRangeTypeByOID`,
`LookupRangeTypeByMultirangeOID`.

**Role/privilege methods**: `RegisterRole`, `UnregisterRole`, `RenameRole`,
`RoleOID`, `RoleNameForOID`, `IsSuperuser`, `RoleIsMemberOf`, `IsAdminOfRole`,
`HasPrivsOfRole`, `GrantRoleMembership`, `RevokeRoleMembership`,
`GrantTablePrivilege`, `RevokeTablePrivilege`, `HasTablePrivilege`,
`GrantColumnPrivilege`, `RevokeColumnPrivilege`, `RelaclText`,
`ProcACLText`, `TypeACLText`, `NamespaceACLText`, `ParameterACLText`,
`DatabaseACLText`, `ForeignServerACLText`, `ForeignDataWrapperACLText`.

**Statistics methods**: `RegisterStatistics`, `DropStatistics`,
`LookupStatistics`, `StatisticsByOID`, `SetStatisticsTarget`,
`RenameStatisticsObject`, `SetStatisticsOwner`, `SetStatisticsSchema`.

**Comment methods**: `SetComment`, `GetComment`, `AllComments`.

**OID methods**: `AllocOID`, `AdvanceNextOIDPast`, `NextOID`, `DBOID`,
`SetDBOID`.

**User operator/aggregate/FDW methods**: `RegisterUserOperator`,
`DropUserOperator`, `LookupUserOperator`, `LookupUserOperatorByOID`;
`RegisterUserOperatorFamily`, `DropUserOperatorFamily`,
`LookupUserOperatorFamily`; `RegisterUserOperatorClass`,
`DropUserOperatorClass`, `LookupUserOperatorClass`; `RegisterAmOpMember`,
`RegisterAmProcMember`; `RegisterForeignDataWrapper`, `DropForeignDataWrapper`,
`LookupForeignDataWrapper`; `RegisterForeignServer`, `DropForeignServer`,
`LookupForeignServer`; `RegisterUserMapping`, `DropUserMapping`,
`LookupUserMapping`; `RegisterAccessMethod`, `DropAccessMethod`,
`LookupAccessMethod`; `RegisterEventTrigger`, `DropEventTrigger`,
`LookupEventTrigger`; `RegisterUserAggregate`, `DropUserAggregate`,
`LookupUserAggregateByName`, `LookupUserAggregateByOID`.

**Inheritance/partition methods**: `RegisterInheritanceChild`,
`UnregisterInheritanceChild`, `InheritanceChildren`,
`AccessibleInheritanceChildren`, `IsInheritanceDescendant`,
`HasTempInheritanceChildren`; `RegisterPartitionChild`,
`UnregisterPartitionChild`, `PartitionChildren`, `IndexPartitionChildren`,
`VisiblePartitionChildren`, `MarkPartitionDetachPending`,
`ClearPartitionDetachPending`, `PendingPartitionDetachCount`.

**Extension/misc methods**: `CreateExtension`, `DropExtension`,
`CreateTablespace`, `DropTablespace`, `RenameTablespace`, `LookupTablespaceOID`;
`CreateCollation`, `DropCollation`, `RenameCollation`, `LookupCollation`; etc.