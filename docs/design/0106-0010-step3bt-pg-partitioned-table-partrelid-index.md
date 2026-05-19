# M0106-0010 Step 3bt — pg_partitioned_table_partrelid_index (OID 3351)

## Context

Step 3bs (commit `fcf6067`) seeded the `pg_partitioned_table` heap (OID 3350)
as a nailed local relation so PG-standby boot could open
`RelationBuildDesc(3350)` without FATAL. With the heap row in place, the next
relation PG opens during the post-3bs boot path is its primary-key index,
`pg_partitioned_table_partrelid_index` (OID 3351), since
`InitPostgres → RelationCacheInitializePhase3` iterates every catalog/index
declared in `IndexList` order and `find_inheritance_children` /
`get_partition_descendants` reach into `PARTRELID` syscache lookups during
early catalog-load.  Without a pg_class row for OID 3351,
`RelationIdGetRelation(3351)` returns NULL and the backend FATALs with
`could not open relation with OID 3351`.

## Authoritative source

`postgres/src/include/catalog/pg_partitioned_table.h:69`:

```
DECLARE_UNIQUE_INDEX_PKEY(pg_partitioned_table_partrelid_index, 3351,
  PartitionedRelidIndexId, pg_partitioned_table,
  btree(partrelid oid_ops));
```

`postgres/src/include/catalog/pg_partitioned_table.h:71`:

```
MAKE_SYSCACHE(PARTRELID, pg_partitioned_table_partrelid_index, 32);
```

Properties:

* UNIQUE PRIMARY (`_PKEY` macro variant) → `IsUnique=true`, `IsPrimary=true`.
* Single key column on `partrelid` (attnum 1 of `pg_partitioned_table`).
* Opclass `oid_ops` (OID 1981); no collation (oid_ops carries `InvalidOid`).
* Heap is OID 3350 (Step 3bs nailed local rel).
* pg_partitioned_table has **no `oid` system column** — `partrelid` itself is
  the primary key, mirroring `pg_foreign_table.ftrelid` (Step 3bi).

## Change set

1. `internal/initdb/initdb.go::pgIndexInitialEntries` — append:

   ```go
   entry(3351, 3350, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)
   ```

   after the Step 3bo `2755 pg_opfamily_oid_index` row.  This emits a
   `Form_pg_index` tuple pinning `(indrelid=3350, indkey=[1], indclass=[1981],
   indcollation=[0], indisunique=true, indisprimary=true)`.

2. `internal/initdb/relcache_init.go::nailedLocalRels` — append the idxSpec

   ```go
   {3351, "pg_partitioned_table_partrelid_index"}
   ```

   after the Step 3bo `{2755, "pg_opfamily_oid_index"}` row.  `flattenRels` +
   `pgIndexNattsByOID` then derive `RelNatts=1` for the nailed pg_class row,
   keeping it consistent with the pg_index tuple's `indnatts=1` and clearing
   PG's `RelationInitIndexAccessInfo` `relnatts disagrees with indnatts`
   assertion (`relcache.c:1492`).

3. `internal/initdb/initdb.go::bootstrapCriticalIndexPlaceholders` — add OID
   3351 to all three critical-index placeholder lists (base/1, base/5,
   global/) so `mdopen(3351)` finds an 8-KiB empty btree root page when the
   nailed-rel pg_class row first resolves the relation.  PG's
   `load_critical_index` PANICs without the placeholder file even though
   no tuples are written there at bootstrap (pg_partitioned_table is empty
   at initdb time, so the populated btree leaf list is empty).

4. `internal/initdb/pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`
   — add `3351: {1}` to the authoritative map.  The strict count guard
   (`len(got) != len(want)`) forces future additions to update this map.

5. `internal/initdb/btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
   — add OID 3351 so the populated 2679 btree must carry a leaf entry for
   the new pg_index tuple.

6. New regression test file
   `internal/initdb/pg_partitioned_table_partrelid_index_test.go` pinning:

   * `TestPgPartitionedTablePartrelidIndexSeededFromInitialEntries` — verifies
     the `Form_pg_index` row exists with the expected
     `(IndRelid, IndKey, IndClass, IndCollation, IsUnique, IsPrimary)` shape.
   * `TestNailedLocalRelsContainsPgPartitionedTablePartrelidIndex` — verifies
     the `nailedLocalRels` idxSpec entry materialises into the flattened list
     with `RelKind='i'`, `RelNatts=1`.

## Why this is safe

* Pure catalog-seed addition; no encoder / builder / Init flow change.
* Mirrors the proven single-column `oid_ops` UNIQUE PKEY pattern already in
  service for pg_language_oid_index (2682, Step 3bk), pg_opclass_oid_index
  (2687, Step 3l), pg_extension_oid_index (3080, Step 3ax),
  pg_event_trigger_oid_index (3468, Step 3at),
  pg_foreign_data_wrapper_oid_index (112, Step 3bd),
  pg_foreign_server_oid_index (113, Step 3bg),
  pg_foreign_table_relid_index (3119, Step 3bi),
  pg_opfamily_oid_index (2755, Step 3bo),
  pg_parameter_acl_oid_index (6247, Step 3br).
* `RelType=0` (index kind) — `flattenRels` zeroes it via `indexNailed`, so
  the Step 3v `tdtypeid==reltype` assertion does not engage for indexes.
* `pg_partitioned_table.partrelid` is the heap's first non-system column; the
  indkey `[1]` references attnum 1 exactly per `pg_partitioned_table_d.h`.

## Verification

* `go build ./...` — PASS.
* `go test -count=1 -run 'TestPgPartitionedTablePartrelidIndexSeededFromInitialEntries|TestNailedLocalRelsContainsPgPartitionedTablePartrelidIndex|TestNailedLocalRelsContainsPgPartitionedTable|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestBootstrapPgIndexTuples|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestNailedRelTypesMatchPG18FormrdescConstants|TestBootstrapMappedLocalCatalogHeapsIncludesPgPartitionedTable|TestPgClassOidIndexHasSingleKeyColumn' ./internal/initdb/` — PASS.
* `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bs (no new regressions).
* `go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Follow-up

OID 3351 satisfies `MAKE_SYSCACHE(PARTRELID, …)`, which is the only syscache
keyed on `pg_partitioned_table`.  pg_partitioned_table has no other declared
indexes in PG18, so the family is complete after this step.  The next
PG-standby boot FATAL surfaced by `TestE2E_FailoverGoopgToPG/async` (with
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) is expected to come from the next
unsatisfied `RelationIdGetRelation` call after `RelationCacheInitializePhase3`
finishes the pg_partitioned_table family; Step 3bu will name it.
