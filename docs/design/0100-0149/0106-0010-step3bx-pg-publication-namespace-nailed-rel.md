# M0106-0010 Step 3bx — pg_publication_namespace nailed local rel (+ both declared indexes)

## Status

Accepted — landed 2026-05-18.

## Context

After Step 3bw seeded `pg_publication_oid_index` (OID 6110), the
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` standby
boot surfaced the next blocker:

```
FATAL:  could not open relation with OID 6237
```

OID 6237 is `pg_publication_namespace`, declared at
`postgres/src/include/catalog/pg_publication_namespace.h:30`:

```c
CATALOG(pg_publication_namespace,6237,PublicationNamespaceRelationId)
{
    Oid     oid;        /* oid */
    Oid     pnpubid BKI_LOOKUP(pg_publication); /* publication OID */
    Oid     pnnspid BKI_LOOKUP(pg_namespace);   /* namespace OID */
} FormData_pg_publication_namespace;

DECLARE_UNIQUE_INDEX_PKEY(pg_publication_namespace_oid_index, 6238,
    PublicationNamespaceObjectIndexId, pg_publication_namespace,
    btree(oid oid_ops));
DECLARE_UNIQUE_INDEX(pg_publication_namespace_pnnspid_pnpubid_index, 6239,
    PublicationNamespacePnnspidPnpubidIndexId, pg_publication_namespace,
    btree(pnnspid oid_ops, pnpubid oid_ops));

MAKE_SYSCACHE(PUBLICATIONNAMESPACE, pg_publication_namespace_oid_index, 64);
MAKE_SYSCACHE(PUBLICATIONNAMESPACEMAP,
    pg_publication_namespace_pnnspid_pnpubid_index, 64);
```

This step seeds the heap (6237) and both declared indexes (6238 PKEY +
6239 composite UNIQUE) in a single pass — same family-complete pattern
as Steps 3bs/3bt (pg_partitioned_table family), Steps 3bu/3bv/3bw
(pg_publication family). Mirrors the nailed-local-rel pattern: pure
catalog-seed addition, no encoder/builder/Init flow change.

## Design

### (a) Heap row (6237) — `internal/initdb/relcache_init.go`

- Add `pgPublicationNamespaceAttrs()` returning the 3-column PG18
  schema verbatim, all fixed-width NOT NULL: `oid(26/4)`,
  `pnpubid(26/4 → pg_publication)`, `pnnspid(26/4 → pg_namespace)`.
- `nailedLocalRels` gains
  `{6237, "pg_publication_namespace", 83, 'r', 3, false,
    pgPublicationNamespaceAttrs()}`
  after the Step 3bu pg_publication entry.
- `RelType=83` is safe: pg_publication_namespace has no
  `PublicationNamespaceRelation_Rowtype_Id` constant in PG18 headers,
  so Step 3v's `tdtypeid` assertion does not fire.

### (b) Heap-page placeholder — `internal/initdb/initdb.go`

- `bootstrapMappedLocalCatalogHeaps` OID list gains
  `6237, // pg_publication_namespace (M0106-0010 step 3bx)`.
- `localRelMap` gains `{6237, 6237}` analogously.

### (c) PKEY index 6238 — `pgIndexInitialEntries` (initdb.go)

- `entry(6238, 6237, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
  — UNIQUE PRIMARY single `oid_ops` key (no collation) over
  pg_publication_namespace heap OID 6237. `oid` is attnum 1.
- `nailedLocalRels` idxSpec list gains
  `{6238, "pg_publication_namespace_oid_index"}`; `flattenRels` +
  `pgIndexNattsByOID` derives `RelKind='i', RelNatts=1`, satisfying
  the `relnatts==indnatts` check (relcache.c:1492).

### (d) Composite UNIQUE index 6239 — `pgIndexInitialEntries` (initdb.go)

- `entry(6239, 6237, []int16{3, 2}, []uint32{oidOps, oidOps},
    []uint32{0, 0}, true, false)`
  — UNIQUE (NOT primary) composite `(pnnspid, pnpubid)` oid_ops key,
  no collation, over pg_publication_namespace heap OID 6237.
- `nailedLocalRels` idxSpec list gains
  `{6239, "pg_publication_namespace_pnnspid_pnpubid_index"}`;
  `RelKind='i', RelNatts=2`.

### (e) Critical-index placeholder pages — `internal/initdb/initdb.go`

Three placeholder OID lists at `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) each gain
`6238, // pg_publication_namespace_oid_index (Step 3bx)` and
`6239, // pg_publication_namespace_pnnspid_pnpubid_index (Step 3bx)`.
Empty-btree placeholder is sufficient because pg_publication_namespace
is unpopulated at bootstrap (Step-3k `makeBtreeRootPage` with
`btm_root=P_NONE`).

## Regression pins

`internal/initdb/pg_publication_namespace_nailed_test.go`:

- `TestNailedLocalRelsContainsPgPublicationNamespace` — RelName,
  RelKind='r', RelNatts=3, full per-column audit (oid/pnpubid/pnnspid).
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationNamespace`
  — asserts `base/{1,5}/6237` exists, 8 KiB, InitPage-stamped.
- `TestPgPublicationNamespaceOidIndexInitialEntry` — IndRelid=6237,
  IndKey=[1], IndClass=[1981] (oid_ops), IndCollation=[0],
  IsUnique=true, IsPrimary=true.
- `TestPgPublicationNamespacePnnspidPnpubidIndexInitialEntry` —
  IndRelid=6237, IndKey=[3,2], IndClass=[1981,1981], IndCollation=[0,0],
  IsUnique=true, IsPrimary=false.

Existing test extensions:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` gains
  `6238:{1}` + `6239:{3,2}` (strict count guard forces future
  additions to update).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains `6238` and `6239` so the populated 2679 btree must carry
  these leaves.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  gains `6237` (strict list guard).

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgPublicationNamespace|TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationNamespace|TestPgPublicationNamespaceOidIndexInitialEntry|TestPgPublicationNamespacePnnspidPnpubidIndexInitialEntry|TestNailedLocalRelsContainsPgPublication|TestBootstrapMappedLocalCatalogHeapsIncludesPgPublication|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3bw (no new regressions; tracked under
  M0106-0012 etc.).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- E2E re-run with `GOOPG_RUN_BLOCKED_M0102_E2E=1
  TestE2E_FailoverGoopgToPG/async` advances past OID 6237 and surfaces
  the next anticipated blocker `FATAL: could not open relation with OID
  6106` (`pg_publication_rel` — Step 3by territory).

## What's next

With Step 3bx landed, the pg_publication_namespace family is fully
seeded (heap + both declared indexes — UNIQUE PRIMARY oid 6238 and
UNIQUE composite (pnnspid, pnpubid) 6239). The next E2E re-run is
expected to advance past OID 6237 and surface OID 6106
(`pg_publication_rel`) as the next missing nailed catalog FATAL
(Step 3by).
