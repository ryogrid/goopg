# M0106-0010 Step 3co — Seed `pg_ts_template` (OID 3764) + indexes 3766/3767

## Goal

Close the FATAL `could not open relation with OID 3764` PG-standby boot
blocker that surfaces immediately after Step 3cn seeded the
`pg_ts_parser` family (OID 3601). `pg_ts_template` is the text-search
template catalog. It is opened during PG-standby boot via
`RelationCacheInitializePhase3` →
`load_critical_index(TSTemplateOidIndexId, TSTemplateRelationId)`.

## Authoritative Upstream Reference

`postgres/src/include/catalog/pg_ts_template.h`

```c
CATALOG(pg_ts_template,3764,TSTemplateRelationId)
{
    Oid         oid;            /* oid */
    NameData    tmplname;
    Oid         tmplnamespace BKI_DEFAULT(pg_catalog) BKI_LOOKUP(pg_namespace);
    regproc     tmplinit BKI_LOOKUP_OPT(pg_proc);
    regproc     tmpllexize BKI_LOOKUP(pg_proc);
} FormData_pg_ts_template;

DECLARE_UNIQUE_INDEX(pg_ts_template_tmplname_index, 3766,
    TSTemplateNameNspIndexId, pg_ts_template,
    btree(tmplname name_ops, tmplnamespace oid_ops));
DECLARE_UNIQUE_INDEX_PKEY(pg_ts_template_oid_index, 3767,
    TSTemplateOidIndexId, pg_ts_template, btree(oid oid_ops));

MAKE_SYSCACHE(TSTEMPLATENAMENSP, pg_ts_template_tmplname_index, 2);
MAKE_SYSCACHE(TSTEMPLATEOID, pg_ts_template_oid_index, 2);
```

Per-database (non-shared) catalog. 5-column schema, all fixed-width
NOT NULL. `tmplinit` uses `BKI_LOOKUP_OPT` — the target proc may be
`InvalidOid` but the column itself is `NOT NULL` (value 0 when
absent), mirroring the `prsheadline` pattern in `pg_ts_parser`.

## Historical Note (Stale Placeholder Reclamation)

Prior to Step 3co the OIDs 3766/3767 were sitting in
`bootstrapMappedLocalCatalogHeaps` and the `bootstrapPostgresDatabase`
heap-placeholder OID list as "stale placeholders" with comments
mislabeling them as `pg_ts_dict` (Step 3cm comment) and `pg_ts_parser`
(Step 3cn comment), respectively. Both prior comments asserted that
3766/3767 "have no upstream catalog assignment". That assertion was
factually incorrect — 3766/3767 are the canonical upstream OIDs for
`pg_ts_template_tmplname_index` / `pg_ts_template_oid_index`. Step 3co
corrects the mislabel by:

1. Updating the heap-placeholder comments to flag 3766/3767 as the
   pg_ts_template indexes and noting that their heap-page placeholders
   are overwritten with a btree root page in the critical-index block
   that runs immediately afterwards. The empty-heap-page write
   becomes a harmless transient state (the btree root page wins).
2. Re-purposing the comment on the 3768 placeholder to flag that OID
   *truly* has no upstream catalog assignment (`pg_ts_template.h`
   only consumes 3764/3766/3767).

## Implementation

### (a) `pgTsTemplateAttrs()` (`internal/initdb/relcache_init.go`)

5-column descriptor returning the verbatim PG18 schema:

| attnum | name           | typeoid | typlen | notnull |
|--------|----------------|---------|--------|---------|
| 1      | oid            | 26      | 4      | true    |
| 2      | tmplname       | 19      | 64     | true    |
| 3      | tmplnamespace  | 26      | 4      | true    |
| 4      | tmplinit       | 24      | 4      | true    |
| 5      | tmpllexize     | 24      | 4      | true    |

### (b) `nailedLocalRels` extension (`relcache_init.go`)

