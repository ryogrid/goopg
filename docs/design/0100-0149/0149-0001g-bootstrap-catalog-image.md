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

## Follow\-up: M0149\-0001g slice 29 — plpgsql at initdb \(2026\-11\-16\)

- PG's initdb runs CREATE EXTENSION plpgsql \(initdb.c load\_plpgsql\).
  `pg_plpgsql_generated.go` \(scripts/gen\-pg\-plpgsql.py\) carries what it
  leaves:
  - the three handler functions;
  - the language row;
  - the extension row;
  - the extension's pg\_depend rows.
- The rows join the pg\_proc and pg\_language seeds. `bootstrapPgPlpgsql`
  writes pg\_extension and pg\_depend with their indexes.
- goopg reloads the pg\_extension heap at startup, so its own catalog knows
  plpgsql is installed.
- CREATE EXTENSION checks for an existing extension before looking for a
  control file, the same order as PG's CreateExtension.
- pg\_depend now carries only plpgsql's rows. The remaining initdb
  dependencies \(views, information\_schema, …\) are still to be captured.

## Follow\-up: M0149\-0001g slice 30 — pg\_largeobject \(2026\-11\-16\)

- pg\_largeobject \(2613\) and its UNIQUE PRIMARY index 2683 \(loid, pageno\)
  are bootstrapped like pg\_largeobject\_metadata \(2995 / 2996\):
  - a nailed relation with PG's three columns;
  - an empty heap and a metapage\-only index;
  - a pg\_index row.
- With it, every catalog relation's relname and relfilenode equal a PG 18.3
  initdb's.
- The bootstrap rowtype 10025 \(captured in slice 18\) no longer names a
  missing relation.

## Follow\-up: M0149\-0001g slice 31 — initdb's pg\_depend \(2026\-11\-16\)

- `pg_depend_generated.go` \(scripts/gen\-pg\-depend.py\) is every pg\_depend
  row of a PG 18.3 initdb, in heap order. `bootstrapPgDepend` writes it with
  both indexes.
- Bootstrap\-catalog objects are pinned by OID and need no rows. The rows
  cover what initdb's post\-bootstrap SQL creates: system views, their rules
  and rowtypes, information\_schema, snowball, plpgsql.
