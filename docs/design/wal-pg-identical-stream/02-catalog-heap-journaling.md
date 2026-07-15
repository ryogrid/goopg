# 02 — Catalog heap journaling (Section B)

| Field  | Value                                                        |
| ------ | ------------------------------------------------------------ |
| Status | draft — **agent-reviewed** 2026-07-15 (0 blocker; 5 major + 2 minor folded in: bespoke-count ~102, heap-read vs heap-write/recovery split, shared/`global/`+`XLOG_RELMAP_UPDATE`, cache write-through + heap-read cost) — **the crux** |
| Date   | 2026-07-15                                                   |
| Scope  | Eliminate the ~110 goopg-private catalog/DDL WAL records that have no PG analog |
| Target | PostgreSQL 18.3 catalog-DML journaling (`postgres/src/backend/catalog/indexing.c`) |

## 1. The problem: PG has no bespoke catalog records

PostgreSQL journals **every** catalog change as ordinary heap-tuple DML on
`pg_catalog` relations. `CREATE SCHEMA` is an `INSERT` into `pg_namespace` (a real
heap) plus index inserts into its two indexes; on the wire it is
`XLOG_HEAP_INSERT` (RM_HEAP) on `pg_namespace`'s `RelFileLocator` + two
`XLOG_BTREE_INSERT_LEAF` (RM_BTREE) + the commit's invalidation messages. There is
**no** "create schema" record kind — the concept does not exist in PG's WAL.

goopg instead journals `CREATE SCHEMA` (and ~110 other DDLs) as a bespoke
`RecordKind` (now tagged with the custom `RmgrGoopgCatalog` rmgr,
[doc 04](../wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md) §3.2).
As long as these records exist, **the WAL stream can never equal PG's** — no
byte-level tweak makes a `RmgrGoopgCatalog` record into a heap insert on
`pg_namespace`. Parity requires goopg to *stop emitting bespoke records* and
instead *mutate real catalog heaps*, which then produce the PG-analog records
automatically.

This is a **catalog-storage rewrite**, not a WAL-generation change. Crucially,
goopg has **already built and proven the mechanism** for a subset of catalogs;
Section B is about extending it to the rest.

## 2. goopg's current catalog model (three substrates)

1. **On-disk bootstrap heaps.** `initdb` writes real OID-keyed relfiles: ~19
   catalogs get *populated* heap rows (`internal/initdb/initdb.go:1302-1320` —
   pg_type/1247, pg_attribute/1249, pg_proc/1255, pg_class/1259, pg_index/2610,
   pg_namespace/2615, …), and ~35 more get **empty 8 KiB placeholder** heaps
   (`mappedLocalCatalogPlaceholderOIDs`, `bootstrapMappedLocalCatalogHeaps`,
   `initdb.go:1321-1368`) so a PG standby's `BasicOpenFile` doesn't ENOENT.
2. **Runtime read model — mostly VIRTUAL.** At runtime the catalog is served from
   `catalog.InMemory`, not by reading the heaps. `catalog.Table` has
   `Virtual bool` + `VirtualRows func() [][]string` (`internal/catalog/catalog.go:335`);
   a SeqScan on a virtual table becomes a Values node. There are **91 `Virtual: true`
   builders** — including **pg_class** (`catalog.go:7602`), pg_namespace, pg_proc,
   pg_database. The **only** runtime heap-backed catalogs are **pg_type and
   pg_attribute**, registered via `catalog.RegisterRealTable` (`catalog.go:11618`)
   in `loadSystemCatalogsIfPresent` (`internal/initdb/open.go:2436-2490`).
3. **Per-DB scoping.** User relations live in `InMemory.namespaces` keyed by
   physical `dbOid` (`catalog.go:2129`); catalog files live at
   `base/<dbOid>/<catOid>`.

### 2.1 A critical distinction: base catalogs vs. views