Heap list gains `{3764, "pg_ts_template", 83, 'r', 5, false,
pgTsTemplateAttrs()}` after the Step 3cn `{3601, "pg_ts_parser", …}`
entry. `RelType=83` is safe — no `TSTemplateRelation_Rowtype_Id`
constant exists in PG18 headers (only
pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
are formrdesc'd shared rels).

`idxSpec` list gains `{3766, "pg_ts_template_tmplname_index"}` and
`{3767, "pg_ts_template_oid_index"}` after the Step 3cn `{3607,
"pg_ts_parser_oid_index"}` entry.

### (c) `pgIndexInitialEntries()` (`internal/initdb/initdb.go`)

Local section gains:

```go
entry(3766, 3764, []int16{2, 3}, []uint32{nameOps, oidOps},
      []uint32{cCollation, 0}, true, false)  // tmplname_index
entry(3767, 3764, []int16{1}, []uint32{oidOps},
      []uint32{0}, true, true)               // oid_index
```

`nameOps` (1986), `oidOps` (1981), `cCollation` (950) are already
defined in the package. `tmplname` is a NameData column and uses
`C_COLLATION_OID` (catalog `name` columns use C collation). The
`oid` column uses no collation (0).

### (d) Critical-index placeholder pages

Both 3766 and 3767 are added to the two "Critical index placeholder
pages" blocks in `bootstrapPostgresDatabase`:

1. The per-database block writing `base/<dboid>/<oid>` for `dboid ∈
   {1, 5}`.
2. The `global/` fallback block — PG's formrdesc may use `InvalidOid`
   for `dbNode` on nailed relations, causing lookups in `global/`
   instead of `base/<dboid>/`.

The btree root page (`makeBtreeRootPage()`) overwrites the heap
placeholder page left by `bootstrapMappedLocalCatalogHeaps`, which
is the desired final state.

### (e) `bootstrapMappedLocalCatalogHeaps` (`initdb.go`)

`oids` slice gains `3764` (the authoritative `pg_ts_template` OID per
`pg_ts_template.h:29`); `localRelMap` gains `{3764, 3764}`. The
pre-existing 3766/3767 entries remain in both lists with corrected
comments (they are now the legitimate index OIDs, with their heap
placeholder pages overwritten by btree root pages in the
critical-index block).

The 3768 placeholder retains its comment, now updated to note
"3768 has no upstream catalog assignment".

### (f) Type-helper registration

No new type-helper entries needed. `oid` (26), `name` (19), and
`regproc` (24) are all already registered in `pgCatalogTypeOID` /
`pgCatalogTypeLen` / `pgTypeByVal` / `pgTypeAlignChar` /
`pgTypeStorageChar` (regproc was wired in Step 3a for pg_proc).

## Regression Tests

New file `internal/initdb/pg_ts_template_nailed_test.go`:

- `TestNailedLocalRelsContainsPgTsTemplate` — pins the heap entry
  for OID 3764 plus every column descriptor.
- `TestNailedLocalRelsContainsPgTsTemplateIndexes` — pins the two
  index entries (3766 / 3767) and verifies `RelKind='i'` and
  `RelNatts` (2 / 1).
- `TestPgTsTemplateIndexInitialEntries` — pins the
  `pgIndexInitialEntries` rows for 3766 / 3767, including IndKey,
  IndClass, IndCollation, IsUnique, IsPrimary.
- `TestPgTsTemplateAttrsTypeOIDsMatchPG18` — pins the
  `pgTsTemplateAttrs()` schema verbatim.

Existing tests extended:

- `internal/initdb/pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`
  gains `3766:{2,3}` and `3767:{1}` (strict count guard catches
  silent drops or extra entries).
- `internal/initdb/btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree`
  `mustHave` list gains 3766 and 3767.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run '<targeted>' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures (same as Step 3cn). No new regressions.
- Cross-package smoke
  `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next Blocker

The next FATAL on PG-standby boot will surface the next missing
catalog OID. Likely candidates (alphabetically following
`pg_ts_template` in the cache-load order): `pg_user_mapping` (1418)
or another remaining unseeded local rel. To be determined by next
E2E reproduction.
