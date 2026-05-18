# 05 — Local Catalogs and Critical Local Indexes

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility); supersedes the
reactive `docs/design/0106-0010-step3*.md` chain for nailed-local rels.

---

## Scope

This file specifies the **per-database** nailed system catalogs and the
seven critical local indexes that
`RelationCacheInitializePhase3` (`src/backend/utils/cache/relcache.c:4116`)
opens before any catcache fetch can succeed. Without these files
populated on disk with PG-canonical heap and btree-tuple bytes, a
vanilla PG18 backend attaching to a goopg cluster FATALs in
`load_critical_index` (`relcache.c:4179-4191`) long before
`InitPostgres` reaches its first SQL command.

In scope:

- The four `formrdesc`-nailed local catalogs:
  - pg_class (1259), pg_attribute (1249), pg_proc (1255), pg_type (1247).
- pg_trigger (2620): the catalog itself is *not* in `formrdesc`'s set,
  but its critical index `pg_trigger_tgrelid_tgname_index` (2701)
  *is* in the `load_critical_index` list at `relcache.c:4191`. PG
  rebuilds pg_trigger's relcache entry on demand from `pg_class`,
  but the index file must be openable from the first attach moment.
  goopg therefore treats pg_trigger as a "5th nailed-local" for
  initdb-time and continuous-maintenance bookkeeping.
- The seven critical local indexes:
  - pg_class_oid_index (2662),
    pg_attribute_relid_attnum_index (2659),
    pg_index_indexrelid_index (2679),
    pg_opclass_oid_index (2687),
    pg_amproc_fam_proc_index (2655),
    pg_rewrite_rel_rulename_index (2693),
    pg_trigger_tgrelid_tgname_index (2701).
- Heap-tuple on-disk format: `HeapTupleHeader`, null bitmap shifting,
  varlena 1-byte / 4-byte LE headers, `ATTRIBUTE_FIXED_PART_SIZE`
  alignment of `FormData_pg_attribute`.
- Btree on-disk format: `BTMetaPageData` (block 0) plus the
  leaf-root layout for an initdb-populated unique index, plus
  `BTPageOpaqueData` special area and `IndexTupleData` per-key
  encoding.
- The three-database directory layout the goopg primary maintains:
  `base/1/` (template1), `base/4/` (template0, reserved — currently
  not seeded by goopg), `base/5/` (postgres). The same catalog and
  index relfilenodes appear in every `base/<dboid>/`.

Out of scope (covered elsewhere in this doc set):

- Shared (`global/`) catalogs and the six critical shared indexes —
  see [`04-shared-catalog-bootstrap.md`](04-shared-catalog-bootstrap.md).
- Non-nailed local catalogs seeded from `pg_*.dat` (pg_am, pg_amop,
  pg_amproc rows, pg_opclass, pg_opfamily, pg_cast, pg_collation,
  pg_conversion, pg_aggregate, pg_range, pg_namespace, pg_language,
  pg_operator, …) — see
  [`06-bki-derived-catalog-seeds.md`](06-bki-derived-catalog-seeds.md).
  This file only covers the *index files* that index into those
  non-nailed heaps when the index OID happens to be on the critical
  list (2655, 2687).
- `pg_internal.init` binary serialisation — see
  [`08-relcache-init-and-version-files.md`](08-relcache-init-and-version-files.md).
- `pg_rewrite._RETURN` rule action encoding for system views — see
  [`07-system-views-and-pg-rewrite.md`](07-system-views-and-pg-rewrite.md).
  (pg_rewrite as a catalog and the pg_rewrite_rel_rulename_index file
  *are* in scope here.)

---

## Upstream references

All `src/...` paths are relative to `postgres/` in the goopg repo;
line numbers are against vendored PG 18.3.

| Symbol | File:line |
|---|---|
| `RelationCacheInitializePhase3` | `src/backend/utils/cache/relcache.c:4116` |
| `formrdesc()` | `src/backend/utils/cache/relcache.c:1894` |
| `load_critical_index()` | `src/backend/utils/cache/relcache.c:4243` (declared `:305`) |
| `criticalRelcachesBuilt` flag | `src/backend/utils/cache/relcache.c:140` |
| Phase-3 nailed-local `formrdesc` block | `src/backend/utils/cache/relcache.c:4134-4143` |
| Phase-3 critical-local-index block | `src/backend/utils/cache/relcache.c:4177-4196` |
| `Natts_pg_class = 34` | `src/include/catalog/pg_class_d.h:64` |
| `Natts_pg_attribute = 25` | `src/include/catalog/pg_attribute_d.h:54` |
| `Natts_pg_proc = 30` | `src/include/catalog/pg_proc_d.h:59` |
| `Natts_pg_type = 32` | `src/include/catalog/pg_type_d.h:61` |
| `Natts_pg_trigger = 19` | `src/include/catalog/pg_trigger_d.h:48` |
| `FormData_pg_class` layout | `src/include/catalog/pg_class.h:33-145` |
| `FormData_pg_attribute` layout | `src/include/catalog/pg_attribute.h:37-186` |
| `FormData_pg_proc` layout | `src/include/catalog/pg_proc.h` |
| `FormData_pg_type` layout | `src/include/catalog/pg_type.h` |
| `FormData_pg_trigger` layout | `src/include/catalog/pg_trigger.h` |
| `ATTRIBUTE_FIXED_PART_SIZE` | `src/include/catalog/pg_attribute.h:194` |
| `ClassOidIndexId = 2662` | `src/include/catalog/pg_class_d.h:25`; `pg_class.h:158` |
| `AttributeRelidNumIndexId = 2659` | `src/include/catalog/pg_attribute_d.h:26`; `pg_attribute.h:219` |
| `IndexRelidIndexId = 2679` | `src/include/catalog/pg_index_d.h:27`; `pg_index.h:75` |
| `OpclassOidIndexId = 2687` | `src/include/catalog/pg_opclass_d.h:25`; `pg_opclass.h:86` |
| `AccessMethodProcedureIndexId = 2655` | `src/include/catalog/pg_amproc_d.h:24`; `pg_amproc.h:70` |
| `RewriteRelRulenameIndexId = 2693` | `src/include/catalog/pg_rewrite_d.h:25`; `pg_rewrite.h:57` |
| `TriggerRelidNameIndexId = 2701` | `src/include/catalog/pg_trigger_d.h:25`; `pg_trigger.h:85` |
| `HeapTupleHeaderData` layout | `src/include/access/htup_details.h:120-176` |
| `HEAP_HASNULL = 0x0001` | `src/include/access/htup_details.h:190` |
| `BITMAPLEN(NATTS)` | `src/include/access/htup_details.h:595-602` |
| Varlena 1-byte / 4-byte rules | `src/include/varatt.h:191-238, 279-297` |
| `BTMetaPageData` struct | `src/include/access/nbtree.h:104-120` |
| `BTPageOpaqueData` struct | `src/include/access/nbtree.h:63-70` |
| `BTREE_MAGIC = 0x053162` | `src/include/access/nbtree.h:150` |
| `BTREE_VERSION = 4`, `BTREE_MIN_VERSION = 2` | `src/include/access/nbtree.h:151-152` |
| `_bt_initmetapage()` | `src/backend/access/nbtree/nbtpage.c:67` |
| `IndexTupleData` layout | `src/include/access/itup.h:35-51` |
| `heap_create_with_catalog` | `src/backend/catalog/heap.c:1122` |
| `heap_drop_with_catalog` | `src/backend/catalog/heap.c:1784` |
| `ATExecAddColumn` | `src/backend/commands/tablecmds.c:7217` |
| `index_create` | `src/backend/catalog/index.c:726` |
| `DefineIndex` | `src/backend/commands/indexcmds.c:542` |
| `ProcedureCreate` | `src/backend/catalog/pg_proc.c:98` |
| `TypeCreate` | `src/backend/catalog/pg_type.c:195` |
| `CreateTriggerFiringOn` | `src/backend/commands/trigger.c:178` |
| `vac_update_relstats` | `src/backend/commands/vacuum.c:1442` |
| `make_new_heap` | `src/backend/commands/cluster.c:705` |
| `ExecuteTruncate` | `src/backend/commands/tablecmds.c:1861` |
| `ReindexIndex` | `src/backend/commands/indexcmds.c:2919` |
| `do_analyze_rel` | `src/backend/commands/analyze.c:278` |
| `CacheInvalidateHeapTuple` | `src/backend/utils/cache/inval.c:1571` |
| `RelationMapUpdateMap` | `src/backend/utils/cache/relmapper.c:325` |
| Heap WAL record IDs | `src/include/access/heapam_xlog.h:33-39, 59-66` |
| Btree WAL record IDs | `src/include/access/nbtxlog.h:27-39` |