Of the 91 virtual builders, **only the base `pg_catalog` *tables* matter for WAL
parity** (~50: pg_namespace, pg_proc, pg_operator, pg_opclass/opfamily/amop/amproc,
pg_cast, pg_conversion, pg_collation, pg_range, pg_aggregate, pg_enum, pg_sequence,
pg_language, pg_ts_*, pg_transform, pg_event_trigger, pg_publication*/subscription*,
pg_statistic_ext, pg_constraint/pg_attrdef/pg_depend, plus shared pg_database/
pg_authid/pg_tablespace/pg_foreign_*).

The rest — pg_catalog **views** (pg_roles, pg_stat_*, pg_locks, …) and all of
`information_schema.*` — **stay virtual, correctly**: in PostgreSQL these are
views/rules or in-memory-computed relations that hold no heap tuples and emit **no
WAL**. So the rewrite touches only the ~50 base catalogs; the ~40 view builders are
out of scope and remain as-is. This materially bounds the work.

## 3. The two DDL-journal families that already coexist

### Family 1 — heap-op journaling + generic heap-scan recovery (the model; already works)

`CREATE TABLE`: `execCreateTable` (`internal/executor/operators_ddl.go:1458`)
mutates the in-memory namespace, then `syncTableToCatalogHeap`
(`operators_ddl.go:13092`) writes **real** pg_class + per-column pg_attribute heap
tuples via `writeHeapRowReturningPG` (`internal/executor/operators_storage.go:8027`)
— which goes through the buffer pool + `markHeapInsertDirty` and emits a **genuine
`XLOG_HEAP_INSERT`** on the catalog's `RelFileNode` — plus the sys-btree index
tuples (`insertPgClassOidIndexEntry`, `sys_catalog_index_insert.go`) emitting
`XLOG_BTREE_INSERT_LEAF`. **Recovery uses no bespoke scanner**: after physical WAL
replay, `loadUserTablesFromHeap` (`open.go:2604`, `…ForDB` `:2616`) **scans the
pg_class heap generically**, decodes each committed tuple (clog-visibility
filtered), and re-registers the `Table`. This is exactly PG's "the catalog is the
heap" model — and it already emits Section-A records.

### Family 2 — bespoke `RecordKind` + dedicated scanner (Section B, the ~110 records)

`CREATE SCHEMA`: the `execCreate` schema arm (`operators_ddl.go:16318-16333`) calls
`im.RegisterSchema(name)` — **in-memory only, no pg_namespace heap write** — then
`WAL.Append(wal.EncodeCreateSchema(name, oid))` emits `RecordKindCreateSchema`(34)
(`internal/wal/recovery.go:3373`). **Recovery**: physical replay ignores it (no
page state); a dedicated pass `replaySchemaDDLRecords`
(`internal/initdb/schema_ddl_recovery.go:38`) re-reads all WAL, switches on the
kind, and calls `RegisterSchemaDuringRecovery`. There are **26 such
`*_ddl_recovery.go` files** (with hand-coded ordering dependencies wired as a long
sequence of `replay*DDLRecords` calls in `open.go`), and **122 `RecordKind`
constants** in `recovery.go`.

## 4. Resolution: extend Family 1 to every base catalog

The design is to make **every base `pg_catalog` table** behave like pg_class does
today: DDL writes real heap tuples + full index maintenance (the PG-analog records
emit automatically), and recovery reconstructs state by scanning the catalog heaps.
Then the bespoke records, their encoders/decoders, and the dedicated scanners are
**deleted**.

### 4.1 What PostgreSQL does (the target)

A DDL builds a `HeapTuple` and calls `CatalogTupleInsert`/`Update`/`Delete`
(`postgres/src/backend/catalog/indexing.c`), which:
1. `heap_insert`/`heap_update`/`heap_delete` on the catalog's `RelFileLocator` →
   **`XLOG_HEAP_INSERT` / `_UPDATE` / `_DELETE`** (RM_HEAP);
2. `CatalogIndexInsert` into **every** index on that catalog → **`XLOG_BTREE_INSERT_LEAF`**
   (+ split/newroot as needed) per index;
3. for a genuinely new relation (sequence, matview): `RelationCreateStorage` →
   **`XLOG_SMGR_CREATE`** per fork (+ `XLOG_RELMAP_UPDATE` for mapped catalogs);
