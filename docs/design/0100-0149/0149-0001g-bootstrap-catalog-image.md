# M0149\-0001g — goopg's bootstrap catalog image equals a PG 18.3 initdb

Split out of
[0149\-0001a1 \(one OID per database\)](0149-0001a1-database-oid-layout.md)
on 2026\-11\-16, when that document reached the 800\-line D3 limit. Each
section below is one M0149\-0001g slice. The task entry in
`.ralph/fix_plan.md` carries each slice's probes and gates, and the deferral
ledger carries what is still open.

Goal: a PG 18.3 node can cold\-start on a goopg data directory and read,
write and maintain every catalog exactly as on its own initdb. Each slice
replaces a goopg\-shaped catalog row, file or index with PG's. Values are
captured from a throwaway PG initdb, or measured from goopg's own files where
the two layouts legitimately differ.

## Follow\-up: M0149\-0001g slice 1 — catalog image vs PG 18.3 \(2026\-11\-16\)

- Dropped the three goopg\-only catalog columns \(objversion, shared,
  nspblocked\). PG built rows with fewer values than those descriptors and
  SIGSEGV'd.
- An index audit over all 117 bootstrap indexes compared leaf tuples with
  heap rows. It found exactly one empty index, 2658
  \(`pg_attribute_relid_attnam_index`\).
  - It is now built at initdb from the single pg\_attribute row source.
  - goopg maintains it at runtime: every pg\_attribute writer calls
    `insertPgAttributeIndexEntries`, which inserts into both 2659 and 2658.
- With every index complete, local catalogs carry `relhasindex = true`
  \(M0149\-0001d did the shared ones\).
- PG 18.3 on a goopg directory can now run ordinary DDL.
- The remaining gaps:
  - 19 missing catalog indexes, starting with pg\_depend's two;
  - three catalogs with missing columns;
  - type and flag fixes;
  - 29 missing TOAST relations;
  - goopg's catalog FSM forks, which PG rejects as invalid pages.

## Follow\-up: M0149\-0001g slice 2 — generic catalog\-index maintenance \(2026\-11\-16\)

- PG inserts every catalog row into every index of its catalog
  \(`CatalogTupleInsert` → `CatalogIndexInsert`\). goopg had one hand\-written
  inserter per index, and some catalogs had none.
- An audit after a DDL workload found 19 indexes missing rows that goopg
  had written, across pg\_constraint, pg\_inherits, pg\_auth\_members,
  pg\_statistic, pg\_db\_role\_setting, pg\_statistic\_ext and
  pg\_publication\_rel.
- `sys_catalog_index_generic.go` now records, per catalog, the indexes no
  explicit inserter keeps: key column positions and fixed\-width key types,
  laid out by `att_align_nominal`.
  - `writeHeapRowCanonical` inserts every new catalog row into those
    indexes.
  - The system\-btree split code reads the tuple layout from the same
    registry.
- Six indexes missing from the image were bootstrapped: pg\_depend 2673 and
  2674, pg\_inherits 2187, pg\_description 2675, and pg\_constraint 2664
  and 2579.
- Remaining work, recorded under M0149\-0001g:
  - 13 missing indexes;
  - descriptor fixes;
  - the TOAST relations and FSM forks;
  - the obj\_description source text;
  - the pg\_enum opclass and the pg\_ts\_template index image.

## Follow\-up: M0149\-0001g slice 3 — FK and DROP on PG \(2026\-11\-16\)

- Bootstrapped six indexes over empty catalogs \(pg\_trigger,
  pg\_largeobject\_metadata, pg\_policy, pg\_seclabel\).
- Added the pg\_init\_privs catalog with its index.
- PG 18.3 over a goopg directory can now create and enforce foreign keys,
  and DROP every object kind tried, including goopg\-created tables with
  CASCADE.
- Remaining indexes: pg\_am, pg\_amop, pg\_amproc and pg\_class 3455. These
  need builds from the bootstrap rows. pg\_largeobject needs its catalog.