---

## Initdb-time output

`RelationCacheInitializePhase3` runs once per backend, immediately
after `InitPostgres` opens the database. It first tries to
`load_relcache_init_file(false)` from `base/<dboid>/pg_internal.init`;
on failure or in bootstrap mode it falls back to in-memory
`formrdesc` descriptors and then to `load_critical_index` for the
seven critical local indexes (`relcache.c:4131, 4134-4143, 4179-4191`).
Both fallback paths require the **on-disk relfile** to exist and be
parseable as a heap or a btree, because `load_critical_index` opens
`pg_class` (now nailed via `formrdesc`) via an indexed scan over
pg_class_oid_index — which means block 0 of `base/<dboid>/2662`
must be a valid `BTMetaPageData` page on the very first attach.

### Phase 3 nailed-rel inventory

| Relation | OID | Rowtype OID (`pg_type`) | TOAST OID | Per-DB filenode | Citation |
|---|---|---|---|---|---|
| pg_class | 1259 | 83 (`RelationRelation_Rowtype_Id`) | 4171 | 1259 | `pg_class.h:33-37` ; formrdesc at `relcache.c:4134` |
| pg_attribute | 1249 | 75 (`AttributeRelation_Rowtype_Id`) | 4175 | 1249 | `pg_attribute.h:37` ; `relcache.c:4136` |
| pg_proc | 1255 | 81 (`ProcedureRelation_Rowtype_Id`) | 2836 | 1255 | `pg_proc.h` ; `relcache.c:4138` |
| pg_type | 1247 | 71 (`TypeRelation_Rowtype_Id`) | 4172 | 1247 | `pg_type.h` ; `relcache.c:4140` |
| pg_trigger | 2620 | 79 | 2336 | 2620 | `pg_trigger.h:37` ; not in `formrdesc`, but its index 2701 is in `load_critical_index` (`relcache.c:4191`) |

All five filenodes equal their relation OID by default and are
recorded in `base/<dboid>/pg_filenode.map`
(`internal/initdb/initdb.go:937-998` — `localRelMap`). Because these
are *mapped* relations, the on-disk relfile name is the
RelFileNumber, not the OID; mutations to `pg_class.relfilenode`
**must** go through `RelationMapUpdateMap` (see Continuous
Maintenance below).

### `pg_class` column inventory (34 cols, PG18)

Source: `src/include/catalog/pg_class.h:33-145`,
`src/include/catalog/pg_class_d.h:29-62`. PG18 adds **relallfrozen**
at position 13 (between relallvisible and reltoastrelid) — goopg's
seeding logic must include this column.

| # | Name | type OID | attlen | byval | align | storage | notnull | initdb value (mapped catalogs) |
|---|---|---|---|---|---|---|---|---|
| 1 | oid | 26 (oid) | 4 | t | i | p | t | catalog OID (1259, 1249, …) |
| 2 | relname | 19 (name) | 64 | f | c | p | t | "pg_class" etc. |
| 3 | relnamespace | 26 | 4 | t | i | p | t | 11 (pg_catalog) |
| 4 | reltype | 26 | 4 | t | i | p | t | rowtype (83 for pg_class) |
| 5 | reloftype | 26 | 4 | t | i | p | t | 0 |
| 6 | relowner | 26 | 4 | t | i | p | t | 10 (POSTGRES) |
| 7 | relam | 26 | 4 | t | i | p | t | 2 (heap) |
| 8 | relfilenode | 26 | 4 | t | i | p | t | **0** for mapped rels; non-zero for ordinary rels |
| 9 | reltablespace | 26 | 4 | t | i | p | t | 0 (pg_default implicit) |
| 10 | relpages | 23 (int4) | 4 | t | i | p | t | 0 |
| 11 | reltuples | 700 (float4) | 4 | t | i | p | t | -1 |
| 12 | relallvisible | 23 | 4 | t | i | p | t | 0 |
| 13 | relallfrozen | 23 | 4 | t | i | p | t | 0 (**PG18 new**) |
| 14 | reltoastrelid | 26 | 4 | t | i | p | t | TOAST OID per row |
| 15 | relhasindex | 16 (bool) | 1 | t | c | p | t | t for any rel with critical index |
| 16 | relisshared | 16 | 1 | t | c | p | t | f for local catalogs |
| 17 | relpersistence | 18 (char) | 1 | t | c | p | t | 'p' |
| 18 | relkind | 18 | 1 | t | c | p | t | 'r' for heap, 'i' for index, 'v' for view |
| 19 | relnatts | 21 (int2) | 2 | t | s | p | t | column count (34, 25, 30, 32, 19, …) |
| 20 | relchecks | 21 | 2 | t | s | p | t | 0 |
| 21 | relhasrules | 16 | 1 | t | c | p | t | f (t only for views with rules) |
| 22 | relhastriggers | 16 | 1 | t | c | p | t | f |
| 23 | relhassubclass | 16 | 1 | t | c | p | t | f |
| 24 | relrowsecurity | 16 | 1 | t | c | p | t | f |
| 25 | relforcerowsecurity | 16 | 1 | t | c | p | t | f |
| 26 | relispopulated | 16 | 1 | t | c | p | t | t |
| 27 | relreplident | 18 | 1 | t | c | p | t | 'n' |
| 28 | relispartition | 16 | 1 | t | c | p | t | f |
| 29 | relrewrite | 26 | 4 | t | i | p | t | 0 |
| 30 | relfrozenxid | 28 (xid) | 4 | t | i | p | t | 3 (`FirstNormalTransactionId`) |
| 31 | relminmxid | 28 | 4 | t | i | p | t | 1 (`FirstMultiXactId`) |
| 32 | relacl | 1034 (aclitem[]) | -1 | f | i | x | f | NULL |
| 33 | reloptions | 1009 (text[]) | -1 | f | i | x | f | NULL |
| 34 | relpartbound | 194 (pg_node_tree) | -1 | f | i | x | f | NULL |

