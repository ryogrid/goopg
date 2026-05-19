# 06 — BKI-Derived Catalog Seeds

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility); centralises the
spec for every `pg_*.dat` row goopg `initdb` must seed.

---

## Scope

This file specifies the **non-nailed local catalogs** that PG18 `initdb`
seeds from `src/include/catalog/pg_*.dat` (consumed by `genbki.pl` into
`postgres.bki`). They are *not* required for relcache Phase 3 (the
nailed-rel bootstrap path documented in `05-`), but they are required
for almost every non-trivial backend operation: operator resolution,
type conversion, index strategy lookup, schema-qualified name
resolution, and language dispatch.

In scope (one subsection each in the per-catalog detail):

- `pg_am` — access-method directory (7 rows).
- `pg_amop` — operator-strategy mapping per opfamily (945 rows;
  load-bearing **cross-type** rows for integer / text / datetime).
- `pg_amproc` — support-function mapping per opfamily (714 rows;
  load-bearing **cross-type cmp** rows for the same families).
- `pg_opclass` — operator-class directory (177 rows).
- `pg_opfamily` — operator-family directory (146 rows).
- `pg_cast` — type cast pairs (235 rows).
- `pg_collation` — collation directory (7 seed rows; `initdb` adds
  ~800 ICU/libc collations at SQL phase).
- `pg_conversion` — encoding conversions (128 rows).
- `pg_aggregate` — aggregate functions (161 rows).
- `pg_range` — built-in range types (6 rows; 6 corresponding
  multirange rows in `pg_type`).
- `pg_namespace` — initial schemas (3 BKI rows + `information_schema`).
- `pg_language` — internal / c / sql / plpgsql.
- `pg_operator` — operator catalogue (799 rows).
- `pg_proc` / `pg_type` — the residual rows not nailed by `05-`
  (3397 procs, 112 base types, plus derived array/multirange/
  row-of-composite types written by `genbki.pl`).

Out of scope:

- Nailed pg_class / pg_attribute / pg_type / pg_proc rows for the
  four nailed local rels — see `05-local-catalog-bootstrap.md`.
- Shared catalogs — see `04-shared-catalog-bootstrap.md`.
- System-view rewrite rules — see `07-system-views-and-pg-rewrite.md`.
- The `pg_internal.init` byte layout that pins descriptors for these
  catalogs — see `08-relcache-init-and-version-files.md`.

---

## Upstream references

| Symbol | File:line |
|---|---|
| BKI bootstrap entry | `src/backend/bootstrap/bootstrap.c:303` |
| `genbki.pl` row emitter | `src/backend/catalog/genbki.pl:1` |
| `pg_am.dat` (7 rows) | `src/include/catalog/pg_am.dat:14` |
| `pg_amop.dat` (945 rows) | `src/include/catalog/pg_amop.dat:14` |
| `pg_amproc.dat` (714 rows) | `src/include/catalog/pg_amproc.dat:14` |
| `pg_opclass.dat` (177 rows) | `src/include/catalog/pg_opclass.dat:14` |
| `pg_opfamily.dat` (146 rows) | `src/include/catalog/pg_opfamily.dat:14` |
| `pg_cast.dat` (235 rows) | `src/include/catalog/pg_cast.dat:14` |
| `pg_collation.dat` (7 rows) | `src/include/catalog/pg_collation.dat:14` |
| `pg_conversion.dat` (128 rows) | `src/include/catalog/pg_conversion.dat:14` |
| `pg_aggregate.dat` (161 rows) | `src/include/catalog/pg_aggregate.dat:14` |
| `pg_range.dat` (6 rows) | `src/include/catalog/pg_range.dat:14` |
| `pg_namespace.dat` (3 rows) | `src/include/catalog/pg_namespace.dat:14` |
| `pg_language.dat` (3 rows) | `src/include/catalog/pg_language.dat:14` |
| `pg_operator.dat` (799 rows) | `src/include/catalog/pg_operator.dat:14` |
| `pg_proc.dat` (3397 rows) | `src/include/catalog/pg_proc.dat:14` |
| `pg_type.dat` (112 rows) | `src/include/catalog/pg_type.dat:14` |
| Syscache table `cacheinfo[]` | `src/backend/utils/cache/syscache.c:82` |
| `MAKE_SYSCACHE(AMOPSTRATEGY, …)` | `src/include/catalog/pg_amop.h:94` |
| `MAKE_SYSCACHE(AMPROCNUM, …)` | `src/include/catalog/pg_amproc.h:73` |
| `MAKE_SYSCACHE(CLAOID, …)` | `src/include/catalog/pg_opclass.h:89` |
| `MAKE_SYSCACHE(CASTSOURCETARGET, …)` | `src/include/catalog/pg_cast.h:62` |
| `MAKE_SYSCACHE(COLLOID, …)` | `src/include/catalog/pg_collation.h:66` |
| `MAKE_SYSCACHE(CONVOID, …)` | `src/include/catalog/pg_conversion.h:69` |
| `MAKE_SYSCACHE(NAMESPACEOID, …)` | `src/include/catalog/pg_namespace.h:60` |
| `MAKE_SYSCACHE(LANGOID, …)` | `src/include/catalog/pg_language.h:73` |
| `MAKE_SYSCACHE(OPEROID, …)` | `src/include/catalog/pg_operator.h:88` |
| `MAKE_SYSCACHE(AMOID, …)` | `src/include/catalog/pg_am.h:54` |
| `MAKE_SYSCACHE(OPFAMILYAMNAMENSP, …)` | `src/include/catalog/pg_opfamily.h:56` |
| `get_op_btree_interpretation` | `src/backend/utils/cache/lsyscache.c:530` |
| `DefineOpClass` | `src/backend/commands/opclasscmds.c:333` |
| `DefineOpFamily` | `src/backend/commands/opclasscmds.c:772` |
| `storeOperators` | `src/backend/commands/opclasscmds.c:1454` |
| `storeProcedures` | `src/backend/commands/opclasscmds.c:1584` |
| `CastCreate` | `src/backend/catalog/pg_cast.c:49` |
| `CreateCast` | `src/backend/commands/functioncmds.c:1539` |
| `CollationCreate` | `src/backend/catalog/pg_collation.c:42` |
| `DefineCollation` | `src/backend/commands/collationcmds.c:53` |
| `ConversionCreate` | `src/backend/catalog/pg_conversion.c:38` |
| `CreateConversionCommand` | `src/backend/commands/conversioncmds.c:32` |
| `AggregateCreate` | `src/backend/catalog/pg_aggregate.c:46` |
| `DefineAggregate` | `src/backend/commands/aggregatecmds.c:53` |
| `RangeCreate` | `src/backend/catalog/pg_range.c:36` |
| `DefineRange` | `src/backend/commands/typecmds.c:1380` |
| `NamespaceCreate` | `src/backend/catalog/pg_namespace.c:43` |
| `CreateSchemaCommand` | `src/backend/commands/schemacmds.c:52` |
| `OperatorCreate` | `src/backend/catalog/pg_operator.c:321` |
| `DefineOperator` | `src/backend/commands/operatorcmds.c:67` |
| `CreateProceduralLanguage` | `src/backend/commands/proclang.c:37` |

