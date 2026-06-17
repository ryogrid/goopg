(idle — nothing in flight)

Last landed: DU-002 slice 142 (loop #107) — DEFERRABLE on all three PRIMARY KEY
forms now round-trips through pg_dump: anonymous table-level (`PRIMARY KEY (a)
DEFERRABLE INITIALLY DEFERRED`), named table-level (`CONSTRAINT pkdef PRIMARY KEY
(a) DEFERRABLE …`), and inline column (`a int PRIMARY KEY DEFERRABLE …`). Fixed a
HARD PARSE ERROR on the inline column form (no DEFERRABLE slot → unconsumed token
failed the whole CREATE TABLE); the two table-level forms previously
accepted-and-dropped the flag. 3 sites: (1) parser — renamed slice-141
`parseUniqueDeferrable` → `parseConstraintDeferrable` (generic); captures the
trailer into new CreateTableStmt.PrimaryKeyDeferrable/InitiallyDeferred (anon
table-level), existing TableConstraintDef.Deferrable/InitiallyDeferred (named,
parsed before NamedConstraints append), and new
ColumnDef.PrimaryDeferrable/PrimaryInitiallyDeferred (both inline cases).
(2) executor — tbl_pkey index build copies flags for anon+inline; named already
threaded via NamedConstraints loop (slice 140). SUBTLETY: inline PK also
populates s.PrimaryKey, so table-level branch reads false → follow-up column scan
adopts inline flags. (3) deparse + pg_constraint UNCHANGED (shared, read from
index — buildConstraintDefString already handles PRIMARY KEY keyword).
Scope: pure dump-fidelity — deferred CHECKING not implemented (per-row enforce).
Files: internal/parser/ddl.go, internal/parser/ast.go, internal/parser/ddl_test.go,
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md.
Verified: TestParsePrimaryKeyDeferrable PASS; TestPort_PgDumpConnectionSetup PASS
(2.43s); parser/catalog/executor suites green; gofmt/build/vet OK. Committed.

Next direction (slice 143): DEFERRABLE on an EXCLUDE constraint
(`EXCLUDE USING gist … DEFERRABLE`) dump surface — the last constraint kind that
still discards the flag (UNIQUE+PK now complete). parseExcludeConstraint
(ddl.go ~1903) currently has no DEFERRABLE capture; TableExclusions / the
exclusion index would need to carry it. OR pick a fresh pg_dump catalog-surface
gap. Deferred-check EXECUTION (check at COMMIT, not per-row) remains a separate
txn-machinery milestone for ALL constraint kinds.
