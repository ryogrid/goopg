# M0106-0010 Step 3ct — pg_database heap row matches PG18 Form_pg_database

## Context

Step 3cs populated `global/2672` (pg_database_oid_index), so PG's
`CheckMyDatabase → SearchSysCache1(DATABASEOID, …)` now finds the
template1 / postgres heap tuples. The next blocker, predicted at the
end of Step 3cs's note, was:

```
TRAP: failed Assert("j > attnum"), File: "heaptuple.c", Line: 642
client backend ... was terminated by signal 6: Aborted
```

This fires when every PG-standby user backend reaches `CheckMyDatabase`
and tries to read `datcollversion` via
`SysCacheGetAttr(DATABASEOID, tup, Anum_pg_database_datcollversion)`.

## Root cause

`bootstrapPostgresDatabase` emitted a 16-column row that pre-dated
PG15/PG18 schema changes:

* Missing `dathasloginevt` (col 8 in PG18, bool).
* Missing `daticurules` (col 16, text, nullable).
* `daticulocale` (col 14) renamed to `datlocale` in PG18.
* `datcollate` / `datctype` are `text` in PG18, not `name` (the
  pre-PG15 column type) — different on-disk layout entirely
  (varlena vs. raw 64-byte fixed).
* `datacl` is `aclitem[]`, not `text`.

The row also lacked `HEAP_HASVARWIDTH` in `t_infomask`. PG18's
`nocachegetattr` (heaptuple.c:520) special-cases that bit:

```c
if (HeapTupleHasVarWidth(tup))
{
    int j;
    for (j = 0; j <= attnum; j++)
        if (TupleDescCompactAttr(tupleDesc, j)->attlen <= 0) {
            slow = true;
            break;
        }
}
```

When the bit is unset, the early walk is skipped. Execution falls
through to the no-nulls/no-varlena fast path, which iterates forward
caching offsets and **asserts `j > attnum`** at exit (line 642). For
any tupleDesc that genuinely has a var-width attribute at position
≤ `attnum`, the loop breaks at `j ≤ attnum` and the assertion fires.

Because PG18's hardcoded `Desc_pg_database` (loaded by `formrdesc`)
has `datcollate` (attlen=-1) starting at attnum 13, any
`SysCacheGetAttr` lookup for attnums 13–18 trips this assertion when
HEAP_HASVARWIDTH is missing. `CheckMyDatabase`'s read of attnum 17
(`datcollversion`) is the first one to fire.

The fix-up bit in `pg_database` is doubly load-bearing: pg_database is
one of PG18's five **formrdesc'd shared critical catalogs**
(`relcache.c:4075-4083`), so the standby never consults goopg's
pg_attribute heap or relcache init file for this rel — only the bare
heap bytes through PG18's compile-time TupleDesc. Schema drift in our
row layout is fully invisible to local tests but produces immediate
FATALs on every standby user-backend connect.

## Fix

`internal/initdb/initdb.go::bootstrapPostgresDatabase` rewrites the
schema to PG18-canonical 18 columns sourced verbatim from
`postgres/src/include/catalog/pg_database.h`:

| # | name             | type      | notes                          |
|---|------------------|-----------|--------------------------------|
| 1 | oid              | oid       |                                |
| 2 | datname          | name      | NAMEDATALEN=64                 |
| 3 | datdba           | oid       | bootstrap superuser            |
| 4 | encoding         | int4      | 6 = PG_UTF8                    |
| 5 | datlocprovider   | char      | 'c' = libc                     |
| 6 | datistemplate    | bool      |                                |
| 7 | datallowconn     | bool      |                                |
| 8 | dathasloginevt   | bool      | PG18 addition                  |
| 9 | datconnlimit     | int4      | -1 = unlimited                 |
| 10 | datfrozenxid    | xid       |                                |
| 11 | datminmxid      | xid       |                                |
| 12 | dattablespace   | oid       | 1663 = pg_default              |
| 13 | datcollate      | text      | "C" (was `name` pre-PG15)      |
| 14 | datctype        | text      | "C" (was `name` pre-PG15)      |
| 15 | datlocale       | text      | NULL — libc, no ICU spec       |
| 16 | daticurules     | text      | NULL — PG18 addition           |
| 17 | datcollversion  | text      | NULL — `BKI_DEFAULT(_null_)`   |
| 18 | datacl          | aclitem[] | NULL — default public access   |