---

## Initdb-time output

The BKI bootstrap backend (`bootstrap.c:303`) inserts each `.dat` row
into the corresponding heap with `heap_insert` and, for every
catalog that carries `MAKE_SYSCACHE` declarations, into its critical
unique indexes. goopg's `initdb` must reproduce the same heap layout
(PG-native `HeapTupleHeader` + `Form_pg_*` payload) at the same
`base/{1,5}/<relfilenode>` paths, plus the corresponding btree
metapages + leaf pages for each `DECLARE_UNIQUE_INDEX`.

### Source-file inventory

| Catalog | Rel OID | `.dat` source | Row count at initdb | Citation |
|---|---|---|---|---|
| `pg_am` | 2601 | `pg_am.dat` | 7 | `pg_am.dat:14`; `pg_am.h:51` (`AccessMethodRelationId`) |
| `pg_amop` | 2602 | `pg_amop.dat` | 945 | `pg_amop.dat:14`; `pg_amop.h:38` |
| `pg_amproc` | 2603 | `pg_amproc.dat` | 714 | `pg_amproc.dat:14`; `pg_amproc.h:34` |
| `pg_opclass` | 2616 | `pg_opclass.dat` | 177 | `pg_opclass.dat:14`; `pg_opclass.h:35` |
| `pg_opfamily` | 2753 | `pg_opfamily.dat` | 146 | `pg_opfamily.dat:14`; `pg_opfamily.h:36` |
| `pg_cast` | 2605 | `pg_cast.dat` | 235 | `pg_cast.dat:14`; `pg_cast.h:34` |
| `pg_collation` | 3456 | `pg_collation.dat` | 7 | `pg_collation.dat:14`; `pg_collation.h:36` |
| `pg_conversion` | 2607 | `pg_conversion.dat` | 128 | `pg_conversion.dat:14`; `pg_conversion.h:35` |
| `pg_aggregate` | 2600 | `pg_aggregate.dat` | 161 | `pg_aggregate.dat:14`; `pg_aggregate.h:38` |
| `pg_range` | 3541 | `pg_range.dat` | 6 | `pg_range.dat:14`; `pg_range.h:35` |
| `pg_namespace` | 2615 | `pg_namespace.dat` | 3 | `pg_namespace.dat:14`; `pg_namespace.h:35` |
| `pg_language` | 2612 | `pg_language.dat` | 3 (+ plpgsql at SQL phase) | `pg_language.dat:14`; `pg_language.h:35` |
| `pg_operator` | 2617 | `pg_operator.dat` | 799 | `pg_operator.dat:14`; `pg_operator.h:34` |
| `pg_proc` | 1255 | `pg_proc.dat` | 3397 | `pg_proc.dat:14` (nailed, see `05-`) |
| `pg_type` | 1247 | `pg_type.dat` | 112 + derived array/multirange | `pg_type.dat:14` (nailed, see `05-`) |