## Follow\-up: M0149\-0001g slice 4 — row\-built catalog indexes \(2026\-11\-16\)

- pg\_am 2651 / 2652, pg\_amop 2756, pg\_amproc 2757 and pg\_class 3455 are
  now built at initdb from the same row sources as their heaps. goopg
  maintains the first three at runtime through the generic registry.
- 3455 is not maintained at runtime. Doing so made a promoted PG node trip
  an nbtree deduplication assertion on a user index; this was bisected to
  that registry entry. It stays open until the WAL interaction is
  understood.

## Follow\-up: M0149\-0001g slice 5 — relfrozenxid by relkind \(2026\-11\-16\)

- PostgreSQL gives pg\_class.relfrozenxid and relminmxid a valid value only
  for relkinds with a table access method \(r, t, m\). heap\_create leaves
  every other relkind at InvalidTransactionId / InvalidMultiXactId, and
  vac\_update\_datfrozenxid asserts it.
- goopg derives both fields from `executor.PGClassFrozenIDs` in every
  writer of an on\-disk pg\_class row:
  - user relations and indexes;
  - initdb's bootstrap rows;
  - the relcache init file.
- goopg's own SQL\-level pg\_class \(catalog virtual rows\) still reports 0/1
  for every relkind; that view is not read by PG.

## Follow\-up: M0149\-0001g slice 6 — heap placeholders must not name indexes \(2026\-11\-16\)

- `bootstrapMappedLocalCatalogHeaps` writes an empty heap page for every
  OID in `mappedLocalCatalogPlaceholderOIDs`. It runs after
  `bootstrapPostgresDatabase` has laid down the catalog btrees, so an index
  OID in that list silently turns a btree into a heap page.
- 3766 and 3767 \(pg\_ts\_template\) were in the list. They have been removed
  and a unit test now rejects any bootstrap index OID there.

## Follow\-up: M0149\-0001g slice 7 — opclass/opfamily rows are PG's \(2026\-11\-16\)

- The bootstrap pg\_opclass and pg\_opfamily rows are now identical to
  PostgreSQL 18.3's `postgres.bki`, including:
  - the genbki\-assigned 10000\+ opclass OIDs;
  - the spgist \(4000\) and BRIN \(3580\) access\-method OIDs, which were
    swapped before.
- Unit tests compare the Go tables with `postgres.bki` directly, so any
  further drift fails at `go test`. Before, it surfaced only as a crash on a
  PG node.

## Follow\-up: M0149\-0001g slice 8 — pg\_am/pg\_amop/pg\_amproc OIDs \(2026\-11\-16\)

- PG's pg\_amop and pg\_amproc rows carry genbki OIDs from 10000 in .dat
  order.
- goopg's pg\_amproc list is already in .dat order, so its OIDs are simply
  base 10000 plus the row index.
- goopg emits pg\_amop rows grouped differently, so each row's OID is looked
  up by natural key in `pg_amop_oids_generated.go`. That file is regenerated
  by `scripts/gen-pg-amop-oids.py` from `postgres.bki`.
- The bki oracle tests now cover pg\_am, pg\_amop, pg\_amproc, pg\_opclass
  and pg\_opfamily.

## Follow\-up: M0149\-0001g slice 9 — nailed descriptors pinned to postgres.bki \(2026\-11\-16\)

- Every nailed catalog's attribute list, and the pg\_attribute heap rows
  derived from it, is compared with the catalog's `create` declaration in
  `postgres.bki`. This covers column names, order and types, plus relnatts.
- The test found four descriptors that were shorter than, or typed
  differently from, the tuples goopg actually writes. Two examples:
  - pg\_operator stopped at oprresult;
  - `oid` and `xid` columns were labelled int4.
- PostgreSQL's syscache reads catalog tuples through compiled structs, so
  these never broke operator execution. They broke every SQL reference to
  the missing columns.
