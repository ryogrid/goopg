# M0106-0010 Step 3v — pg_shseclabel reltype must equal PG's SharedSecLabelRelation_Rowtype_Id

## Symptom

After Step 3u landed (PG standby reaches `PM_HOT_STANDBY`), running
`TestE2E_FailoverGoopgToPG/async` re-surfaced a different failure: the
standby's `SELECT status FROM pg_catalog.pg_stat_wal_receiver` probe never
returns and the test times out at 300s. The walsender/walreceiver
handshake itself succeeds (standby log:
`started streaming WAL from primary at 0/0 on timeline 1`) — but every
client backend that the postmaster forks aborts before reaching
`ReadyForQuery`:

```
TRAP: failed Assert("relation->rd_att->tdtypeid == relp->reltype"),
      File: "relcache.c", Line: 4293, PID: 55878
postgres: startup(ExceptionalCondition+0xbe)
postgres: startup(RelationCacheInitializePhase3+0x388)
postgres: startup(InitPostgres+0xc10)
postgres: startup(PostgresMain+0x2f1)
```

PG's `PMQuitRequest` path then terminates the entire postmaster, the
crash-and-restart cycle repeats indefinitely, and any session attempting
to read `pg_stat_wal_receiver` simply blocks until the test deadline.

## Root cause

`RelationCacheInitializePhase3` (`postgres/src/backend/utils/cache/relcache.c:4293`)
asserts that every nailed relation's in-memory TupleDesc carries the same
composite-type OID (`tdtypeid`) that the relation's heap row reports as
`reltype`. The two values come from independent sources:

1. **In-memory `tdtypeid`** — set when the relation was bootstrapped. If
   `load_relcache_init_file(true|false)` returns false, PG calls
   `formrdesc(<name>, <Rowtype_Id>, ...)` for each critical nailed rel
   (Phase2 for shared, Phase3 for local). `formrdesc` hardcodes
   `tdtypeid = Rowtype_Id` from `postgres/src/include/catalog/pg_*_d.h`.
2. **Heap-row `reltype`** — read via `SearchSysCache1(RELOID, …)` against
   the actual `pg_class` heap that goopg's `bootstrapPgClassTuples` wrote
   at initdb time, sourced from `nailedRel.RelType` in
   `internal/initdb/relcache_init.go`.

goopg's init file is currently rejected on every PG-standby boot — its
`buildRelationDataBlob` writes `rd_id` at offset 0 but PG18's
`RelationData` puts `rd_id` at offset 72 (verified via a sizeof program
built against `postgres/local_install/include/server`), so PG reads
`rd_isnailed` as false and the nailed-rel count sanity check at
`relcache.c:6538` fails — meaning the formrdesc fallback runs on every
backend startup. (Fixing the init-file layout is tracked separately;
formrdesc is a correct, supported fallback as long as the heap rows
match.)

For 8 of the 9 nailed catalogs, goopg's `RelType` already matches PG18's
`Rowtype_Id`. The exception was `pg_shseclabel`:

| catalog | goopg `RelType` | PG18 `Rowtype_Id` (`*_d.h`) |
|---|---|---|
| pg_database     | 1248 | `DatabaseRelation_Rowtype_Id     = 1248` ✓ |
| pg_authid       | 2842 | `AuthIdRelation_Rowtype_Id       = 2842` ✓ |
| pg_auth_members | 2843 | `AuthMemRelation_Rowtype_Id      = 2843` ✓ |
| pg_shseclabel   | **4065** | `SharedSecLabelRelation_Rowtype_Id = 4066` ✗ |
| pg_subscription | 6101 | `SubscriptionRelation_Rowtype_Id = 6101` ✓ |
| pg_class        | 83   | `RelationRelation_Rowtype_Id     = 83` ✓ |
| pg_attribute    | 75   | `AttributeRelation_Rowtype_Id    = 75` ✓ |
| pg_proc         | 81   | `ProcedureRelation_Rowtype_Id    = 81` ✓ |
| pg_type         | 71   | `TypeRelation_Rowtype_Id         = 71` ✓ |

`pg_shseclabel`'s 4065 value pre-dates the 18.x bootstrap reassignment.
The mismatch is silent on Phase2 (formrdesc just stamps 4066 on the
faked-up `tdtypeid`), but the moment Phase3's verification loop reaches
the `pg_shseclabel` entry it triggers `Assert(4066 == 4065)` → PANIC →
postmaster termination.

The symptom was dormant through Steps 3a–3u because earlier blockers
crashed the backend before Phase3's nailed-rel verification loop ran;
Step 3u (NULL `pg_attribute.attoptions`) was the last one in that chain.

## Fix

Change a single value in `internal/initdb/relcache_init.go::nailedSharedRels`:

```diff
- {3592, "pg_shseclabel", 4065, 'r', 6, true, pgShseclabelAttrs()},
+ {3592, "pg_shseclabel", 4066, 'r', 6, true, pgShseclabelAttrs()},
```

Effect (this flows through both write sites for the value automatically):

- `internal/initdb/initdb.go::pgClassRow(rel)` writes
  `rel.RelType = 4066` as the `reltype` column of the pg_shseclabel
  heap row.
- `internal/initdb/relcache_init.go::buildPgClassBlob(rel)` writes
  `rel.RelType = 4066` into the `Form_pg_class` blob inside
  `pg_internal.init` (used when the init-file layout bug is also
  fixed — see follow-up).

Either way, PG's Phase3 comparison reduces to `4066 == 4066`, the loop
completes, `criticalRelcachesBuilt` is set, the standby finishes booting,
and `pg_stat_wal_receiver` returns immediately.

## Regression pin

`internal/initdb/pg_nailed_reltype_test.go::TestNailedRelTypesMatchPG18FormrdescConstants`
asserts every nailed shared + local catalog's `RelType` against the
hardcoded `*Relation_Rowtype_Id` constant in
`postgres/src/include/catalog/pg_*_d.h`. The test covers all 9
formrdesc'd catalogs (5 shared from Phase2 + 4 local from Phase3) and
fails loudly if any future edit reintroduces the kind of off-by-one that
caused this PANIC loop.

## Verification

- `go test -count=1 -run TestNailedRelTypesMatchPG18FormrdescConstants
  ./internal/initdb/` — PASS (all 9 sub-tests).
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures unchanged (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestOpenOldClusterWithoutM0030FilesStillWorks`,
  `TestSynchronousCommitFlushesByDefault`); no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` —
  re-runs past the PANIC loop: standby's `pg_stat_wal_receiver` SELECT
  now returns, advancing the test to the next blocker.

## Carry-over

This step does **not** fix the underlying init-file layout bug (`rd_id`
at offset 0 vs PG18's offset 72; `rd_isnailed` never written; nailed-rel
sanity-check mismatch). The standby still falls back to `formrdesc` for
all 9 critical catalogs on every boot, then PG rewrites
`pg_internal.init` itself once Phase3 completes successfully. A future
step should rewrite `buildRelationDataBlob` to match PG18's actual
`RelationData` layout (488-byte struct with `rd_id` at offset 72,
`rd_isnailed` at offset 33, `rd_refcnt` at offset 24) so PG accepts our
init file directly and avoids the formrdesc fallback. Until then,
Phase3-via-formrdesc + PG-side init-file rewrite is functionally correct
but wastes I/O on every backend startup.