### `pg_attribute` column inventory (25 cols, PG18)

Source: `src/include/catalog/pg_attribute.h:37-186`,
`src/include/catalog/pg_attribute_d.h:28-52`. PG18 has **no
`attcacheoff`** — it was removed in PG12 and is no longer part of the
on-disk row; older docs that listed 27/28 columns are stale.
`ATTRIBUTE_FIXED_PART_SIZE = offsetof(FormData_pg_attribute,
attcollation) + sizeof(Oid)` (`pg_attribute.h:194`) — i.e. the first
20 columns are guaranteed fixed-layout and copied verbatim into
in-memory `TupleDesc->attrs[i]`.

| # | Name | type OID | attlen | align | notnull | initdb value |
|---|---|---|---|---|---|---|
| 1 | attrelid | 26 | 4 | i | t | parent catalog OID |
| 2 | attname | 19 | 64 | c | t | column name |
| 3 | atttypid | 26 | 4 | i | t | per-column |
| 4 | attlen | 21 | 2 | s | t | from pg_type.typlen |
| 5 | attnum | 21 | 2 | s | t | 1..relnatts (or negative for sys cols) |
| 6 | atttypmod | 23 | 4 | i | t | -1 |
| 7 | attndims | 21 | 2 | s | t | 0 |
| 8 | attbyval | 16 | 1 | c | t | from pg_type.typbyval |
| 9 | attalign | 18 | 1 | c | t | from pg_type.typalign |
| 10 | attstorage | 18 | 1 | c | t | 'p' for plain, 'x' for ext |
| 11 | attcompression | 18 | 1 | c | t | '\0' |
| 12 | attnotnull | 16 | 1 | c | t | t for system columns |
| 13 | atthasdef | 16 | 1 | c | t | f |
| 14 | atthasmissing | 16 | 1 | c | t | f |
| 15 | attidentity | 18 | 1 | c | t | '\0' |
| 16 | attgenerated | 18 | 1 | c | t | '\0' |
| 17 | attisdropped | 16 | 1 | c | t | f |
| 18 | attislocal | 16 | 1 | c | t | t |
| 19 | attinhcount | 21 | 2 | s | t | 0 |
| 20 | attcollation | 26 | 4 | i | t | 100 (default) or 0 |
| 21 | attstattarget | 21 | 2 | s | **f** | NULL (NULL = "ANALYZE default") |
| 22 | attacl | 1034 | -1 | i | f | NULL |
| 23 | attoptions | 1009 | -1 | i | f | NULL |
| 24 | attfdwoptions | 1009 | -1 | i | f | NULL |
| 25 | attmissingval | 2277 (anyarray) | -1 | d | f | NULL |

Columns 21–25 sit *after* `ATTRIBUTE_FIXED_PART_SIZE` and are
nullable; the on-disk null bitmap covers them. goopg's
`bootstrapPgAttributeTuples` (initdb.go:1206) emits NULL for all
five trailing columns on every nailed-rel column.

### `pg_proc` column inventory (30 cols, PG18)

Source: `src/include/catalog/pg_proc.h`,
`src/include/catalog/pg_proc_d.h:28-57`. All columns documented in
the brief are valid; emphasising the eight variadic/oid-array
columns whose binary encoding is goopg-specific:

| # | Name | type OID | attlen | align | notes |
|---|---|---|---|---|---|
| 1 | oid | 26 | 4 | i | proc OID |
| 2 | proname | 19 | 64 | c | |
| 3 | pronamespace | 26 | 4 | i | 11 |
| 4 | proowner | 26 | 4 | i | 10 |
| 5 | prolang | 26 | 4 | i | 12 (internal) for AM handlers |
| 6 | procost | 700 | 4 | i | 1.0 |
| 7 | prorows | 700 | 4 | i | 0 |
| 8 | provariadic | 26 | 4 | i | 0 |
| 9 | prosupport | 26 | 4 | i | 0 |
| 10 | prokind | 18 | 1 | c | 'f' |
| 11 | prosecdef | 16 | 1 | c | f |
| 12 | proleakproof | 16 | 1 | c | f |
| 13 | proisstrict | 16 | 1 | c | per row |
| 14 | proretset | 16 | 1 | c | per row |
| 15 | provolatile | 18 | 1 | c | 's' / 'v' / 'i' |
| 16 | proparallel | 18 | 1 | c | 's' / 'r' / 'u' |
| 17 | pronargs | 21 | 2 | s | per row |
| 18 | pronargdefaults | 21 | 2 | s | 0 |
| 19 | prorettype | 26 | 4 | i | per row |
| 20 | proargtypes | 30 (oidvector) | -1 | i | **oidvector**, lbound=0 |
| 21 | proallargtypes | 1028 (oid[]) | -1 | i | **oid[]** ArrayType, lbound=1 — NULL when ≡ proargtypes |
| 22 | proargmodes | 1002 (char[]) | -1 | i | NULL when all IN |
| 23 | proargnames | 1009 (text[]) | -1 | i | NULL when unnamed |
| 24 | proargdefaults | 194 | -1 | i | NULL |
| 25 | protrftypes | 1028 | -1 | i | NULL |
| 26 | prosrc | 25 (text) | -1 | i | non-null |
| 27 | probin | 25 | -1 | i | NULL for INTERNAL |
| 28 | prosqlbody | 194 | -1 | i | NULL for non-SQL |
| 29 | proconfig | 1009 | -1 | i | NULL |
| 30 | proacl | 1034 | -1 | i | NULL |

`oidvector` (column 20) and `oid[]` (column 21) use *different*
ArrayType encodings — `oidvector` has lbound=0 and no nulls
bitmap; `oid[]` has lbound=1 and the standard ArrayType header.
goopg's `oidVectorBytes` / `oidArrayBytes` helpers
(initdb.go:1449, :1497) implement this split.

### `pg_type` column inventory (32 cols, PG18)

Source: `src/include/catalog/pg_type.h`,
`src/include/catalog/pg_type_d.h:28-59`. typalign sits at offset 128
of `Form_pg_type` after the FormData cast, which is why goopg's
`pg_type_bootstrap.go` must produce PG-canonical heap rows (v0 row
encoding FATALs PG with `\0` at offset 128 — Step 3cq in the
M0106 history).