### Per-catalog detail

#### pg_am — 2601 (rowtype 11671)

7 rows (`pg_am.dat`): `heap` (2), `btree` (403), `hash` (405), `gist`
(783), `gin` (2742), `spgist` (4000), `brin` (3580). Indexes:
`pg_am_oid_index` (2652, PKEY), `pg_am_name_index` (2651). Syscaches
`AMOID` (`pg_am.h:54`), `AMNAME` (`pg_am.h:55`).

#### pg_amop — 2602 (rowtype 11672)

945 rows. Indexes: `pg_amop_fam_strat_index` (2653,
`(amopfamily, amoplefttype, amoprighttype, amopstrategy)`),
`pg_amop_opr_fam_index` (2654, `(amopopr, amoppurpose, amopfamily)`),
`pg_amop_oid_index` (2756, PKEY). Syscaches `AMOPSTRATEGY` and
`AMOPOPID` (`pg_amop.h:94-95`). Canonical OID landmarks: 7 (btree
int4 `<`-strategy in family 1976), 410 (btree int8 `=` operator
mapped at strategy 3).

#### pg_amproc — 2603 (rowtype 11673)

714 rows. Indexes: `pg_amproc_fam_proc_index` (2655) and
`pg_amproc_oid_index` (2757, PKEY). Syscache `AMPROCNUM`
(`pg_amproc.h:73`). Comparison-support rows are the load-bearing
ones for index scans — `RelationInitIndexAccessInfo` looks up
`amprocnum=1` per `(opfamily, lefttype, righttype)` and stores
the proc OID in the relcache opclass entry; missing rows cause
a `cache lookup failed for support function` PANIC on first scan.

#### pg_opclass — 2616 (rowtype 11674)

177 rows. Indexes: `pg_opclass_am_name_nsp_index` (2686), and
`pg_opclass_oid_index` (2687, PKEY). Syscaches `CLAAMNAMENSP`
(`pg_opclass.h:88`) and `CLAOID` (`pg_opclass.h:89`). Landmarks:
1978 (`int4_ops`), 1981 (`oid_ops`), 1986 (`name_ops`,
`opckeytype=2275 cstring`), 3126 (`text_ops`).

#### pg_opfamily — 2753 (rowtype 11675)

146 rows. Indexes: `pg_opfamily_am_name_nsp_index` (2754),
`pg_opfamily_oid_index` (2755, PKEY). Syscache `OPFAMILYAMNAMENSP`
(`pg_opfamily.h:56`). Landmarks: 1976 (`INTEGER_BTREE_FAM_OID`),
1988 (`numeric_ops` btree), 1994 (`TEXT_BTREE_FAM_OID`), 434
(`datetime_ops` btree), 1989 (`OID_BTREE_FAM_OID`).

#### pg_cast — 2605 (rowtype 11676)

235 rows. Indexes: `pg_cast_oid_index` (2660, PKEY) and
`pg_cast_source_target_index` (2661). Syscache `CASTSOURCETARGET`
(`pg_cast.h:62`). Six columns: `oid, castsource, casttarget,
castfunc, castcontext, castmethod` — pinned by the goopg unit
test `pg_cast_nailed_test.go`.

#### pg_collation — 3456 (rowtype 11677)

7 BKI rows: `default` (100), `C` (950), `POSIX` (951),
`ucs_basic` (962), `unicode` (963), `pg_c_utf8` (811),
`pg_unicode_fast` (6411). Indexes: `pg_collation_name_enc_nsp_index`
(3164) and `pg_collation_oid_index` (3085, PKEY). Syscaches
`COLLNAMEENCNSP` (`pg_collation.h:65`), `COLLOID`
(`pg_collation.h:66`).

`initdb`'s SQL phase (`src/bin/initdb/initdb.c:1690`) then runs
`pg_import_system_collations(pg_catalog)` which adds ~800 libc and
ICU collations sourced from the host's locale database. goopg must
seed at least the 7 BKI rows at bootstrap; the libc/ICU enumeration
is a runtime concern (initdb-time on the goopg primary, not at
standby attach).

#### pg_conversion — 2607 (rowtype 11678)

128 rows. Indexes: `pg_conversion_default_index` (2668),
`pg_conversion_name_nsp_index` (2669) and `pg_conversion_oid_index`
(2670, PKEY). Syscaches `CONDEFAULT` (`pg_conversion.h:68`),
`CONNAMENSP` (no `MAKE_SYSCACHE`), `CONVOID` (`pg_conversion.h:69`).
8 columns pinned by `pg_conversion_nailed_test.go`.

#### pg_aggregate — 2600 (rowtype 11680)

