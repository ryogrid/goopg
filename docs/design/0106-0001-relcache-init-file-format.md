# 0106-0001 — PG Relcache Init File Format and Generation

**Status:** superseded
**Superseded by:** [`bootstrap-procedure/08-relcache-init-and-version-files.md`](bootstrap-procedure/08-relcache-init-and-version-files.md)
**Date:** 2026-05-17 (superseded 2026-05-19)
**Milestone:** M0106
**Upstream reference:** `postgres/src/backend/utils/cache/relcache.c`

> **This doc is retained for history only.** The full specification — including
> the complete nailed-rel inventory across shared+local files, the continuous
> maintenance rule for unlinking `pg_internal.init` on nailed-rel mutation, the
> per-database layout, the trailing-count read-time validation, and the WAL
> `RelcacheInitFileInval` flag — now lives in the bootstrap-procedure bundle.
> See [`bootstrap-procedure/README.md`](bootstrap-procedure/README.md) for the
> full reading order.

## Problem

PG's `load_relcache_init_file()` reads pre-built relation descriptors
for nailed system catalogs and indexes from a binary file. Without this
file, `formrdesc()` handles only heap catalogs — index descriptors are
not built, causing `RelationIdGetRelation(indexOID)` to return
`InvalidRelation`. `load_critical_index()` then PANICs, aborting
backend startup.

The postmaster and recovery process reach PM_HOT_STANDBY without
needing indexes. Only client backends require them (for catalog
lookups during `InitPostgres`).

## Goal

Generate PG-compatible relcache init files during goopg init so PG
backends can start from a goopg-produced backup.

## Design

### File Locations
- Shared: `global/pg_internal.init` (maps to `global/RELCACHE_INIT_FILENAME`)
- Local: `base/<dboid>/pg_internal.init`

### Binary Format

```
MAGIC: uint32 = RELCACHE_INIT_FILEMAGIC (0x573266)
For each nailed relation:
  relDescLen:  Size (uint32) — sizeof(RelationData) in this PG build
  relDesc:     RelationData struct (binary blob, relDescLen bytes)
  relFormLen:  Size (uint32) — sizeof(FormData_pg_class)
  relForm:     FormData_pg_class struct (binary blob)
  For each attribute (relnatts times):
    attrLen:   Size (uint32) — ATTRIBUTE_FIXED_PART_SIZE
    attr:      FormData_pg_attribute (binary blob)
  optLen:      Size (uint32) — length of access method options
  options:     byte[optLen] (varlena; zero length if none)
```

### Shared Catalogs to Include
Based on `formrdesc()` calls in `RelationCacheInitializePhase2`:

| Relation | OID | Indexes |
|----------|-----|---------|
| pg_database | 1262 | 2671 (datname), 2672 (oid) |
| pg_authid | 1260 | 2676 (rolname), 2677 (oid) |
| pg_auth_members | 1261 | 2695 (member_role) |
| pg_shseclabel | 3592 | 3593 (objects) |
| pg_subscription | 6100 | (subscription indexes) |

### Local Catalogs to Include
Based on `load_critical_index()` calls in `RelationCacheInitializePhase3`:

| Relation | OID | Indexes |
|----------|-----|---------|
| pg_class | 1259 | 2662 (oid), 2663 (relname_nsp) |
| pg_attribute | 1249 | 2658 (relid_attnam), 2659 (relid_attnum) |
| pg_type | 1247 | 2703 (oid), 2704 (typname_nsp) |
| pg_proc | 1255 | 2690 (oid), 2691 (proname_args_nsp) |
| pg_index | 2610 | 2679 (indrelid) |
| pg_opclass | 2616 | 2687 (oid) |
| pg_amproc | 2603 | 2655 (amproc_oid) |
| pg_rewrite | 2618 | 2693 (rel_rulename) |
| pg_trigger | 2620 | 2701 (tgrelid_tgname) |

All listed above plus their indexes must be included. The full list
of indexes can be derived from `load_critical_index()` calls in
relcache.c (lines 4179-4223).

### Implementation Approach

1. **Define struct layouts**: Encode `FormData_pg_class` and
   `FormData_pg_attribute` for each nailed relation using PG-native
   binary encoding (same approach as `EncodeRowPG`).

2. **Build RelationData blobs**: The `RelationData` struct contains
   internal pointers that must be fixed up when loaded. Fortunately
   `load_relcache_init_file()` does NOT use raw pointers from the
   file — it allocates fresh memory and copies data. The key fields
   that must be valid:
   - `rd_id` (OID)
   - `rd_node` (RelFileLocator with dbNode, spcNode, relNumber)
   - `rd_rel` → filled from Form_pg_class
   - `rd_att` → built from Form_pg_attribute array
   - Various flags set correctly

3. **Generate files during goopg init**: Add a new bootstrap function
   `bootstrapRelcacheInitFiles()` that constructs and writes both
   `global/pg_internal.init` and `base/1/pg_internal.init`.

4. **Verification**: Run E2E test to confirm PG backends start without
   PANIC and accept queries.

### Risk Assessment

- **sizeof(RelationData)**: Must match the compiled PG binary exactly.
  Any mismatch causes `load_relcache_init_file()` to reject the file
  (`goto read_failed`). The sizeof value needs to be extracted from
  the PG binary (DWARF) or computed from source.

- **FormData_pg_attribute offsets**: Must match PG18's compiled
  `ATTRIBUTE_FIXED_PART_SIZE`. Verify via DWARF.

- **RelationData field offsets**: Only `rd_id`, `rd_node`, and a few
  flags need correct values. Most fields are overwritten by
  `load_relcache_init_file()`.

### Files to Modify/Create

| File | Change |
|------|--------|
| `internal/initdb/relcache_init.go` (new) | relcache init file generation |
| `internal/initdb/initdb.go` | Call bootstrapRelcacheInitFiles() |
| `internal/initdb/initdb_test.go` | Verify files created |

### References

- `postgres/src/backend/utils/cache/relcache.c` — load/write functions
- `postgres/src/include/catalog/pg_class.h` — FormData_pg_class
- `postgres/src/include/catalog/pg_attribute.h` — FormData_pg_attribute
- `postgres/src/include/utils/rel.h` — RelationData struct