- Cross\-catalog census method: start PG 18.3 on a goopg directory, then
  `select * from <catalog>` with index scans disabled. Diff the output
  against a scratch PG initdb. The latest residuals are recorded in
  fix\_plan under M0149\-0001g slice 9.

## Follow\-up: M0149\-0001g slice 10 — shared catalog rows at initdb \(2026\-11\-16\)

- `writeSharedHeapRows` writes the one global/ copy of a shared catalog's
  bootstrap heap. It shares `buildMultiPageHeapFile` with the local writer,
  which writes base/1 and base/5.
- pg\_auth\_members is its first user: PG initdb's three predefined
  memberships for pg\_monitor, plus the catalog's four btree indexes built
  from those rows.
- Rows written at initdb with xmin 1 are loaded by goopg's own catalog
  reload, so goopg and a PG node see the same memberships.

## Follow\-up: M0149\-0001g slice 11 — ACL columns: NULL or a real ACL, never "\{\}" \(2026\-11\-16\)

- In PostgreSQL a NULL ACL column means acldefault: the owner holds every
  privilege, PUBLIC whatever the object kind defaults to.
- An empty ACL grants nothing, and PG builds it with ndim 1 and dims\[0\] = 0
  \(allocacl\(0\)\).
- goopg wrote the text "\{\}" into ACL columns. The codec turns that into an
  ndim\-0 array, which `check_acl` rejects.
- From slice 11 on, bootstrap pg\_namespace rows carry PG's initdb ACLs
  \(`executor.EncodeAclItemArrayText`\), and user schemas store NULL.
- relacl, proacl and the other ACL columns still go through "\{\}". They
  are the next slice.

## Follow\-up: M0149\-0001g slice 12 — catalog relacl and the empty\-ACL shape \(2026\-11\-16\)

- Bootstrap pg\_class rows take their relacl from `pg_class_relacl_generated.go`.
  `scripts/gen-pg-class-relacl.sh` regenerates it by running its own PG 18.3
  initdb; the bootstrap superuser is rendered as `B`.
- User relations store NULL until a GRANT, then the ACL goopg's store
  materializes.
- An empty ACL is encoded as `allocacl(0)`: one dimension of length 0.
- Writers must mark NULL columns in the tuple's null bitmap. The old
  pg\_class\-only writer did not, which went unnoticed until relacl became
  NULL. All bootstrap heap writers now go through `buildMultiPageHeapFile`.

## Follow\-up: M0149\-0001g slice 13 — proacl, and aclitem\[\] alignment \(2026\-11\-16\)

- `scripts/gen-pg-initdb-acls.sh` generates `pg_initdb_acls_generated.go`
  with both initdb ACL tables:
  - relacl, keyed by `nspname.relname`;
  - proacl, keyed by function OID.
- `_aclitem` is typalign 'd'. The heap codec \(`catalog.PhysicalTypeAlign`\),
  the pg\_attribute rows \(`pgTypeAlignChar`\) and the relcache init file
  \(`pgAttrAlignChar`\) must all agree on that. A varlena's attalign comes
  from its type, not from attlen.

## Follow\-up: M0149\-0001g slice 14 — alignment pinned to pg\_type.dat \(2026\-11\-16\)

- `catalog.PhysicalTypeAlign` is now tested against every pg\_type row in
  `postgres.bki`.
- An array's alignment is its element's.
- `doubleAlignedVarlena` and `doubleAlignedArrayElem` hold the 'd' cases that
  cannot be derived from the element's scalar entry.
- The test's `knownAlignGaps` names the types whose on\-disk format must change
  before their alignment can be PG's: fixed\-length types goopg stores as
  varlena, and composites.

## Follow\-up: M0149\-0001g slice 15 — pg\_proc attributes from pg\_proc.dat \(2026\-11\-16\)

- The bootstrap pg\_proc heap takes each built\-in's scalar attributes from
  `pg_proc_bki_attrs_generated.go`, regenerated by
  `scripts/gen-pg-proc-attrs.py` from `postgres.bki`.
