# M0106-0010 Step 3n — pg_index `indkey` heap-attnum fixes for PG18

## Context

After Step 3m landed `bootstrapPgClassOidIndex`, an E2E re-run of
`TestE2E_FailoverGoopgToPG/async` (`GOOPG_RUN_BLOCKED_M0102_E2E=1`)
advanced past the PANIC for shared critical index 2671 but every PG
backend that subsequently opened a connection FATAL'd with

```
FATAL:  column is not in index
```

emitted from `systable_beginscan()` at
`postgres/src/backend/access/index/genam.c:446`:

```c
for (j = 0; j < IndexRelationGetNumberOfAttributes(irel); j++)
{
    if (key[i].sk_attno == irel->rd_index->indkey.values[j])
    {
        idxkey[i].sk_attno = j + 1;
        break;
    }
}
if (j == IndexRelationGetNumberOfAttributes(irel))
    elog(ERROR, "column is not in index");
```

The caller's `key[i].sk_attno` is a PG-canonical heap attnum derived
from compile-time `Anum_pg_*_*` constants. If goopg's pg_index row
records a different attnum in `indkey`, the linear scan never matches
and the FATAL trips before any heap or btree page is touched.

## Root cause

Four `pgIndexInitialEntries` rows carried wrong heap attnums that pre-
dated the PG18 catalog reorganisation (and one outright typo). Each
was silently wrong before Step 3n because Steps 3a–3m never exercised
the affected systable scans — they only opened (loaded) the indexes,
they never *searched* through them.

| OID | Index | Before | After (PG18) | Source |
|---|---|---|---|---|
| 2659 | pg_attribute_relid_attnum_index | `[1, 6]` | `[1, 5]`  | `pg_attribute.h` — `attnum` moved to col 5 |
| 2693 | pg_rewrite_rel_rulename_index   | `[2, 7]` | `[3, 2]`  | `pg_rewrite.h` — `ev_class`=3, `rulename`=2 |
| 2701 | pg_trigger_tgrelid_tgname_index | `[2, 3]` | `[2, 4]`  | `pg_trigger.h` — `tgname` at col 4 after `tgparentid` |
| 3593 | pg_shseclabel_object_index      | `[3, 2, 5]` | `[1, 2, 3]` | `pg_shseclabel.h` — `objoid`=1, `classoid`=2, `provider`=3 |

The 2659 entry is the load-bearing one for early backend startup —
`RelationCacheInitializePhase3` opens
`pg_attribute_relid_attnum_index` (a critical index) and then does
`SearchSysCache(ATTNUM, …)` against it for any non-nailed relation
seen during `InitPostgres()`. The wrong indkey trips genam.c:446
before any heap row is read.

## Fix

`internal/initdb/initdb.go::pgIndexInitialEntries` — adjust the four
`indkey` vectors to the PG18 column ordering. No other column of the
affected entries changes; `indclass`/`indcollation` already match the
canonical PG18 opclass/collation per key column (e.g. `oid_ops`/`0`
for `objoid`/`classoid`, `text_ops`/`C_COLLATION_OID` for `provider`).

The change is data-only — no encoder or layout change. All other
nailed-index `indkey` rows were audited against `postgres/src/include/
catalog/pg_*.h` and confirmed correct:

* 2671 pg_database_datname_index `[2]` (datname=2) ✓
* 2676 pg_authid_rolname_index `[2]` (rolname=2) ✓
* 2695 pg_auth_members_member_role_index `[3, 2, 4]` ✓
* 2691 pg_proc_proname_args_nsp_index `[2, 20, 3]` (proargtypes=20) ✓
* 2655 pg_amproc_fam_proc_index `[2, 3, 4, 5]` ✓
* 2654 pg_amop_opr_fam_index `[7, 6, 2]` ✓
* 2680 pg_inherits_relid_seqno_index `[1, 3]` (inhseqno=3) ✓

## Test pin

`internal/initdb/pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`
asserts every entry's `IndKey` against the authoritative PG18 column
ordering, with a count check that forces future additions to update
the pinned map (prevents silently adding a row with a wrong indkey).

## Verification

* `go test -count=1 -run 'TestPgIndexInitialEntriesIndkeyMatchesPG18|
  TestPgClassOidIndexHasSingleKeyColumn|
  TestNailedIndexRelnattsAgreesWithIndnatts|
  TestPgIndexColDefsMatchesRelcacheAttrs|
  TestBootstrapPgIndex' ./internal/initdb/` PASS.
* `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures (`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`,
  `TestSynchronousCommitFlushesByDefault`, `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`, `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — unchanged from Step 3m baseline.
* `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.
* `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run TestE2E_FailoverGoopgToPG/async`
  — FATAL "column is not in index" cleared; surfaces the next blocker
  `pg_attribute catalog is missing 1 attribute(s) for relation OID 2671`
  (Step 3o — the pg_attribute heap rows for shared catalog indexes are
  not yet seeded into `global/1249`).

## Files

* `internal/initdb/initdb.go` — `pgIndexInitialEntries` indkey fixes.
* `internal/initdb/pg_index_indkey_test.go` — new pin.
