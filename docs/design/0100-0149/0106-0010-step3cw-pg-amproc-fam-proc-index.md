# M0106-0010 Step 3cw — Populate pg_amproc_fam_proc_index (OID 2655)

## Status
Accepted — 2026-05-18.

## Context

Step 3cv (pg_shseclabel PG18 4-column schema fix) cleared the persistent
`invalid attalign value:` FATAL in `populate_compact_attribute_internal`.
The next PG-standby boot blocker from
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` is:

```
FATAL: XX000: missing support function 1 for attribute 1 of index "pg_authid_rolname_index"
```

emitted from `postgres/src/backend/access/index/indexam.c:946`:

```c
if (!RegProcedureIsValid(procId))
    elog(ERROR, "missing support function %d for attribute %d of index \"%s\"",
         procnum, attnum, RelationGetRelationName(irel));
```

This fires when `irel->rd_support[procindex] == InvalidOid`. The
`rd_support` array is filled during `IndexSupportInitialize` via a
sysscan of `pg_amproc_fam_proc_index` (PG18 OID 2655) keyed on
`(amprocfamily, amproclefttype, amprocrighttype, amprocnum)`. Step 3k's
empty btree placeholder (`btm_root = P_NONE`) returns zero rows for
every probe, so PG stores `InvalidOid` in every `rd_support` slot and
FATALs the moment any index dispatch reaches its comparison function.

`pg_authid_rolname_index` is the first dispatch target — it is opened
early in `InitPostgres` for client-auth role lookups.

## Why nothing earlier surfaced this

Steps 3l/3m/3o/3p/3r/3y populated `pg_class_oid_index` (2662),
`pg_attribute_relid_attnum_index` (2659), `pg_index_indexrelid_index`
(2679), `pg_opclass_oid_index` (2687), `pg_database_oid_index` (2672),
and registered `pg_amop_fam_strat_index` (2653) / `pg_amproc_fam_proc_index`
(2655) as nailed rels but only seeded the heap rows — Step 3y
explicitly noted the 4-column composite-key encoder was deferred for
amop/amproc because the AMOPSTRATEGY syscache tolerates a zero-row
result at planning time.

The `AMPROC` lookup is different. It is unconditionally required by
`IndexSupportInitialize` for every nailed index that PG opens. Zero
rows is fatal, not tolerated.

## Decision

Populate `pg_amproc_fam_proc_index` (OID 2655) with a 2-page btree
(metapage + populated leaf-root) carrying one composite-key IndexTuple
per `pg_amproc` heap row produced by `bootstrapPgAmprocTuples`.

The composite key has four columns:

| col | type   | opclass    |
|-----|--------|------------|
|  1  | oid    | oid_ops    | amprocfamily
|  2  | oid    | oid_ops    | amproclefttype
|  3  | oid    | oid_ops    | amprocrighttype
|  4  | int2   | int2_ops   | amprocnum

This is goopg's first 4-column composite-key IndexTuple. Layout (no
nulls, all fixed-width):

```
[0..1]   ip_blkid.bi_hi  (heapBlk>>16, LE uint16)
[2..3]   ip_blkid.bi_lo  (heapBlk&0xFFFF, LE uint16)
[4..5]   ip_posid        (heapOff, LE uint16)
[6..7]   t_info          (size_low_13_bits = 24, no flags)
[8..11]  amprocfamily    (LE uint32)
[12..15] amproclefttype  (LE uint32)
[16..19] amprocrighttype (LE uint32)
[20..21] amprocnum       (LE int16)
[22..23] MAXALIGN padding (zero)
```

Total = `MAXALIGN(IndexTupleHeader + 4 + 4 + 4 + 2) = MAXALIGN(22) = 24`.

`pgAmprocInitialEntries` currently emits 36 rows. At 24 bytes/tuple +
4 bytes per line pointer = 28 bytes/item × 36 = ~1 KiB — well below
the 8152-byte single-leaf payload limit. The simple
`pgBuildBtreeLeafRootPage` builder is sufficient; the bulk-load builder
(currently 16-byte-only) does not need to be generalised in this step.

## Implementation

1. **New encoder** in `internal/initdb/btree_index_bootstrap.go`:
   `pgBuildIndexTupleOidOidOidInt2Key(heapBlk, heapOff, family, lefttype, righttype, num)`
   returning the 24-byte tuple above.

2. **New bootstrap** in same file:
   `bootstrapPgAmprocFamProcIndex(dataDir string, tids []heapTID) error`
   - Walks `pgAmprocInitialEntries()`, pairs each row with its heapTID.
   - Sorts ascending lexicographic on `(family, lefttype, righttype, num)`.
   - Calls `pgBuildBtreeLeafRootPage(tuples)` + `pgBuildBtreeMetapageWithRoot(1, 0)`.
   - Writes the 2-block file to `base/{1,5}/2655` + `global/2655`.

3. **Heap-bootstrap signature change**:
   `bootstrapPgAmprocTuples` now returns `([]heapTID, error)` instead
   of just `error`. Single existing test call updated.

4. **Init wiring**:
   `Init` captures `pgAmprocTIDs, err := bootstrapPgAmprocTuples(abs)`
   and calls `bootstrapPgAmprocFamProcIndex(abs, pgAmprocTIDs)`
   immediately after.

No empty-placeholder list changes: 2655 is already in the three
`bootstrapPostgresDatabase` lists (base/1/, base/5/, global/) from
prior steps — the populated file overwrites the placeholder.

## Why this is safe / scope-bounded

- The 36 rows in `pgAmprocInitialEntries` are deterministic — every
  pinned default opclass's cmp + sortsupport (where shipped) +
  equalimage entries plus the integer_ops cross-type cmp procs.
- The sort comparator is the canonical 4-key btree lex order PG uses
  for the AMPROC syscache — `_bt_compare` walks (family, left, right, num)
  with the matching opclasses (oid_ops, oid_ops, oid_ops, int2_ops).
- No encoder shared by other indexes is touched.
- The 16-byte bulk-load builder is unchanged; future steps that need
  composite-key indexes >407 entries will generalise it.

## Verification

- `go test -count=1 -run 'TestPgBuildIndexTupleOidOidOidInt2Key|TestBootstrapPgAmprocFamProcIndex|TestBootstrapPgAmprocTuples' ./internal/initdb/` — PASS (3/3).
- `go test -count=1 ./internal/initdb/` — same 15 pre-existing
  baseline failures as Step 3cv (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`:
  the `missing support function` FATAL is gone. New blocker is
  `FATAL: 28000: role "ryo" does not exist` — `InitPostgres →
  InitializeSessionUserId` (miscinit.c:802) cannot find the OS-user
  role in `pg_authid` because goopg's bootstrapped `pg_authid` only
  seeds `postgres` / no test-user row exists. Step 3cx territory.

