# M0143-0001 — an in-process test that crosses a DATABASE boundary

Status: partial — the postmaster-level DDL/DML chain half landed; the
reload-loop half (multi-database WAL-replay/restart) remains open.

## Gap this closes

`CREATE DATABASE` is intercepted by string-prefix match
(`classifyDatabaseDDL`/`tryHandleDatabaseDDL`, `internal/postmaster/database_ddl.go`)
before the in-process SQL parser ever sees it (`internal/postmaster/dispatch_extended.go:634-645`),
so nothing in `internal/executor`'s fast test harness (`newVMFixture`/`newHOTFixture`)
could exercise `CREATE DATABASE` followed by DDL/DML against the new
database's own real, catalog-allocated oid. The 2026-09-17 scoping
investigation (recorded in `.ralph/fix_plan.md` under this task) found the
individual halves already had in-process coverage —
`internal/postmaster/database_ddl_test.go` calls `tryHandleDatabaseDDL`
directly, and `internal/executor/fk_dbid_routing_test.go` exercises per-dbOid
FK routing — but always with `ctx.CurrentDatabaseOid` set to a *synthetic*
constant (e.g. `7001`), never a real oid a `CREATE DATABASE` call actually
allocated and resolved through `wireExtensionRows` the way a live client
connection does. Nothing chained the two halves.

## What landed (`internal/postmaster/database_ddl_chain_test.go`)

`TestDatabaseDDLChainedExecutorDML`:

1. `s.tryHandleDatabaseDDL("CREATE DATABASE r", ...)` — real dispatch path.
2. `s.wireExtensionRows(ectx, "r")` — the same per-connection resolution a
   real client goes through, stamping `ectx.CurrentDatabaseOid` with `r`'s
   real, catalog-allocated oid (not a hand-picked constant).
3. `CREATE TABLE parent`/`child` (with a `REFERENCES` FK) run against that
   context via the executor pipeline (parser → `optimizer.Plan` →
   `executor.Build` → `Open`/`Next`/`Close`), mirroring
   `internal/executor/storage_ddl_test.go`'s `runDDL` pattern.
4. A second context, bound the same way to `"postgres"`, gets its own
   `other`/`otherchild` tables + FK.
5. `SELECT conname FROM pg_constraint WHERE contype = 'f'` under each
   context must return **exactly that context's own** FK row — checked by
   `conname` identity (`child_pid_fkey` / `otherchild_pid_fkey`), not just
   row count.
6. Cross-database namespace isolation: `SELECT ... FROM parent` under the
   `postgres` context, and `SELECT ... FROM other` under the `r` context,
   must both fail with "relation does not exist".

This exercises `pgConstraintTableRel`'s per-database FK routing
(`internal/executor/sys_pg_constraint.go`, `PGConstraintRowsForDBOid`'s
`tbl.ForeignKeys` loop) and the per-database table-namespace split a plain
`CREATE TABLE` lands in — the exact two paths M0143-0001's filing named as
previously reachable only through manual `psql` sessions.

## A count-only assertion would have missed a real swap (test-design note)

While writing the test, temporarily forcing `PGConstraintRowsForDBOid`'s
`dbOid` argument to `DefaultDBOid` unconditionally (simulating the same
"hardcoded default database" bug class `M0143-0002`/`-0002b` found in other
call sites) did **not** change either context's row *count* — both still
read back exactly 1 row — because `db r`'s query silently returned
`otherchild`'s FK instead of `child`'s. A count-only assertion
(`len(rows) == 1`) would have passed on the broken code. The test instead
asserts the `conname` value itself, which failed correctly under the
mutation and passed once it was reverted. Kept as an explicit code comment
at the assertion site so a future edit doesn't quietly weaken it back to a
count check.

## NamespaceDBOid: an already-documented, unrelated non-bug this test brushed against

`catalog.NamespaceDBOid` (`internal/catalog/catalog.go:25197`) deliberately
maps a connection oid of `0` **or `PostgresDBOid` (5)** back to
`DefaultDBOid` (1), because every catalog write path still unconditionally
persists under `DefaultDBOid` until per-database slice 4d migrates them
(see that function's own comment). This test's `"postgres"` context
therefore reads/writes `DefaultDBOid`'s namespace — expected, current,
already-documented behavior, not something this task found or needs to fix.
The test's own two databases (`r`, a genuinely distinct oid, vs `postgres`,
which resolves to `DefaultDBOid`) are still a valid two-database check
because `r`'s oid is never 0 or 5.

## What's still open — the reload-loop half

Exercising the *reload* `ListDatabases` loop
(`internal/initdb/catalog_heap_reload.go:252,335,442,730,884`,
`internal/initdb/open.go:1564,3577,3873`) and `pgConstraintTableRel`'s
per-DB branch under a genuine multi-database WAL-replay/restart in-process
(no server socket/psql, but heavier than a single `Server`+`Context` —
needs `internal/initdb`'s open/recovery entry point against a temp data dir
directly) has no existing precedent and was explicitly scoped out of this
loop, per the original filing's own resume point ("scope the reload-loop
half as a likely-separate follow-up... rather than attempting both in one
sitting"). `internal/testport/database_template_oid_collision_test.go:9-11`
also flags that the in-process `*postmaster.Server` harness "hangs on
multi-DB-write shutdown" — a pre-existing harness limitation that may bite
this half specifically and should be understood before attempting it.

## Gates

`go build ./...` clean. `go test ./internal/postmaster/... ./internal/catalog/... ./internal/executor/...`
PASS. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(units scope does not include `internal/postmaster`, run separately above).
No production code changed — test-only, no deferral-ledger row needed for
this half (the reload-loop half stays tracked by the still-open task text
in `.ralph/fix_plan.md`, not a ledger row, since nothing was landed there to
defer).