- goopg's seed table still supplies the rest: name, argument types, prosrc
  handler key, and goopg's own volatility.
- Only the PG\-visible heap row changes. goopg's reload never reads built\-in
  pg\_proc rows back.

## Follow\-up: M0149\-0001g slice 16 — initdb's system\_functions.sql state for pg\_proc \(2026\-11\-16\)

- Built\-ins that initdb re\-creates after bootstrap take their final state
  from `pg_proc_initdb_overrides_generated.go`. That covers SQL\-standard
  bodies, argument defaults and names, and declared attributes.
- `scripts/gen-pg-proc-initdb-overrides.py` regenerates the file from a
  throwaway PG 18.3 initdb and keeps only the rows that differ from
  `postgres.bki`.
- Large node trees use PG's inline pglz form.

## Follow\-up: M0149\-0001g slice 17 — pg\_attribute metadata from a PG initdb \(2026\-11\-16\)

- `pg_attribute_meta_generated.go` holds PG's pg\_attribute metadata for
  every system\-relation column. `scripts/gen-pg-attribute-meta.py`
  regenerates it.
- `pgAttributeRow` overlays it when goopg writes the same atttypid.
- For stored relations, attalign and attbyval must equal what goopg's codec
  does. `TestPgAttributeMetaMatchesCodec` enforces this, and goopg's type
  helpers were corrected where it disagreed.
- Index columns on name are a format gap: PG keys them as cstring.

## Follow\-up: M0149\-0001g slice 18 — system rowtypes and reltype \(2026\-11\-16\)

- The bootstrap pg\_type gains each system relation's composite rowtype and
  its array type, as complete PG rows from `pg_type_rowtypes_generated.go`.
  `scripts/gen-pg-type-rowtypes.py` regenerates that file.
- `pgClassRelType` is the single source of `pg_class.reltype` for the heap
  row and the relcache init file:
  - the captured rowtype where one exists;
  - 0 for indexes.

## Follow\-up: M0149\-0001g slice 19 — built\-in object descriptions \(2026\-11\-16\)

- pg\_description and pg\_shdescription carry PG initdb's descriptions of
  built\-in objects, from `pg_description_generated.go`.
  `scripts/gen-pg-description.py` regenerates it.
- Only objects with fixed pg\_\*.dat OIDs in catalogs goopg bootstraps are
  included.
- Descriptions of initdb\-assigned objects \(locale collations, text\-search
  objects, plpgsql\) wait for those catalogs.

## Follow\-up: M0149\-0001g slice 20 — text\-search bootstrap rows \(2026\-11\-16\)

- The five pg\_ts\_\* catalogs carry postgres.bki's rows and their nine
  indexes, from `pg_ts_bootstrap_generated.go`. `scripts/gen-pg-ts-bootstrap.py`
  regenerates it from postgres.bki alone; no initdb is needed.
- The snowball dictionaries initdb adds later depend on C functions goopg
  does not seed.

## Follow\-up: M0149\-0001g slice 21 — snowball text search \(2026\-11\-16\)

- The snowball template, dictionaries, configurations and maps that initdb's
  `snowball_create.sql` adds, together with the two dict\_snowball C
  functions, come from `pg_ts_snowball_generated.go`.
  `scripts/gen-pg-ts-snowball.py` regenerates it.
- A PG node loads `$libdir/dict_snowball` from its own installation.

## Follow\-up: M0149\-0001g slice 22 — pg\_init\_privs \(2026\-11\-16\)

- `catalog.PGInitdbInitPrivs` holds the 229 rows that initdb's
  `setup_privileges()` records. `scripts/gen-pg-init-privs.sh` regenerates
  it from a throwaway PG initdb.
- It is the single source for both the on\-disk heap 3394 \(with index
  3395\) and goopg's virtual pg\_init\_privs, so the two cannot drift.