| # | Name | type OID | attlen | align | notes |
|---|---|---|---|---|---|
| 1 | oid | 26 | 4 | i | type OID |
| 2 | typname | 19 | 64 | c | |
| 3 | typnamespace | 26 | 4 | i | 11 |
| 4 | typowner | 26 | 4 | i | 10 |
| 5 | typlen | 21 | 2 | s | per type |
| 6 | typbyval | 16 | 1 | c | per type |
| 7 | typtype | 18 | 1 | c | 'b' base / 'c' composite / 'p' pseudo |
| 8 | typcategory | 18 | 1 | c | per type |
| 9 | typispreferred | 16 | 1 | c | per type |
| 10 | typisdefined | 16 | 1 | c | t |
| 11 | typdelim | 18 | 1 | c | ',' usually |
| 12 | typrelid | 26 | 4 | i | composite type rowtype owner |
| 13 | typsubscript | 26 | 4 | i | regproc |
| 14 | typelem | 26 | 4 | i | array element type |
| 15 | typarray | 26 | 4 | i | array sibling type |
| 16 | typinput | 26 | 4 | i | regproc |
| 17 | typoutput | 26 | 4 | i | regproc |
| 18 | typreceive | 26 | 4 | i | regproc |
| 19 | typsend | 26 | 4 | i | regproc |
| 20 | typmodin | 26 | 4 | i | regproc |
| 21 | typmodout | 26 | 4 | i | regproc |
| 22 | typanalyze | 26 | 4 | i | regproc |
| 23 | typalign | 18 | 1 | c | **offset 128 after Form_pg_type cast** |
| 24 | typstorage | 18 | 1 | c | per type |
| 25 | typnotnull | 16 | 1 | c | per type |
| 26 | typbasetype | 26 | 4 | i | 0 for non-domains |
| 27 | typtypmod | 23 | 4 | i | -1 |
| 28 | typndims | 23 | 4 | i | 0 |
| 29 | typcollation | 26 | 4 | i | 0 or 100 |
| 30 | typdefaultbin | 194 | -1 | i | NULL |
| 31 | typdefault | 25 | -1 | i | NULL |
| 32 | typacl | 1034 | -1 | i | NULL |

### `pg_trigger` column inventory (19 cols, PG18)

Source: `src/include/catalog/pg_trigger.h`,
`src/include/catalog/pg_trigger_d.h:28-46`. Vanilla initdb leaves
pg_trigger **empty**; the catalog file exists with a single
heap-initialised page and the 2701 index file likewise exists with
only the metapage. Both files must be openable from the first
backend attach.

| # | Name | type OID | attlen | align | notes |
|---|---|---|---|---|---|
| 1 | oid | 26 | 4 | i | |
| 2 | tgrelid | 26 | 4 | i | parent table OID |
| 3 | tgparentid | 26 | 4 | i | partitioned-trigger parent |
| 4 | tgname | 19 | 64 | c | |
| 5 | tgfoid | 26 | 4 | i | regproc |
| 6 | tgtype | 21 | 2 | s | bit-packed event mask |
| 7 | tgenabled | 18 | 1 | c | 'O' |
| 8 | tgisinternal | 16 | 1 | c | |
| 9 | tgconstrrelid | 26 | 4 | i | |
| 10 | tgconstrindid | 26 | 4 | i | |
| 11 | tgconstraint | 26 | 4 | i | |
| 12 | tgdeferrable | 16 | 1 | c | |
| 13 | tginitdeferred | 16 | 1 | c | |
| 14 | tgnargs | 21 | 2 | s | |
| 15 | tgattr | 22 (int2vector) | -1 | i | |
| 16 | tgargs | 17 (bytea) | -1 | i | |
| 17 | tgqual | 194 | -1 | i | NULL |
| 18 | tgoldtable | 19 | 64 | c | NULL |
| 19 | tgnewtable | 19 | 64 | c | NULL |

### Critical local index inventory

| Index | OID | On | Key tuple shape | Opclass family | Citation |
|---|---|---|---|---|---|
| pg_class_oid_index | 2662 | pg_class (1259) | `(oid)` UNIQUE | oid_ops (1989) | `pg_class.h:158`; `pg_class_d.h:25` |
| pg_attribute_relid_attnum_index | 2659 | pg_attribute (1249) | `(attrelid oid_ops, attnum int2_ops)` UNIQUE | oid_ops + int2_ops (1979) | `pg_attribute.h:219`; `pg_attribute_d.h:26` |
| pg_index_indexrelid_index | 2679 | pg_index (2610) | `(indexrelid oid_ops)` UNIQUE | oid_ops | `pg_index.h:75`; `pg_index_d.h:27` |
| pg_opclass_oid_index | 2687 | pg_opclass (2616) | `(oid oid_ops)` UNIQUE | oid_ops | `pg_opclass.h:86`; `pg_opclass_d.h:25` |
| pg_amproc_fam_proc_index | 2655 | pg_amproc (2603) | `(amprocfamily oid_ops, amproclefttype oid_ops, amprocrighttype oid_ops, amprocnum int2_ops)` UNIQUE | oid_ops + int2_ops | `pg_amproc.h:70`; `pg_amproc_d.h:24` |
| pg_rewrite_rel_rulename_index | 2693 | pg_rewrite (2618) | `(ev_class oid_ops, rulename name_ops)` UNIQUE | oid_ops + name_ops | `pg_rewrite.h:57`; `pg_rewrite_d.h:25` |
| pg_trigger_tgrelid_tgname_index | 2701 | pg_trigger (2620) | `(tgrelid oid_ops, tgname name_ops)` UNIQUE | oid_ops + name_ops | `pg_trigger.h:85`; `pg_trigger_d.h:25` |

### Initial row counts (vanilla PG18 `initdb --no-clean --no-sync`)

Counts below are post-`bootstrap_template1` plus the `system_views.sql`
phase but pre any user DDL. `genbki.pl` expands DECLARE_INDEX /
DECLARE_TOAST into pg_class rows, which is why pg_class is much
larger than `pg_class.dat` (4 entries — `wc -l` of
`src/include/catalog/pg_class.dat` shows 4 `oid =>` lines, the rest
are generated).

| Catalog | Initial rowcount | Notes |
|---|---|---|
| pg_class | ~390 | nailed catalogs + indexes + toast + views + composite rowtypes |
| pg_attribute | ~3600 | sum of relnatts across pg_class |
| pg_proc | 3397 | direct row count of `pg_proc.dat` (`grep -cE '^\\{\\s*oid\\s*=>' src/include/catalog/pg_proc.dat`) |
| pg_type | ~620 | 112 in `pg_type.dat` plus auto-generated array & rowtype peers |
| pg_trigger | 0 | empty after initdb; populated only by `CREATE TRIGGER` |

Verification recipe (do **not** check the goopg output against
hard-coded constants — run a fresh vanilla `initdb` and count):

```bash
PGDATA=$(mktemp -d) initdb -D "$PGDATA" --no-sync
postgres --single -D "$PGDATA" template1 <<<"SELECT count(*) FROM pg_class;"
postgres --single -D "$PGDATA" template1 <<<"SELECT count(*) FROM pg_attribute;"
```

### Heap-tuple on-disk format

Every catalog row is stored as a `HeapTupleHeaderData` (23 bytes
fixed) followed by an optional null bitmap, optional alignment
padding, then the column payloads.
`src/include/access/htup_details.h:122-176` defines the struct:

| Offset | Field | Width | Purpose |
|---|---|---|---|
| 0 | `t_choice` | 12 B | union of `HeapTupleFields` (`t_xmin uint32`, `t_xmax uint32`, `t_field3 uint32` which is `t_cid`/`t_xvac`) and `DatumTupleFields` (in-memory only) |
| 12 | `t_ctid` | 6 B | `ItemPointerData {bi_hi:uint16, bi_lo:uint16, ip_posid:uint16}` |
| 18 | `t_infomask2` | 2 B | low 11 bits = `natts`; high 5 bits = flags (`HEAP_KEYS_UPDATED`, `HEAP_HOT_UPDATED`, `HEAP_ONLY_TUPLE`) |
| 20 | `t_infomask` | 2 B | per-row flags — `HEAP_HASNULL = 0x0001`, `HEAP_HASVARWIDTH = 0x0002`, `HEAP_HASEXTERNAL = 0x0004`, `HEAP_HASOID_OLD = 0x0008`, `HEAP_XMIN_COMMITTED = 0x0100`, `HEAP_XMIN_INVALID = 0x0200`, `HEAP_XMIN_FROZEN = 0x0300`, `HEAP_XMAX_INVALID = 0x0800`, … (`htup_details.h:190-273`) |
| 22 | `t_hoff` | 1 B | MAXALIGNed offset from tuple start to first data byte |
| 23 | `t_bits[]` | `BITMAPLEN(natts)` B if `HEAP_HASNULL` else 0 | null bitmap, **LSB-first** within each byte: bit `i` = `t_bits[i/8] & (1 << (i%8))`; `1` means present, `0` means NULL |
| `t_hoff` | data | varies | column payloads, MAXALIGN-padded per `attalign` |

`BITMAPLEN(natts)` (`htup_details.h:595-602`) = `(natts + 7) / 8`.
`t_hoff` is always MAXALIGN-rounded so the data section starts on an
8-byte boundary; for pg_class (34 columns) with HEAP_HASNULL the
bitmap is `ceil(34/8) = 5` bytes, and
`t_hoff = MAXALIGN(23 + 5) = MAXALIGN(28) = 32`.

#### Null bitmap encoding (Step 3i provenance)

The M0106-0010 history records two regressions in goopg's bitmap
encoder:

- Step 3i — bitmap was BE within a byte (PG expects LE). Fixed by
  emitting `bits[i/8] |= 1 << (i%8)`.
- Step 3p — null bitmap omitted entirely when all values present; PG
  requires `HEAP_HASNULL` *and* a bitmap if and only if at least one
  attribute is actually null. The reverse (set `HEAP_HASNULL` but no
  bitmap) is illegal.

Both fixes are now baked into
`internal/executor/codec.go::EncodeRowPG` /
`NullBitmapPG` (see `bootstrapPostgresDatabase`'s use at
`initdb.go:830-851`). The reference doc is
`docs/design/0106-0010-step3i-null-bitmap-encoding.md`.

#### Varlena encoding

Source: `src/include/varatt.h:191-238, 279-297`. The first byte of a
varlena value selects between three encodings:

| First byte | Header | Encoding |
|---|---|---|
| `0x01..0x7F` (low bit 0, length in upper 7 bits) | 1 B | inline short; total len = (b0 >> 1) bytes, payload follows |
| `(b0 & 0x03) == 0x02` | 1 B | TOAST pointer; next byte is `varatt_external` tag (`VARTAG_ONDISK = 18`, `VARTAG_INDIRECT = 1`, `VARTAG_EXPANDED_RO = 2`, `VARTAG_EXPANDED_RW = 3` — `varatt.h:81-100`) |
| `(b0 & 0x03) == 0x00`, with high bit 0 in the 4-byte length | 4 B | inline long; `uint32` LE length-with-header in the first 4 bytes |

All multi-byte fields are little-endian on the architectures
goopg targets (x86_64, arm64) — `pg_control.floatFormat` and
`float8ByVal=true` (`02-pg-control-and-checkpoint.md`) require it.

### Btree on-disk format

#### Metapage (block 0)