The tuple now routes through a small local helper that:

* Computes the PG-convention null bitmap via `executor.NullBitmapPG`
  (4 trailing nullable cols → bits 14–17 clear).
* Constructs the tuple via `storage.NewHeapTupleWithNulls` so
  `HEAP_HASNULL` and `t_hoff = MAXALIGN(23 + 3)` = 32 are stamped.
* Sets `HEAP_HASVARWIDTH` explicitly — this is the actual fix for the
  reported assertion.

`relcache_init.go::pgDatabaseAttrs` is updated in lockstep so goopg's
own pg_attribute heap rows + init-file blob agree on the 18-column
layout (`RelNatts` bumped 16 → 18). Comment block on `pgDatabaseAttrs`
cites `pg_database.h` and explains why this list and the bootstrap
row builder must move together.

## Regression pins

`internal/initdb/pg_database_pg18_schema_test.go`:

* `TestPgDatabaseAttrsMatchesPG18FormPgDatabase` — pins each of the
  18 attrs by (name, TypeOID, Num, Len) against the authoritative
  PG18 column list. The strict count check rejects future additions
  that forget to update this fixture in lockstep with the row
  builder.
* `TestBootstrapPostgresDatabaseTupleHasVarWidthAndNullBitmap` —
  end-to-end pin: invokes `bootstrapPostgresDatabase` into a temp
  dir, walks the page header to find both heap tuples (template1 +
  postgres), and asserts that `HEAP_HASVARWIDTH` and `HEAP_HASNULL`
  are both set and `t_natts` reads back as 18 on each. This guards
  the actual byte that closes the assertion path; a future encoder
  rewrite cannot silently drop the flag without tripping this test.

## Verification

```
$ go test -count=1 -run 'TestPgDatabaseAttrsMatchesPG18FormPgDatabase|TestBootstrapPostgresDatabaseTupleHasVarWidthAndNullBitmap' ./internal/initdb/
ok    github.com/goopg/goopg/internal/initdb    0.025s

$ go test -count=1 ./internal/initdb/
FAIL — same 15 pre-existing baseline failures as Step 3cs
       (TestMigration*, TestCreate*, TestBootstrappedPG*,
        TestSynchronousCommitFlushesByDefault,
        TestOpenOldClusterWithoutM0030*,
        TestSystemCatalogRelfilesAreValidHeapPages,
        TestCommittedTableSurvivesCrashRestart,
        TestRuntimeCloseTriggersFinalCheckpoint,
        TestMultipleTablesLoadFromHeap)
no new regressions; baseline-diff confirmed via `git stash` + rerun

$ go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/
all PASS

$ GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -count=1 -timeout 180s \
    -run TestE2E_FailoverGoopgToPG/async ./internal/testport/
```

The E2E run no longer emits the `Assert("j > attnum")` TRAP. The
first FATAL is now:

```
FATAL: XX000: could not open relation with OID 2964
```

OID 2964 = `pg_db_role_setting` (DbRoleSettingRelationId), opened by
`process_settings(MyDatabaseId, GetSessionUserId())` immediately after
`CheckMyDatabase` returns. The follow-on `invalid attalign value:`
FATALs in the same log are the cascade documented in Step 3cs: the
first failed `InitPostgres` leaves stale catcache state and every
follower backend trips a different code path before reaching the
real first FATAL. Step 3cu's scope is to seed `pg_db_role_setting`
(local OID 2964, 4 columns per `pg_db_role_setting.h`) so the
relcache lookup succeeds.