4. at commit, **`XLOG_XACT_COMMIT`** carrying the shared-invalidation messages.

Catalogs each major DDL touches: `CREATE SCHEMA` → pg_namespace (+2 indexes) +
pg_depend; `CREATE FUNCTION` → pg_proc (+2 indexes) + pg_depend; `CREATE SEQUENCE`
→ pg_class + pg_sequence + a 1-tuple sequence heap; `CREATE TYPE`/domain/enum →
pg_type (+ pg_enum). All of these are Section-A records — no new record kind.

### 4.2 Per-catalog rewrite recipe

For each base catalog `C` (and each DDL that mutates it), replace
`WAL.Append(Encode<X>)` with:

1. **Build the PG-physical catalog tuple** for `C` and `writeHeapRowReturningPG`
   into `base/<dbOid>/<C-oid>` (reuse the Family-1 path;
   `syncTableToCatalogHeap:13100` already routes by `heapDBOid`). INSERT and DELETE
   are covered by the existing heap AM; **ALTER needs an emitted `XLOG_HEAP_UPDATE`**
   — goopg emits heap updates for user tables but `RecordKindHeapUpdate`(27) is
   currently decode-only for catalogs (doc 01 appendix), so wire the update path
   for catalog relfiles.
2. **Insert index tuples into every index on `C`** (reuse `sys_catalog_index_insert.go`
   / `sys_catalog_btree_split.go`). Today only pg_class's 2 indexes + pg_attribute's
   1 are maintained at runtime; the other ~50 catalogs' bootstrap btrees have **no
   runtime insert path**, and **non-default databases have no catalog btree files at
   all** (`operators_ddl.go:13111-13138`) — both must be built (§6).
3. **`XLOG_SMGR_CREATE`** for genuinely new relations (sequences/matviews).
4. **Move the read model** for `C`. **Default = an in-memory cache rebuilt from the
   heap at startup + runtime write-through** (the pg_class precedent; PG-parallel:
   relcache/catcache are caches over the heap). Two requirements the design commits
   to (review MAJOR-5):
   - **Write-through coherency:** the DDL must update the in-memory cache in the
     **same operation** as the heap write — "rebuilt at startup" alone leaves
     post-DDL reads stale. This is the project's recurring sibling-path hazard (cf.
     `pg_attribute ALTER needs heap re-sync`), so cache-write and heap-write are one
     atomic step, not two.
   - **Read cost:** true heap-**read** (`RegisterRealTable`) is a heap **SeqScan**
     with no index-backed syscache; using it for a hot, high-cardinality catalog
     (pg_proc, pg_type) would be a severe read regression vs PG's indexed syscaches.
     So true heap-read is reserved for **low-traffic** catalogs; hot catalogs keep
     the write-through cache. (A real syscache is future work, out of this scope.)
   The `VirtualRows` closure is deleted; queries read the cache (or, for low-traffic
   catalogs, the heap).
5. **Delete** `C`'s `RecordKind*` constants + their `Encode*`/`Decode*` and the
   corresponding `*_ddl_recovery.go` scanner. Recovery for `C` becomes a generic
   heap scan à la `loadUserTablesFromHeap`.

### 4.2.1 Shared / mapped catalogs need `global/` + `XLOG_RELMAP_UPDATE` (review MAJOR-4)

Step 1's `base/<dbOid>/<C-oid>` path is correct only for the **per-database**
catalogs. Two PG facts the recipe must also honor:

- **Shared catalogs live in `global/`** as a single cluster-wide relation
  (`RelFileLocator` dbOid = 0): pg_database, pg_authid, pg_auth_members,
  pg_tablespace, pg_foreign_*/pg_user_mapping. Their heap write targets `global/<oid>`,
  not `base/<dbOid>/`, and cross-DB visibility (R4) follows from the single relfile.
