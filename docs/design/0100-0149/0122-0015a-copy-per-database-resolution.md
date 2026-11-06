# 0122-0015a — COPY resolves relations in the connecting database

**Milestone:** M0122-0015 (test-suite porting family). Kind: impl. Filed
2026-09-22 while porting `010_dump_connstr.pl`; landed 2026-11-01.

## The divergence

Inside any `CREATE DATABASE`-created database, `COPY t1 TO STDOUT` raised
`relation "t1" does not exist` while `SELECT count(*) FROM t1` in the SAME
session returned rows (both the unqualified and the `public.`-qualified
spelling failed). Consequence: `pg_dump <userdb>` emitted schema and died at
its first `COPY public.<t> FROM stdin` — **no user-created database was
dumpable**. The same shape broke the COPY FROM twin and the multi-statement
batch spelling `BEGIN; COPY t1 TO STDOUT; COMMIT`.

## Why it happened — the hand-built-context class

goopg shares one in-memory catalog across databases; per-database visibility
is carried by the executor context (`CurrentDatabaseOid` + the
`PgClassRows`/`pg_index`/… row closures installed by `wireExtensionRows`)
and by the plan-time catalog wrapper (`sessionPlanCatalog`'s `DBOid`, which
`LookupTable` keys off — M0122-0007 slices 4c/4e). The batch dispatch wires
both (dispatch.go). The standalone COPY entry point built its
`executor.Context` **by hand** and planned against the **raw**
`s.cfg.Catalog`, so relation lookup fell back to `DefaultDBOid`'s namespace.
This was the third field-by-field discovery on the same hand-built context:
M0119-0006 (GetSetting hooks), M0134-0009 R5 (session identity), and now the
database. The task demanded the durable close — wire it through the same
dispatch plumbing, not a fourth patch — plus a sibling audit.

## The fix

Two planning sites, Rule-#2 siblings:

| path | context source | planning shape (pre → post) |
|---|---|---|
| `dispatchCopyViaExecutor` (standalone COPY, own auto-commit tx) | hand-built ectx, no per-DB fields | `optimizer.Plan(stmt, s.cfg.Catalog)` → `optimizer.PlanWithSettings(stmt, sessionPlanCatalog(sess, cat, resolveConnDBOid(cat, connTx.DBName)), sessionPlannerSettings(sess))` + `ectx.PlanCatalog = planCat` + `s.wireExtensionRows(ectx, connTx.DBName)` |
| `runInlineCopy` (COPY inside a multi-statement batch) | borrows the batch ectx (already per-DB-wired) | `optimizer.Plan(cs, s.cfg.Catalog)` → `optimizer.PlanWithSettings(cs, ctxPlanCatalog(ectx, cat), ctxPlannerSettings(ectx))` — the `executeOneSimpleStmt` shape |

`sessionPlanCatalog`/`resolveConnDBOid` resolve the OID directly from the
connection's dbName because `wireExtensionRows` stamps `ectx` only later
(the dispatch PlanCatalog site's ordering note, M0122-0007 4c). `connTx ==
nil` (embedded/test connections) leaves `dbName` empty → OID 0 →
`effectiveDBOid` maps back to `DefaultDBOid` — the legacy behaviour is kept
exactly where there is no live connection.

## Sibling audit (the task's own requirement)

Every other `executor.NewContext` site in `internal/postmaster` is scoped on
purpose, so COPY was the last under-scoped one:

* `subscription_refresh.go` (tablesync COPY, M0122-0014) stamps
  `CurrentDatabaseOid = rr.dbOid` and passes the resolved `catalog.Table`
  into `NewCopyFromExecutor` directly.
* `role_ddl.go::runAuthidHeapTxn` stamps `DefaultDBOid` explicitly (shared
  `pg_authid` heap-sync guard; documented in place).
* `database_ddl.go` sites stamp the **created** database's oid
  (`newOid`) or `DefaultDBOid` for shared-catalog heap sync.
* `dispatch_extended.go` (the extended path) already calls
  `wireExtensionRows` (gap #7c).

## Pins & gates

* `TestCopyStandaloneResolvesNonDefaultDatabase`
  (`internal/postmaster/copy_executor_test.go`): user-db COPY TO (unqualified
  + `public.`), COPY FROM proven by re-read, the inline-batch twin, and the
  default-database `items` control. Mutation-checked both ways: reverting
  the standalone plan line fails with the production 42P01; reverting the
  twin line fails at the inline arm.
* Live: standalone + inline COPY TO/FROM in a user database, pg_dump of the
  user database emits its data and round-trips through psql (3/3 rows), the
  inline `BEGIN; … FROM STDIN; ROLLBACK` twin persists nothing, the
  default-database controls unchanged.
* Gates: `go build ./...` clean; postmaster units (the one failure,
  `TestSimpleQueryBatchAbortUndoesEarlierCreateTable`, reproduced
  identically twice on the **unmodified** tree under `-parallel 4` load and
  passes in isolation — recorded pre-existing load flake, no nightly
  subject, not filed); `TestPort_PgDump005Filterfile` all 11 subtests PASS.

## Test-port consequence

`005_pg_dump_filterfile`'s 23 unported assertions listed three blockers —
PREPARE qualified types (closed by M0122-0015c), the cross-database pg_proc
leak (closed by M0122-0015d — re-verified: zero `foo%` rows in `postgres`
after the sourcedb fixture), and this COPY defect. **18 of 23 ported** in
this commit (subtests `pg_dumpall_exclude_database`,
`restore_of_other_objects`); the remaining 5 (the include-index/trigger arm)
are blocked by a NEW measured defect: `pg_get_indexdef(oid)` and
`pg_get_triggerdef(oid)` return the **empty string**, so the custom archive
carries INDEX/TRIGGER TOC entries with empty bodies and a restore loses
every index and trigger (S2). Filed as **M0122-0015h**; ledger rows
2026-11-01. The extended protocol has no COPY route at all (scope note
ledgered).