- goopg's SQL nspacl for pg\_catalog and information\_schema is seeded with
  PUBLIC USAGE to equal its initprivs. Schema public would need an owner of
  pg\_database\_owner, which goopg's ACL store cannot express yet.

## Follow\-up: M0149\-0001g slice 23 — goopg\-side descriptions and schema public \(2026\-11\-16\)

- The generated description tables now live in package catalog
  \(`pg_description_generated.go`\).
  - They seed goopg's comment store, which backs `pg_description`,
    `obj_description` and pg\_dump's getComments.
  - initdb writes the same rows to disk.
- `obj_description(oid, name)` keys classoid on the named system catalog,
  as `system_functions.sql` does.
- Schema public reports owner pg\_database\_owner and PG's initdb nspacl.
  `aclOwnerDisplay` renders that owner in the ACL store, which otherwise
  assumes the bootstrap superuser.
- Invariant: pg\_dump of a fresh goopg database equals pg\_dump of a fresh
  PG 18.3 database.

## Follow\-up: M0149\-0001g slice 24 — pg\_toast in goopg's pg\_namespace \(2026\-11\-16\)

- goopg's pg\_namespace view lists all four bootstrap namespaces with PG's
  owner, nspacl and description. pg\_toast had been hidden since the era
  when its OID collided with public's.

## Follow\-up: M0149\-0001g slice 25 — measured catalog sizes in pg\_class \(2026\-11\-16\)

- `stampPgClassRelStats` runs after every catalog file is written and
  before template0 is cloned. It patches relpages, reltuples, relallvisible
  and relallfrozen in each pg\_class tuple from the relation's actual file.
- The numbers describe goopg's own files, not PG's. The two heap layouts
  differ, so copying PG initdb's values would misstate sizes to a PG node's
  estimate\_rel\_size.
- goopg's own planner is unaffected: it reads catalog sizes from the VM and
  its private stats sidecar, never from pg\_class.

## Follow\-up: M0149\-0001g slice 26 — every catalog TOAST relation \(2026\-11\-16\)

- `nailedToastPairs` lists all 39 DECLARE\_TOAST pairs of a PG 18.3 initdb.
  It is the single source for:
  - the toast relations' pg\_class and pg\_attribute rows;
  - the parents' reltoastrelid;
  - the empty files;
  - the toast indexes' pg\_index rows, derived in `pgIndexInitialEntries`.
- Mapped catalogs: PG stores relfilenode 0 and looks the file up in
  pg\_filenode.map; goopg still stores the OID. That breaks a PG node's
  VACUUM FULL on pg\_proc. Fixing it is the next slice.
- Relcache init files are rebuilt by PG at every startup, so their content
  beyond existence never reaches a PG node.

## Follow\-up: M0149\-0001g slice 27 — mapped catalogs \(2026\-11\-16\)

- `relmap_catalogs.go` lists PG 18.3's mapped relations in its map files'
  insertion order: 46 global and 17 local.
  - Their pg\_class rows carry relfilenode 0.
  - Both pg\_filenode.map files are written from these lists and are
    byte\-identical to a PG initdb's.
- goopg still names each file by its OID, so every mapping is the identity.
  - goopg only copies and journals map images; it never resolves paths
    through them.
  - A PG node does resolve paths through them, and records a rewrite
    \(VACUUM FULL, CLUSTER, REINDEX\) of a mapped catalog in the map.

## Follow\-up: M0149\-0001g slice 28 — no hand edits in generated pg\_proc data \(2026\-11\-16\)

- `pg_proc_seed_data.go` and `pg_proc_names_generated.go` must equal
  `cmd/gen-pg-proc-data` output.
  - Two hand\-added rows had duplicated existing functions under new OIDs.
  - That broke pg\_proc's unique signature index on a PG node.
- A goopg\-only function needs its own seed list with an OID outside PG's
  range, and must not duplicate a pg\_proc.dat signature.
- `TestPgProcInitialEntriesSignaturesUnique` enforces the uniqueness.