161 rows, 22 columns. Index: `pg_aggregate_fnoid_index` (2650, PKEY).
Syscache `AGGFNOID` (`pg_aggregate.h:152`). Every row references a
`pg_proc` row via `aggfnoid` (regproc).

#### pg_range — 3541 (rowtype 11679)

6 rows: `int4range/int4multirange`, `numrange/nummultirange`,
`tsrange/tsmultirange`, `tstzrange/tstzmultirange`,
`daterange/datemultirange`, `int8range/int8multirange`. Indexes:
`pg_range_rngtypid_index` (3542, PKEY) and
`pg_range_rngmultitypid_index` (2228). Each row pulls in two
`pg_type` rows (the range type + the multirange type) and two
`pg_proc` support rows (`rngcanonical`, `rngsubdiff`).

#### pg_namespace — 2615 (rowtype 11681)

3 BKI rows: `pg_catalog` (11), `pg_toast` (99), `public` (2200).
Indexes: `pg_namespace_nspname_index` (2684) and
`pg_namespace_oid_index` (2685, PKEY). Syscaches `NAMESPACENAME`
(`pg_namespace.h:59`), `NAMESPACEOID` (`pg_namespace.h:60`).
The SQL phase adds `information_schema` (OID 13183 on a stock
build, but dynamically assigned) plus its ~70 views.

#### pg_language — 2612 (rowtype 11682)

3 BKI rows: `internal` (12), `c` (13), `sql` (14). Indexes:
`pg_language_name_index` (2681), `pg_language_oid_index` (2682,
PKEY). Syscaches `LANGNAME` (`pg_language.h:72`), `LANGOID`
(`pg_language.h:73`). The SQL phase adds `plpgsql` (OID 13568)
through `CREATE EXTENSION plpgsql`.

#### pg_operator — 2617 (rowtype 11683)

799 rows. Indexes: `pg_operator_oid_index` (2688, PKEY) and
`pg_operator_oprname_l_r_n_index` (2689,
`(oprname, oprleft, oprright, oprnamespace)`). Syscaches
`OPERNAMENSP` (`pg_operator.h:87`), `OPEROID`
(`pg_operator.h:88`). Every row referenced by a `pg_amop.amopopr`
must exist or `get_op_btree_interpretation`
(`lsyscache.c:530`) returns an empty strategy list and the
planner cannot push the qualification down.

#### Non-nailed pg_proc / pg_type rows

`pg_proc.dat` has 3397 entries; `05-` only nails the rows the
relcache touches at boot (the AM handlers and a handful of
syscache-driven procs). The residual ~3380 entries — every
builtin function, every aggregate transition function, every
range support proc, every cast function — must be seeded here.
Similarly `pg_type.dat` carries 112 base types; `genbki.pl`
auto-generates the matching array types (one per row with
`array_type_oid`), the 6 multirange types, and the row types
for every catalog (`pg_class_d.h:CLASS_RELTYPE_OID = 83`, etc.).
Total written by initdb: 612 `pg_type` rows on a stock PG18
cluster (`SELECT count(*) FROM pg_type` after fresh initdb).

### Cross-type opfamily rows — the M0106 step 3h surface

`pg_amop` ships ~140 cross-type strategy rows for the four pinned
btree families. Without them, an index scan that compares across a
type boundary (e.g. `WHERE int4col = $1::int2`) fails strategy
resolution: `get_op_btree_interpretation` returns `NIL` because no
`(amopopr, amopfamily)` row exists in the
`AMOPOPID` syscache, the planner falls back to a sequential scan,
and any backend that re-checks the strategy directly (e.g.
`partition pruning`, `ECs that synthesise an indexqual`) panics
with `no operator found for ... in family ...`.

| Family | OID | Cross-type pair | Strategy | Operator OID | Citation |
|---|---|---|---|---|---|
| integer_ops | 1976 | int2/int4 | 1..5 | 534, 540, 532, 542, 536 | `pg_amop.dat:34` |
| integer_ops | 1976 | int2/int8 | 1..5 | 1864, 1866, 1862, 1867, 1865 | `pg_amop.dat:50` |
| integer_ops | 1976 | int4/int2 | 1..5 | 535, 541, 533, 543, 537 | `pg_amop.dat:90` |
| integer_ops | 1976 | int4/int8 | 1..5 | 37, 80, 15, 82, 76 | `pg_amop.dat:107` |
| integer_ops | 1976 | int8/int2 | 1..5 | 1870, 1872, 1868, 1873, 1871 | `pg_amop.dat:154` |
| integer_ops | 1976 | int8/int4 | 1..5 | 418, 420, 416, 430, 419 | `pg_amop.dat:170` |
| text_ops | 1994 | name/text | 1..5 | 660, 661, 254, 663, 662 | `pg_amop.dat:340` |
| text_ops | 1994 | text/name | 1..5 | 2972, 2973, 254, 2975, 2974 | `pg_amop.dat:356` |
| datetime_ops | 434 | date/timestamp | 1..5 | 2345, 2346, 2347, 2348, 2349 | `pg_amop.dat:520` |
| datetime_ops | 434 | date/timestamptz | 1..5 | 2358, 2359, 2360, 2361, 2362 | `pg_amop.dat:536` |
| datetime_ops | 434 | timestamp/date | 1..5 | 2371, 2372, 2373, 2374, 2375 | `pg_amop.dat:570` |
| datetime_ops | 434 | timestamp/timestamptz | 1..5 | 2534, 2535, 2536, 2537, 2538 | `pg_amop.dat:586` |
| datetime_ops | 434 | timestamptz/date | 1..5 | 2384, 2385, 2386, 2387, 2388 | `pg_amop.dat:620` |
| datetime_ops | 434 | timestamptz/timestamp | 1..5 | 2540, 2541, 2542, 2543, 2544 | `pg_amop.dat:636` |
| numeric_ops | 1988 | numeric/numeric | 1..5 | 1754, 1755, 1752, 1757, 1756 | `pg_amop.dat:420` |

