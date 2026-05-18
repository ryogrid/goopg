# M0106-0010 Step 3bl — pg_operator_oprname_l_r_n_index (OID 2689) catalog seed

## Context

E2E test `TestE2E_FailoverGoopgToPG/async` (gated by
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) drives a real PG18 standby boot from
goopg's bootstrap output. After Step 3bk seeded both pg_language
indexes (2681 name + 2682 oid), the standby surfaces the next
blocker:

```
FATAL: could not open relation with OID 2689
```

OID 2689 = `pg_operator_oprname_l_r_n_index` per
`postgres/src/include/catalog/pg_operator.h:86`:

```c
DECLARE_UNIQUE_INDEX(pg_operator_oprname_l_r_n_index, 2689,
    OperatorNameNspIndexId, pg_operator,
    btree(oprname name_ops, oprleft oid_ops, oprright oid_ops,
          oprnamespace oid_ops));
MAKE_SYSCACHE(OPERNAMENSP, pg_operator_oprname_l_r_n_index, 256);
```

This is the UNIQUE non-PKEY companion to the already-seeded
`pg_operator_oid_index` (2688, _PKEY variant). PG's
`RelationCacheInitializePhase3` loads this index as part of the
relcache scan over pg_operator, specifically when constructing the
OPERNAMENSP syscache, before any user query reaches the executor.

## Root cause

`RelationIdGetRelation(2689)` calls `RelationBuildDesc → ScanPgRelation`
which dispatches through `pg_class_oid_index`. There is no
`Form_pg_class` row for OID 2689 → `ScanPgRelation` returns NULL →
`relation_open` FATALs with "could not open relation with OID 2689".

## Fix

Pure catalog-seed addition mirroring the multi-column UNIQUE non-PKEY
pattern of Step 3y (`pg_amop_fam_strat_index`, 4 oid keys), and Step
3ad (`pg_opclass_am_name_nsp_index`, mixed name_ops + oid_ops). No
encoder, builder, or `Init` flow change.

### `internal/initdb/initdb.go`

`pgIndexInitialEntries` gains:

```go
entry(2689, 2617,
    []int16{2, 8, 9, 3},
    []uint32{nameOps, oidOps, oidOps, oidOps},
    []uint32{cCollation, 0, 0, 0},
    true, false) // UNIQUE, NOT primary
```

- `IndexRelid = 2689` — index OID.
- `IndRelid = 2617` — pg_operator heap OID (already a nailed local rel
  at `relcache_init.go:122`).
- `IndKey = [2, 8, 9, 3]` — heap attnums for `(oprname, oprleft,
  oprright, oprnamespace)`. pg_operator column order per
  `pg_operator.h`: 1=oid, 2=oprname, 3=oprnamespace, 4=oprowner,
  5=oprkind, 6=oprcanmerge, 7=oprcanhash, 8=oprleft, 9=oprright,
  10=oprresult, 11=oprcom, 12=oprnegate, 13=oprcode, 14=oprrest,
  15=oprjoin.
- `IndClass = [name_ops, oid_ops, oid_ops, oid_ops]`.
- `IndCollation = [C_COLLATION, 0, 0, 0]` — `name_ops` carries C
  collation per the convention used in
  `pg_opclass_am_name_nsp_index` (2686), `pg_collation_name_enc_nsp_index`
  (3164), `pg_namespace_nspname_index` (2684).
- `IsUnique = true, IsPrimary = false` — `DECLARE_UNIQUE_INDEX` is
  not the `_PKEY` variant; pg_operator's PKEY is OID 2688.

The three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) need 2689 inserted between the
existing 2688 and 2690 entries.

### `internal/initdb/relcache_init.go`

`nailedLocalRels` idxSpec list gains `{2689, "pg_operator_oprname_l_r_n_index"}`
after the Step 3bk 2682 entry. `flattenRels` + `pgIndexNattsByOID()`
derives `RelKind='i', RelNatts=4`, satisfying PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check
(`relcache.c:1492`).

`pgOperatorAttrs()` already declares the first 10 columns of
pg_operator (`oid` … `oprresult`), so attnums 2, 3, 8, 9 are all
present in the existing TupleDesc — no expansion needed.

## Flow

The seed threads automatically through the existing bootstrap flow:

1. `bootstrapPgClassTuples` writes a `Form_pg_class` row for OID 2689.
2. `bootstrapPgAttributeTuples` writes four pg_attribute rows for the
   four key columns (matching the indexKeyAttrs(4) shape).
3. `bootstrapPgIndexTuples` writes the `Form_pg_index` row and captures
   the heap TID in `pgIndexTIDs[2689]`.
4. `bootstrapPgIndexIndexrelidIndex` adds the leaf for 2689 to the
   populated 2-page btree at `base/{1,5}/2679 + global/2679`.
5. `bootstrapPgClassOidIndex` adds the corresponding leaf at file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds the composite-key leaves
   at file 2659.

The Step-3k empty-btree placeholder at `base/{1,5}/2689 + global/2689`
is sufficient because pg_operator is currently unpopulated (no operator
rows are bootstrapped) — any `SearchSysCache4(OPERNAMENSP, …)` probe
correctly returns no row.

## Regression pins

- `TestPgOperatorOprnameLRNIndexSeededFromInitialEntries` — pins
  `(IndRelid=2617, IndKey=[2,8,9,3], IsUnique=true, IsPrimary=false,
  IndCollation[0]≠0, IndCollation[1..3]=0)`.
- `TestNailedLocalRelsContainsPgOperatorOprnameLRNIndex` — pins
  `(RelName="pg_operator_oprname_l_r_n_index", RelKind='i',
  RelNatts=4)`.

Both in `internal/initdb/pg_operator_oprname_l_r_n_index_test.go`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains
  `2689: {2, 8, 9, 3}` (strict count guard auto-rejects future
  additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 2689 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestPgOperatorOprnameLRNIndex|TestNailed
  LocalRelsContainsPgOperatorOprnameLRNIndex|TestPgIndexInitial
  EntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailed
  IndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcache
  Attrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKey
  Column|TestPgLanguageOidIndex|TestPgLanguageNameIndex'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bk (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

Closes the FATAL "could not open relation with OID 2689" PG-standby
boot blocker.
