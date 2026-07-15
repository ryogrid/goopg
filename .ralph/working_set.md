(idle — nothing in flight)

Loop #46 landed and committed (`e9245a0c fix(catalog): add opclass->opfamily
pg_depend edge for member-less operator classes (DU-002)`), resuming exactly
the next-step pointer loop #45 left behind.

**Task:** M0122-0007 4e follow-up — `catalog.PGDependRowsForDBOid` was
missing a `pg_depend` row between a member-less (`STORAGE`-only) operator
class and its owning operator family, so real pg_dump's dependency-driven
topological sort could emit `CREATE OPERATOR CLASS` before `CREATE OPERATOR
FAMILY` on restore (`operator family "op_family" does not exist for access
method "btree"`).

**Fix:** added an unconditional row per `c.userOperatorClasses` entry
(filtered `oc.DBOid == dbOid && oc.FamilyOID != 0`): `classid=2616`
(pg_opclass), `objid=<class OID>`, `refclassid=2753` (pg_opfamily),
`refobjid=<class.FamilyOID>`, `deptype='a'`. Verified live against
`postgres/src/backend/commands/opclasscmds.c:731-735` (`DefineOpClass`'s
`recordDependencyOn(&myself, &referenced, DEPENDENCY_AUTO)`) that upstream
uses AUTO ('a'), NOT NORMAL ('n') as the prior loop's note guessed from
memory — corrected here. `pg_dump.c`'s `getDependencies` treats 'a'/'n'
identically for ordering (only excludes 'p'/'e'), so this correction was
about byte-matching upstream, not fixing a functional bug.

**Files touched:** `internal/catalog/catalog.go` (`PGDependRowsForDBOid`,
~line 13860, new loop right before `return rows`), `.ralph/fix_plan.md`
(closed the M0122-0007 4e opclass/pg_depend bullet + added a new open bullet
for the next resume point), `.ralph/deferral_ledger.md` (resolved row +
fresh open row), `docs/design/0122-0018-per-database-catalog-namespace.md`
(new "`PGDependRowsForDBOid` opclass→opfamily pg_depend edge" section +
status line), `docs/design/README.md` (surgical Python string-replace on the
single-line 0122-0018 row — do NOT use the Edit tool on that row, it is one
70KB+ line; the exact text to search for changes every loop, `grep -o` for
the row's tail near a unique recent keyword first).

**Confirmed via a LIVE re-run of `TestPort_PgDumpConnectionSetup`** (not doc
archaeology): the DU-002 probe's failure point moved past the family/class
ordering collision entirely to a NEW blocker: `conversion "aliasconv"
already exists` when restoring into the fresh `dumprestore_du002` database.

**Next DU-002 resume point (root-caused by inspection, not yet fixed):**
`catalog.UserConversion` (catalog.go ~line 3141) has NO `DBOid` field, and
`c.userConversions` (catalog.go ~line 2325) is one flat, server-wide
`[]*UserConversion` slice — same collision shape the M0122-0007 4e series has
now fixed 8 times (Domain/UserCollation/enumTypes/compositeTypes/RangeType/
Routines/AccessMethod/UserOperatorFamily+UserOperatorClass). Apply the
identical pattern: add `DBOid uint32`, fold a leading dbOid into the
conversion lookup key (mirror `userOpFamilyKey`/`accessMethodKey`), thread a
trailing variadic `dbOid ...uint32` through register/lookup/drop (grep
`RegisterUserConversion`/conversion-drop in catalog.go first — exact names
not yet confirmed), wire `internal/executor/operators_ddl.go`'s `CREATE
CONVERSION`/`DROP CONVERSION` call sites via
`catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)`. New test mirroring
`TestAccessMethodCrossDatabaseIsolation`/
`TestOperatorFamilyAndClassCrossDatabaseIsolation`. Verify via `go test -v
-run '^TestPort_PgDumpConnectionSetup$' ./internal/testport/` (soft `t.Logf`
probe — check the log line for the new blocker after the fix).

**Key symbols:** `catalog.UserConversion` (struct, no DBOid yet),
`c.userConversions` (`[]*UserConversion`, catalog.go ~2325), whatever
Register/Lookup/Drop functions exist around catalog.go ~12659-12739 (seen in
this loop's ledger grep but not yet read in full), `internal/executor/
operators_ddl.go`'s CREATE/DROP CONVERSION handlers (not yet located).

Housekeeping: a concurrent interactive session landed an unrelated commit
(`d33222d6 "table fixing test"`, adds `tmp/deferral_ledger.md` — a
markdown-table-repair-tool test artifact) between this loop's pre-commit
gate run and its actual commit; zero file overlap with this loop's change,
confirmed via `git show --stat`, not a concern. The main tree still also
carries the same pre-existing untracked scratch content noted by loop #45
(`postgres` submodule placeholder, `Markdown_Table_Repair_Design_Doc.md`,
`tools/mdtablefix/`, `analysis/perf-optimize3/runs/...`, various `.txt`/
`.csv`/`.png` files) — all left untouched, not part of any Ralph commit.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean; `go
test ./internal/catalog/...` PASS; `go test -short $(go list ./... | grep -v
/internal/testport)` (full repo, short mode) 0 FAIL (includes
`internal/initdb` 222s, `internal/server` 18s, `internal/wal` 7s — all
clean); `go test -v -run '^TestPort_PgDumpConnectionSetup$'
./internal/testport/` PASS (soft-log confirms advance to the conversions
blocker); `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh`
PASS twice (once standalone pre-commit check, once via the pre-commit git
hook on the actual commit — 0 failed, all 3 pgbench workloads both times);
`make ralph-state-guard` — found+auto-repaired a stale `progress.json`
"failed"/loop-46-vs-completed mismatch (previous loop's clean-exit marker,
not a real project-completion state), then reported consistent.
`scripts/tpch-spotcheck.sh` NOT run this loop (executor/planner untouched —
only a virtual-catalog pg_depend row-emission function changed, no query
plan/row-count-affecting code path); a future executor/planner-adjacent loop
should still run it per Hard-won Rule #1.

In-flight: none. git push status: `wal-format-mod` is ahead of
`origin/wal-format-mod` by 3 commits (`e9245a0c` this loop's + `d33222d6` +
the pre-existing `9c2ab21d` from loop #45, none pushed). Not pushed — push
requires explicit human direction per this repo's standing git-safety
protocol.
