# 02a — Phase-B0 enablers: detailed design

| | |
|---|---|
| Status | draft — pending agent review |
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
// converted catalog; loadUserTablesFromHeapForDB becomes the pg_class +
// pg_attribute pair of descriptors.
type catalogReloadDesc struct {
    Name   string // "pg_namespace" — log/error labels only
    RelOid uint32 // 2615
    Shared bool   // true → heap lives in global/<oid> (B4); false → base/<dbOid>/<oid>
    Order  int    // explicit total order within a reload pass; see §2.4

    // Decode converts one visible PG-canonical heap tuple into a typed row.
    // Header-driven (t_hoff, null bitmap) — never assumes a fixed layout.
    Decode func(ht storage.HeapTuple) (any, error)

    // Apply re-registers the row into the in-memory catalog. nsDBOid keeps
    // the loadUserTablesFromHeapForDB per-DB registration semantics.
    Apply func(cat *catalog.InMemory, nsDBOid uint32, row any) error
}

// reloadCatalogHeaps runs every descriptor (sorted by Order) against
// base/<heapDBOid>/ (or global/ for Shared descs), applying rows into the
// namespace selected by nsDBOid. It is the single generic replacement for
// the 26 bespoke *_ddl_recovery.go scanners.
func reloadCatalogHeaps(mgr *storage.Manager, cat *catalog.InMemory,
    clog *mvcc.CLog, heapDBOid, nsDBOid uint32, descs []catalogReloadDesc) error
```

### 2.3 The shared scan loop — visibility rules preserved verbatim

The loop factored out of `loadUserTablesFromHeapForDB` is: `mgr.NBlocks` →
`ReadBlock` → `PageLinePointerCount` → `PageGetHeapTuple` per slot, then a
tuple-visibility filter before `Decode`. The filter is the part that MUST be
carried over byte-for-byte (it encodes two subtle recovery facts,
`open.go:2754-2771`):

1. **CLOG-backed xmin/xmax test** — a row is live iff xmin committed and xmax is
   0 / aborted, consulted against the recovered CLOG (not the in-memory arrays,
   which are empty at startup).
2. **Basebackup out-of-range-xmin pass-through + aborted-only filter** — rows
   whose xmin lies beyond the recovered CLOG horizon (a basebackup shipped from a
   primary whose clog is ahead) are treated as live unless explicitly aborted.
   Dropping this rule silently empties catalogs on a standby bootstrap.

Both rules live in ONE function used by every descriptor; a unit test pins them
with a synthetic page (committed/aborted/in-progress/out-of-range xmin rows).

### 2.4 Ordering (risk R5 made explicit)

Today the recovery-pass order is hand-wired by call sequence in
`internal/initdb/open.go:1233-1547` (schema scanner at :1233 runs before table
loads at :1364/:1445; sequences/defaults at :1498-1547 run after). The framework
replaces comment-enforced order with `Order` constants and a table in this doc
that every descriptor must cite:

| Order | Catalog | Must precede | Why |
|---|---|---|---|
| 10 | pg_namespace | everything below | schema OID → name map needed to register any schema-qualified object |
| 20 | pg_class | pg_attribute, pg_proc, pg_sequence | relation OIDs referenced everywhere |
| 30 | pg_attribute | (paired with pg_class) | column defs complete table registration |
| 40 | pg_proc | pg_aggregate (B2) | functions before objects that reference them |
| 50 | pg_sequence | — | needs pg_class rows for seqrelid |
| 90 | (B2+ catalogs slot in between as they convert) | | each conversion adds its row here |

During the transition window a converted catalog's descriptor and the remaining
bespoke scanners coexist; the descriptor list and the scanner call sequence in
`open.go` are interleaved by the same ordering constants so relative order never
changes as catalogs migrate.

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
(pg_class precedent: `classTID` at `operators_ddl.go:13181`) and refreshed by
each UPDATE's returned TID; the reload scan re-seeds TIDs at startup. Key-scan
re-location is rejected as the default (a per-DDL heap scan, and a second code
path to keep in sync).

### 3.4 Proof obligation in B0.2

The slice lands with ONE existing pg_class ALTER re-sync path (e.g. table rename
re-sync) converted from delete+insert to `updateHeapRowCanonicalPG`, so the gate
can assert `pg_waldump` shows `Heap/UPDATE` on relfile 1259 and a real PG standby
replays it.

## 4. B0.3 — per-DB catalog index bootstrap at CREATE DATABASE

Today only DefaultDBOid has catalog btree files; `syncTableToCatalogHeap` skips
index maintenance entirely for other DBs (`operators_ddl.go:13185-13215`), and
`CREATE DATABASE` creates catalog heaps but no indexes. PG creates a full catalog
(heaps + indexes) in every database by copying the template.

Change:
1. `CREATE DATABASE` (template-clone path in `internal/server/database_ddl.go` /
   its executor arm) bulk-builds every base-catalog btree present in the template
   into `base/<newDbOid>/`, reusing `pgBuildBtreeLeafRootPage` /
   `pgBuildBtreeBulkLoad` (`internal/initdb/btree_index_bootstrap.go:149/:226`)
   over the just-copied heap contents.
2. The DefaultDBOid guard in `syncTableToCatalogHeap` is removed; runtime index
   inserts route to `base/<ctx.dbOid>/<indexOid>` unconditionally.
3. The startup loader keeps scanning heaps directly (indexes are for PG-tool
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

Written atomically (write temp + rename, `write_relmap_file`). The WAL record is
`xl_relmap_update{ Oid dbid; Oid tsid; int32 nbytes; char data[]; }`
(`postgres/src/include/utils/relmapper.h:27-33`, opcode `XLOG_RELMAP_UPDATE =
0x00`, RM_RELMAP_ID) where `data` is the entire new RelMapFile image; redo
(`relmap_redo`) CRC-checks and rewrites the target file.

### 5.2 goopg design

- New `internal/wal/relmap.go`: `EncodeRelMapFile(mappings)` (layout above, CRC32C
  via the existing Castagnoli table), `EncodeRelmapUpdatePG(dbid, tsid, image)`
  via `framePGAssembled(RmgrRelMap, 0x00, xid, body)` (add `RmgrRelMap = 15` to
  the rmgr table), decoded-replay arm = CRC-verify + rewrite
  `base/<dbid>/pg_filenode.map` (or `global/` when dbid=0).
- `internal/initdb/initdb.go`'s three ad-hoc map writers (:136, :1862, :2060)
  unify onto `EncodeRelMapFile` so bootstrap and WAL paths share one encoder.
- Runtime consumption: none initially — goopg's relfile names are OID-keyed and
  never remapped; the map exists for PG-tool/standby fidelity. Documented as
  intentional.

### 5.3 When it is actually needed — and the deferral

Steady-state INSERT/UPDATE/DELETE on a mapped catalog emits **no** relmap record
(doc 02 §4.2.1); only relfilenode-changing operations (VACUUM FULL / CLUSTER /
TRUNCATE of a mapped catalog — none of which goopg performs on catalogs today)
and `CREATE DATABASE` file-copy bootstrap fidelity require it. **No B1 catalog
depends on it** (pg_namespace and pg_sequence are unmapped; pg_proc is mapped
but only sees DML). B0.4 is therefore implemented last or deferred with a
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
