# 0110-0012 — CREATE SCHEMA durability across restart (WAL-record mechanism)

Status: accepted

## Problem

goopg's `CREATE SCHEMA` was a catalog-only, **non-durable** side effect. The
schema name was recorded in the in-memory catalog registry (`InMemory.schemas`,
which backs the `pg_namespace` virtual catalog and schema-qualified relation
resolution), but the registration was never persisted. On the next server
restart the registry was rebuilt from the on-disk catalog seeds — which only
contain the three bootstrap namespaces `pg_catalog` (11), `pg_toast` (99), and
`public` (2200) — so every user-created schema silently disappeared.

This surfaced repeatedly while porting `postgres/src/bin/pg_amcheck/t/003_check.pl`
(M0110-0003): a `pg_amcheck --schema s1` run that was clean *before* a required
stop→corrupt→restart cycle reported `no relations to check in schemas matching
"s1"` *after* the restart, because `s1` was gone. Every AC-003 surrogate worked
around it by scoping fixtures to `public`.

Unlike `CREATE TABLE` (which persists `pg_class`/`pg_attribute` rows to the heap
at runtime via `syncTableToCatalogHeap`), a schema has **no per-schema on-disk
file namespace** in goopg, so there is no natural heap-append point and no need
for one. This is exactly the situation `CREATE DATABASE` is already in.

## Decision

Mirror the established **`CREATE/DROP DATABASE` WAL-record durability
mechanism** (M0054-0001), *not* the `pg_class` heap-append mechanism. The
database DDL path is the precedent for "parser-bypassed catalog DDL with no
per-object on-disk file namespace": it appends a goopg-private WAL record and a
recovery driver replays it into the in-memory registry after physical replay.
`index_ddl_recovery.go` (M0079-0001) already mirrors the same shape a second
time, so this is a well-worn pattern.

### Wire format (`internal/wal/recovery.go`)

Two new record kinds (next free bytes after `RecordKindClogTruncate`=33):

- `RecordKindCreateSchema = 34` — `kind(1) | oid(4) | nameLen(2) | name`.
  The OID is carried so recovery restores the **same** identifier the live
  server assigned (`RegisterSchema` allocates from `nextOID`).
- `RecordKindDropSchema = 35` — `kind(1) | nameLen(2) | name`. No OID needed.

`Encode/DecodeCreateSchema` and `Encode/DecodeDropSchema` follow
`Encode/DecodeCreateDatabase` verbatim. Both kinds are physical-replay no-ops:
they are added to the `applyRecord` switch (returning `(false, nil)`) and to
`nativeApplyRecordKindKnown` so goopg's own crash recovery does not reject them.

### Recovery driver (`internal/initdb/schema_ddl_recovery.go`)

`replaySchemaDDLRecords(walDir, cat)` is a verbatim mirror of
`replayDatabaseDDLRecords`: it reads every WAL record once after physical
replay, and for each CREATE/DROP SCHEMA record calls the catalog's idempotent
recovery hooks. It is wired into `internal/initdb/open.go` immediately after the
database-DDL replay (same ordering rationale: records are walked in stream
order, so a CREATE followed by a DROP cancels out).

### Catalog hooks (`internal/catalog/catalog.go`)

- `RegisterSchemaDuringRecovery(name, oid)` — idempotent; sets
  `schemas[name] = oid` and advances `nextOID` past it (mirrors
  `RegisterIndexDuringRecovery`).
- `UnregisterSchemaDuringRecovery(name)` — idempotent delete.

### Emit sites

`CREATE SCHEMA` reaches the engine through two routes; both emit the record:

1. **Parsed form** (the common case, e.g. `CREATE SCHEMA s1`): the parser
   produces a `CompatNoopStmt{ObjType:"schema"}`, executed by
   `ddlOp.execCompatNoop` case `"schema"` — after `RegisterSchema` it appends
   `EncodeCreateSchema(name, SchemaOID(name))` via `o.ctx.WAL`.
2. **Parser-rejected forms**: handled in `dispatchSimpleQueryViaExecutor`'s
   compat-no-op branch — after `RegisterSchema` it appends the same record via
   `s.cfg.WAL` (type-asserting the catalog to `*catalog.InMemory` for `SchemaOID`).

`DROP SCHEMA` goes through `ddlOp.execDropCompat` (`objType == "schema"`); after
`UnregisterSchema` it appends `EncodeDropSchema(name)` via `o.ctx.WAL`.

In all sites the append is guarded by `WAL != nil` so test/embedded paths
without a WAL writer keep their prior behaviour.

## Consequences / scope

- **In scope, delivered:** a user schema created over the wire survives a clean
  stop→restart and stays visible in `pg_namespace`; a `DROP SCHEMA` is likewise
  durable (a stale CREATE record cannot resurrect it). OID is preserved.
- **Non-transactional**, exactly like `CREATE DATABASE`: the record is appended
  at execution time, not at COMMIT. A `CREATE SCHEMA` inside a rolled-back
  explicit transaction would still persist. This matches the database-DDL
  precedent and is acceptable for v0; making it transactional is future work.
- **PG-standby visibility is unchanged / out of scope:** this mechanism does
  *not* write a `pg_namespace` heap row or maintain the `pg_namespace_nspname`
  /`oid` indexes (2684/2685), so an attaching PG18 standby does not see the
  user schema via its syscache. goopg's own schema resolution uses the
  in-memory registry, not the index, so this is sufficient for the goopg-internal
  durability requirement. Heap-row + index maintenance for PG-standby parity is
  a separate, optional follow-up (it would mirror `syncTableToCatalogHeap`).

## Tests

- `internal/wal/schema_ddl_test.go` — encode/decode round-trip (incl. OID and
  multi-byte names), wrong-kind and truncated-payload rejection.
- `internal/initdb/schema_ddl_recovery_test.go` — full Open→append→Close→Open
  replay: CREATE survives (OID preserved), CREATE+DROP cancels out, missing
  pg_wal dir is a no-op.
- `internal/testport/create_schema_durability_test.go`
  (`TestPort_CreateSchemaSurvivesRestart`) — end-to-end over a live cluster:
  `CREATE SCHEMA` over the wire → stop→restart → still in `pg_namespace`;
  `DROP SCHEMA` → stop→restart → stays gone. Proves the real executor emit site
  and the routing the unit tests cannot.

## References

- M0054-0001 CREATE/DROP DATABASE WAL durability (`internal/server/database_ddl.go`,
  `internal/initdb/database_ddl_recovery.go`).
- M0079-0001 CREATE/DROP INDEX WAL recovery (`internal/initdb/index_ddl_recovery.go`).
