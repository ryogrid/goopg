# M0106-0010 Step 3ar — pg_event_trigger nailed rel

## Blocker

After Step 3aq seeded `pg_enum_typid_sortorder_index` (OID 3534, completing the
trio of `pg_enum.h` indexes), the next E2E re-run of
`TestE2E_FailoverGoopgToPG/async` (`GOOPG_RUN_BLOCKED_M0102_E2E=1`) produces a
new PG-standby boot blocker:

```
FATAL:  could not open relation with OID 3466
```

`postgres/src/include/catalog/pg_event_trigger_d.h:23` defines
`#define EventTriggerRelationId 3466`; the FATAL is therefore emitted from
`RelationBuildDesc(3466) → ScanPgRelation(3466)` returning no row, because
goopg's `nailedLocalRels` has never seeded a `pg_class` row for
pg_event_trigger.

## Root cause

`pg_event_trigger` is a regular local catalog relation (`CATALOG(pg_event_trigger,3466,EventTriggerRelationId)`).
PG opens it during early backend startup whenever the event-trigger framework
is consulted; without a `pg_class` tuple at OID 3466, the standard
`SearchSysCache1(RELOID, 3466)` path returns NULL and the backend FATALs.

A secondary defect surfaced while wiring the fix: the empty-heap-placeholder
list in `bootstrapMappedLocalCatalogHeaps` and the `localRelMap` entry both
carried OID `4044` mis-labelled as `pg_event_trigger`. `4044` is not assigned
to any catalog in PG18 (`grep` over `postgres/src/include/catalog/*.h`
returns nothing). The mis-label meant that even if a future step added the
nailed-rel entry under the correct OID 3466, the on-disk heap file under
`base/{1,5}/3466` would still be missing and `mdopen` would FATAL with
ENOENT immediately after the `pg_class` lookup succeeded.

## Fix

Pure catalog-seed change. No encoder, builder, or `Init` flow change.

1. `internal/initdb/relcache_init.go::nailedLocalRels` gains
   `{3466, "pg_event_trigger", 83, 'r', 7, false, pgEventTriggerAttrs()}`.
   `RelType=83` is safe because PG18 does not formrdesc pg_event_trigger
   (no `EventTriggerRelation_Rowtype_Id` constant in the headers); the
   Step 3v `relation->rd_att->tdtypeid == relp->reltype` Phase-3 assertion
   does not fire.

2. New `pgEventTriggerAttrs()` returns the 7-column PG18 schema verbatim
   from `pg_event_trigger.h` / `pg_event_trigger_d.h`:

   | Num | Name        | TypeOID | Len | NotNull |
   |-----|-------------|---------|-----|---------|
   | 1   | oid         | 26 (oid)| 4   | true    |
   | 2   | evtname     | 19 (name)| 64 | true    |
   | 3   | evtevent    | 19 (name)| 64 | true    |
   | 4   | evtowner    | 26 (oid)| 4   | true    |
   | 5   | evtfoid     | 26 (oid)| 4   | true    |
   | 6   | evtenabled  | 18 (char)| 1  | true    |
   | 7   | evttags     | 1009 (_text)| -1 | false |

   `evttags` is declared inside the `CATALOG_VARLEN` block and has no
   `BKI_FORCE_NOT_NULL` annotation, so it is the only nullable column.
   The Step 3i null-bitmap plumbing
   (`writeMultiPageHeapRows → NewHeapTupleWithNulls`) handles the layout
   shift transparently if any row ever lands with a NULL `evttags`.

3. `internal/initdb/initdb.go`:
   - Line ~451 (`bootstrapMappedLocalCatalogHeaps` OID list): replace
     `4044, // pg_event_trigger` with
     `3466, // pg_event_trigger (M0106-0010 step 3ar)` so the empty heap
     placeholder lands at the correct on-disk OID.
   - Line ~765 (`localRelMap` entries): replace
     `{4044, 4044}, // pg_event_trigger` with
     `{3466, 3466}, // pg_event_trigger (M0106-0010 step 3ar)` so the
     relmapper agrees with the canonical EventTriggerRelationId.

The nailed-rel entry threads automatically through the existing bootstrap
flow:

- `bootstrapPgClassTuples` writes the `Form_pg_class` row for OID 3466.
- `bootstrapPgAttributeTuples` writes 7 `pg_attribute` rows.
- `bootstrapPgClassOidIndex` adds the leaf for 3466 to `base/{1,5}/2662` and
  `global/2662`.
- `bootstrapPgAttributeRelidAttnumIndex` adds 7 composite-key leaves to
  `2659`.
- `buildPgClassBlob` adds a `Form_pg_class` blob to the per-relation
  `pg_internal.init`.
- `bootstrapMappedLocalCatalogHeaps` writes the empty heap page at
  `base/{1,5}/3466`.

No index entries are added in this step — the two indexes declared by
`pg_event_trigger.h` (3467 `pg_event_trigger_evtname_index`, 3468
`pg_event_trigger_oid_index`) are not yet load-bearing for the standby
boot path. They will be added in a follow-up step if and when a
`SearchSysCache1(EVENTTRIGGEROID, …)` or `EVENTTRIGGERNAME` lookup
surfaces as the next blocker.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestNailedLocalRelsContainsPgEventTrigger|TestBootstrapMappedLocalCatalogHeapsIncludesPgEventTrigger|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedLocalRelsContainsPgEnum|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — pre-existing 14 baseline failures
  unchanged.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

E2E confirmation: next `GOOPG_RUN_BLOCKED_M0102_E2E=1
TestE2E_FailoverGoopgToPG/async` re-run is expected to advance past the
`could not open relation with OID 3466` FATAL to the next single-OID
nailed-rel/index blocker surfaced by `RelationCacheInitializePhase3`.

## Regression pins

- `TestNailedLocalRelsContainsPgEventTrigger` — pins the nailedLocalRels
  entry's OID/Name/RelKind/RelNatts and per-column `(Name, TypeOID, Num,
  Len, NotNull)` against the authoritative PG18 schema.
- `TestBootstrapMappedLocalCatalogHeapsIncludesPgEventTrigger` — asserts
  the empty heap page is written under OID 3466 (not 4044) at both
  `base/1/` and `base/5/`. Explicitly rejects a regression where 4044
  re-appears.
- `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages` — wantOIDs
  list updated 4044 → 3466.

## References

- `postgres/src/include/catalog/pg_event_trigger.h` — CATALOG definition
  and the two index DECLAREs (3467 / 3468).
- `postgres/src/include/catalog/pg_event_trigger_d.h` — authoritative OID
  constants (`EventTriggerRelationId = 3466`).
