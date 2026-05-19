# M0106-0010 Step 3bv — pg_publication_pubname_index (OID 6111)

## Context

Step 3bu (commit `46ca3f7`) seeded the `pg_publication` heap (OID 6104) as a
nailed local relation so PG-standby boot could open
`RelationBuildDesc(6104)` without FATAL. With the heap row in place, the next
relation PG opens during the post-3bu boot path is the secondary unique
index on `pubname`, `pg_publication_pubname_index` (OID 6111). PG's
`RelationCacheInitializePhase3 → load_critical_index` iterates each declared
index, and `MAKE_SYSCACHE(PUBLICATIONNAME, pg_publication_pubname_index, 8)`
triggers a lookup of OID 6111 during early syscache priming. Without a
pg_class row for OID 6111, `RelationIdGetRelation(6111)` returns NULL and the
backend FATALs with `could not open relation with OID 6111`. The E2E run
`TestE2E_FailoverGoopgToPG/async` (with `GOOPG_RUN_BLOCKED_M0102_E2E=1`)
surfaces this exact message after Step 3bu lands.

## Authoritative source

`postgres/src/include/catalog/pg_publication.h:73`:

```
DECLARE_UNIQUE_INDEX(pg_publication_pubname_index, 6111,
  PublicationNameIndexId, pg_publication, btree(pubname name_ops));
```

`postgres/src/include/catalog/pg_publication.h:76`:

```
MAKE_SYSCACHE(PUBLICATIONNAME, pg_publication_pubname_index, 8);
```

Properties:

* UNIQUE (NOT primary; `DECLARE_UNIQUE_INDEX`, not `_PKEY` variant) →
  `IsUnique=true`, `IsPrimary=false`.
* Single key column on `pubname` (attnum 2 of `pg_publication`; attnum 1 is
  the `oid` system column).
* Opclass `name_ops` (OID 1986); collation `C_COLLATION_OID` (950) — `name`
  columns in catalogs always use C collation.
* Heap is OID 6104 (Step 3bu nailed local rel).
* Sibling PKEY (`pg_publication_oid_index`, OID 6110, on `oid oid_ops`) is
  not seeded here; PG's syscache init order surfaces 6111 first, so 6110 is
  intentionally deferred to the next step.

## Change set

1. `internal/initdb/initdb.go::pgIndexInitialEntries` — append:

   ```go
   entry(6111, 6104, []int16{2}, []uint32{nameOps}, []uint32{cCollation}, true, false)
   ```

   after the Step 3bt `3351 pg_partitioned_table_partrelid_index` row. This
   emits a `Form_pg_index` tuple pinning `(indrelid=6104, indkey=[2],
   indclass=[1986], indcollation=[950], indisunique=true, indisprimary=false)`.

2. `internal/initdb/relcache_init.go::nailedLocalRels` — append the idxSpec

   ```go
   {6111, "pg_publication_pubname_index"}
   ```

   after the Step 3bt `{3351, "pg_partitioned_table_partrelid_index"}` row.
   `flattenRels` + `pgIndexNattsByOID` derive `RelNatts=1` for the nailed
   pg_class row, keeping it consistent with the pg_index tuple's
   `indnatts=1` and clearing PG's `RelationInitIndexAccessInfo` `relnatts
   disagrees with indnatts` assertion (`relcache.c:1492`).

3. `internal/initdb/initdb.go::bootstrapCriticalIndexPlaceholders` — add OID
   6111 to all three critical-index placeholder lists (`base/1`, `base/5`,
   `global/`) so `mdopen(6111)` finds an 8-KiB empty btree root page when
   the nailed-rel pg_class row first resolves the relation. PG's
   `load_critical_index` PANICs without the placeholder file even though no
   tuples are written there at bootstrap (pg_publication is empty at initdb
   time, so the populated btree leaf list is empty).

4. `internal/initdb/pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`
   — add `6111: {2}` to the authoritative map. The strict count guard
   (`len(got) != len(want)`) forces future additions to update this map.

5. `internal/initdb/btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
   — add OID 6111 so the populated 2679 btree must carry a leaf entry for
   the new pg_index tuple.

6. New regression test file
   `internal/initdb/pg_publication_pubname_index_test.go` pinning:

   * `TestNailedLocalRelsContainsPgPublicationPubnameIndex` — verifies the
     `nailedLocalRels` idxSpec entry materialises into the flattened list
     with `RelKind='i'`, `RelNatts=1`.
   * `TestPgPublicationPubnameIndexInitialEntry` — verifies the
     `Form_pg_index` row exists with the expected `(IndRelid, IndKey,
     IsUnique, IsPrimary)` shape.

## Why this is safe

* Pure catalog-seed addition; no encoder / builder / Init flow change.
* Mirrors the proven single-column `name_ops` UNIQUE-NOT-PKEY pattern
  already in service for pg_namespace_nspname_index (2684, Step 3t),
  pg_event_trigger_evtname_index (3467, Step 3as),
  pg_extension_name_index (3081, Step 3ay),
  pg_foreign_data_wrapper_name_index (548, Step 3bc),
  pg_foreign_server_name_index (549, Step 3bf),
  pg_language_name_index (2681, Step 3bj).
* `RelType=0` (index kind) — `flattenRels` zeroes it via `indexNailed`, so
  the Step 3v `tdtypeid==reltype` assertion does not engage for indexes.
* `pg_publication.pubname` is the heap's second column (attnum 2); the
  indkey `[2]` references that attnum exactly per `pg_publication.h` and
  `pg_publication_d.h`.

## Verification

* `go build ./...` — PASS.
* `go test -count=1 -run
  'TestNailedLocalRelsContainsPgPublicationPubnameIndex|TestPgPublicationPubnameIndexInitialEntry|TestNailedLocalRelsContainsPgPublication|TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18'
  ./internal/initdb/` — PASS.
* `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bu (no new regressions).
* `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Follow-up

OID 6111 satisfies `MAKE_SYSCACHE(PUBLICATIONNAME, …)`. The remaining
declared index on `pg_publication` is `pg_publication_oid_index` (OID 6110,
PKEY on `oid oid_ops`, backing `MAKE_SYSCACHE(PUBLICATIONOID, …, 8)`); this
is the expected next FATAL surfaced by `TestE2E_FailoverGoopgToPG/async`
with `GOOPG_RUN_BLOCKED_M0102_E2E=1`, and Step 3bw will name it.
