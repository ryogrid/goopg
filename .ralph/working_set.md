(idle — nothing in flight)

Loop #29 COMPLETE: M0119-0004 DU-002 slice 389 — CREATE COLLATION pg_dump
round-trip. The first NEW dumpable object since the COMMENT-ON seam (slices
386–388) was exhausted for every object type goopg already dumps.

A user collation lives in pg_collation with a user namespace; pg_dump's
getCollations filters the BKI-pinned pg_catalog built-ins and dumpCollation
reconstructs `CREATE COLLATION <schema>.<name> (provider = …, locale = …)` from
the catalog columns. goopg had NO CREATE COLLATION parser (hard parse error).

Files (committed, pushed):
- internal/parser/ast.go: new CreateCollationStmt.
- internal/parser/ddl.go: parseCreateCollationTail + `collation` CREATE-dispatch case.
- internal/catalog/catalog.go: UserCollation struct, userCollations field,
  CreateCollation + CollationAttrsByName; pgCollation.VirtualRows appends user rows.
- internal/executor/operators_ddl.go: execCreateCollation + dispatch case.
- internal/planner/planner.go: CreateCollationStmt → DDL wrap.
- internal/server/dispatch.go: "CREATE COLLATION" command tag.
- internal/parser/create_collation_test.go (new), internal/catalog/create_collation_test.go (new).
- internal/testport/pgdump_connsetup_test.go: CREATE COLLATION public.mycoll
  (LOCALE='C') fixture + assert `CREATE COLLATION public.mycoll (provider = libc,
  locale = 'C');`.
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 389 section.
- .ralph/fix_plan.md slice 389 entry; deferral ledger row appended.

Gates: TestParseCreateCollation, TestCreateCollationVirtualRows,
TestPort_PgDumpConnectionSetup all PASS; parser/catalog/planner/executor suites
PASS; go build clean; gofmt clean on my edits (3 files show pre-existing
go1.25/1.26 alignment noise NOT in my hunks); pgbench smoke = pre-commit hook;
ralph-state-guard OK.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: assert icu/builtin +
FROM-existing CREATE COLLATION forms through pg_dump; CREATE CONVERSION /
text-search-config dump (currently noop, 0 rows); aggregates (pg_proc prokind='a');
or persist user collations to a pg_collation heap for restart round-trip.