- **Mapped ("nailed") catalogs emit `XLOG_RELMAP_UPDATE`.** For the mapped catalogs
  — pg_class, pg_attribute, pg_type, pg_proc (per-DB) and the shared set — the
  relfilenode lives in `pg_filenode.map`, and a relation-file change emits an
  **`XLOG_RELMAP_UPDATE`** (RM_RELMAP) in addition to the heap/btree records. For
  true byte-parity these must be emitted; **goopg has no relmap emit path today**
  (`RelationMapUpdateMap` was a stub in the deleted `canonical.go`), so a real
  `pg_filenode.map` writer + `XLOG_RELMAP_UPDATE` encoder is **net-new** and part of
  this scope. (Steady-state INSERT/UPDATE/DELETE on an existing mapped catalog does
  **not** emit relmap — only relfilenode changes, e.g. VACUUM FULL / rewrite / TRUNCATE
  do — so the common DDL path is heap+btree only; relmap is needed for the
  rewrite/create cases and for bootstrap fidelity.)



Physical WAL replay already reconstructs the catalog heap pages (the `XLOG_HEAP_*`
/ `XLOG_BTREE_*` records replay like any relation). Startup then does a **generic
per-catalog heap scan** (generalize `loadUserTablesFromHeapForDB`) to rebuild the
in-memory caches — one reload routine parameterized by catalog, replacing the 26
bespoke scanners and their fragile ordering dependencies. Ordering falls out of
heap visibility + OID references naturally, as in PG.

## 5. Catalog inventory (rewrite state)

> **Read-model nuance (review MAJOR-3):** "heap-backed" splits into three distinct
> properties — heap-**written** (DDL emits heap tuples), heap-**recovered** (startup
> rebuilds from the heap), and heap-**read** (queries read the heap at runtime).
> Only **pg_type + pg_attribute** are heap-**read** today (`RegisterRealTable`,
> `open.go:2470/2484`). **pg_class** is heap-written + heap-recovered
> (`syncTableToCatalogHeap` / `loadUserTablesFromHeap`) but still virtual-**read**
> via an in-memory cache — i.e. pg_class is the **existing precedent for the
> cache-rebuilt-from-heap read model** this design adopts for the base catalogs
> (§4.2 step 4); pg_index is `Virtual:true` (`catalog.go:9281`).

| State | Catalogs |
| --- | --- |
| **Heap-read at runtime already (`RegisterRealTable`)** | pg_type, pg_attribute |
| **Heap-write + heap-recovery done, read still cached/virtual (the precedent)** | pg_class (+ pg_index writes) |
| **Base catalogs to convert (virtual-only today → heap)** | pg_namespace, pg_proc, pg_operator, pg_opclass, pg_opfamily, pg_amop, pg_amproc, pg_cast, pg_conversion, pg_collation, pg_range, pg_aggregate, pg_enum, pg_sequence, pg_language, pg_ts_dict/config/parser/template, pg_transform, pg_event_trigger, pg_publication(+rel/namespace), pg_subscription(+rel), pg_statistic_ext(+data), pg_constraint, pg_attrdef, pg_depend, pg_description, pg_init_privs; shared: pg_database, pg_authid, pg_auth_members, pg_tablespace, pg_foreign_server, pg_foreign_data_wrapper, pg_user_mapping |
| **Stay virtual (views / computed — no WAL in PG either)** | pg_roles, pg_stat_*, pg_locks, pg_settings, pg_prepared_*, pg_cursors, …; all `information_schema.*` |

## 6. Per-DB scoping resolution

The heap layout is already per-DB (`base/<dbOid>/<catOid>`), so this is additive
work, not a redesign:
- **Bootstrap every base catalog's indexes in every database** at `CREATE DATABASE`
  time (today non-default DBs skip catalog btree files, `operators_ddl.go:13111-13138`).
- Once every catalog is uniformly heap-backed with indexes in every DB, retire the
  **postgres-DB mirror shim** (`sys_catalog_postgres_db_mirror.go`,
  `mirrorTouchedCatalogsToPostgresDB`), which exists only because the model isn't
  uniform yet.

## 7. What is retired

