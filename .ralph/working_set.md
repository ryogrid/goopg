(idle — nothing in flight)

Loop #31 COMPLETE: M0119-0004 DU-002 slice 391 — ICU / non-deterministic / FROM
collation pg_dump round-trip (PRODUCTION fix + virtual-NULL infra). Closes the
slice-389 deferral (only libc was asserted). Two real bugs fixed:
  1. execCreateCollation FROM branch dropped Deterministic (PG DefineCollation
     copies collform->collisdeterministic) → now copies src.Deterministic.
  2. empty `text` virtual cells decoded as '' not NULL → dumpCollation ICU branch
     emitted spurious `, rules = ''`. New catalog.VirtualNull sentinel mapped to
     NullConst at top of planner.TypedVirtualCell (shared by executor
     rematerialiseVirtualRows sibling); pg_collation user-row builder emits
     VirtualNull for absent locale/rules columns per provider.

Files (commit pending push):
- internal/executor/operators_ddl.go: FROM copies src.Deterministic.
- internal/catalog/catalog.go: VirtualNull const + pg_collation user-row nz()/NULL.
- internal/planner/planner.go: TypedVirtualCell VirtualNull→NullConst guard.
- internal/catalog/create_collation_test.go: ci_coll non-deterministic assertion.
- internal/testport/pgdump_connsetup_test.go: ci_coll + ci_from fixtures/asserts.
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 391 section.
- .ralph/fix_plan.md slice 391 entry; deferral ledger row appended.

Gates: TestCreateCollationVirtualRows + TestPort_PgDumpConnectionSetup PASS;
catalog+planner+parser suites PASS; go build clean; gofmt clean on my hunks
(only pre-existing go1.25/1.26 alignment noise elsewhere); ralph-state-guard OK.
Pre-commit pgbench smoke runs via hook on commit.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: ICU `rules` clause
(collicurules — parser+executor+virtual row, would re-emit `, rules = '...'`);
convert the 7 BKI built-in pg_collation rows' empty locale cells to VirtualNull
for full NULL fidelity; CREATE CONVERSION dump (new object); aggregates
(pg_proc prokind='a'); or persist user collations to a pg_collation heap.
