# 0106-0010 Step 3an — Seed `pg_enum` (OID 3501) nailed rel

Status: accepted (2026-05-18)
Milestone: M0106-0010
Predecessor: Step 3am (`pg_default_acl_oid_index`)

## Problem

After Step 3am closed the last `pg_default_acl` companion index, the next
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` re-run
surfaces a new PG-standby boot blocker: every forked client backend
FATALs with

```
FATAL:  could not open relation with OID 3501
```

repeated on every connection attempt from the test harness's
`psql -c "SELECT status FROM pg_catalog.pg_stat_wal_receiver"` probe.

OID 3501 is `pg_enum` per
`postgres/src/include/catalog/pg_enum.h:32`:

```
CATALOG(pg_enum,3501,EnumRelationId)
```

`pg_enum` stores values of enum types (`CREATE TYPE … AS ENUM (…)`). It is
opened during backend `InitPostgres` via the standard relcache lookup path
(any catcache for `ENUMOID` / `ENUMTYPOIDNAME` indirectly touches the heap
descriptor); without a `pg_class` row, `RelationBuildDesc(3501) →
ScanPgRelation(3501)` returns NULL and the load_relation_oid PANIC path
FATALs at `postgres/src/backend/access/common/relation.c:61`.

## Fix

Mirror Steps 3w (pg_aggregate=2600), 3aa (pg_cast=2605), 3ag
(pg_conversion=2607), and 3ak (pg_default_acl=826) — a pure
catalog-seed addition with no encoder, builder, or `Init` flow change.

### (a) `internal/initdb/relcache_init.go`

Add `pgEnumAttrs()` returning the 4-column PG18 schema sourced verbatim
from `pg_enum.h` / `pg_enum_d.h`:

| # | Name           | TypeOID | Len | NotNull | Notes                          |
|---|----------------|---------|-----|---------|--------------------------------|
| 1 | oid            |      26 |   4 | true    | oid                            |
| 2 | enumtypid      |      26 |   4 | true    | oid → owning enum type         |
| 3 | enumsortorder  |     700 |   4 | true    | float4                         |
| 4 | enumlabel      |      19 |  64 | true    | NameData                       |

Add the entry to `nailedLocalRels` right after the Step 3ak pg_default_acl
row:

```go
{3501, "pg_enum", 83, 'r', 4, false, pgEnumAttrs()},
```

`RelType=83` is safe — `pg_enum` is not formrdesc'd (no
`EnumRelation_Rowtype_Id` constant in PG18 headers), so Step 3v's
`relation->rd_att->tdtypeid == relp->reltype` Phase3 assertion does not
fire.

All four columns are fixed-width and NOT NULL; the heap is currently
empty (no enum values are bootstrapped at initdb time), so the varlena
encoder is not exercised.

### (b) `internal/initdb/initdb.go`

`localRelMap` (`bootstrapPostgresDatabase`) gains `{3501, 3501}` slotted
between `3381 pg_statistic_ext` and `3596 pg_seclabel`. Without this
entry PG's relfilenode mapper cannot resolve OID 3501 to a backing file.

`bootstrapMappedLocalCatalogHeaps` OID list gains `3501` (same slot). The
empty 8 KiB `InitPage`-stamped heap is sufficient because pg_enum has
zero rows at initdb time; any `ENUMOID` syscache lookup expects a NULL
return at early-boot time.

### Companion indexes (intentionally deferred)

Per `pg_enum.h:48-50` three indexes back this catalog:

- `pg_enum_oid_index` (OID 3502) — UNIQUE PRIMARY KEY on `btree(oid
  oid_ops)`. Backs `MAKE_SYSCACHE(ENUMOID, …, 8)`.
- `pg_enum_typid_label_index` (OID 3503) — UNIQUE non-PKEY on
  `btree(enumtypid oid_ops, enumlabel name_ops)`. Backs
  `MAKE_SYSCACHE(ENUMTYPOIDNAME, …, 8)`.
- `pg_enum_typid_sortorder_index` (OID 3534) — UNIQUE non-PKEY on
  `btree(enumtypid oid_ops, enumsortorder float4_ops)`.

All three are deferred to later steps to preserve the single-OID rhythm
of Steps 3w → 3x → 3y → … . Once 3501 stops FATALing, the next E2E
re-run will surface 3502 (or whichever index PG probes first) as the
next blocker and the same pattern applies (Step 3ao/3ap/3aq).

Note: `pg_enum_typid_sortorder_index` is the first nailed index that
would key on `float4_ops` instead of the integer/text op families
already seeded by Steps 3b/3d. When that index becomes a blocker, the
`pgIndexInitialEntries` seed will need a `float4_ops` opclass OID
constant (PG18 `pg_opclass.dat` assigns it `OID 1970`); the underlying
opclass row was already seeded by goopg only if it appears in Step 3b's
`pgOpclassInitialEntries` list — that's an inventory check to perform
during Step 3aq, not now.

## Test plan

New regression pin in
`internal/initdb/pg_enum_nailed_test.go::TestNailedLocalRelsContainsPgEnum`
asserts `(RelName="pg_enum", RelKind='r', RelNatts=4, len(Attrs)=4)` and
pins every `(Name, TypeOID, Num, Len, NotNull)` against `pg_enum_d.h`.
Rejects silent removal that would re-introduce the FATAL.

Existing pin extended:
`TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
gains `3501` so the placeholder list cannot silently drop pg_enum.

Verified:

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestNailedLocalRelsContainsPgEnum|TestNailedLocalRelsContainsPgDefaultAcl|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3am (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Files

- `internal/initdb/relcache_init.go` — `pgEnumAttrs()` + `nailedLocalRels` entry.
- `internal/initdb/initdb.go` — `localRelMap` + `bootstrapMappedLocalCatalogHeaps` OID list.
- `internal/initdb/pg_enum_nailed_test.go` — regression pin (new).
- `internal/initdb/pg_mapped_local_catalog_heap_test.go` — `wantOIDs` extended.
- `docs/design/0106-0010-step3an-pg-enum-nailed-rel.md` — this doc.
