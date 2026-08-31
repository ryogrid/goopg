# 0106-0010-batched-15 — Expand pg_type from 37 to 194 rows

**Status:** accepted
**Date:** 2026-05-19
**Milestone:** M0106-0010 (vanilla PG18 standby attach)
**Task:** 15 of 35 in `docs/design/bootstrap-procedure/10-implementation-roadmap.md`

---

## Problem

`bootstrapPgTypeTuples` previously seeded only the type OIDs directly
referenced by nailed-relation attributes (~37 rows). PG18 lookups via
`SearchSysCache1(TYPEOID, …)` or `SearchSysCache2(TYPENAMENSP, …)` for
any other base type would return NULL and FATAL during standby-boot
`RelationCacheInitializePhase3` or subsequent backend startup.

---

## Solution

### 1. Code generator: `cmd/gen-pg-type-data/main.go`

Parses `postgres/src/include/catalog/pg_type.dat` (113 entries) and
`postgres/src/include/catalog/pg_proc.dat` (function name → OID map),
then emits `internal/initdb/pg_type_seed_data.go` containing
`pgTypeAllEntries()` — 193 entries total:

- 113 base types from `pg_type.dat` (including pseudo-types, composite
  rowtypes for bootstrapped catalogs, range types, multirange types).
- 83 array-peer entries derived from the `array_type_oid` field of each
  base entry. Array alignment: `'d'` if element alignment is `'d'`,
  otherwise `'i'` (matches `genbki.pl` rule).

### 2. `bootstrapPgTypeTuples` expansion

`pg_type_bootstrap.go::bootstrapPgTypeTuples` now:

1. Starts from `pgTypeAllEntries()` (193 entries).
2. Merges any nailed-attr OIDs that resolve via `pgTypeCanonical()` but
   are absent from the generated set (e.g. OID 10028 `_pg_statistic`,
   a goopg-specific OID not in `pg_type.dat`).
3. Writes all entries to `base/{1,5}/1247` in OID-ascending order.
4. Returns `map[uint32]heapTID` (keyed by type OID) instead of
   `[]heapTID` — same pattern as `bootstrapPgClassOidIndex`.

### 3. `bootstrapPgTypeOidIndex` signature change

`btree_index_bootstrap.go::bootstrapPgTypeOidIndex` now accepts
`map[uint32]heapTID` instead of `[]heapTID`. The old signature required
a parallel entry count from `pgTypeInitialEntries()` (26 entries vs. the
194 TIDs produced by the expanded bootstrap), which caused a hard error.
The new signature derives the `(oid, tid)` pairs directly from the map.

---

## Files changed

| File | Change |
|------|--------|
| `cmd/gen-pg-type-data/main.go` | New code generator |
| `internal/initdb/pg_type_seed_data.go` | Generated: 193-entry `pgTypeAllEntries()` |
| `internal/initdb/pg_type_bootstrap.go` | `bootstrapPgTypeTuples` returns `map[uint32]heapTID`; merges generated + canonical sets |
| `internal/initdb/btree_index_bootstrap.go` | `bootstrapPgTypeOidIndex` takes `map[uint32]heapTID` |
| `internal/initdb/pg_type_bootstrap_test.go` | Added `TestPgTypeAllEntriesCountAndCoverage`, `TestPgTypeAllEntriesTypalignValid`; updated existing test to use `len(tidMap)` |
| `internal/initdb/initdb_test.go` | Updated `TestInitCreatesSystemCatalogRelfiles` to allow multi-block pg_type heap |

---

## Regression pins

| Test | What it guards |
|------|---------------|
| `TestPgTypeAllEntriesCountAndCoverage` | Count == 193; critical OIDs (bool 16, int4 23, text 25, oid 26, …) present |
| `TestPgTypeAllEntriesTypalignValid` | Every entry's `typalign` ∈ `{c,s,i,d}` and `typstorage` ∈ `{p,e,x,m}` |
| `TestBootstrapPgTypeOidIndexWritesPopulatedBtree` | Index has 194 leaves (193 generated + OID 10028); OID 23 present; ascending order |
| `TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs` | Nailed-attr OIDs still covered via `pgTypeCanonical()` fallback |
| `TestBootstrapPgTypeTuplesWritesCanonicalHeap` | All heap tuples have valid typalign/typstorage bytes |

---

## Known gap

The 612-row target (112 base + ~500 derived) requires ~416 additional
rows from composite rowtypes for non-nailed catalogs, user tables, views,
etc. Those are the province of task 32 in the implementation roadmap
(`PgCanonicalHeapInsert` continuous-maintenance plumbing). The 194 rows
produced here close the immediate PG-boot syscache miss for any standard
base type OID lookup.
