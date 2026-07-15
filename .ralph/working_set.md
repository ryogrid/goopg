(idle — nothing in flight)

Loop #47 landed and committed (`91302a1d fix(catalog): add DBOid scoping to
userConversions (M0122-0007 4e)`), resuming exactly the next-step pointer
loop #46 left behind.

**Task:** M0122-0007 4e follow-up — `catalog.userConversions` cross-database
isolation (DU-002 round-trip probe unblock). `UserConversion` had no `DBOid`
field and `c.userConversions` was one flat, server-wide `[]*UserConversion`
slice, so `CREATE CONVERSION public.aliasconv ...` restoring into a fresh
second database collided with the source database's own same-named
conversion (`conversion "aliasconv" already exists`).

**Fix:** applied the identical M0122-0007 4e pattern, mirroring
`UserCollation` byte-for-byte (slice-of-pointers + `DBOid` field, NOT a map
— unlike `userOperatorClasses`): `UserConversion.DBOid uint32` added;
`CreateConversion`/`DropConversion` gained a trailing variadic
`dbOid ...uint32` (via `resolveDBOid`); `CreateConversionDuringRecovery`
stamps `DefaultDBOid` (WAL replay carries no dbOid yet). New
`ListUserConversionsForDBOid`/`PGConversionRowsForDBOid` (mirror the
collation pair — no BKI-builtins prefix since all ~130 pg_conversion
built-ins are pg_catalog-scoped and dump-filtered) replace the old
unfiltered `ListUserConversions()`-backed `pgConversion.VirtualRows`
closure. Per-connection wiring added end-to-end:
`executor.Context.PgConversionRows` (context.go), a `pg_conversion` branch in
`operators.go`'s virtual-row materializer (single site serving both simple +
extended protocols via the shared `ectx`), `dispatch.go`'s
`pgConversionRowLister` + `wireExtensionRows` wiring. Threaded
`catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)` through both
`operators_ddl.go` call sites (CREATE/DROP CONVERSION).

**Files touched:** `internal/catalog/catalog.go` (UserConversion struct +
CreateConversion/DropConversion/CreateConversionDuringRecovery/
ListUserConversionsForDBOid/PGConversionRowsForDBOid, ~lines 3142-3178 and
12645-12820ish), `internal/catalog/create_conversion_dbscope_test.go` (new,
`TestCreateConversionCrossDatabaseIsolation`), `internal/executor/context.go`
(`PgConversionRows` field), `internal/executor/operators.go` (pg_conversion
branch in the virtual-row materializer switch), `internal/executor/
operators_ddl.go` (2 call sites: CREATE CONVERSION ~line 16397, DROP
CONVERSION ~line 15275), `internal/server/dispatch.go`
(`pgConversionRowLister` interface + `wireExtensionRows` wiring),
`.ralph/fix_plan.md` (closed the userConversions bullet, added the new
ALTER-CONVERSION-parser-gap bullet), `.ralph/deferral_ledger.md` (resolved
row + fresh open row), `docs/design/0122-0018-per-database-catalog-namespace.md`
(new "Conversion registry ... dbOid scoping" section + status line),
`docs/design/README.md` (surgical Python string-replace on the single-line
0122-0018 row — do NOT use the Edit tool on that row, it is one 70KB+ line;
grep the row's tail near a unique recent keyword first, verify line count
stays 922 after the replace).

**Confirmed via a LIVE re-run of `TestPort_PgDumpConnectionSetup`** (not doc
archaeology): the DU-002 probe's failure point moved past `conversion
"aliasconv" already exists` entirely to a NEW blocker — the FIRST
non-catalog-scoping blocker in this whole M0122-0007 4e audit sequence:
`ALTER CONVERSION public.aliasconv OWNER TO postgres;` fails `syntax error
at or near "expected keyword table (got conversion)"`. `CONVERSION` is not a
recognized `ALTER <objtype>` keyword in the parser's grammar (the error
message's "expected keyword table" phrasing suggests the parser fell through
to a table-only `ALTER` production).

**Next DU-002 resume point (root-caused by the probe's error text, not yet
fixed):** this is a parser-grammar gap, not a catalog dbOid-scoping gap —
materially different mechanism from every M0122-0007 4e follow-up so far.
Grep the parser's `ALTER FUNCTION`/`ALTER COLLATION` OWNER-TO productions
(both parse today, since the probe reached this far past them) as the
sibling precedent for where to add an `ALTER CONVERSION <name> OWNER TO
<role>` production. Check pg_dump's `dumpConversion` in
`postgres/src/bin/pg_dump/pg_dump.c` for which other ALTER forms it actually
emits (RENAME TO / SET SCHEMA likely alongside OWNER TO) before broadening
scope beyond what the probe needs. Once parsed, wire the executor side:
`catalog.InMemory` needs `SetConversionOwner`/`RenameConversion`/
`SetConversionSchema` mirroring the `UserCollation` trio this loop's own fix
established as precedent (`SetCollationOwner`/`RenameCollation`/
`SetCollationSchema`, catalog.go ~12493-12573 — same file, same section,
easy to find). Verify via `go test -v -run '^TestPort_PgDumpConnectionSetup$'
./internal/testport/` (soft `t.Logf` probe — check the log line for the new
blocker after the fix; expect it to advance past `aliasconv`'s ALTER
statement to whatever the dump restores next).

**Key symbols:** `internal/parser`'s `ALTER` statement grammar (exact file
not yet located this loop — grep `ALTER COLLATION` or `ParseAlterCollation`-
shaped function names as the entry point), `catalog.InMemory.SetCollationOwner`/
`RenameCollation`/`SetCollationSchema` (catalog.go, the precedent trio to
mirror for conversions), `internal/executor/operators_ddl.go`'s `ALTER
...OWNER TO` dispatch switch (wherever COLLATION's case lives — that's where
a new CONVERSION case needs to be added once the parser accepts it).

Housekeeping: same pre-existing untracked scratch content noted by prior
loops (`postgres` submodule placeholder, `Markdown_Table_Repair_Design_Doc.md`,
`tools/mdtablefix/`, `analysis/perf-optimize3/runs/...`, various `.txt`/
`.csv`/`.png` files, `ci/batch/lib/__pycache__/`, `kaitai-struct-dash*.txt`)
— all left untouched, not part of this loop's commit.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean; `go
test ./internal/catalog/... ./internal/initdb/... ./internal/executor/...
./internal/server/... ./internal/wal/...` PASS; `go test -short $(go list
./... | grep -v /internal/testport)` (full repo, short mode) 0 FAIL
(includes `internal/initdb` ~222s — clean); `go test -v -run
'^TestPort_PgDumpConnectionSetup$' ./internal/testport/` PASS (soft-log
confirms advance to the ALTER CONVERSION blocker); `RALPH_PRECOMMIT_SCOPE=smoke
bash scripts/ralph-precommit-test.sh` PASS twice (once standalone, once via
the pre-commit git hook on the actual commit — 0 failed, all 3 pgbench
workloads both times); `make ralph-state-guard` — found+auto-repaired the
same stale `progress.json` "completed"/loop-47-vs-running mismatch pattern
prior loops have hit (previous loop's clean-exit marker, not a real
project-completion state), then reported consistent.
`scripts/tpch-spotcheck.sh` NOT run this loop (executor/planner untouched —
only virtual-catalog registry/row-emission code changed, no query
plan/row-count-affecting code path); a future executor/planner-adjacent loop
should still run it per Hard-won Rule #1.

In-flight: none. git push status: `wal-format-mod` is ahead of
`origin/wal-format-mod` by 4 commits (`91302a1d` this loop's + the 3
pre-existing ones noted by loop #46's handoff: `e9245a0c`, `d33222d6`,
`9c2ab21d`). Not pushed — push requires explicit human direction per this
repo's standing git-safety protocol.
