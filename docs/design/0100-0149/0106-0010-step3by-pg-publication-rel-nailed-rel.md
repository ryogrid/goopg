# M0106-0010 Step 3by — pg_publication_rel nailed local rel + 3 indexes

## Blocker

After Step 3bx seeded the `pg_publication_namespace` family (heap 6237 +
PKEY 6238 + composite 6239), the next PG-standby boot FATAL surfaced as:

```
FATAL: could not open relation with OID 6106
```

OID 6106 is `pg_publication_rel` per
`postgres/src/include/catalog/pg_publication_rel.h:29`:

```c
CATALOG(pg_publication_rel,6106,PublicationRelRelationId)
{
    Oid         oid;
    Oid         prpubid BKI_LOOKUP(pg_publication);
    Oid         prrelid BKI_LOOKUP(pg_class);

#ifdef  CATALOG_VARLEN
    pg_node_tree prqual;
    int2vector   prattrs;
#endif
} FormData_pg_publication_rel;
```

Three indexes are declared upstream:

| OID | Name | Kind | Key |
|-----|------|------|-----|
| 6112 | `pg_publication_rel_oid_index` | UNIQUE PRIMARY | `(oid oid_ops)` |
| 6113 | `pg_publication_rel_prrelid_prpubid_index` | UNIQUE | `(prrelid oid_ops, prpubid oid_ops)` |
| 6116 | `pg_publication_rel_prpubid_index` | non-UNIQUE | `(prpubid oid_ops)` |

6112 backs `MAKE_SYSCACHE(PUBLICATIONREL, …, 64)`, 6113 backs
`MAKE_SYSCACHE(PUBLICATIONRELMAP, …, 64)`, 6116 has no syscache (used
by `GetPublicationRelations()` to enumerate relations belonging to a
publication via `systable_beginscan`).

## Fix

Family-complete seed in one step, mirroring the Step-3bx pattern for
`pg_publication_namespace` (heap + 2 indexes) but with three indexes
instead of two — and the first **non-UNIQUE** index entry pinned in
`pgIndexInitialEntries`.

### (a) `pgPublicationRelAttrs()` (`relcache_init.go`)

Returns the 5-column PG18 schema verbatim. First 3 columns are
fixed-width NOT NULL (oid, prpubid, prrelid as oid/4); last 2 are
CATALOG_VARLEN nullable: `prqual` is `pg_node_tree` (TypeOID 194,
Len -1) and `prattrs` is `int2vector` (TypeOID 22, Len -1). Neither
varlena column carries `BKI_FORCE_NOT_NULL` upstream so `NotNull=false`.
The heap is unpopulated at bootstrap so the varlena encoder is not
exercised; both types are already covered by `pgCatalogTypeOID` and
`pgCatalogTypeLen` from earlier steps.

`nailedLocalRels` gains
`{6106, "pg_publication_rel", 83, 'r', 5, false, pgPublicationRelAttrs()}`
after the Step-3bx pg_publication_namespace entry. RelType=83 is safe —
no `PublicationRelRelation_Rowtype_Id` in PG18 headers, so Step 3v's
tdtypeid assertion does not fire.

### (b) `bootstrapMappedLocalCatalogHeaps` (`initdb.go`)

OID list gains `6106, // pg_publication_rel (M0106-0010 step 3by)`
after the Step-3bx 6237 entry. `localRelMap` gains `{6106, 6106}`
analogously. (The legacy `6101, // pg_publication_rel` placeholder
remains untouched — same pattern as Step 3bu leaving the stale `6003`
comment alone.)

### (c) `pgIndexInitialEntries` (`initdb.go`)

Three entries appended after the Step-3bx 6239 row:

```go
entry(6112, 6106, []int16{1},    []uint32{oidOps},        []uint32{0},   true,  true)  // PKEY oid
entry(6113, 6106, []int16{3, 2}, []uint32{oidOps,oidOps}, []uint32{0,0}, true,  false) // UNIQUE (prrelid, prpubid)
entry(6116, 6106, []int16{2},    []uint32{oidOps},        []uint32{0},   false, false) // non-UNIQUE prpubid
```

### (d) `nailedLocalRels` idxSpec list (`relcache_init.go`)

Three `idxSpec` entries added after the Step-3bx 6239 row so
`flattenRels` + `pgIndexNattsByOID` derive `RelKind='i'`,
`RelNatts=1/2/1` and the `relnatts == indnatts` check
(`relcache.c:1492`) passes for each.

### (e) Critical-index placeholder OID lists (`initdb.go`)

All three OIDs (6112, 6113, 6116) added to both the `base/{1,5}/`
loop and the `global/` loop at `bootstrapPostgresDatabase`. Empty-btree
placeholder is sufficient because pg_publication_rel is unpopulated
at bootstrap — no SearchSysCache lookups against PUBLICATIONREL or
PUBLICATIONRELMAP can match.

## Regression pins

Per the documented per-loop pattern:

- `TestNailedLocalRelsContainsPgPublicationRel` — 5-column shape,
  RelKind/RelNatts/Attrs pinned to PG18.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgPublicationRel` —
  empty-heap placeholder at `base/{1,5}/6106` is non-zero and
  `BlockSize`-sized.
- `TestPgPublicationRelOidIndexInitialEntry` — PKEY pinned.
- `TestPgPublicationRelPrrelidPrpubidIndexInitialEntry` — UNIQUE
  composite pinned.
- `TestPgPublicationRelPrpubidIndexInitialEntry` — non-UNIQUE single
  pinned (first non-UNIQUE entry for this family — `IsUnique=false`
  guard is meaningful).
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` extended with
  `6112:{1}`, `6113:{3,2}`, `6116:{2}` plus the strict count guard
  (forces new additions to also update the pin).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 6112 + 6113 + 6116.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  extended with 6106 (strict list guard).

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgPublicationRel|…|TestPgPublicationRelPrpubidIndexInitialEntry|TestPgIndexInitialEntriesIndkeyMatchesPG18|…'
  ./internal/initdb/` — all targeted tests PASS.
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures unchanged from Step 3bx (no new regressions).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.

## Next blocker

`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
should advance past OID 6106 and surface the next anticipated blocker
in the `pg_publication_*` / `pg_subscription_*` family of nailed
relations (Step 3bz territory).