- The **~102 bespoke `RecordKind` constants** (`internal/wal/recovery.go`) for
  Section B DDLs and their `Encode*`/`Decode*` — replaced by the heap/btree AM's
  existing records. (The file has **122** `RecordKind` constants total; ~20 are the
  heap/btree/xact/smgr/clog analogs — `HeapInsert`=4, `HeapDelete`=6, `BtreeInsert`=5,
  `XactCommit`=8, `Checkpoint`=2, `SmgrCreate`=11, `ClogTruncate`=33, … — which
  [Part A](01-record-content-parity.md) *rewrites*, not deletes.)
- **26 `*_ddl_recovery.go` scanners** (`internal/initdb/`) and their `replay*DDLRecords`
  wiring in `open.go` — replaced by one generic per-catalog heap-scan reload.
- **~50 base-catalog `VirtualRows` builders** (`internal/catalog/catalog.go`) —
  replaced by heap-backed reads / heap-rebuilt caches. (The ~40 view builders stay.)
- The `RmgrGoopgCatalog=128` custom rmgr becomes **unused** once no bespoke record
  remains — goopg then emits *only* real PG rmgrs, completing the header side of
  parity too.

## 8. Phased plan (each phase independently landable + gated)

1. **Enabler**: generalize `loadUserTablesFromHeap` into a per-catalog reload; wire
   catalog `XLOG_HEAP_UPDATE`; bootstrap base-catalog indexes in every DB (§6).
2. **High-leverage catalogs**: pg_namespace, pg_proc, pg_sequence (most-referenced;
   exercise INSERT + the sequence SMGR_CREATE + ALTER path).
3. **Type/operator families**: pg_type(non-composite)/enum/range, pg_operator,
   pg_opclass/opfamily/amop/amproc, pg_cast, pg_conversion, pg_collation, pg_aggregate.
4. **Extension/config catalogs**: pg_ts_*, pg_transform, pg_event_trigger,
   pg_publication*/subscription*, pg_statistic_ext, pg_constraint/attrdef/depend.
5. **Shared catalogs**: pg_database, pg_authid/auth_members, pg_tablespace,
   pg_foreign_*/user_mapping (global/ dir; extra care — cross-DB visibility).

Each phase converts a catalog's DDLs (encode→heap ops), swaps its read model,
deletes its bespoke record + scanner + virtual builder, then re-runs the full
regress + isolation suites and re-inits the data dir.

## 9. Performance: write amplification is PG parity, not regression

A PG-shaped `CREATE SCHEMA` emits several records (heap insert + N index inserts +
commit invals) where goopg emits one bespoke record today. This is **more WAL** —
but it is *exactly what PG writes*, so it is parity by definition, and it is DDL
(not a hot path). Two offsets: (a) FPI **hole removal** from
[doc 01](01-record-content-parity.md) §2 shrinks the dominant per-record cost
(full-page images) below today's hole-less FPIs; (b) building the record at the
source (no native→PG conversion) keeps the per-record CPU at one encode. The design
does **not** add a translation step — every catalog mutation writes the PG record
directly, honoring the "emit-at-source" principle.

## 10. Verification & risks (Section B)

- **Per catalog**: full regress + `internal/testport` isolation suites (catalog
  shape + DDL semantics are PG-compat surfaces — the project's costliest silent-
  regression class); `psql \d`/`\df`/`\dn` + `information_schema` parity vs PG 18.3;
  re-init the data dir (on-disk catalog format changes).
- **Recovery**: crash after each converted DDL, restart, assert the catalog object
  survives via the generic heap-scan reload (replaces the per-DDL scanner test).
- **Parity**: `pg_waldump --rmgr=Heap`/`Btree` decodes the catalog DDL records; a
  real PG 18 standby replays a goopg `CREATE SCHEMA`/`FUNCTION` and sees the catalog
  row (`TestE2E_FailoverGoopgToPG` extended with DDL).
- **Risks**: (R1) catalog read/write blast radius — stage per catalog, never batch;
  (R2) per-DB index bootstrap correctness (every catalog, every DB); (R3) ALTER →
  `XLOG_HEAP_UPDATE` for catalog relfiles (new emit path); (R4) shared-catalog
  cross-DB visibility (pg_database/pg_authid live in global/); (R5) startup reload
  ordering now implicit via OID refs + visibility — verify dependent objects
  (a function in a schema, an operator over a type) reload correctly.
