# 02a — Phase-B0 enablers: detailed design

| | |
|---|---|
| Status | draft — **agent-reviewed 2026-07-16**, two lenses (PG-fidelity vs ./postgres; goopg-integration vs code): 4 blocker + 10 major + 8 minor findings, ALL folded in with inline `(review …)` tags |
| Date | 2026-07-16 |
| Scope | The four B0 enablers every catalog conversion depends on: (1) generic per-catalog heap-scan reload, (2) catalog `XLOG_HEAP_UPDATE` emit, (3) per-DB catalog index bootstrap, (4) `pg_filenode.map` writer + `XLOG_RELMAP_UPDATE` |
| Target | PostgreSQL 18.3 catalog-DML journaling and recovery (`postgres/src/backend/catalog/indexing.c`, `postgres/src/backend/utils/cache/relmapper.c`) |
| Parent | [02-catalog-heap-journaling.md](02-catalog-heap-journaling.md) §4.2, §6, §8.1 |

## 1. Scope & non-goals

B0 delivers the shared machinery; it converts **no** catalog by itself. Every slice
is a pure enabler landed behind the existing behavior (B0.1 is a zero-behavior-change
refactor; B0.2/B0.3 are additive paths proven on the already-heap-backed pg_class).

Out of scope: any B1+ catalog conversion; a real indexed syscache (doc 02 §4.2 step 4
defers it); pg_depend; commit-time invalidation-message content (Part-A xact scope);
`XLOG_SEQ_LOG` (see doc 02c §3).

## 2. B0.1 — generic per-catalog heap-reload framework

### 2.1 Why

Recovery for a converted catalog is "scan the catalog heap, rebuild the in-memory
registry" — exactly what `loadUserTablesFromHeapForDB`
(`internal/initdb/open.go:2692`) already does for pg_class + pg_attribute. Phase B
needs that loop for ~50 catalogs without copying its hard-won visibility rules 50
times. B0.1 factors the loop once; each catalog contributes only a descriptor.

### 2.2 API

New file `internal/initdb/catalog_heap_reload.go`:

```go
// catalogReloadDesc describes how one base catalog's heap is scanned at
// startup and re-applied to the in-memory registry. One descriptor per
// converted catalog.
type catalogReloadDesc struct {
    Name   string // "pg_namespace" — log/error labels only
    RelOid uint32 // 2615
    Shared bool   // true → heap lives in global/<oid> (B4); false → base/<dbOid>/<oid>
    Order  int    // explicit total order within a reload pass; see §2.4
    Fatal  bool   // scan error aborts startup (schema/table precedent) vs warn-and-continue
                  // (statistics/index precedent, open.go:1479-1488) (review MAJOR-4)

    // Decode converts one live heap tuple into a typed row. Header-driven
    // (t_hoff, null bitmap); receives the layout verdict (canonical vs
    // legacy) the visibility filter already computed (§2.3).
    Decode func(ht storage.HeapTuple, canonical bool) (any, error)

    // ApplyBatch re-registers all rows of this catalog at once. Batch —
    // not row-at-a-time — because the exemplar itself needs a join
    // (pg_class rows + their pg_attribute columns registered together);
    // single-row catalogs just loop (review MAJOR-4).
    ApplyBatch func(cat *catalog.InMemory, nsDBOid uint32, rows []any) error
}

// reloadCatalogHeaps runs every descriptor (sorted by Order) against
// base/<heapDBOid>/ (or global/ for Shared descs), applying rows into the
// namespace selected by nsDBOid. It is the generic replacement for the 26
// bespoke *_ddl_recovery.go scanners.
func reloadCatalogHeaps(mgr *storage.Manager, cat *catalog.InMemory,
    clog *mvcc.CLog, heapDBOid, nsDBOid uint32, descs []catalogReloadDesc) error
```