- A row whose depender or referenced object goopg does not bootstrap is left
  out \(`pgDependMissingObjects`: pg\_settings' two rules\). A dangling row
  would make a PG node report dependencies on objects that do not exist.

## Follow\-up: M0149\-0001g slice 32 — initdb's non\-\_RETURN rules \(2026\-11\-16\)

- `pg_rewrite_rules_generated.go` \(scripts/gen\-pg\-rewrite\-rules.py\)
  carries every rewrite rule a PG 18.3 initdb creates besides the views'
  \_RETURN rules: pg\_settings\_u and pg\_settings\_n.
- They join the pg\_rewrite seed. pg\_depend no longer has to leave any row
  out, so `pgDependMissingObjects` is empty.

## Follow\-up: M0149\-0001g slice 33 — imported collations \(2026\-11\-16\)

- `pg_collation_generated.go` \(scripts/gen\-pg\-collations.py\) is a PG 18.3
  initdb's whole pg\_collation heap, in heap order:
  - the bki rows as setup\_collation leaves them;
  - the libc collations from `locale -a`;
  - the ICU collations.
- Unlike every other captured table, its content depends on the host's
  locales and ICU version, as PG's own import does. goopg carries the build
  host's.
  - A PG node with a different ICU reports a collation version mismatch, the
    same as a PG cluster whose ICU was upgraded.
- goopg's own SQL catalog still exposes only the seven built\-in
  collations; goopg has no ICU.

## Follow\-up: M0149\-0001g slice 34 — goopg's pg\_depend view \(2026\-11\-16\)

- `catalog.PGDependInitdb` is now the single source for both the on\-disk
  pg\_depend that initdb writes and goopg's own pg\_depend view. The view
  lists those rows first, then the runtime rows.
- catalog pg\_statistic is deferred.
  - PG's rows carry anyarray stavalues typed by each column, plus
    element\-statistics kinds for array columns.
  - A faithful capture needs a typed array encoder per element type.

## Follow\-up: M0149\-0001j — typed pg\_statistic stavalues \(2026\-11\-16\)

- goopg wrote every stavalues slot as a text\-element array without a
  dimension count. A PG node saw them empty, and a typed reading would have
  handed text datums to the column's own comparison operators.
- Now, for column types whose text form does not depend on session settings:
  - the slot is an anyarray of the column's own type, built by goopg's array
    codec, which is PG's update\_attstats form;
  - goopg's reload decodes non\-text element types through
    `catalog.StatValuesDecoder`, which the executor installs;
  - legacy text blobs keep their old path, so existing data is read
    unchanged.
- Types outside the set keep the legacy blob until their text round\-trip is
  settled \(time types depend on DateStyle and TimeZone\).
- Slice 3 \(name\): goopg's array codec gives name\[\] PG's element image,
  64\-byte NameData at typalign 'c', so name stavalues and user name\[\]
  columns are both readable on a PG node. Blobs written before this change
  \(elemtype 25\) still decode through the text path.
- Slice 4 \(planner\): goopg's selectivity code now reads a name
  literal, the resolver's CastExpr\{StringConst → name\}, as the folded
  name Const PG uses. Before this, goopg's own estimates ignored the name
  statistics it writes.
- Slice 5 \(date\-time types\): typed stavalues sit behind a round\-trip
  guard. The blob must decode back to ANALYZE's exact strings, so only
  canonical\-style text \(ISO, MDY, UTC\) is typed. Other sessions keep
  the legacy blob, which a PG node reads as no statistics rather than wrong
  ones. time / timetz statistics are rendered by time\_out / timetz\_out
  \(statValueText\), shared by ANALYZE, stats import and the endpoint probe.

## Follow\-up: M0149\-0001k — fixed\-length geometric and tid storage \(2026\-11\-16\)

- PG stores point, lseg, box, line, circle and tid as fixed\-length
  structs \(typlen 16/32/32/24/24/6\). goopg stored their text as a
  varlena, so a PG node read garbage and shifted every later column.
- A heap value carries no format marker, so the switch is per cluster.
  The `fixed\_length\_geometric\_tid` capability in
  global/pg\_goopg\_features is written by initdb and read at open.
  Older clusters keep the varlena format unchanged.
- internal/utils/adt/fixedgeo is the one encoder and renderer. It is
  shared by the executor heap codec and the pgoutput decoder, and is
  consulted through catalog PhysicalTypeIsVarlena / PhysicalTypeAlign.
  goopg's in\-memory Datum stays the canonical output text.
- Open: money \(needs a real int64 type first\), aclitem, array elements
  of these types, and their COPY BINARY send format \(M0149\-0001k1…k4\).
- M0149\-0001k1 \(money\): the Datum is a scale\-2 numeric holding the Cash
  count, so ordering and comparison need no money arm. cash\_in / cash\_out
  \(internal/utils/adt/cash, C locale\) handle text. Storage is the
  8\-byte Cash behind its own `fixed\_length\_money` capability.
  money arithmetic is rewritten at plan time into PG's cash\_\* operator
  functions. A FuncCall with Operator set keeps the `?column?` name and
  infix EXPLAIN, and the executor applies cash.c's rounding and overflow
  rules. regress money passes.
- M0149\-0001k2 \(aclitem\): a scalar aclitem column stores the 16\-byte
  AclItem behind `fixed\_length\_aclitem`. The aclitemout renderer lives in
  internal/utils/adt/aclitem, shared by the heap codec and pgoutput. Role
  names come from the scan's session renderer.
- M0149\-0001k3 \(arrays\): elements of the fixedgeo types use the same
  fixed images. box\[\] splits and joins on its typdelim `;`, and the
  bootstrap pg\_type now carries typdelim `;` for box and \_box.
- M0149\-0001k4 \(COPY BINARY\): fixedgeo Send / Recv produce the types'
  send formats, byte\-identical to PG's COPY output, and money uses
  cash\_send. Server\-side COPY FROM a file now honours FORMAT binary.
- M0149\-0001k5 \(money indexes\): money is a btree key, the int8 order of
  the Cash count. It is tuple\-format only under fixed\_length\_money.
  pg\_index.indclass now names the default opclass for money / time /
  timetz / cidr / reg\*; with 0 there, PG cannot open the table.
  money\[\] elements are the 8\-byte Cash too, and a money operand of `\|\|`
  renders through cash\_out, as anytextcat does.
