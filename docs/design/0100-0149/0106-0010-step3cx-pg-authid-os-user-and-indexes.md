# 0106-0010 Step 3cx — pg_authid OS-user role + rolname/oid index population

Status: implemented

## Context

Step 3cw landed the populated `pg_amproc_fam_proc_index` (OID 2655) so PG
standby boot could resolve the `name_ops` comparison support function for
`pg_authid_rolname_index`. The next FATAL on the
`TestE2E_FailoverGoopgToPG/async` standby boot path is:

```
FATAL: 28000: role "ryo" does not exist
        InitializeSessionUserId (miscinit.c:802)
```

This fires from PG's `InitializeSessionUserId →
SearchSysCache1(AUTHNAME, $USER)`, which traverses the (then-empty)
`pg_authid_rolname_index` (OID 2676). Two distinct issues block resolution:

1. **`bootstrapPostgresRole` collapsed both seeded roles onto OID 10.**
   The old code added a second heap tuple for `os.Getenv("USER")` but kept
   the bootstrap superuser OID (`BOOTSTRAP_SUPERUSERID = 10`). PG's
   `pg_authid_oid_index` would then collide and an `AUTHOID` lookup for
   the OS user could not find a stable row.
2. **`pg_authid_rolname_index` (OID 2676) and `pg_authid_oid_index`
   (OID 2677) were still Step-3k empty placeholders** (`btm_root =
   P_NONE`), so even with the heap row in place the syscache lookup
   returned zero rows.

## Implementation

### Distinct OIDs in `bootstrapPostgresRole`

`internal/initdb/initdb.go::bootstrapPostgresRole`:

- Returns `([]pgAuthidEntry, error)` so the index bootstrap can consume
  one `heapTID` per seeded row.
- Seeds `postgres` at OID 10 (`BOOTSTRAP_SUPERUSERID`) and — when
  `os.Getenv("USER")` is non-empty and not already `postgres` — also
  seeds that user at OID 16384 (`FirstNormalObjectId`).
- New companion struct `pgAuthidEntry { OID, Rolname, TID }` lives next
  to `heapTID` for direct re-use by the index builders.

### New IndexTuple builder `pgBuildIndexTupleNameKey`

`internal/initdb/btree_index_bootstrap.go`. Single fixed-width NAME column,
no nulls:

```
[0..1]   bi_hi          (ItemPointerData)
[2..3]   bi_lo
[4..5]   ip_posid
[6..7]   t_info         (low 13 bits = size; INDEX_VAR/NULL bits clear)
[8..71]  NameData       (zero-padded to NAMEDATALEN=64)
```

Total = `MAXALIGN(IndexTupleHeader + NAMEDATALEN) = MAXALIGN(72) = 72` —
already 8-byte aligned, so no trailing pad is required. The NameData
mirrors `encodeValuePG`'s heap encoding for `name`: zero-padded fixed
64 bytes, no varlena header, no trailing terminator at exactly 64 bytes.

`namestrcpy` semantics: overly long names are silently truncated to fill
all 64 bytes (no terminator); this matches upstream PG and is pinned by
`TestPgBuildIndexTupleNameKeyTruncatesAtNamedataLen`.

### `bootstrapPgAuthidIndexes`

Both indexes are shared catalogs and live exclusively under `global/`:

- **OID 2677 (`pg_authid_oid_index`)**: oid-keyed leaves, sorted ascending
  on OID; built via existing `pgBuildIndexTupleOidKey`.
- **OID 2676 (`pg_authid_rolname_index`)**: name-keyed leaves, sorted
  lexicographically on `rolname` bytes — matches `btnamecmp` for ASCII
  single-byte rolnames, which is what bootstrap seeds (postgres / OS
  user).

Each index is a 2-page file (metapage + leaf-root) emitted via
`pgBuildBtreeLeafRootPage` + `pgBuildBtreeMetapageWithRoot`, overwriting
the empty placeholders written by `bootstrapSharedCatalogPlaceholders`.

### Wiring

`Init` (`internal/initdb/initdb.go`) captures the returned slice
(`pgAuthidEntries, err := bootstrapPostgresRole(abs)`) and calls
`bootstrapPgAuthidIndexes(abs, pgAuthidEntries)` immediately after the
existing `bootstrapPgDatabaseOidIndex` call — i.e. before any of the
heap/index bootstraps that depend on a working role cache.

## Regression pins

`internal/initdb/pg_authid_indexes_test.go`:

- `TestPgBuildIndexTupleNameKeyLayoutMatchesPG18`: byte-exact 72-byte
  layout with `bi_hi`/`bi_lo` split using a deliberately asymmetric
  block number (0xDEADBEEF) so the Step-3s LE-uint32 trap regression is
  caught loudly; `t_info=0x0048`; rolname bytes followed by zero padding.
- `TestPgBuildIndexTupleNameKeyTruncatesAtNamedataLen`: 80-byte input
  fills all 64 NameData bytes.
- `TestBootstrapPgAuthidIndexesWritesPopulatedBtrees`: both files
  written at `global/2676` and `global/2677`; `btm_root == 1`;
  line-pointer counts match the seeded entry count; mandatory presence
  of an OS-user `"ryo"` leaf in 2676 and an OID 16384 leaf in 2677
  (the latter is the precise key that previously triggered the FATAL).

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestPgBuildIndexTupleNameKey|TestBootstrapPgAuthidIndexes' ./internal/initdb/` — PASS (3/3).
- `go test -count=1 ./internal/initdb/` — 15 pre-existing baseline failures
  (`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`,
  `TestSynchronousCommitFlushesByDefault`, `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) confirmed unchanged versus Step 3cw.
- Cross-package smoke: `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Next blocker

`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` advances
past the `role "ryo" does not exist` FATAL. The next FATAL surfaced by
the standby boot path will be tracked in the immediate-next Step
(3cy onward) once observed end-to-end.