## Regression pins

In `internal/initdb/pg_amproc_fam_proc_index_test.go`:

- `TestPgBuildIndexTupleOidOidOidInt2KeyLayoutMatchesPG18`
  byte-exact 24-byte layout with `bi_hi`/`bi_lo` split (catches the
  Step-3s LE-uint32 trap), `t_info=0x0018`, zero MAXALIGN pad.
- `TestBootstrapPgAmprocFamProcIndexWritesPopulatedBtree`
  end-to-end: file = 2 blocks at all three on-disk locations;
  metapage `btm_root == 1`; leaf line-pointer count == 36; mandatory
  presence of (family=1976, left=23, right=23, num=1) `btint4cmp`
  AND (family=1994, left=19, right=19, num=1) `btnamecmp` rows — the
  latter is the one whose absence triggered the Step 3cw FATAL.

## Carry-over

- `pg_amop_fam_strat_index` (OID 2653) is still an empty placeholder.
  It is tolerated by the AMOPSTRATEGY syscache at boot time but will
  need population when an actual planner-path index scan over
  cross-type strategies surfaces. The same 4-column composite-key
  builder added in this step can be reused (the keys are
  `amopfamily, amoplefttype, amoprighttype, amopstrategy`,
  identical layout).
- The bulk-load builder is still hard-coded to 16-byte tuples.
  A future step that needs >292 rows in a 24-byte composite-key
  index will generalise it.
