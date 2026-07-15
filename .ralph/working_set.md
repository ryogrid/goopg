Task: M0122-0007 4e follow-up — `catalog.compositeTypes` (`CREATE TYPE ... AS
(...)`) gains per-database isolation, closing the composite-type slice of the
prior loop's resume point (last unaudited sibling map: domains, enumTypes
already done). COMPLETE, gates all green, committed (f08ba8d9).

Files: internal/catalog/catalog.go (CompositeType.DBOid field; new
compositeKey/lookupCompositeTypeByNameLocked helpers next to enumKey/
lookupEnumByNameLocked; RegisterCompositeType/RegisterCompositeTypeWithFields/
RenameCompositeType/SetCompositeTypeOwner/DropCompositeType/HasCompositeType/
LookupCompositeType/LookupCompositeTypeFields all gained a trailing variadic
dbOid; Catalog interface's LookupCompositeType/LookupCompositeTypeFields
signatures updated to match); internal/executor/operators_ddl.go (18
write-path call sites thread o.ctx.CurrentDatabaseOid: execCreateType's
composite branch, execAlterType's ADD/RENAME/DROP/ALTER ATTRIBUTE/RENAME TO/
OWNER TO branches, execAlterTypeAttrCmds, execDropType's composite branch);
internal/executor/operators_tx.go (undoEnumDDLFromContext's DropCompositeType
call threads ctx.CurrentDatabaseOid — grepped for this sibling PROACTIVELY
this loop, before running any tests, unlike the enum loop which found it via
test failure); internal/server/dispatch.go (undoEnumDDLForRollback's
DropCompositeType call threads its existing dbOid param, same proactive
grep); internal/executor/operators_tx_composite_test.go (fixed pre-existing
test: bare &Context{} had no CurrentDatabaseOid set while its
RegisterCompositeTypeWithFields calls implicitly resolved to DefaultDBOid —
a test-fixture inconsistency exposed by the new real dbOid plumbing, not a
product bug; fixed by setting ctx.CurrentDatabaseOid: catalog.DefaultDBOid
and passing catalog.DefaultDBOid explicitly to Register/Lookup calls in both
test funcs); new internal/catalog/create_composite_type_test.go
(TestCreateCompositeTypeCrossDatabaseIsolation, mirrors
TestCreateEnumCrossDatabaseIsolation); docs/design/
0097-0017-0001-enum-domain-types.md (new "Follow-up (2026-07-15, third
loop)" section) + docs/design/README.md (row updated); .ralph/
deferral_ledger.md (new row, incl. the RangeType finding below) + .ralph/
fix_plan.md (new [x] entry).

Key symbols: catalog.compositeKey, catalog.lookupCompositeTypeByNameLocked,
catalog.InMemory.RegisterCompositeTypeWithFields/LookupCompositeType/
DropCompositeType, executor.undoEnumDDLFromContext,
server.undoEnumDDLForRollback.

Hypothesis/Findings: Applied the sibling-path lesson from the enum loop
proactively — grepped operators_tx.go/dispatch.go for the second undo copy
BEFORE running tests, so no server-package regression this time. Still
caught one regression via the full targeted test run: operators_tx_composite_test.go's
bare Context{} had CurrentDatabaseOid=0 while RegisterCompositeTypeWithFields
(no dbOid arg) resolved to DefaultDBOid=1 — mismatch once the real dbOid
started flowing through undo. Fixed the test, not the product path (no real
Context ever has a literal 0 CurrentDatabaseOid). **Important finding while
auditing scope**: catalog.RangeType (CREATE TYPE ... AS RANGE) has NO DBOid
field and NO dbOid-taking methods at all (RegisterRangeType/RenameRangeType/
SetRangeTypeOwner/DropRangeType all lack the parameter) — confirmed by
direct grep, NOT assumed. This is the real next candidate in this series,
recorded in the deferral ledger (resume point 4) rather than incorrectly
assumed already fixed.

Next step: `catalog.RangeType` cross-database isolation — audit
RegisterRangeType/LookupRangeType/RenameRangeType/SetRangeTypeOwner/
DropRangeType's map shape in internal/catalog/catalog.go (search
`RegisterRangeType`), apply the identical DBOid-field + rangeKey(dbOid,name)
pattern (mirrors compositeKey/enumKey/domainKey exactly), thread dbOid
through execCreateType's RANGE branch/execAlterType's range-type RENAME TO/
OWNER TO branches/execDropType's range branch in operators_ddl.go. Grep
operators_tx.go + server/dispatch.go for a range-type ROLLBACK-undo sibling
UP FRONT (before running tests) — apply this loop's own lesson, don't wait
for a test failure to discover it. After range types, the domains/enums/
composites WAL-restart-persistence gap (deferral ledger resume points (1)
across all three rows) and the ~15-40 remaining read-only Lookup call sites
per type (resume point (2)) are the next tier of work in this series.

Gates run (all PASS this loop): go build ./...; go vet ./... (whole repo);
go test -count=1 ./internal/catalog/... ./internal/executor/...
./internal/server/... ./internal/planner/... (clean, incl. new
TestCreateCompositeTypeCrossDatabaseIsolation and the 2 fixed
operators_tx_composite_test.go tests); go test -short full repo excl.
testport (51 packages, 0 FAIL, internal/initdb=237s the long pole);
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke
ralph-precommit-test.sh — 1 transient "current transaction is aborted"
pgbench flake on first run (unrelated to this change: TPC-B touches
accounts/branches/tellers, no DDL types), confirmed as a flake via a clean
retry (0 failed, 3 workloads) before proceeding; pre-commit hook's own
pgbench-smoke gate PASSED on the actual commit. make ralph-state-guard —
clean, no repair needed this loop.

In-flight: none — task complete, committed (f08ba8d9), pushed step not yet
done (push is a separate explicit action, not part of this loop's gates).
Untouched foreign/stray files present at loop start and still present
(analysis/tpch-explain-baseline.md, ci/logs/launch.log, postgres submodule
dirty, weekly_loc.*, analysis/perf-optimize3/runs/*, kaitai-struct-dash*.txt)
— same as every prior loop, left alone (not part of this loop's diff).
