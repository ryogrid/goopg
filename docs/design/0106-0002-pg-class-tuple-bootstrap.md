# 0106-0002: Populate pg_class Heap Tuples for Nailed Relations

## Status

draft (2026-05-17)

## Context

M0106 added PG-compatible relcache init files (`global/pg_internal.init`,
`base/<dboid>/pg_internal.init`) during goopg init. These binary files
encode `RelationData`, `FormData_pg_class`, and `FormData_pg_attribute`
blobs for every nailed shared and local relation. PG's
`load_relcache_init_file()` reads them successfully — the `RelationIdGetRelation()`
code path finds reldescs in the cache and returns them.

However, `RelationBuildDesc()` — called directly by `load_critical_index()`
during backend startup — **bypasses the relcache entirely**. It calls
`ScanPgRelation(targetRelId, ...)` which opens the **actual `pg_class` heap
file** (`base/<dboid>/1259` for local, `global/1259` for shared) and scans
for a matching tuple. If the heap page contains zero tuples (currently an
empty `InitPage`'d page or an empty btree root page), `ScanPgRelation`
returns NULL → `RelationBuildDesc` returns NULL → `ereport(PANIC, "could
not open critical system index %u")`.

**strace confirmation (2026-05-17):**
```
openat("global/pg_internal.init", O_RDONLY) = 8   ← loads OK
openat("base/5/pg_internal.init", O_RDONLY) = 9   ← loads OK
openat("base/5/2662", O_RDONLY) = 9                ← index file exists
openat("base/5/1259", O_RDONLY) = 9                ← pg_class heap: ZERO TUPLES
→ ScanPgRelation(2662, ...) → NULL → PANIC
```

This is **vanilla PG behaviour** and must not be modified. The fix must be in
goopg: we must populate the `pg_class` heap with actual tuples for every
nailed relation.

## Nailed Relations Inventory

### Shared (global/1259)

| OID  | Name | Kind | Natts |
|------|------|------|-------|
| 1262 | pg_database | r | 16 |
| 1260 | pg_authid | r | 12 |
| 1261 | pg_auth_members | r | 5 |
| 3592 | pg_shseclabel | r | 6 |
| 6100 | pg_subscription | r | 9 |
| 2671 | pg_database_datname_index | i | 2 |
| 2672 | pg_database_oid_index | i | 2 |
| 2676 | pg_authid_rolname_index | i | 2 |
| 2677 | pg_authid_oid_index | i | 2 |
| 2695 | pg_auth_members_member_role_index | i | 2 |
| 3593 | pg_shseclabel_object_index | i | 2 |

### Local (base/1/1259, base/5/1259)

| OID  | Name | Kind | Natts |
|------|------|------|-------|
| 1247 | pg_type | r | 14 |
| 1249 | pg_attribute | r | 24 |
| 1259 | pg_class | r | 14 |
| 1255 | pg_proc | r | 13 |
| 2610 | pg_index | r | 4 |
| ... | _(remaining heaps)_ | r | varies |
| 2703 | pg_type_oid_index | i | 2 |
| 2704 | pg_type_typname_nsp_index | i | 2 |
| 2658 | pg_attribute_relid_attnam_index | i | 2 |
| 2659 | pg_attribute_relid_attnum_index | i | 2 |
| 2662 | pg_class_oid_index | i | 2 |
| 2663 | pg_class_relname_nsp_index | i | 2 |
| 2690 | pg_proc_oid_index | i | 2 |
| 2691 | pg_proc_proname_args_nsp_index | i | 2 |
| 2679 | pg_index_indrelid_index | i | 2 |
| ... | _(remaining indexes)_ | i | 2 |

Full list of 10 shared + 37 local nailed relations is already defined in
`internal/initdb/relcache_init.go` (nailedSharedRels + nailedLocalRels).

## Implementation

### pg_class Column Layout (PG18)

The pg_class table has these columns (from `postgres/src/include/catalog/pg_class.h`):

| Ordinal | Name | Type | Width |
|---------|------|------|-------|
| 0 | oid | oid | 4 |
| 1 | relname | name | 64 |
| 2 | relnamespace | oid | 4 |
| 3 | reltype | oid | 4 |
| 4 | reloftype | oid | 4 |
| 5 | relowner | oid | 4 |
| 6 | relam | oid | 4 |
| 7 | relfilenode | oid | 4 |
| 8 | reltablespace | oid | 4 |
| 9 | relpages | int4 | 4 |
| 10 | reltuples | float4 | 4 |
| 11 | relallvisible | int4 | 4 |
| 12 | reltoastrelid | oid | 4 |
| 13 | relhasindex | bool | 1 |
| 14 | relisshared | bool | 1 |
| 15 | relpersistence | char | 1 |
| 16 | relkind | char | 1 |
| 17 | relnatts | int2 | 2 |
| 18 | relchecks | int2 | 2 |
| 19 | relhasrules | bool | 1 |
| 20 | relhastriggers | bool | 1 |
| 21 | relhassubclass | bool | 1 |
| 22 | relrowsecurity | bool | 1 |
| 23 | relforcerowsecurity | bool | 1 |
| 24 | relispopulated | bool | 1 |
| 25 | relispartition | bool | 1 |
| 26 | relrewrite | oid | 4 |
| 27 | relfrozenxid | xid | 4 |
| 28 | relminmxid | xid | 4 |
| 29 | relacl | aclitem[] | -1 |
| 30 | reloptions | text[] | -1 |
| 31 | relpartbound | text | -1 |

### Approach

We reuse the existing `executor.EncodeRowPG()` to encode tuples for each
nailed relation. The `bootstrapPostgresDatabase` function already does this
for `pg_database` entries (template1 + postgres). We extend the same pattern
to encode pg_class tuples for all nailed relations.

Key design decisions:
1. **Reuse nailed metadata** from `relcache_init.go` (nailedSharedRels, nailedLocalRels).
2. Populate pg_class tuples in `base/1/1259` and copy to `base/5/1259`.
3. Write shared entries to `global/1259`.
4. Index tuples (kind='i') get relnatts=2 (per `idxSpec` convention).
5. All fields not explicitly set default to zero/NULL (acceptable for bootstrap:
   vanilla PG backends only need oid, relname, relnamespace, reltype, relkind,
   relnatts, relisshared, relpersistence, relfilenode during critical index
   loading).

### File Placement

- `internal/initdb/initdb.go`: Add `bootstrapPgClassTuples()` called before
  `bootstrapRelcacheInitFiles()`.
- No new files needed — the nailed relation metadata already lives in
  `internal/initdb/relcache_init.go`.

## Operational relcache / catcache Maintenance (NOT DEFERRED)

Bootstrap-time pg_class tuples are necessary but NOT sufficient. Once the
primary is running, DDL operations (CREATE TABLE, ALTER TABLE, DROP TABLE,
CREATE INDEX, etc.) must maintain PG-compatible catalog state so that:

1. **pg_class stays current**: every DDL that adds, modifies, or removes a
   relation must insert/update/delete the corresponding pg_class heap tuple
   using PG-native physical encoding.
2. **catcache stays consistent**: goopg's internal catalog cache (analogue of
   PG's catcache) must reflect the on-disk pg_class contents so internal
   lookups and PG standby queries agree on relation metadata.
3. **relcache init file stays fresh**: after any catalog change, goopg must
   regenerate the relcache init file (`pg_internal.init`) so a PG standby
   that reconnects (or a new standby bootstrapped from a later basebackup)
   loads the correct relation descriptor set. This mirrors PG's
   `write_relcache_init_file()` triggered by `RelationCacheInitFileRemove`.

This is load-bearing for the M0105/M0106 goal of "vanilla PG standby can
boot and serve queries from a goopg basebackup at any point in the
primary's lifetime", not just immediately after init. **Do NOT defer this
work** — even if it appears to expand the milestone scope, it is a
hard requirement for correct ongoing replication. An init-time-only
snapshot will bit-rot the moment the first DDL runs.

### Specific requirements

- `internal/catalog/`: extend the catalog write path so INSERT/UPDATE/DELETE
  on `pg_class` encodes tuples in PG-native format (or writes both formats
  atomically).
- `internal/executor/codec.go`: `EncodeRowPG()` must support all pg_class
  column types (already verified: oid, name, int4, float4, bool, char, int2,
  xid, text).
- `internal/initdb/relcache_init.go`: expose `WriteRelcacheInitFile()` as a
  public function callable from the catalog-change path. Add an invalidation
  trigger so the next checkpoint or graceful-shutdown event regenerates the
  init file if the catalog was dirtied.
- `internal/server/`: wire the init-file regeneration into the checkpointer
  (shutdown checkpoint) or into a new background writer cycle, mirroring
  PG's `RelationCacheInitFileRemove` + `write_relcache_init_file` pattern.

## Verification

1. Unit test: `TestInitCreatesSystemCatalogRelfiles` extended to verify
   `base/1/1259` contains tuples with expected OIDs.
2. E2E test: `TestE2E_FailoverGoopgToPG/async` must progress past PANIC
   to "database system is in recovery mode" (or beyond).
3. strace: confirm `base/5/1259` is opened O_RDONLY AND `ScanPgRelation`
   finds the tuple (no PANIC).
4. Post-DDL test: CREATE TABLE on goopg primary, take a second basebackup,
   start a new PG standby — verify init file still loads correctly (no
   stale or missing entries).

## Dependencies

- M0106-0001 through M0106-0004: nailed relation metadata + init file writer
  (already in place).
- `executor.EncodeRowPG()`: PG-native tuple encoder (already in place from
  M0105-0010).
