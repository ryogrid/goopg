# 0106-0010 step 3r — pg_index 2678/2679 PG18 OID correction

## Background

Step 3p (2026-05-18) built a populated 2-page btree at file OID 2678,
intending it to be `pg_index_indexrelid_index` — the index PG uses for
`SearchSysCache1(INDEXRELID, …)`. Step 3q (2026-05-18) then split a
single mis-labelled entry in `pgIndexInitialEntries()` into two rows,
labelling 2678 as `pg_index_indexrelid_index` (`indkey={1}`,
`IsUnique=true`, `IsPrimary=true`) and 2679 as `pg_index_indrelid_index`
(`indkey={2}`, `IsUnique=true`, `IsPrimary=false`).

Both Step 3p and Step 3q got the PG18 OID assignment backwards. The
authoritative mapping in
`postgres/src/include/catalog/pg_index_d.h` is:

```
#define IndexIndrelidIndexId 2678   /* pg_index_indrelid_index */
#define IndexRelidIndexId    2679   /* pg_index_indexrelid_index */
```

And in `postgres/src/include/catalog/indexing.h`:

```
DECLARE_INDEX(pg_index_indrelid_index, 2678, IndexIndrelidIndexId,
              pg_index, btree(indrelid oid_ops));
DECLARE_UNIQUE_INDEX_PKEY(pg_index_indexrelid_index, 2679,
              IndexRelidIndexId, pg_index, btree(indexrelid oid_ops));
```

So:

* OID 2678 = `pg_index_indrelid_index` — keyed on `indrelid` (col 2),
  declared with `DECLARE_INDEX` (NON-UNIQUE, not a primary key).
* OID 2679 = `pg_index_indexrelid_index` — keyed on `indexrelid`
  (col 1), declared with `DECLARE_UNIQUE_INDEX_PKEY` (UNIQUE PRIMARY).

The syscache that early backend startup hits via
`load_critical_index(IndexRelidIndexId, IndexRelationId)` →
`RelationInitIndexAccessInfo` → `SearchSysCache1(INDEXRELID, oid)`
goes through OID 2679, not 2678 (see `pg_index.h:77`:
`MAKE_SYSCACHE(INDEXRELID, pg_index_indexrelid_index, 64);`).

## Symptom

After Step 3q landed, the next E2E re-run
(`TestE2E_FailoverGoopgToPG/async` with
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) reported
`FATAL: column is not in index`, with `nocachegetattr` log preamble
firing for a 34-column relation (pg_class) at attnum 32 and for a
21-column relation (pg_index) at attnum 16–18.

The FATAL is emitted by `genam.c:446` inside `systable_beginscan` when
the caller's `sk_attno` (a heap attnum) is not found in
`irel->rd_index->indkey.values[]`. With the OIDs inverted:

1. PG's `load_critical_index(IndexRelidIndexId=2679, …)` loads the
   index relation at OID 2679 — in goopg this is labelled
   `pg_index_indrelid_index` with `indkey={2}`.
2. The first sysscan against that index from
   `RelationInitIndexAccessInfo`/`SearchSysCache1(INDEXRELID, oid)`
   passes `sk_attno=1` (the `indexrelid` heap column). `genam.c`
   walks `indkey={2}`, never finds `1`, and FATALs.

Equivalently: Step 3p's populated 2-page btree was written to file
OID 2678, but the syscache index PG looks up is OID 2679 — Step 3k's
empty placeholder at file 2679 returns zero rows.

## Fix

`internal/initdb/initdb.go::pgIndexInitialEntries()` swap:

```go
entry(2678, 2610, []int16{2}, []uint32{oidOps}, []uint32{0}, false, false), // pg_index_indrelid_index   (DECLARE_INDEX, non-unique)
entry(2679, 2610, []int16{1}, []uint32{oidOps}, []uint32{0}, true,  true),  // pg_index_indexrelid_index (PKEY)
```

`internal/initdb/relcache_init.go` swap the `idxSpec` labels for
2678/2679 so `nailedLocalRels[OID].RelName` agrees with PG18.

`internal/initdb/btree_index_bootstrap.go::bootstrapPgIndexIndexrelidIndex`
write the populated leaf-root file to `2679` (was `2678`). The file
name is the only data-layer change; the index tuple content
(`pgBuildIndexTupleOidKey` keyed on the `indexrelid` OID) is correct
and reused verbatim. File 2678 keeps its Step-3k empty btree
placeholder because `pg_index_indrelid_index` is not used by any
syscache during early startup.

Empty-placeholder OID lists (`initdb.go:589/671/686`) already
include both 2678 and 2679, so the populated btree correctly
overwrites the placeholder at 2679 while 2678 stays empty.

## Tests

Updated pins (no new files):

* `TestPgIndex2678And2679AreDistinctWithCorrectFlags` — swap expected
  IndKey, IsUnique, IsPrimary so 2678 = (`{2}`, false, false) and
  2679 = (`{1}`, true, true). Comment refreshed to point at
  pg_index_d.h directly.
* `TestNailedLocalRelsContainsPgIndexIndexrelidIndex` — extended to
  guard *both* OIDs and their PG18 labels; previously it only
  checked OID 2678.
* `TestPgIndexInitialEntriesIndkeyMatchesPG18` — swap 2678/2679
  indkeys in the pinned map.
* `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree` — file
  path strings `2678` → `2679` at all three on-disk locations.

Verified:
- `go test -count=1 -run 'TestPgIndex2678|TestNailedLocalRelsContainsPgIndexIndexrelidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedIndexRelnattsAgreesWithIndnatts' ./internal/initdb/`
  PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3q; no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.

## What's next

The E2E re-run (`TestE2E_FailoverGoopgToPG/async`,
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) should advance past
`column is not in index` to either (a) a sysscan against a different
shared-critical index that still has only the Step-3k empty btree —
likely `pg_authid_oid_index` (2677) or `pg_database_oid_index` (2672)
— in which case Step 3s populates those, or (b) a deeper pg_attribute
seed gap for the SHARED catalogs (1262 pg_database, 1260 pg_authid,
1261 pg_auth_members, 3592 pg_shseclabel).
