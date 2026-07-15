Task: M0122-0007 4e follow-up — `catalog.enumTypes` (`CREATE TYPE ... AS
ENUM`) gains per-database cross-database isolation, resuming the exact next
candidate the prior loop's ledger row named. COMPLETE, gates all green,
about to commit.

Files: internal/catalog/catalog.go (EnumType.DBOid field; new enumKey/
lookupEnumByNameLocked helpers next to domainKey/lookupDomainByNameLocked;
RegisterEnum/RenameEnum/RenameEnumValue/SetEnumOwner/AddEnumValue/
AddEnumValueResult/RemoveEnumValue/DropEnum/LookupEnum all gained a
trailing variadic dbOid; Catalog interface's LookupEnum signature updated
to match); internal/executor/operators_ddl.go (7 write-path call sites
thread o.ctx.CurrentDatabaseOid: execCreateType/execAlterType's RENAME
VALUE/RENAME TO/OWNER TO/ADD VALUE/execDropType's enum branch);
internal/executor/operators_tx.go (undoEnumDDLFromContext's 3 calls thread
ctx.CurrentDatabaseOid); internal/server/dispatch.go +server.go +
twophase.go (SIBLING undo path `undoEnumDDLForRollback` — found by test
failure, NOT threaded by the executor-package fix alone — gained a dbOid
param, threaded from 7 call sites total: 5 in dispatch.go use in-scope
ctx.CurrentDatabaseOid, twophase.go's abortForPrepareSSIFailure too,
server.go's connection-teardown path has no ctx so resolves via the
pre-existing resolveConnDBOid(cat, connTx.DBName) helper); new
internal/catalog/create_enum_test.go (TestCreateEnumCrossDatabaseIsolation,
mirrors TestCreateDomainCrossDatabaseIsolation); docs/design/
0097-0017-0001-enum-domain-types.md (new "Follow-up (2026-07-15, later
loop)" section incl. the sibling-path bug writeup) + docs/design/README.md
(row updated); .ralph/deferral_ledger.md (new row); .ralph/fix_plan.md (new
[x] entry).

Key symbols: catalog.enumKey, catalog.lookupEnumByNameLocked,
catalog.InMemory.RegisterEnum/LookupEnum/DropEnum, executor.
undoEnumDDLFromContext, server.undoEnumDDLForRollback, server.
resolveConnDBOid.

Hypothesis/Findings: M-NIGHTLY queue empty this loop (ci/logs/action-
items.md run 20260715-010036, all 11 items already [x] in fix_plan.md —
confirmed via grep). Resumed exactly where working_set left off: audit
catalog's enum registry for the DBOid-less collision the DU-002 probe hit
(`type "gtype" already exists`). Confirmed `c.enumTypes` was the same
bare-name-keyed map shape `domains`/`userCollations` had before their
fixes; applied the identical domainKey/lookupDomainByNameLocked pattern.
**Caught a real regression before committing**: threading only the
executor package's `undoEnumDDLFromContext` broke
`TestSimpleQueryMidBatchBeginUndoesEarlierAutocommitCreateType`/`...AddValue`
in internal/server — dispatch.go has a SECOND, independent copy of the same
undo logic (`undoEnumDDLForRollback`) for the simple-query ROLLBACK/failed-
COMMIT/SSI-abort/two-phase-abort/teardown paths, which still called
Remove/Rename/DropEnum with no dbOid (silently defaulting to DefaultDBOid),
mismatching the raw — possibly 0 in embedded/test contexts —
ctx.CurrentDatabaseOid used at CREATE time. This is exactly the
`pattern_sibling_paths_must_agree` memory's failure mode; fixed by threading
dbOid through the second path too (grep for ALL call sites of the mutator
methods, not just the ones in the package you're already editing, is the
generalizable lesson). Full local suite (all packages, all 3 gates) is
clean after the fix.

Next step: composite types (`c.compositeTypes`/`compositeTypeNames`,
`internal/catalog/catalog.go`) are very likely the next same-shaped
DBOid-less collision — the last unaudited sibling map in this M0122-0007 4e
series (domains, userCollations, and now enums are done). Audit
`RegisterCompositeType`/`RegisterCompositeTypeWithFields`/
`RenameCompositeType`/`SetCompositeTypeOwner`/`DropCompositeType`'s map
shape first, then apply the domainKey/enumKey pattern; remember to grep
internal/server/dispatch.go for a possible sibling undo/rollback path too
(PendingCreatedComposites already exists in both undoEnumDDLFromContext and
undoEnumDDLForRollback's composite-drop steps — check those too if
composite types get their own dbOid threading).

Gates run (all PASS this loop): go build ./...; go vet ./... (whole repo);
go test ./internal/catalog/... ./internal/executor/... ./internal/server/...
./internal/planner/... (clean, incl. new TestCreateEnumCrossDatabaseIsolation
and the 2 previously-broken-then-fixed server tests); go test -short full
repo excl. testport (all packages, 0 FAIL); go test -v -run
'^TestPort_PgDumpConnectionSetup$' ./internal/testport/ PASS (probe moved to
a new `DEFAULT 'na'::character varying` CAST-target parser gap, logged not
failed); scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33);
RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh PASS clean (0 failed, 3
workloads); make ralph-state-guard — auto-repaired 1 stale marker (previous
loop's clean-exit progress.json), consistent after.

In-flight: none — task complete; commit (pathspec-scoped, per the concurrent-
loop-commit rule) + pre-commit hook's own pgbench smoke + push are the only
remaining mechanical steps. Untouched foreign/stray files present at loop
start and still present (analysis/tpch-explain-baseline.md, ci/logs/
launch.log, postgres submodule dirty, weekly_loc.*, analysis/perf-optimize3/
runs/*, kaitai-struct-dash*.txt) — same as every prior loop, left alone (not
part of this loop's diff).
