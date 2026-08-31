# M0106-0010 Step 3ah — pg_conversion_default_index (OID 2668)

## Context

Step 3ag seeded the `pg_conversion` heap relation (OID 2607). Re-running
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
exposed the next blocker:

```
FATAL: could not open relation with OID 2668
```

— repeated on every backend the upstream PG standby's postmaster
forked, ultimately causing `psql ... pg_stat_wal_receiver` (and any
client query) to fail.

A separate test-infra issue was uncovered along the way:
`internal/testutil/replcluster.cloneDataDir` could not overwrite the
standby's `base/1/pg_internal.init` because `bootstrapRelcacheInitFiles`
chmods that file to `0o400` and `os.OpenFile(..., O_TRUNC|O_WRONLY)`
fails on a read-only file. Fixed in the same commit by
`os.Remove(target)` before each write — matching the pattern already
used by `copyInitFiles` in the failover test. Without this fix
`TestE2E_PhysicalReplication` (goopg→goopg replication) regressed to
"permission denied" and the failover harness never got far enough to
surface the 2668 FATAL.

## Authoritative source

`postgres/src/include/catalog/pg_conversion.h:63`:

```c
DECLARE_UNIQUE_INDEX(pg_conversion_default_index, 2668,
    ConversionDefaultIndexId, pg_conversion,
    btree(connamespace oid_ops, conforencoding int4_ops,
          contoencoding int4_ops, oid oid_ops));
MAKE_SYSCACHE(CONDEFAULT, pg_conversion_default_index, 8);
```

`pg_conversion_d.h`:

| attnum | name           | type    |
| ------ | -------------- | ------- |
| 1      | oid            | oid     |
| 2      | conname        | name    |
| 3      | connamespace   | oid     |
| 4      | conowner       | oid     |
| 5      | conforencoding | int4    |
| 6      | contoencoding  | int4    |
| 7      | conproc        | regproc |
| 8      | condefault     | bool    |

So OID 2668 is the `CONDEFAULT` syscache backing index on
`pg_conversion` (heap OID 2607): four columns
`(connamespace, conforencoding, contoencoding, oid)`, UNIQUE but not
PRIMARY (`DECLARE_UNIQUE_INDEX`, not the `_PKEY` variant — the PKEY is
OID 2670, `pg_conversion_oid_index`). None of the four keys are textual
so all collation slots are `0` (`oid_ops` and `int4_ops` are typeless).

## Fix

Pure catalog-seed addition. No encoder, builder, or `Init` flow change.

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains

```go
entry(2668, 2607, []int16{3, 5, 6, 1},
    []uint32{oidOps, int4Ops, int4Ops, oidOps},
    []uint32{0, 0, 0, 0},
    true, false), // pg_conversion_default_index
```

— composite key `{3, 5, 6, 1}` matches the column order declared by
`DECLARE_UNIQUE_INDEX`; `IsUnique=true`, `IsPrimary=false`. Same
composite-UNIQUE pattern as `pg_amop_fam_strat_index` (2754, Step 3y)
and `pg_collation_name_enc_nsp_index` (3164, Step 3ae) — minus the
`name_ops` slot, because pg_conversion's `conname` (`name` type) is not
in the default-index column set.

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{2668, "pg_conversion_default_index"}`. `flattenRels` consults
`pgIndexNattsByOID()` (returns 4 for OID 2668) so the nailed rel
carries `RelKind='i', RelNatts=4`, and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check
(relcache.c:1492) passes.

(c) The three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) gain `2668` so the Step-3k empty
btree placeholder (`btm_root = P_NONE`) is laid down at the correct
relfile paths. The empty placeholder is sufficient because
pg_conversion is currently unpopulated (no conversion rows are
bootstrapped) — a zero-row lookup is the expected outcome.

The seed threads automatically through `bootstrapPgClassTuples` →
`bootstrapPgAttributeTuples` (4 indexKeyAttrs rows) →
`bootstrapPgIndexTuples` (writes Form_pg_index row with `indnatts=4` +
captures TID in `pgIndexTIDs[2668]`) →
`bootstrapPgIndexIndexrelidIndex` (leaf at file 2679) →
`bootstrapPgClassOidIndex` (leaf at 2662) →
`bootstrapPgAttributeRelidAttnumIndex` (4 composite-key leaves at 2659).

(d) `internal/testutil/replcluster/replcluster.go::cloneDataDir`
removes any pre-existing target file before `OpenFile` so the
read-only `pg_internal.init` files written by the standby's
`Init()` step do not block the clone overwrite.

## Tests

New pins in
`internal/initdb/pg_conversion_default_index_test.go`:

- `TestPgConversionDefaultIndexSeededFromInitialEntries` — asserts
  `(IndRelid=2607, IndKey=[3 5 6 1], IsUnique=true, IsPrimary=false,
   IndCollation=[0 0 0 0])`.
- `TestNailedLocalRelsContainsPgConversionDefaultIndex` — asserts
  `RelName="pg_conversion_default_index", RelKind='i', RelNatts=4`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds
  `2668: {3, 5, 6, 1}` (strict count guard so future additions cannot
  bypass the registry).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 2668, requiring the populated 2679 btree to carry this
  leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgConversionDefaultIndex|TestNailedLocalRelsContainsPgConversion|TestNailedLocalRelsContainsPgCast|TestBootstrapPgIndexIndexrelidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ag (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.
- `go test -count=1 -run '^TestE2E_PhysicalReplication$'
  ./internal/testport/` — PASS (cloneDataDir fix unblocks goopg→goopg
  replication).
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` —
  advances past the "could not open relation with OID 2668" FATAL to
  the next blocker.

## Next blocker (Step 3ai)

The next E2E re-run is expected to surface either OID 2669
(`pg_conversion_name_nsp_index`) or 2670
(`pg_conversion_oid_index`) — the two remaining `pg_conversion`
companion indexes per `pg_conversion.h:64-65` — or a different
nailed-rel FATAL at a later OID. Whichever surfaces follows the same
single-OID catalog-seed-addition pattern.
