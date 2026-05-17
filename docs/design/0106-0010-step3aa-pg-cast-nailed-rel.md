# M0106-0010 Step 3aa — pg_cast nailed relation

## Context

After Step 3z (`pg_auth_members_role_member_index` OID 2694), the E2E
`TestE2E_FailoverGoopgToPG/async` advanced one blocker further. The next
FATAL from the PG18 standby is:

```
FATAL: could not open relation with OID 2605
```

OID 2605 is `pg_cast` — `postgres/src/include/catalog/pg_cast_d.h:23`:

```
#define CastRelationId 2605
```

The error is emitted from
`postgres/src/backend/access/common/relation.c:61` after
`RelationBuildDesc(2605) → ScanPgRelation(2605)` returns no row. goopg's
`bootstrapMappedLocalCatalogHeaps` already seeds an empty 8 KiB
`InitPage`-stamped heap at `base/{1,5}/2605`, so PG's `mdopen` succeeds;
the FATAL is a pg_class lookup miss, not a file-open miss.

The catalog is opened early by `parse_coerce.c` /
`find_coercion_pathway` — any cast resolution (most notably implicit
casts in `pg_authid_oid_index` syscache initialisation paths and
`InitPostgres`-time GUC processing) probes `pg_cast` via
`SearchSysCache2(CASTSOURCETARGET, …)`.

## Fix

Pure catalog-seed addition; no encoder, builder, or `Init` flow change.

### `internal/initdb/relcache_init.go`

1. New `pgCastAttrs()` — returns the 6-column PG18 pg_cast schema
   verbatim from `pg_cast.h` (`CATALOG(pg_cast, 2605, …)`):

   | num | name        | TypeOID | Len | NotNull |
   |---:|-------------|--------:|----:|:-------:|
   | 1 | `oid`         | 26 (oid)  | 4 | true |
   | 2 | `castsource`  | 26 (oid)  | 4 | true |
   | 3 | `casttarget`  | 26 (oid)  | 4 | true |
   | 4 | `castfunc`    | 26 (oid)  | 4 | true |
   | 5 | `castcontext` | 18 (char) | 1 | true |
   | 6 | `castmethod`  | 18 (char) | 1 | true |

2. `nailedLocalRels` gains
   `{2605, "pg_cast", 83, 'r', 6, false, pgCastAttrs()}` immediately
   after the Step 3w `pg_aggregate` entry.

`RelType = 83` (`pg_class_d.h::PG_CLASS_OID_INDEX_TYPEID`-style
placeholder) is safe because pg_cast is **not** formrdesc'd — PG18
headers do not define a `CastRelation_Rowtype_Id` constant, so Step
3v's `relation->rd_att->tdtypeid == relp->reltype` Phase3 assertion
(`relcache.c:4293`) cannot fire for this entry.

### Companion indexes deferred

`pg_cast_d.h` also declares two indexes:

```
#define CastOidIndexId          2660  // pg_cast_oid_index (PKEY oid_ops)
#define CastSourceTargetIndexId 2661  // pg_cast_source_target_index
                                       //   (UNIQUE castsource, casttarget oid_ops)
```

These are intentionally **not** seeded in Step 3aa. Following the same
single-OID rhythm as Steps 3w → 3x (pg_aggregate heap, then its
companion index 2650), each subsequent E2E re-run will surface the next
FATAL and motivate a focused Step 3ab / 3ac. The companion indexes
have empty Step-3k placeholder relfiles already.

## Flow-through

The single `nailedLocalRels` entry threads automatically through the
existing bootstrap pipeline:

1. `bootstrapPgClassTuples` writes the `Form_pg_class` row for OID
   2605 to `base/{1,5}/1259` (pg_class heap) and captures its TID in
   the `pgClassTIDs` map.
2. `bootstrapPgAttributeTuples` writes six `pg_attribute` heap rows
   (one per column) to `base/{1,5}/1249` and adds them to the
   per-(attrelid, attnum) TID map.
3. `bootstrapPgClassOidIndex` adds a new leaf for OID 2605 to the
   populated 2-page btree at `base/{1,5}/2662 + global/2662`.
4. `bootstrapPgAttributeRelidAttnumIndex` adds six composite-key
   leaves to `base/{1,5}/2659 + global/2659`.
5. `bootstrapPgIndexIndexrelidIndex` is unaffected (no index entries
   added in this step).
6. `writeRelcacheInitFile` emits a `RelationData` + `Form_pg_class` +
   six `Form_pg_attribute` blob group for the new rel into the
   `base/{1,5}/pg_internal.init` payload.

The companion empty 8 KiB heap page at `base/{1,5}/2605` is already
written by `bootstrapMappedLocalCatalogHeaps`; PG opens it once the
pg_class row resolves and reads zero tuples (acceptable — initdb-time
goopg does not seed any cast functions).

## Regression pin

`internal/initdb/pg_cast_nailed_test.go::TestNailedLocalRelsContainsPgCast`
asserts the nailedLocalRels entry's `(OID, RelName, RelKind, RelNatts)`
and pins every (Name, TypeOID, Num, Len, NotNull) against the
authoritative PG18 column layout. Forces future edits to keep the schema
in sync with `pg_cast_d.h` and prevents silent removal of the entry.

## Verification

```
go build ./...                                                # PASS
go test -count=1 -run \
  'TestNailedLocalRelsContainsPgCast|TestNailedLocalRelsContainsPgAggregate|
   TestPgIndexInitialEntriesIndkeyMatchesPG18|TestNailedIndexRelnattsAgreesWithIndnatts|
   TestPgClassOidIndexHasSingleKeyColumn|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages' \
  ./internal/initdb/                                          # PASS
go test -count=1 ./internal/initdb/                           # 14 pre-existing
                                                              # baseline failures
                                                              # unchanged from Step 3z
go test -count=1 ./internal/executor/ ./internal/server/ \
                  ./internal/storage/ ./internal/catalog/ \
                  ./internal/mvcc/                            # PASS
```

The next E2E re-run is expected to surface either:
- `could not open relation with OID 2660` (pg_cast_oid_index — early
  syscache wire-up for CASTSOURCETARGET), or
- `could not open relation with OID 2661` (pg_cast_source_target_index
  — the index actually used by the syscache lookup).

Either is straightforward Step-3aa-style follow-up.