*Note* — `btree/numeric_ops` (1988) does **not** have cross-type
rows in `pg_amop.dat`; PG's planner handles int↔numeric conversions
via the implicit-cast path. The numeric row above is the
self-typed self-strategy row, included here only to anchor the
family OID for the verification queries below.

`pg_amproc` carries the matching cross-type comparison functions:

| Family | OID | Cross-type pair | amprocnum | Proc OID | Citation |
|---|---|---|---|---|---|
| integer_ops | 1976 | int2/int4 | 1 | 2190 (`btint24cmp`) | `pg_amproc.dat:50` |
| integer_ops | 1976 | int2/int8 | 1 | 2192 (`btint28cmp`) | `pg_amproc.dat:54` |
| integer_ops | 1976 | int4/int2 | 1 | 2191 (`btint42cmp`) | `pg_amproc.dat:58` |
| integer_ops | 1976 | int4/int8 | 1 | 2188 (`btint48cmp`) | `pg_amproc.dat:62` |
| integer_ops | 1976 | int8/int2 | 1 | 2193 (`btint82cmp`) | `pg_amproc.dat:66` |
| integer_ops | 1976 | int8/int4 | 1 | 2189 (`btint84cmp`) | `pg_amproc.dat:70` |
| text_ops | 1994 | name/text | 1 | 2417 (`btnametextcmp`) | `pg_amproc.dat:180` |
| text_ops | 1994 | text/name | 1 | 2418 (`bttextnamecmp`) | `pg_amproc.dat:184` |
| datetime_ops | 434 | date/timestamp | 1 | 2344 (`date_cmp_timestamp`) | `pg_amproc.dat:220` |
| datetime_ops | 434 | date/timestamptz | 1 | 2357 (`date_cmp_timestamptz`) | `pg_amproc.dat:224` |
| datetime_ops | 434 | timestamp/date | 1 | 2370 (`timestamp_cmp_date`) | `pg_amproc.dat:228` |
| datetime_ops | 434 | timestamp/timestamptz | 1 | 2533 (`timestamp_cmp_timestamptz`) | `pg_amproc.dat:232` |
| datetime_ops | 434 | timestamptz/date | 1 | 2383 (`timestamptz_cmp_date`) | `pg_amproc.dat:236` |
| datetime_ops | 434 | timestamptz/timestamp | 1 | 2526 (`timestamptz_cmp_timestamp`) | `pg_amproc.dat:240` |

### Catalog cache pinning

The syscache table at `src/backend/utils/cache/syscache.c:82`
(`cacheinfo[]`) materialises one `CatCache` per `MAKE_SYSCACHE`
declaration. Every entry in the table requires both the catalog
heap **and** the unique index named in the macro to exist at
startup — the cache is built lazily on first lookup
(`syscache.c:122`) but the `Assert(OidIsValid(cacheinfo[cacheId].
indoid))` invariant fires immediately when the index OID is
unknown, i.e. before goopg has populated `pg_class`. Concretely,
goopg must seed at least:

- `pg_am_oid_index` 2652 — `AMOID` cache (`pg_am.h:54`).
- `pg_amop_fam_strat_index` 2653 — `AMOPSTRATEGY` (`pg_amop.h:94`).
- `pg_amop_opr_fam_index` 2654 — `AMOPOPID` (`pg_amop.h:95`).
- `pg_amproc_fam_proc_index` 2655 — `AMPROCNUM` (`pg_amproc.h:73`).
- `pg_opclass_oid_index` 2687 — `CLAOID` (`pg_opclass.h:89`).
- `pg_cast_source_target_index` 2661 — `CASTSOURCETARGET`
  (`pg_cast.h:62`).
- `pg_collation_oid_index` 3085 — `COLLOID` (`pg_collation.h:66`).
- `pg_conversion_oid_index` 2670 — `CONVOID` (`pg_conversion.h:69`).
- `pg_namespace_oid_index` 2685 — `NAMESPACEOID`
  (`pg_namespace.h:60`).
