(idle — nothing in flight)

Loop #32 COMPLETE: M0119-0004 DU-002 slice 392 — ICU collation `rules` round-trip.
Closes the slice-391 deferral (a) (rules unmodelled). Last unexercised limb of
dumpCollation's `provider = icu` branch (`if (collicurules) { …, rules = … }`,
pg_dump.c:14988). Changes:
  - parser: RULES moved from accept-and-ignore default → `case "rules"`; new
    CreateCollationStmt.Rules (ast.go + ddl.go).
  - catalog: UserCollation.Rules surfaced as collicurules via virtual-row
    builder's `nz(uc.Rules)` (→ VirtualNull when unset; slice-391 NULL infra).
  - executor: execCreateCollation stores s.Rules for icu provider only; FROM
    branch copies src.Rules.

Files (commit pending):
- internal/parser/ast.go: CreateCollationStmt.Rules field.
- internal/parser/ddl.go: `case "rules"` + doc tweak.
- internal/parser/create_collation_test.go: rules case + assertion.
- internal/catalog/catalog.go: UserCollation.Rules + nz(uc.Rules) virtual cell.
- internal/catalog/create_collation_test.go: NULL-when-unset / verbatim / FROM.
- internal/executor/operators_ddl.go: store/copy Rules.
- internal/testport/pgdump_connsetup_test.go: ci_rules fixture + assertion.
- docs/design/0110-0001-pg-dump-tap-port.md: Slice 392 section.
- .ralph/fix_plan.md slice 392 entry; deferral ledger row appended.

Gates: TestParseCreateCollation + TestCreateCollationVirtualRows PASS;
parser+catalog+planner suites PASS; TestPort_PgDumpConnectionSetup PASS (5.3s,
byte-identical vs real pg_dump 18.3); go build clean; gofmt clean on my hunks
(only pre-existing go1.25/1.26 alignment noise elsewhere). Pre-commit pgbench
smoke runs via hook on commit.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: persist user collations
to a pg_collation heap (restart durability — slices 389–392 gap); CREATE
CONVERSION dump (new object); aggregates (pg_proc prokind='a'); ALTER COLLATION
OWNER/RENAME; convert the 7 BKI built-in pg_collation rows' empty locale cells
to VirtualNull for full NULL fidelity.
