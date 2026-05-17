# M0106-0010 Step 3af — pg_collation_oid_index (OID 3085)

## Context

After Step 3ae landed the catalog seed for
`pg_collation_name_enc_nsp_index` (OID 3164), the next
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` re-run
produced a fresh blocker:

```
FATAL: could not open relation with OID 3085
```

repeated on every backend the standby's postmaster forked.

## Authoritative source

`postgres/src/include/catalog/pg_collation.h:63`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_collation_oid_index, 3085,
    CollationOidIndexId, pg_collation, btree(oid oid_ops));
```

`pg_collation_d.h`:

| attnum | name                  | type |
| ------ | --------------------- | ---- |
| 1      | oid                   | oid  |
| 2      | collname              | name |
| 3      | collnamespace         | oid  |
| ...    | ...                   | ...  |

So OID 3085 is the `COLLOID` syscache backing index on pg_collation
(OID 3456): one column, UNIQUE PRIMARY (`_PKEY` variant). Sibling of
3164 (the composite UNIQUE non-PKEY index seeded by Step 3ae).

## Fix

Pure catalog-seed addition mirroring Step 3ab (`pg_cast_oid_index`),
Step 3l (`pg_opclass_oid_index`), and Step 3ae's pattern. No encoder,
builder, or `Init` flow change.

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains

```go
entry(3085, 3456, []int16{1},
    []uint32{oidOps},
    []uint32{0},
    true, true), // pg_collation_oid_index
```

— single oid_ops key (attnum 1 = `oid`); no collation; UNIQUE PRIMARY
(`IsUnique=true`, `IsPrimary=true`). Same single-column oid PKEY pattern
as `pg_cast_oid_index` (2660), `pg_opclass_oid_index` (2687), and every
other catalog `*_oid_index`.

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{3085, "pg_collation_oid_index"}`. `flattenRels` consults
`pgIndexNattsByOID()` (returns 1 for OID 3085) so the nailed rel
carries `RelKind='i', RelNatts=1` and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check passes.

(c) The three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) already include `3085` from an
earlier sweep — no edit needed. The Step-3k empty btree placeholder
(`btm_root = P_NONE`) is sufficient because pg_collation is currently
unpopulated (no collation rows are bootstrapped), so a zero-row oid
lookup is the expected outcome.

The seed threads automatically through `bootstrapPgClassTuples` →
`bootstrapPgAttributeTuples` (1 indexKeyAttrs row) →
`bootstrapPgIndexTuples` (writes Form_pg_index row with indnatts=1 +
captures TID in `pgIndexTIDs[3085]`) →
`bootstrapPgIndexIndexrelidIndex` (leaf at file 2679) →
`bootstrapPgClassOidIndex` (leaf at 2662) →
`bootstrapPgAttributeRelidAttnumIndex` (1 composite-key leaf at 2659).

## Tests

New pins in
`internal/initdb/pg_collation_oid_index_test.go`:

- `TestPgCollationOidIndexSeededFromInitialEntries` — asserts
  `(IndRelid=3456, IndKey=[1], IsUnique=true, IsPrimary=true,
   IndCollation=[0])`.
- `TestNailedLocalRelsContainsPgCollationOidIndex` — asserts
  `RelName="pg_collation_oid_index", RelKind='i', RelNatts=1`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `3085: {1}` (strict
  count guard so future additions cannot bypass the registry).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 3085, requiring the populated 2679 btree to carry this
  leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgCollationOidIndex|TestNailedLocalRelsContainsPgCollationOidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestPgCollationNameEncNspIndex'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ae (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` —
  advances past the "could not open relation with OID 3085" FATAL to
  the next blocker: `FATAL: could not open relation with OID 2607`
  (`pg_class_tblspc_relfilenode_index`, the next missing nailed
  index). That OID will be addressed by Step 3ag.

## Next blocker (Step 3ag)

OID 2607 — `pg_class_tblspc_relfilenode_index`, per
`postgres/src/include/catalog/pg_class.h`. Single composite index on
`(reltablespace, relfilenode)`. The catalog-seed pattern is identical
to this step; the new wrinkle is two new oid_ops keys on existing
pg_class attnums.