- `pg_language_oid_index` 2682 — `LANGOID` (`pg_language.h:73`).
- `pg_operator_oid_index` 2688 — `OPEROID` (`pg_operator.h:88`).
- `pg_opfamily_am_name_nsp_index` 2754 — `OPFAMILYAMNAMENSP`
  (`pg_opfamily.h:56`).

---

## Continuous maintenance

After initdb, every operation that touches one of these catalogs
runs through a `Cmd*` / `*Create` API in `src/backend/commands/`
or `src/backend/catalog/`. The API path is responsible for:

1. inserting the heap tuple (`CatalogTupleInsert`,
   `src/backend/catalog/indexing.c:227`);
2. updating every relevant unique index (`CatalogIndexInsert`,
   `indexing.c:140`);
3. emitting the appropriate `XLOG_HEAP_INSERT` (or
   `XLOG_HEAP_DELETE` / `XLOG_HEAP_UPDATE`) record;
4. emitting a catcache invalidation through
   `CacheInvalidateHeapTuple` (`src/backend/utils/cache/inval.c:1419`).

goopg's DDL executor must mirror these four steps for every
operation listed below.

### User-DDL rules

| DDL | Affected heaps | Affected indexes | Driver | Citation |
|---|---|---|---|---|
| `CREATE OPERATOR CLASS` | pg_opclass, pg_amop, pg_amproc, sometimes pg_opfamily | matching `_oid_index` + `_fam_*_index` | `DefineOpClass` → `storeOperators`/`storeProcedures` | `opclasscmds.c:333`, `:1454`, `:1584` |
| `CREATE OPERATOR FAMILY` | pg_opfamily | `pg_opfamily_am_name_nsp_index`, `pg_opfamily_oid_index` | `DefineOpFamily` | `opclasscmds.c:772` |
| `ALTER OPERATOR FAMILY ADD …` | pg_amop, pg_amproc | `fam_strat_index`, `fam_proc_index` | `AlterOpFamily` | `opclasscmds.c:870` |
| `CREATE CAST` | pg_cast | `pg_cast_oid_index`, `pg_cast_source_target_index` | `CreateCast` → `CastCreate` | `functioncmds.c:1539`, `pg_cast.c:49` |
| `CREATE COLLATION` | pg_collation | `pg_collation_name_enc_nsp_index`, `pg_collation_oid_index` | `DefineCollation` → `CollationCreate` | `collationcmds.c:53`, `pg_collation.c:42` |
| `CREATE CONVERSION` | pg_conversion | all three `pg_conversion_*_index` | `CreateConversionCommand` → `ConversionCreate` | `conversioncmds.c:32`, `pg_conversion.c:38` |
| `CREATE AGGREGATE` | pg_aggregate, pg_proc | `pg_aggregate_fnoid_index`, `pg_proc_oid_index`, `pg_proc_proname_args_nsp_index` | `DefineAggregate` → `AggregateCreate` | `aggregatecmds.c:53`, `pg_aggregate.c:46` |
| `CREATE TYPE … AS RANGE` | pg_range, pg_type (range + multirange), pg_proc (canonical/subdiff/constructors), pg_cast (constructor casts) | matching `_oid_index` for each, plus `pg_range_rngtypid_index` (3542) and `pg_range_rngmultitypid_index` (2228) | `DefineRange` → `RangeCreate` | `typecmds.c:1380`, `pg_range.c:36` |
| `CREATE SCHEMA` | pg_namespace | `pg_namespace_nspname_index`, `pg_namespace_oid_index` | `CreateSchemaCommand` → `NamespaceCreate` | `schemacmds.c:52`, `pg_namespace.c:43` |
| `DROP SCHEMA` | pg_namespace + cascade graph | same | `RemoveObjects` | `dropcmds.c:46` |
| `CREATE OPERATOR` | pg_operator | `pg_operator_oid_index`, `pg_operator_oprname_l_r_n_index` | `DefineOperator` → `OperatorCreate` | `operatorcmds.c:67`, `pg_operator.c:321` |
| `CREATE LANGUAGE` | pg_language, pg_proc (handler/validator/inline) | `pg_language_name_index`, `pg_language_oid_index` | `CreateProceduralLanguage` | `proclang.c:37` |

### WAL records

Each of the heap mutations above emits one of:

- `XLOG_HEAP_INSERT` (`src/backend/access/heap/heapam.c:2099`) — new
  tuple. RM `RM_HEAP_ID`, info `XLOG_HEAP_INSERT`.
- `XLOG_HEAP_UPDATE` (`heapam.c:3303`) — in-place rewrite (e.g.
  `ALTER` paths).
- `XLOG_HEAP_DELETE` (`heapam.c:2796`) — `DROP` paths.
- `XLOG_BTREE_INSERT_LEAF` / `XLOG_BTREE_INSERT_UPPER` for every
  unique-index insertion (`src/backend/access/nbtree/nbtxlog.c`).