**B0.1 is *behavior-preserving*, not blindly "run everything"** (review MAJOR-4):
the M0114 catalog-cache fast path (`open.go:1344-1370` — a cache hit skips the
pg_class/pg_attribute scan entirely and `writeCatalogCache` runs after a
successful scan) is kept by letting the pg_class/pg_attribute descriptor pair
sit behind the same cache check; and the pg_class+pg_attribute two-pass join
(collect class rows → build attrByRelOID → register once per table with
columns, reloptions re-decode, DBOid stamping) lives inside their shared
ApplyBatch, exactly as today.

**Write-side / reload-side dbOid routing (review BLOCKER-3 — load-bearing).**
Today catalog heap WRITES go to `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
which maps the postgres DB to DefaultDBOid (`operators_ddl.go:13052`), while
startup RELOAD scans `cat.DBOID()` — which `detectCatalogDBOID`
(`open.go:2924-2958`) resolves to the **postgres DB's OID** on any real cluster.
The two are glued by `mirrorTouchedCatalogsToPostgresDB`
(`sys_catalog_postgres_db_mirror.go:122-143`): a non-WAL-logged block copy of a
HARDCODED six-relfile set (1259, 1249, 2610, 2662, 2663, 2659). A converted
catalog whose relfile is not in that set writes rows recovery never reads —
user objects silently vanish on restart. Until B5 retires the shim, **every
conversion must add its heap (and index) relfiles to the mirror set** (recipe
02b step 2 makes this mandatory), and the reload descriptor scans the SAME
dbOid the mirror populates. The mirror copy is not WAL-logged; the WAL stream
carries the DefaultDBOid pages, which is what a PG standby replays — the e2e
gate therefore asserts objects in the DB the WAL wrote, and full stream-parity
for the postgres-DB copies is explicitly a B5 (mirror-retirement) outcome.

### 2.3 The shared scan loop — the ACTUAL visibility rules (review BLOCKER-4)

The loop factored out of `loadUserTablesFromHeapForDB` is: `mgr.NBlocks` →
`ReadBlock` → `PageLinePointerCount` → `PageGetHeapTuple` per slot, then the
liveness filter. What the code actually implements (an earlier draft of this
section paraphrased it wrongly — the rules below are transcribed from
`open.go:2724-2771` and are normative):

1. **Any non-zero xmax kills the row** (`open.go:2727`) — unconditionally, even
   if the deleting xact aborted. Correct today because catalog mutations are
   delete+reinsert (an aborted DDL's reinserted row also dies by rule 2, and
   the delete's xmax stamping is not rolled back). **B0.2 changes this
   calculus**: once catalog UPDATEs stamp xmax on superseded versions, the rule
   keeps working (old versions must die), but an ABORTED update would kill the
   only live version — the framework therefore upgrades rule 1 to
   "xmax committed or in the recovered-CLOG unknown window → dead; xmax
   aborted → live", and a unit test pins the aborted-update case.
2. **Aborted xmin kills the row** — for ALL layouts.
3. **Legacy-layout rows additionally require committed xmin**
   (`open.go:2769`); PG18-canonical rows deliberately do NOT (the
   basebackup out-of-range-xmin pass-through: a canonical row whose xmin lies
   beyond the recovered CLOG horizon is live unless explicitly aborted —
   dropping this empties catalogs on standby bootstrap). The
   canonical-vs-legacy verdict comes from which decode succeeded, so the
   filter is decode-entangled, not a pre-Decode gate — the framework runs
   decode-then-filter exactly like today, and `Decode` receives the layout
   verdict. Post-conversion catalogs are all-canonical, so their safety against
   crashed in-progress DDL rests on the xact-recovery pass backfilling aborts
   into the CLOG before the reload runs — the pass ordering pins it
   (review MINOR-8).

Today three inline variants of this filter exist (pg_class scan, pg_attribute
scan, and the clog-committed branch); B0.1 unifies them behind one function
with the pg_attribute variant's laxer committed-branch behavior preserved via
the layout flag. A synthetic-page unit test pins every row class
(committed/aborted/in-progress/out-of-range xmin × zero/committed/aborted
xmax × canonical/legacy).

### 2.4 Ordering (risk R5 made explicit)

Today the recovery-pass order is hand-wired by call sequence in
`internal/initdb/open.go` — and it extends well past the table loads: schema
:1233, tablespace :1245 (must precede table loads — reltablespace resolution),
foreign-server :1256 before user-mapping :1266, conversions/collations/ts-*
:1297-1330, aggregates :1337 (writes pg_proc registry rows BEFORE table loads),
tables :1364, databases :1421 (gates the per-DB loop :1439-1451 — pg_database
converts only in B4 yet orders every per-DB descriptor run), index-DDL replay
:1501, sequences :1508-1520, defaults :1526, matviews/views :1537/:1548, roles
:1559-1574, event triggers :1975, **functions :1992**, domains :2038
(review MAJOR-5 — an earlier draft's table missed most of these and even
reversed pg_proc vs pg_sequence relative to today's sequence).

The framework's rule is therefore: **Order constants are DERIVED from today's
call sequence, one constant per existing pass**, and a conversion inherits its
catalog's existing slot — it never invents a new relative position. The full
table (maintained in `catalog_heap_reload.go` beside the constants, seeded from
the list above) is the normative order; the doc table below shows only the B1
slice of it:

| Order (slot) | Pass | Notes |
|---|---|---|
| = schema pass (:1233) | pg_namespace descriptor | before tablespace/table/… as today |
| = table pass (:1364/:1445) | pg_class+pg_attribute descriptor pair | behind the M0114 cache check |
| = sequence pass (:1508) | pg_sequence descriptor | after index replay :1501, as today |
| = function pass (:1992) | pg_proc descriptor | LATE — after sequences/views/roles, exactly where replayFunctionDDLRecords runs today; the aggregate pass (:1337) keeps writing its pg_proc registry rows earlier during the B1–B2 window |

During the transition window a converted catalog's descriptor and the remaining
bespoke scanners coexist, interleaved by these slot constants.

### 2.5 Bootstrap-row policy

initdb populates some catalog heaps with builtin rows (pg_namespace: pg_catalog /
public / information_schema; pg_proc: ~3k builtins in the heap file). The
in-memory registry compiles builtins in; re-Applying them from the heap must be
either idempotent or skipped. Policy, per catalog, chosen in its descriptor:

- **Skip-below-FirstUserOID** (default; pg_namespace, pg_proc): only rows with
  `oid >= catalog.FirstUserOID` (the precedent filter at `open.go:2772`) are
  Applied. Builtin rows remain compiled-in; the heap still holds them for
  PG-tool reads.
- **Reload-all** (catalogs whose registry is built purely from the heap, none in
  B1): Apply everything.

The descriptor records the choice; the recipe doc (02b §2 step 4) requires it.

### 2.6 Double-apply idempotency during transition

Until a catalog converts, its bespoke scanner still runs; after it converts, only
its descriptor runs. There is no overlap for a single catalog (recipe rule: the
scanner is deleted in the same landing as the emit swap). But a NOT-yet-converted
catalog's scanner may re-register objects that reference a converted catalog's
rows (e.g. the function scanner resolving schema OIDs after pg_namespace
converts) — the reload framework therefore runs interleaved by Order with the
remaining scanners, not as a separate phase.

## 3. B0.2 — catalog `XLOG_HEAP_UPDATE` emit

### 3.1 Why a real UPDATE (not delete+insert)

PG journals every catalog ALTER as a heap UPDATE: `RenameSchema` →
`CatalogTupleUpdate(rel, &tup->t_self, tup)`
(`postgres/src/backend/commands/schemacmds.c:249,294`); generic renames via
`AlterObjectRename_internal` → `CatalogTupleUpdate`
(`postgres/src/backend/commands/alter.c:337`). A delete+insert pair would emit
`XLOG_HEAP_DELETE` + `XLOG_HEAP_INSERT` where PG emits one `XLOG_HEAP_UPDATE` —
a permanent, per-DDL stream divergence. goopg's catalog mutations today are
delete-old-rows + reinsert (`syncTableToCatalogHeap`,
`internal/executor/operators_ddl.go:13167`), so the UPDATE path is net-new for
catalogs (the record body itself was flipped to PG format in Part A).

### 3.2 API

`internal/executor/operators_storage.go`, beside `writeHeapRowReturningPG`
(:8038):

```go
// updateHeapRowCanonicalPG performs a PG-faithful catalog heap UPDATE:
// stamps xmax + forward t_ctid on the old version at oldTID, writes the new
// version (PG-canonical encoding), emits xl_heap_update (Part-A encoder)
// with the old/new TIDs, and returns the new TID. The caller maintains
// indexes: an update touching any indexed column is non-HOT and must insert
// fresh entries into EVERY index on the catalog (PG semantics); old index
// entries remain until vacuum.
func updateHeapRowCanonicalPG(ctx *Context, rel storage.RelFileNode,
    cols []catalog.Column, oldTID storage.ItemPointer, newRow Row,
) (storage.ItemPointer, error)
```

Replay needs no new arm: Part A already replays `xl_heap_update` physically for
flipped emitters; the registry side is rebuilt by the reload scan (§2), so the
record only has to bring the page bytes back.

### 3.3 TID bookkeeping — the write-through cache carries TIDs

An UPDATE needs the current row's TID. Contract: **every converted catalog's
write-through cache stores `{row, TID}`**, seeded by the INSERT return value
and refreshed by each UPDATE's returned TID; the reload scan re-seeds TIDs at
startup. This is NET-NEW machinery (review MAJOR-6): today's `classTID`
(`operators_ddl.go:13181`) is consumed by the two pg_class index inserts and
discarded — no registry keeps a TID, which is exactly why today's ALTER paths
resort to delete-all-rows + full re-sync. R-B0-3's live-version check on
`updateHeapRowCanonicalPG` is the safety net for a missed refresh. Key-scan
re-location is rejected as the default (a per-DDL heap scan, and a second code
path to keep in sync).

### 3.4 Proof obligation in B0.2

The slice lands with ONE existing pg_class ALTER re-sync path (e.g. table rename
re-sync) converted from delete+insert to `updateHeapRowCanonicalPG`, so the gate
can assert `pg_waldump` shows `Heap/UPDATE` on relfile 1259 and a real PG standby
replays it.

## 4. B0.3 — per-DB catalog index bootstrap at CREATE DATABASE

Today only DefaultDBOid has catalog btree files; `syncTableToCatalogHeap` skips
index maintenance entirely for other DBs (`operators_ddl.go:13185-13215`). And
the gap is deeper than indexes (review MAJOR-7): goopg's `CREATE DATABASE`
(server-side only — `internal/server/database_ddl.go`; the SQL parser has no
executor arm for it) creates just `base/<oid>/PG_VERSION`
(`createDatabasePhysicalDirectory`, :698-717); `copyTemplateTables` copies USER
relation files and re-syncs pg_class/pg_attribute rows, while other catalog
heap files materialize lazily via smgr O_CREATE — a new DB has **no
pg_namespace/pg_proc heap and no bootstrap rows at all** (no pg_catalog/public
rows, no builtin procs). PG creates a full catalog (heaps + indexes) per
database by copying the template.

Change (two parts, heaps first):
1. **Per-DB catalog-heap bootstrap**: `CREATE DATABASE` copies the template's
   base-catalog HEAP files (initdb-populated bootstrap rows included) into
   `base/<newDbOid>/` — the PG-shaped file copy — instead of leaving them to
   lazy O_CREATE.
2. **Per-DB catalog-index build**: over the copied heaps, build every
   base-catalog btree. Note the tooling reality (review MAJOR-7):
   `pgBuildBtreeBulkLoad` (`btree_index_bootstrap.go:226`) handles only fixed
   16-byte key tuples; name-keyed indexes (2684 nspname, 2663 relname_nsp) have
   dedicated bootstrappers (`initdb.go:1744`). The clone path reuses whichever
   initdb bootstrapper built each index — or, simpler and byte-equivalent,
   copies the template's index FILES along with the heaps (PG's approach);
   the design picks **file copy** for both heaps and indexes.
3. The DefaultDBOid guard in `syncTableToCatalogHeap` is removed; runtime index
   inserts route to `base/<ctx.dbOid>/<indexOid>` unconditionally.
4. The startup loader keeps scanning heaps directly (indexes are for PG-tool
   parity and future syscache use, not the loader), so index corruption cannot
   break recovery.

Retiring the postgres-DB mirror shim (doc 02 §6) is a B5 consequence, not part of
this slice.

## 5. B0.4 — `pg_filenode.map` writer + `XLOG_RELMAP_UPDATE` (deferrable)

### 5.1 On-disk format (source of truth)

`postgres/src/backend/utils/cache/relmapper.c:73-95`:

```
RelMapFile = {
  int32  magic          = 0x592717  (RELMAPPER_FILEMAGIC)
  int32  num_mappings   (<= 64)
  RelMapping mappings[64] = { Oid mapoid; RelFileNumber mapfilenumber; }
  pg_crc32c crc          (CRC-32C of all preceding bytes)
}                        -- 4 + 4 + 64*8 + 4 = 524 bytes, little-endian
```

The struct is written raw — the file is host-endian (LE on all goopg targets;
PG defines no byte order for it) (review MINOR-3). Written atomically (write
temp + rename, `write_relmap_file`). The WAL record is
`xl_relmap_update{ Oid dbid; Oid tsid; int32 nbytes; char data[]; }`
(`postgres/src/include/utils/relmapper.h:27-33`, opcode `XLOG_RELMAP_UPDATE =
0x00`, RM_RELMAP_ID = **7**, `postgres/src/include/access/rmgrlist.h:35`) where
`data` is the entire new RelMapFile image; redo (`relmap_redo`) length-checks
the image and rewrites the file, RECOMPUTING the CRC — the WAL image's CRC is
never verified at redo in PG (only `read_relmap_file` verifies)
(review MINOR-2).

### 5.2 goopg design

- New `internal/wal/relmap.go`: `EncodeRelMapFile(mappings)` (layout above, CRC32C
  via the existing Castagnoli table), `EncodeRelmapUpdatePG(dbid, tsid, image)`
  via `framePGAssembled(RmgrRelMap, 0x00, xid, body)` — `RmgrRelMap Rmgr = 7`
  (`RM_RELMAP_ID`, `postgres/src/include/access/rmgrlist.h:35`; goopg's
  `internal/wal/xlog_record.go` already reserves 4..7 for
  Database/Tablespace/MultiXact/RelMap) (review BLOCKER-1: an earlier draft said
  15, which is RM_SEQ_ID — a real standby would have misrouted the record to
  seq_redo). Decoded-replay arm = length-check + rewrite
  `base/<dbid>/pg_filenode.map` (or `global/` when dbid=0), recomputing the CRC
  as PG does; goopg may additionally verify the image CRC as a local hardening.
- `internal/initdb/initdb.go`'s three ad-hoc map writers (:136, :1862, :2060)
  unify onto `EncodeRelMapFile` so bootstrap and WAL paths share one encoder.
- Runtime consumption: none initially — goopg's relfile names are OID-keyed and
  never remapped; the map exists for PG-tool/standby fidelity. Documented as
  intentional.

### 5.3 When it is actually needed — and the deferral

Steady-state INSERT/UPDATE/DELETE on a mapped catalog emits **no** relmap record
(doc 02 §4.2.1); relmap is emitted only by relfilenode-changing operations
(VACUUM FULL / CLUSTER / TRUNCATE / REINDEX of a mapped catalog or its indexes —
`RelationMapUpdateMap` call sites via `swap_relation_files`,
`postgres/src/backend/commands/cluster.c:1181`, and
`RelationSetNewRelfilenumber`, `relcache.c:3702` — none of which goopg performs
on catalogs today) and by **CREATE DATABASE under the default WAL_LOG strategy**
(`CREATEDB_WAL_LOG`, `postgres/src/backend/commands/dbcommands.c:741`), whose
`RelationMapCopy` writes the new DB's map WITH a WAL record
(`write_relmap_file(..., write_wal=true)`, `relmapper.c:312`); the FILE_COPY
strategy emits none (the map rides the directory copy) (review MAJOR-1 — an
earlier draft had this inverted). goopg's CREATE DATABASE clone is
file-copy-shaped, so its current behavior maps to FILE_COPY and needs no relmap
record; the WAL_LOG-fidelity question becomes due only when a real PG standby
must construct a goopg-created database from WAL alone. **No B1 catalog depends
on B0.4** (pg_namespace and pg_sequence are unmapped; pg_proc is mapped but only
sees DML). B0.4 is therefore implemented last or deferred with a
deferral-ledger row; this section stays the normative design either way.

## 6. Write-through cache contract (normative)

1. **One operation**: the DDL mutates the in-memory registry and writes the heap
   row (and index entries) inside the same critical section — never "heap now,
   cache on next reload". This is the project's recurring sibling-path hazard
   (doc 02 review MAJOR-5).
2. **Crash consistency**: a crash between WAL append and registry mutation is
   safe — restart rebuilds the registry from the heap via §2. The reverse order
   (registry first, heap write fails) must roll the registry mutation back or
   surface the error to the DDL (no silent divergence).
3. **TIDs**: the cache stores each row's heap TID (§3.3).
4. **Invalidation**: single-process today — no cross-backend invalidation needed.
   The commit-record invalidation messages (Part-A `HAS_INVALS`) remain the
   future multi-backend hook; nothing in B0 changes them.

## 7. Gates (per slice)

| Slice | Gate |
|---|---|
| B0.1 | build/vet; wal + initdb + executor unit suites; crash-recovery tests; **full regress** (visibility-filter blast radius); goopg↔goopg replication smoke. No WAL-visible change ⇒ pg_waldump/e2e not required. |
| B0.2 | full gate + `pg_waldump` decodes `Heap/UPDATE` on relfile 1259 + `TestE2E_FailoverGoopgToPG` + data-dir re-init. |
| B0.3 | full gate + multi-DB regress (CREATE DATABASE → DDL inside it → restart → verify) + data-dir re-init. |
| B0.4 | wal unit round-trip vs the relmapper.c layout; `pg_waldump --rmgr=RelMap`; initdb `pg_filenode.map` byte-diff vs PG's. |

## 8. Risks

- **R-B0-1 (visibility filter)**: any drift in the §2.3 rules empties catalogs on
  standby bootstrap or resurrects aborted DDL. Mitigation: single shared
  function + synthetic-page unit test + full regress in the B0.1 gate.
- **R-B0-2 (ordering)**: a descriptor with a wrong Order breaks dependent-object
  reload only under crash-recovery paths. Mitigation: §2.4 table is normative;
  crash-after-DDL tests per conversion exercise it.
- **R-B0-3 (TID staleness)**: a missed TID refresh turns a later ALTER into an
  update of a dead tuple. Mitigation: `updateHeapRowCanonicalPG` verifies the old
  tuple at oldTID is the live version (xmax==0) and errors loudly otherwise.
- **R-B0-4 (index bootstrap volume)**: CREATE DATABASE cost grows by ~50 btree
  builds. Acceptable: DDL path, PG does the same work via file copy.
