# M0106-0010 batched-14: pg_proc Full Expansion to 3397 Entries

## Summary

Expand `bootstrapPgProcTuples` from 32 hand-crafted entries to all 3397 entries
sourced verbatim from `postgres/src/include/catalog/pg_proc.dat`, and switch
`bootstrapPgProcOidIndex` from the single-leaf `pgBuildBtreeLeafRootPage` builder
to the multi-leaf `pgBuildBtreeBulkLoad` builder.

## Motivation

The prior 32-entry list was sufficient for standby startup to locate the 7 AM
handlers and 24 type I/O regprocs that `fmgr_info` dereferences during
`InitPostgres`. Expanding to the full 3397-entry set eliminates the risk of
`SearchSysCache1(PROCOID, …)` returning NULL for any function that PG probes
after the catalogs are fully loaded (e.g. aggregate transition functions,
operator support functions, SQL-language helpers).

## Design

### Generator (`cmd/gen-pg-proc-data/main.go`)

A standalone `//go:build ignore` tool that:

1. Parses `postgres/src/include/catalog/pg_type.dat` to build a
   `typname → OID` map. Array type OIDs are extracted from `array_type_oid`
   fields and stored as `_typname → array_type_oid`.
2. Parses `postgres/src/include/catalog/pg_proc.dat` using a regex
   `(\w+)\s*=>\s*'([^']*)'` to extract per-entry key-value pairs. The `descr`
   field is intentionally skipped because it may contain escaped single quotes.
3. Resolves all type-name references (prorettype, proargtypes, proallargtypes)
   to numeric OIDs via the type map.
4. Sorts by OID and emits `internal/initdb/pg_proc_seed_data.go` containing
   `pgProcAllEntries() []pgProcEntry`.

Run from the repository root:

```
go run cmd/gen-pg-proc-data/main.go > internal/initdb/pg_proc_seed_data.go
```

### Struct change (`pgProcEntry`)

Added `Lang uint32` field after `NotStrict bool`. Zero value means
`INTERNALlanguageId` (12). The generator sets:

- `Lang: 13` for entries with `prolang => 'c'`
- `Lang: 14` for entries with `prolang => 'sql'`
- `Lang: 0` (default) for all others (internal language)

### `pgProcRow` update

Column 5 (`prolang`) now returns `e.Lang` when non-zero, falling back to 12.

### `pgProcInitialEntries` simplification

The 120-line hand-crafted function is replaced by a one-liner:

```go
func pgProcInitialEntries() []pgProcEntry {
    return pgProcAllEntries()
}
```

### `bootstrapPgProcOidIndex` upgrade

Replaced `pgBuildBtreeLeafRootPage` + manual meta assembly with
`pgBuildBtreeBulkLoad(tuples, 1)`. The bulk-load helper:

- Falls through to the single-leaf path when `len(tuples) <= 407` (max per
  single-leaf root).
- Builds a proper multi-level btree when more entries are present. With 3397
  entries: approximately 9 leaf pages + internal pages + root.

### Key parsing rules

| Field | Absent | Present |
|-------|--------|---------|
| `proargtypes` | `nil` (AM-handler default → [2281]) | `[]uint32{}` for empty string, else resolved OIDs |
| `provolatile` | 0 (→ 'v' at row emission) | literal char |
| `proparallel` | 0 (→ 's' at row emission) | literal char |
| `proisstrict` | absent → strict (NotStrict=false) | `'f'` → NotStrict=true |
| `proretset` | false | `'t'` → true |
| `prolang` | 0 → 12 (internal) | 'sql' → 14, 'c' → 13 |

### OID 3317 (pg_stat_get_wal_receiver) correction

The former hand-crafted entry had `RetSet: true` in error. The canonical
`pg_proc.dat` entry has no `proretset => 't'`. The generated entry correctly
has `RetSet: false`. The test `TestPgProcRowStatGetWalReceiverIsSRF` was
updated to match.

## Test changes

- `TestPgProcInitialEntriesCoverAMHandlers`: count updated 32 → 3397; volatile
  comparison normalises `Volatile == 0` to `'v'` before comparison (generated
  entries use 0 for the default).
- `TestBootstrapPgProcOidIndexWritesPopulatedBtree`: updated from exact
  2-block check to multi-leaf check (file > 2 blocks, BTREE_MAGIC valid,
  bthandler OID 330 present in some leaf page).
- `TestPgProcRowStatGetWalReceiverIsSRF`: updated `proretset` expectation
  from true to false.

## Files changed

| File | Change |
|------|--------|
| `cmd/gen-pg-proc-data/main.go` | New generator tool |
| `internal/initdb/pg_proc_seed_data.go` | Generated — 3397 entries |
| `internal/initdb/initdb.go` | Add `Lang uint32` to struct; update `pgProcRow`; simplify `pgProcInitialEntries` |
| `internal/initdb/btree_index_bootstrap.go` | Switch to `pgBuildBtreeBulkLoad` |
| `internal/initdb/pg_proc_bootstrap_test.go` | Update count + volatile check + retset check |
| `internal/initdb/pg_proc_oid_index_test.go` | Update to multi-leaf assertions |
| `docs/design/0106-0010-batched-14-pg-proc-full-expansion.md` | This document |