For the system catalogs in this doc set the records are always
emitted under `RelationIsLogicallyLogged() == false`; they are
physical-only and replayed verbatim by the standby's startup
process. There is no DDL-specific WAL record class — every mutation
is a plain heap/btree write.

### Cache invalidation

`CacheInvalidateHeapTuple` (`inval.c:1419`) consults the static
table `RelationGetSyscacheList` to decide which catcaches to
invalidate per `(reloid, tuple)`. The relevant entries are:

| Catalog | Invalidated catcaches |
|---|---|
| pg_am | `AMOID`, `AMNAME` |
| pg_amop | `AMOPSTRATEGY`, `AMOPOPID` |
| pg_amproc | `AMPROCNUM` |
| pg_opclass | `CLAAMNAMENSP`, `CLAOID` |
| pg_opfamily | `OPFAMILYAMNAMENSP`, `OPFAMILYOID` |
| pg_cast | `CASTSOURCETARGET` |
| pg_collation | `COLLNAMEENCNSP`, `COLLOID` |
| pg_conversion | `CONDEFAULT`, `CONNAMENSP`, `CONVOID` |
| pg_aggregate | `AGGFNOID` |
| pg_range | `RANGETYPE`, `RANGEMULTIRANGE` |
| pg_namespace | `NAMESPACENAME`, `NAMESPACEOID` |
| pg_language | `LANGNAME`, `LANGOID` |
| pg_operator | `OPERNAMENSP`, `OPEROID` |

