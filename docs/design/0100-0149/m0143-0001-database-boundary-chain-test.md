# M0143-0001 — an in-process test that crosses a DATABASE boundary

Status: **complete** — both halves landed: the postmaster-level DDL/DML chain
(`TestDatabaseDDLChainedExecutorDML`) and the reload-loop restart chain
(`TestDatabaseDDLReloadAcrossRestart`, added 2026-09-17).

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

## The reload-loop half (`internal/postmaster/database_ddl_reload_test.go`, landed 2026-09-17)

### Why the `*postmaster.Server`-with-`Run()` harness was avoided

`internal/testport/database_template_oid_collision_test.go:9-11` says the
in-process `*postmaster.Server` harness "hangs on multi-DB-write shutdown."
Tracing it: that harness calls `srv.Run(ctx)` (a real loopback listener +
accept loop), and shutdown is pure `ctx` cancellation — `Server.Run`
(`internal/postmaster/server.go:582-701`) has no separate `Stop` method. None
of the existing in-process `Server` test harnesses (`dbidRestartServer` and
siblings) set `shutdownDeadline`, so after the accept loop returns, shutdown
always falls to the **unbounded** branch, `s.connWG.Wait()`
(`server.go:693`) — every accepted connection's handler goroutine must
`Done()` this WaitGroup by returning, which only happens on client-side
`Close()`/read error. A backend goroutine still alive for any reason (an
un-closed pooled `sql.DB`, or a handler stuck inside a per-database write
path) blocks this forever, with no diagnostic (the goroutine-dump-on-timeout
at `server.go:686-689` only fires on the *bounded* branch, which nothing here
uses). No repro or stack trace was ever captured — the claim traces to a
single commit message (`29a38bb24`) with no further diagnosis.

This test sidesteps the whole mechanism: `postmaster.Server` is constructed
via `New(Config{...})` and used purely as a **method-holder** for
`tryHandleDatabaseDDL`/`wireExtensionRows` — `Run()` is never called, so
there is no listener, no accept loop, no `connWG`, and the hang cannot occur
by construction. Durability and the actual restart ride
`internal/initdb.Runtime` directly (`Open`/`Close`/`Open` on the same data
dir), mirroring `internal/initdb/heap_catalog_load_test.go`'s existing
restart shape.

### Config.TxnMgr — a durability gap this test would have hidden

`syncPgDatabaseHeapRow` (the write that makes `CREATE DATABASE`'s
`pg_database` row, `global/1262`, survive a restart) is gated on
`s.cfg.TxnMgr != nil` inside `runPgDatabaseHeapTxn`
(`internal/postmaster/database_ddl.go:1365-1368`) and silently no-ops
otherwise — no error at CREATE DATABASE time. The already-landed DDL-chain
test never sets `Config.TxnMgr` (it builds its own separate
`transam.Manager`, since it never restarts), so it never exercised this
write. Verified live: building this test's `Server` `Config` without
`TxnMgr` reproduces exactly that — `ListDatabases()` comes back empty after
restart, with no error anywhere in the chain. Fixed by passing
`TxnMgr: rt.TxnMgr` in the `Config` (see `runPgDatabaseHeapTxn`'s own
signature for why: it needs a transaction manager to open the short-lived
internal write transaction the pg_database sync runs under).

### What `TestDatabaseDDLReloadAcrossRestart` covers

Two non-default databases (`r1`, `r2`), each with its own FK-bearing table
pair, through `initdb.Init` → `initdb.Open` → DDL (commits, unlike the
DDL-chain test's intentionally-uncommitted style) → `Close` → `Open` again on
the same dir:

1. `reloadDatabasesFromHeap` (`internal/initdb/catalog_heap_reload.go:1002`)
   repopulates `ListDatabases()` with both `r1`/`r2` post-restart — this is
   the *precondition* every per-database pass in that file depends on.
2. `loadForeignKeysFromHeap`'s per-database scan
   (`loadForeignKeysFromHeapForDB`, keyed by `pgConstraintTableRel`'s
   `tableCatalogHeapDBOid` routing) reconstructs each database's own FK with
   the right **identity** post-reload, checked by `conname`
   (`child_pid_fkey` / `otherchild_pid_fkey`), not just row count.
3. Namespace isolation survives reload: neither database's table is visible
   under the other's real oid after the restart.

Verified both directions live: (a) omitting `Config.TxnMgr` (above) makes
the test fail exactly as predicted; (b) temporarily forcing
`loadForeignKeysFromHeap`'s per-database loop to always pass
`catalog.DefaultDBOid` instead of the real per-DB `dbOid` (simulating a
reload-time version of the same "hardcoded default database" bug class
`M0143-0002`/`-0002b` found elsewhere) makes both FK assertions fail with
empty results (`[]`, want `[child_pid_fkey]`/`[otherchild_pid_fkey]`) —
confirming the test genuinely exercises the reload loop's per-database
routing, not just the already-covered query-time routing. Both mutations
were reverted before commit; production code carries no diff from this task.

## Gates

`go build ./...` clean; `go test ./internal/postmaster/... ./internal/catalog/...
./internal/initdb/... ./internal/executor/...` PASS; `go vet
./internal/postmaster/...` clean; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (units scope excludes
`internal/postmaster`, run separately above). No production code changed —
test-only, no deferral-ledger row needed.
