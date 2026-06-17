(idle — nothing in flight)

Last landed: DU-002 slice 139 (loop #104) — DEFERRABLE INITIALLY DEFERRED on an
ANONYMOUS table-level UNIQUE constraint round-trips through pg_dump.
Fixed a HARD PARSE ERROR: goopg's anonymous table-level UNIQUE parser case had
NO `DEFERRABLE` branch (unlike PRIMARY KEY, which silently discarded it), so
`UNIQUE (a) DEFERRABLE …` failed the whole CREATE TABLE. 4 sites: parser captures
`[NOT] DEFERRABLE [INITIALLY DEFERRED|IMMEDIATE]` → new CreateTableStmt arrays
TableUniqueDeferrable/TableUniqueInitiallyDeferred; new catalog.Index.Deferrable/
InitiallyDeferred fields; executor table-level UNIQUE loop threads them;
buildConstraintDefString appends ` DEFERRABLE [INITIALLY DEFERRED]` + pg_constraint
emits condeferrable/condeferred from the index.
Scope: pure dump-fidelity — goopg does NOT implement deferred constraint CHECKING
(all checked immediately); flag dumped but enforced per-row.
Files: internal/parser/ddl.go, internal/parser/ast.go, internal/parser/ddl_test.go,
internal/catalog/catalog.go, internal/executor/operators_ddl.go,
internal/executor/expr.go, internal/executor/constraintdef_nnd_test.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/deferral_ledger.md.
Verified: TestParseTableUniqueDeferrable PASS; TestBuildConstraintDefNullsNotDistinct
PASS; TestPort_PgDumpConnectionSetup PASS (2.55s); parser/catalog/executor suites
green; gofmt/build/vet OK. Committed + pushed.

Next direction (slice 140): DEFERRABLE on the NAMED table-level / inline-column /
PRIMARY KEY UNIQUE forms (still discard the flag — mirror the anonymous form),
OR INSERT/UPDATE enforcement of the slice-134–138 NULLS-equal semantics
(riskier multi-encoding-site `encodeIndexKeyFromCols`), OR an exclusion-constraint
(`EXCLUDE USING gist`) dump surface. The deferred-check EXECUTION (check at COMMIT
not per-row) is a separate larger transaction-machinery milestone (ledger).