A standby that has already loaded its `pg_internal.init` relies on
the WAL replayer's `xact_redo_commit` (`src/backend/access/transam/
xact.c:6232`) to re-issue the same invalidation messages so the
standby's catcaches don't return stale rows after a primary DDL.
goopg must emit identical invalidation messages when its own DDL
path runs, otherwise vanilla-PG standbys attached to a goopg
primary will see stale `pg_class`/`pg_amop`/… rows.

---

## What goopg must produce

Source of truth: the files under `internal/initdb/` referenced in the
file list at the top of this task. Status is taken from the current
seed-function bodies and their unit tests.

| Catalog | Status | Notes |
|---|---|---|
| pg_am | `done` | 7-row inventory pinned by `pg_am_bootstrap_test.go::TestPgAmInitialEntriesCoverPg18Defaults`. `bootstrapPgAmTuples` writes `base/{1,5}/2601`. |
| pg_amop | `partial` | `pgAmopInitialEntries` (`initdb.go:2029`) emits **85** rows for btree families 1976/1989/1994/2095/424/429/1991/2097 — missing 860 rows vs PG (945 total). Includes int family cross-type rows; missing the text-cross, datetime-cross, hash, gist, gin, brin, spgist tail. |
| pg_amproc | `partial` | `pgAmprocInitialEntries` (`initdb.go:2191`) emits **36** rows for the same btree families. Missing cross-type cmp procs for text and datetime, all support functions for hash / gist / gin / spgist / brin (PG has 714 rows total). |
| pg_opclass | `partial` | 12 btree rows seeded (`pgOpclassInitialEntries`, `initdb.go:1904`); 165 more rows for hash, gist, gin, spgist, brin remain unsewn. |
| pg_opfamily | `missing` | No `pgOpfamilyInitialEntries` function exists; only the nailed pg_class row (`pg_opfamily_nailed_test.go`) is in place. PG18 ships 146 families. |
| pg_cast | `missing` | `nailedLocalRels` contains pg_cast (OID 2605) but `base/{1,5}/2605` is an empty page (`pg_cast_nailed_test.go`). All 235 rows unsewn. |
| pg_collation | `missing` | Only the nailed pg_class row exists. 7 BKI rows + the SQL-phase libc/ICU enumeration are not yet emitted. |
| pg_conversion | `missing` | `pg_conversion_nailed_test.go` only checks the nailed-rel descriptor. 128 rows unsewn. |
| pg_aggregate | `missing` | Only the nailed pg_class row is in place. 161 rows unsewn. |
| pg_range | `missing` | `pg_range_nailed_test.go` verifies the empty heap; 6 BKI rows (and the matching 6 multirange entries in pg_type) unsewn. |
| pg_namespace | `done` | `bootstrapPgNamespaceTuples` writes the 3 BKI rows; `information_schema` (SQL-phase) is correctly out of scope for the standby's pre-attach state. |
| pg_language | `missing` | `pg_language_*_index` tests cover the index shape but no heap rows are emitted. |
| pg_operator | `missing` | 799 rows unsewn. Tests cover only the index shape. Missing operator rows break `get_op_btree_interpretation` because every `pg_amop.amopopr` must dereference a real `pg_operator` OID. |
| pg_proc (non-nailed) | `partial` | `bootstrapPgProcTuples` (`initdb.go:1849`) emits 7 AM-handler rows. The other ~3390 rows referenced by pg_amproc, pg_aggregate, pg_cast, pg_range, pg_proc views are missing. |
| pg_type (derived) | `partial` | `bootstrapPgTypeTuples` writes the nailed-rel rowtypes. Array types, multirange types, and the 6 range types from pg_range are missing. |

### Cross-type opfamily rows — specific gaps

Against the cross-type table above, the following pairs are
**present** in `pgAmopInitialEntries()` / `pgAmprocInitialEntries()`
today: every integer pair (int2/int4, int2/int8, int4/int2,
int4/int8, int8/int2, int8/int4) for both amop strategies 1..5
and amproc num=1. That subset is complete (`pg_amop_bootstrap_test.go:104-109`,
`pg_amproc_bootstrap_test.go:222-227`).

The following pairs are **missing** in goopg's seed today:

- `text_ops` 1994: name/text and text/name (10 amop rows + 2 amproc
  rows). Required when a column declared as `name` is compared with
  a `text` literal in a system view — `pg_namespace`'s `nspname`
  column is the canonical trigger.
- `datetime_ops` 434: date/timestamp, date/timestamptz,
  timestamp/date, timestamp/timestamptz, timestamptz/date,
  timestamptz/timestamp (30 amop rows + 6 amproc rows). PG's
  `pg_stat_activity` view filters by `backend_start <= now()`
  which generates a date/timestamptz strategy lookup.
- `numeric_ops` 1988: no cross-type rows in `pg_amop.dat`, but the
  self-typed numeric row (and amproc cmp `1769`) is still missing
  in goopg because the family was never wired into
  `pgOpclassInitialEntries`. M0106 step 3h fires here on any
  numeric index scan.

The integer-family cross-type rows already in goopg are correct;
the M0106 step 3h surface lives in the text / datetime / numeric
families above.

---

## Verification

1. **Row-count parity** — after a fresh `goopg initdb`, the
   primary should answer the same numbers as a vanilla `pg_initdb`:

   ```sql
   SELECT relname, n FROM (
     VALUES ('pg_am',7), ('pg_amop',945), ('pg_amproc',714),
            ('pg_opclass',177), ('pg_opfamily',146),
            ('pg_cast',235), ('pg_conversion',128),
            ('pg_aggregate',161), ('pg_range',6),
            ('pg_language',4),     -- includes plpgsql
            ('pg_operator',799),   -- BKI only; SQL phase doesn't add ops
            ('pg_namespace',4)     -- + information_schema
   ) AS expected(relname,n)
   JOIN pg_class c USING (relname)
   WHERE (SELECT count(*) FROM pg_class WHERE relname=expected.relname) <> n;
   -- Should return zero rows on a freshly bootstrapped goopg cluster.
   ```

2. **Byte-diff vs vanilla** — compare each catalog's relfilenode
   contents under both database OIDs:

   ```bash
   pg_filedump -i -f base/1/2602 > goopg.txt
   pg_filedump -i -f /tmp/vanilla/base/1/2602 > pg.txt
   diff goopg.txt pg.txt   # MUST be empty (modulo line pointer offsets)
   ```

   Run for every relfilenode in the inventory table: 2601 / 2602 /
   2603 / 2616 / 2753 / 2605 / 3456 / 2607 / 2600 / 3541 / 2615 /
   2612 / 2617.

3. **Cross-type spot-check** — every operator the standby's
   catalog cache touches during boot must resolve:

   ```sql
   -- Integer family.
   SELECT amopfamily, amoplefttype::regtype, amoprighttype::regtype,
          amopstrategy, amopopr::regoperator
   FROM   pg_amop
   WHERE  amopfamily = 1976 AND amoplefttype <> amoprighttype
   ORDER  BY amoplefttype, amoprighttype, amopstrategy;
   -- Expect 30 rows (6 cross pairs × 5 strategies).

   -- Text family.
   SELECT count(*) FROM pg_amop
   WHERE amopfamily = 1994 AND amoplefttype <> amoprighttype;
   -- Expect 10.

   -- Datetime family.
   SELECT count(*) FROM pg_amop
   WHERE amopfamily = 434 AND amoplefttype <> amoprighttype;
   -- Expect 30.
   ```

4. **Operator coverage** — `\do` from psql lists ~700 operators on
   stock PG18. On goopg the count must match before any vanilla-PG
   standby attaches:

   ```bash
   psql -At -c "SELECT count(*) FROM pg_operator" goopg
   # Expect 799 (BKI seed; SQL phase adds none).
   psql -At -c "SELECT count(*) FROM pg_type"      goopg
   # Expect 612 (112 BKI base + 500 derived array / multirange / rowtype).
   ```

5. **Standby boot drill** — once 1–4 pass, `TestE2E_FailoverGoopgToPG/async`
   must reach the WAL-streaming stage without any `cache lookup failed
   for opclass`, `for operator`, `for support function` or `could not
   open relation with OID` errors. The closing assertion is
   `pg_isready -h <standby>` returning `accepting connections` while
   `pg_stat_wal_receiver.status = 'streaming'`.