Source: `src/include/access/nbtree.h:104-120` for
`BTMetaPageData`; `_bt_initmetapage` at
`src/backend/access/nbtree/nbtpage.c:67-122` for the initdb-time
encoding. The metapage sits in block 0 of every btree relfile and
must satisfy `_bt_getmeta`'s sanity check: `P_ISMETA(opaque) &&
metad->btm_magic == BTREE_MAGIC`. Layout from `pd_lower`:

| Offset (from PageGetContents) | Field | Width | Initdb value |
|---|---|---|---|
| 0 | `btm_magic` | uint32 | `BTREE_MAGIC = 0x053162` (`nbtree.h:150`) |
| 4 | `btm_version` | uint32 | `BTREE_VERSION = 4` (`nbtree.h:151`); reader accepts `[BTREE_MIN_VERSION=2, BTREE_VERSION=4]` (`nbtree.h:152`) |
| 8 | `btm_root` | BlockNumber (uint32) | `1` if an initial leaf-root has been written (e.g. by goopg's `bootstrapPgClassOidIndex`); `0 = P_NONE` for an empty index |
| 12 | `btm_level` | uint32 | `0` for leaf-only roots |
| 16 | `btm_fastroot` | BlockNumber | equal to `btm_root` |
| 20 | `btm_fastlevel` | uint32 | equal to `btm_level` |
| 24 | `btm_last_cleanup_num_delpages` | uint32 | 0 |
| 28 | 4-byte alignment pad | — | 0 (so `btm_last_cleanup_num_heap_tuples` lands on offset 32, an 8-byte boundary) |
| 32 | `btm_last_cleanup_num_heap_tuples` | float8 | `-1.0` sentinel |
| 40 | `btm_allequalimage` | bool | `false` for any index containing a name-keyed column (name_ops is not deduplication-safe); `true` for `(oid)` indexes |
| 41..47 | trailing pad | — | 0 (sizeof multiple of 8) |

The metapage's `BTPageOpaqueData` (last 16 bytes of the page) has
`btpo_flags = BTP_META = 1 << 3`; all other flag bits are zero.

#### Leaf-root page

Block 1 of an initdb-populated index is a single page holding both
the root and the leaf level. Its `BTPageOpaqueData` flags include
`BTP_LEAF | BTP_ROOT` (`nbtree.h:79-82`). Items are appended via
`ItemId`s pointing to `IndexTupleData`-prefixed payloads.

#### IndexTupleData

Source: `src/include/access/itup.h:35-51`.

| Offset | Field | Width | Meaning |
|---|---|---|---|
| 0 | `t_tid.ip_blkid` | 4 B | heap block number |
| 4 | `t_tid.ip_posid` | 2 B | heap line-pointer slot |
| 6 | `t_info` | 2 B | bit 15 = `INDEX_NULL_MASK` (has nulls), bit 14 = `INDEX_VAR_MASK` (has var-width attrs), bit 13 = AM-defined, bits 12..0 = total tuple size |
| 8 | optional null bitmap | `ceil(nkeys/8)` B (only if has nulls) | LSB-first per byte |
| n | key columns | varies | MAXALIGN-padded per `attalign` from the index's opclass |

For all seven critical local indexes, the key columns are fixed-width
(`oid`, `int2`, `name` — name is fixed-width 64 B), no nulls are
allowed (unique indexes on non-null catalog columns), and `t_info`
bit 14/15 are clear. `name_ops` keys are stored padded to NAMEDATALEN
(64 B) — they do **not** use the varlena 1-byte encoding even though
`name` is in the varlena type domain.

---

## Continuous maintenance

After a goopg primary has started serving traffic, every DDL
statement mutates these catalogs in lockstep with the user heap.
Each mutation must (a) write the new heap tuple via PG-canonical
encoding, (b) emit `XLOG_HEAP_*` and `XLOG_BTREE_INSERT_LEAF`
records so a streaming standby can replay the change, (c) update the
relevant per-tuple indexes, and (d) issue a `SI` invalidation
message via `CacheInvalidateHeapTuple` so other backends drop their
cached relcache/catcache entries.

A vanilla PG18 walreceiver may attach at any point in this lifetime,
so the on-disk state **must remain PG-canonical at every commit
boundary**. There is no "settle after initdb" window.

### Per-DDL mutation matrix

| Operation | Catalogs touched | Indexes touched | WAL records | Cache inval. |
|---|---|---|---|---|
| `CREATE TABLE` | pg_class (heap +rowtype) ; pg_attribute (×relnatts) ; pg_type (rowtype + array) | 2662, 2659, 2679 (no rows yet for the new heap), pg_type_oid_index | XLOG_HEAP_INSERT × (1 + relnatts + 2) ; XLOG_BTREE_INSERT_LEAF × index count | `RELOID`, `RELNAMENSP`, `TYPEOID`, `TYPENAMENSP` |
| `CREATE INDEX` | pg_class (index row) ; pg_attribute (×nkeys) ; pg_index | 2662, 2659, 2679 | XLOG_HEAP_INSERT × (1 + nkeys + 1) ; XLOG_BTREE_INSERT_LEAF × 3 | `RELOID`, `INDEXRELID`, parent-table `RELOID` |
| `DROP TABLE` | pg_class DELETE ; pg_attribute DELETE × ; pg_type DELETE × 2 ; pg_depend DELETE × | 2662, 2659, 2679 | XLOG_HEAP_DELETE × ; XLOG_BTREE_INSERT_LEAF for tombstone? (no — btree just marks dead) | `RELOID`, `RELNAMENSP`, `TYPEOID` |
| `DROP INDEX` | pg_class DELETE ; pg_attribute DELETE × ; pg_index DELETE | 2662, 2659, 2679 | XLOG_HEAP_DELETE × | `RELOID`, `INDEXRELID` |
| `ALTER TABLE … ADD COLUMN` | pg_attribute INSERT ; pg_class UPDATE (relnatts) | 2659, 2662 (HOT update) | XLOG_HEAP_INSERT + XLOG_HEAP_UPDATE | `RELOID`, `ATTNUM`, `ATTNAME` |
| `ALTER TABLE … DROP COLUMN` | pg_attribute UPDATE (attisdropped) ; pg_class UPDATE | 2659 (no key change → no leaf insert), 2662 (HOT) | XLOG_HEAP_UPDATE × 2 | `RELOID`, `ATTNUM`, `ATTNAME` |
| `ALTER TABLE … ALTER COLUMN TYPE` | pg_attribute UPDATE (atttypid, attlen, …) ; pg_class UPDATE (relfilenode bump if rewrite) ; pg_depend rewrite | 2659 (HOT), 2662 (HOT) | XLOG_HEAP_UPDATE × 2 ; on full rewrite: relmap update + new file | `RELOID`, `ATTNUM` |
| `CREATE FUNCTION` | pg_proc INSERT | 2690 (pg_proc_oid_index, **not** critical-local) ; pg_proc_proname_args_nsp_index (see 06) | XLOG_HEAP_INSERT ; XLOG_BTREE_INSERT_LEAF × 2 | `PROCOID`, `PROCNAMEARGSNSP` |
| `CREATE TYPE` | pg_type INSERT (base) + array type | pg_type_oid_index 2703 ; pg_type_typname_nsp_index | XLOG_HEAP_INSERT × 2 ; XLOG_BTREE_INSERT_LEAF × 4 | `TYPEOID`, `TYPENAMENSP` |
| `CREATE TRIGGER` | pg_trigger INSERT ; pg_class UPDATE (relhastriggers=t if first) ; pg_depend INSERT × | **2701** ; 2662 | XLOG_HEAP_INSERT × ; XLOG_BTREE_INSERT_LEAF × | `RELOID` (relcache must reload to see the new trigger) |
| `VACUUM` | pg_class UPDATE (relfrozenxid, relminmxid, relallvisible, relallfrozen) | 2662 (HOT) | XLOG_HEAP_UPDATE ; XLOG_HEAP2_PRUNE_VACUUM_SCAN / `…_CLEANUP` (`heapam_xlog.h:60-62` — PG18 folded freeze into prune, no separate XLOG_HEAP2_FREEZE_PAGE) ; XLOG_HEAP2_VISIBLE (`:63`) | `RELOID` (planner stats stale) |
| `VACUUM FULL` / `CLUSTER` | pg_class UPDATE (relfilenode bump) — for nailed rels via **`RelationMapUpdateMap`** (`relmapper.c:325`) ; new heap file + reindex of every index | 2662 (HOT) ; every index rebuilt → metapage rewrite | XLOG_HEAP_UPDATE ; XLOG_RELMAP_UPDATE (`xlog.h`) for mapped rels ; full XLOG image for new heap and indexes | `RELOID`, `SMGR_RELATION` |
| `TRUNCATE` | pg_class UPDATE (relfilenode bump for non-mapped) | 2662 (HOT) | XLOG_HEAP_TRUNCATE (`heapam_xlog.h`) ; XLOG_HEAP_UPDATE ; XLOG_RELMAP_UPDATE if mapped | `RELOID`, `SMGR_RELATION` |
| `REINDEX INDEX` | pg_class UPDATE (index relfilenode bump) — for nailed indexes via `RelationMapUpdateMap` | 2662 (HOT) | XLOG_HEAP_UPDATE ; XLOG_BTREE_NEWROOT ; XLOG_RELMAP_UPDATE for mapped indexes | `RELOID`, parent `RELOID` |
| `ANALYZE` | pg_class UPDATE (reltuples, relpages, relallvisible, relallfrozen) ; pg_statistic INSERT/UPDATE | 2662 (HOT) | XLOG_HEAP_UPDATE ; XLOG_HEAP_INSERT × stats rows | `RELOID` (planner stats) |

The "HOT" annotation in the "Indexes touched" column means the
pg_class index does *not* receive a new leaf entry because the row
OID (the indexed key) is unchanged; PG performs an in-place / HOT
update of the same heap tuple. The btree leaf is still touched via a
visibility-map / page-prune update, but no new IndexTuple is added.

### WAL records (header constants)

Source: `src/include/access/heapam_xlog.h:33-66`,
`src/include/access/nbtxlog.h:27-39`. Each row below is one
`info & XLR_RMGR_INFO_MASK` byte the goopg WAL emitter must produce
when mutating the corresponding catalog or index.

| RmgrID | info byte | Name | Used by |
|---|---|---|---|
| `RM_HEAP_ID` | 0x00 | `XLOG_HEAP_INSERT` | every catalog INSERT |
| `RM_HEAP_ID` | 0x10 | `XLOG_HEAP_DELETE` | every catalog DELETE |
| `RM_HEAP_ID` | 0x20 | `XLOG_HEAP_UPDATE` | every catalog UPDATE (HOT or not) |
| `RM_HEAP_ID` | 0x60 | `XLOG_HEAP_LOCK` | SELECT FOR UPDATE on catalogs |
| `RM_HEAP2_ID` | 0x10/0x20/0x30 | `XLOG_HEAP2_PRUNE_ON_ACCESS` / `_VACUUM_SCAN` / `_VACUUM_CLEANUP` | VACUUM, opportunistic prune (PG18 merged freeze into these; **no `XLOG_HEAP2_FREEZE_PAGE` in PG18**) |
| `RM_HEAP2_ID` | 0x40 | `XLOG_HEAP2_VISIBLE` | VM bit set |
| `RM_HEAP2_ID` | 0x50 | `XLOG_HEAP2_MULTI_INSERT` | bulk-load paths |
| `RM_BTREE_ID` | 0x00 | `XLOG_BTREE_INSERT_LEAF` | every UNIQUE-index INSERT on critical-local indexes |
| `RM_BTREE_ID` | 0x30/0x40 | `XLOG_BTREE_SPLIT_L` / `_SPLIT_R` | leaf split on growth |
| `RM_BTREE_ID` | 0xC0 | `XLOG_BTREE_VACUUM` | index VACUUM |

### Cache invalidation rule

`CacheInvalidateHeapTuple(rel, tuple, newtuple)` (`inval.c:1571`)
must be called on every successful insert/update/delete of a
catalog tuple, in the same transaction, before commit. The macro
expands into SI message enqueues keyed by syscache ID (`RELOID`,
`ATTNUM`, etc.) which other backends consume from shared memory at
their next transaction-start `AcceptInvalidationMessages` call. For
in-place updates of nailed-rel pg_class tuples (the VACUUM /
ANALYZE path), `CacheInvalidateHeapTupleInplace` (`inval.c:1593`)
is used instead so the inval fires even though the heap update is
not transactional.

### Relmap considerations

The five nailed-local catalogs are *mapped* relations
(`pg_class.relfilenode = 0`; real relfilenumber lives in
`base/<dboid>/pg_filenode.map`). Any operation that bumps the
relfilenode of one of these — `VACUUM FULL`, `CLUSTER`, `TRUNCATE`,
`REINDEX` on a critical local index — must go through
`RelationMapUpdateMap` (`relmapper.c:325`) which emits an
`XLOG_RELMAP_UPDATE` record and rewrites the local
`pg_filenode.map` atomically. A bare `pg_class.relfilenode` UPDATE
without the relmap update would silently desync mapped readers from
on-disk file location and surface as `could not open file base/<dboid>/<oid>`
on the next attach.

---

## What goopg must produce

### Per-catalog and per-index initdb-time status

| Artefact (per `base/<dboid>/`) | goopg implementation | Status | Gap |
|---|---|---|---|
| pg_class heap (1259) | `bootstrapPgClassTuples` (`initdb.go:1176`) | **partial** | All nailed-rel + critical-index rows are seeded with the PG18 34-column layout, but only ~25 of the ~390 vanilla rows are emitted; the remaining ~365 (pg_proc rowtype TOAST rels, every non-nailed catalog rowtype, every shared-catalog index row, every composite-type rowtype) come from `bootstrapMappedLocalCatalogHeaps` (`initdb.go:529`) which only writes empty heap pages and does not insert pg_class rows for them. Reading `pg_class` from a goopg standby therefore returns ~25 rows, not 390. |
| pg_attribute heap (1249) | `bootstrapPgAttributeTuples` (`initdb.go:1206`) | **partial** | Emits the per-column rows for every relation seeded into pg_class; same coverage gap (~25 rels × ~10 columns ≈ 250 rows, vs. ~3600 vanilla). |
| pg_proc heap (1255) | `bootstrapPgProcTuples` (`initdb.go:1849`) | **partial** | Seeds **7 AM-handler rows** (heap_tableam_handler=3, bthandler=330, hashhandler, ginhandler, gisthandler, brinhandler, spghandler — see `pgProcInitialEntries` at `:1655`). Vanilla has 3397 rows; the 3390-row gap is the load-bearing gap blocking standby `InitPostgres`'s syscache lookups for I/O functions, operators, and aggregates. |
| pg_type heap (1247) | `pg_type_bootstrap.go::bootstrapPgTypeTuples` | **partial** | Seeds the type rows referenced by nailed-rel attrs (int2, int4, int8, oid, name, bool, char, text, xid, float4, aclitem[], text[], pg_node_tree, anyarray); ~25 rows vs. ~620 vanilla. |
| pg_trigger heap (2620) | `bootstrapMappedLocalCatalogHeaps` (`initdb.go:558`, emits empty heap page) | **done** | Vanilla initdb leaves pg_trigger empty; one heap-initialised page is the correct on-disk state. |
| pg_class_oid_index (2662) | `bootstrapPgClassOidIndex` (`btree_index_bootstrap.go`; invoked at `initdb.go:373`) | **partial** | Metapage + 1-leaf root with one tuple per seeded pg_class row; needs ~365 more leaf entries to cover the full vanilla set, plus likely a leaf split into a 3-page tree once row count exceeds ~340. |
| pg_attribute_relid_attnum_index (2659) | `bootstrapPgAttributeRelidAttnumIndex` (`initdb.go:389`) | **partial** | Same per-row coverage gap as pg_attribute. |
| pg_index_indexrelid_index (2679) | `bootstrapPgIndexIndexrelidIndex` (`initdb.go:353`) | **partial** | Seeds entries for the critical-index pg_index rows actually written; non-critical indexes (~150 of them) are not seeded. |
| pg_opclass_oid_index (2687) | `bootstrapPgOpclassOidIndex` (`initdb.go:362`) | **partial** | Seeds the opclass rows currently in pg_opclass (`internal/initdb/initdb.go::bootstrapPgOpclassTuples`); coverage tied to opclass row coverage in `06-`. |
| pg_amproc_fam_proc_index (2655) | `bootstrapPgAmprocFamProcIndex` (`initdb.go:328`) | **partial** | Same per-row coverage gap as pg_amproc seeding in `06-`. |
| pg_rewrite_rel_rulename_index (2693) | `bootstrapPgRewriteRelRulenameIndex` (`btree_index_bootstrap.go:1551`; invoked at `initdb.go:405`) | **partial** | Seeds the `pg_stat_wal_receiver._RETURN` row from Step 3dm; needs rules for every other system view (~60 views) — see `07-`. |
| pg_trigger_tgrelid_tgname_index (2701) | `makeBtreeRootPage()` → empty metapage-only file (`initdb.go:1024`) | **done** | Vanilla has zero rows in pg_trigger, so an empty btree (metapage only, `btm_root = 0`, `btm_version = 4`) is the correct on-disk state. |

### Runtime DDL-handler status

| Trigger | goopg handler | Status | Gap |
|---|---|---|---|
| `CREATE TABLE` heap+attr+type insert | `internal/executor/operators_ddl.go::execCreateTable` (`:305`) | **partial** | Inserts rows into the in-memory catalog and the goopg catalog persist layer, but does **not** emit PG-canonical `XLOG_HEAP_INSERT` records for pg_class / pg_attribute / pg_type, and does not call `CacheInvalidateHeapTuple` on shared-memory consumers. A vanilla standby would not see the new table. |
| `CREATE INDEX` | `execCreateIndex` (`:823`) | **partial** | Same gap as CREATE TABLE; additionally needs `XLOG_BTREE_INSERT_LEAF` for the new index's data. |
| `DROP TABLE` / `DROP INDEX` | `execDropTable` / `execDropIndex` (`:744, :860`) | **partial** | Catalog DELETE on the goopg side; missing the WAL+inval path for replicated catalog deletes and the file-unlink WAL record (`XLOG_SMGR_TRUNCATE` / `XLOG_SMGR_CREATE`). |
| `ALTER TABLE ADD/DROP/ALTER COLUMN` | `execAlterTableAddColumn` (`:991`) and siblings | **partial** | Same WAL+inval gap; the pg_class.relnatts UPDATE in particular must be PG-canonical so a standby's relcache rebuild produces the new TupleDesc. |
| `CREATE FUNCTION` | `execCreateFunction` (`:1553`) | **partial** | pg_proc row inserted into goopg catalog; PG-canonical `XLOG_HEAP_INSERT` against base/<dboid>/1255 not emitted. |
| `CREATE TYPE` | `execCreateType` (`:2173`) | **partial** | Same. |
| `CREATE TRIGGER` | `execCreateTrigger` (`:1895`) | **partial** | pg_trigger row inserted into goopg catalog; no pg_trigger heap-tuple emission on the disk file `base/<dboid>/2620`. |
| `VACUUM` | `internal/executor/operators_vacuum.go` | **partial** | The pg_class HOT update for relfrozenxid/relminmxid/relallvisible/relallfrozen path exists in goopg's catalog layer, but the PG18-canonical `XLOG_HEAP2_PRUNE_VACUUM_*` and `XLOG_HEAP2_VISIBLE` records are only partially produced (`internal/wal/recovery.go:96, :292, :346`). |
| `VACUUM FULL` / `CLUSTER` / `TRUNCATE` | `execTruncate` (`:1504`) | **missing** | No relmap-update path for mapped catalogs; no `XLOG_RELMAP_UPDATE` emission. A `TRUNCATE pg_class` on goopg today would silently corrupt a streaming standby. |
| `REINDEX INDEX` (mapped critical index) | none | **missing** | A `REINDEX INDEX pg_class_oid_index` against a goopg primary cannot succeed; the relmap path is not yet wired. |
| `ANALYZE` | `internal/executor/operators_analyze.go` | **partial** | pg_class reltuples/relpages UPDATE is performed on the goopg side; PG-canonical WAL not emitted. |

### Recommended Go entry points

The continuous-maintenance work consolidates into three additions
under `internal/catalog/`:

1. `func PgCanonicalHeapInsert(rel catalog.Relation, tuple
   *executor.Row) (LSN, error)` — encode the row via
   `EncodeRowPG`/`NullBitmapPG`, append to the on-disk
   `base/<dboid>/<filenode>` page, emit `XLOG_HEAP_INSERT`, and
   enqueue an SI message via the equivalent of
   `CacheInvalidateHeapTuple`.
2. `func PgCanonicalBtreeInsert(rel catalog.Relation, key
   IndexKey, heapTID HeapTID) (LSN, error)` — emit
   `XLOG_BTREE_INSERT_LEAF`, splitting as needed.
3. `func RelationMapUpdateMap(dboid uint32, relid uint32,
   relfilenode uint32, shared bool) error` — emit
   `XLOG_RELMAP_UPDATE` and atomically rewrite
   `base/<dboid>/pg_filenode.map` (or `global/pg_filenode.map`
   when `shared`).

Every existing DDL handler in `internal/executor/operators_ddl.go`
funnels through these three helpers for its catalog side-effects;
the in-memory goopg catalog path can remain in parallel until the
PG-canonical path is fully wired.

---

## Verification

1. **`pg_filedump` byte-diff vs vanilla.**

   ```bash
   PGDATA=$(mktemp -d) initdb -D "$PGDATA" --no-sync
   GOOPGDATA=$(mktemp -d) goopg init -D "$GOOPGDATA"
   for oid in 1259 1249 1255 1247 2620 2662 2659 2679 2687 2655 2693 2701; do
     pg_filedump -i -f "$PGDATA/base/1/$oid"   > "/tmp/van.$oid.txt"
     pg_filedump -i -f "$GOOPGDATA/base/1/$oid" > "/tmp/goo.$oid.txt"
     diff -u "/tmp/van.$oid.txt" "/tmp/goo.$oid.txt" || echo "DIFF on $oid"
   done
   ```

   Today the byte-diff is non-empty on every heap relfile because of
   the per-row coverage gaps in the table above; the diff on the
   index files is metapage-clean (matching `BTMetaPageData` bytes for
   block 0 of every populated critical index) but leaf-page-dirty for
   the heap-tuple-coverage gap reasons.

2. **Row-count parity.**

   ```bash
   postgres --single -D "$GOOPGDATA" template1 <<<"
     SELECT relname, n FROM (VALUES
       ('pg_class',390),('pg_attribute',3600),('pg_proc',3397),
       ('pg_type',620),('pg_trigger',0)
     ) AS expected(relname,n)
     JOIN (SELECT relname, (SELECT count(*) FROM pg_catalog.pg_class) AS actual
           FROM pg_catalog.pg_class WHERE relname = 'pg_class') USING (relname);
   "
   ```

   The expected counts are derived from a one-shot fresh vanilla
   `initdb`; the numbers above are illustrative and must be
   regenerated whenever the vendored PG submodule is bumped.

3. **Metapage byte invariants.** For each of the seven critical
   local indexes:

   ```bash
   pg_filedump -i -f -R 0 "$GOOPGDATA/base/1/$oid"
   ```

   must print `Block 0`, `BTREE_MAGIC: 0x053162`, `Version: 4`,
   `Root: <block>`, `Level: <n>`, `FastRoot: <block>`,
   `FastLevel: <n>`, with `Root` matching `FastRoot` and
   `Level == FastLevel`, satisfying
   `BTREE_MIN_VERSION (=2) <= Version <= BTREE_VERSION (=4)`
   (`nbtree.h:151-152`).

4. **Null-bitmap encoder property test.** Add a Go test under
   `internal/initdb/` that for every pg_attribute row goopg writes,
   round-trips
   `(natts, nullSet) → BITMAPLEN(natts) → t_bits → re-read → nullSet'`
   and asserts `nullSet == nullSet'`. Cross-reference Step 3i
   regression.

5. **Catalog DDL invalidation round-trip.** A new
   `internal/catalog/inval_test.go` runs the
   `PgCanonicalHeapInsert` helper, captures the emitted `XLOG_HEAP_INSERT`
   bytes via the goopg WAL writer, feeds them into a vanilla PG18
   standby running under `TestE2E_FailoverGoopgToPG/async`, and
   asserts that `SELECT relname FROM pg_class WHERE oid = <new>` on
   the standby returns the inserted row.

6. **E2E gate.**
   `TestE2E_FailoverGoopgToPG/async` is the integration sentinel:
   the standby's `RelationCacheInitializePhase3` must not FATAL on
   `load_critical_index` for any of OIDs 2655, 2659, 2662, 2679,
   2687, 2693, 2701; and the per-backend `InitPostgres` must
   complete its initial pg_class/pg_attribute/pg_proc/pg_type
   syscache fills without a "missing N attribute(s) for relation
   OID" or "cache lookup failed for type/proc" FATAL. Today the
   test fails earlier (pg_proc row coverage) — once the gaps in the
   table above are closed, this section becomes the next
   verification surface.
