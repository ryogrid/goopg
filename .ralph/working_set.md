(idle — nothing in flight)

Last landed: DU-002 slice 134 (loop #99) — `CREATE UNIQUE INDEX … NULLS NOT
DISTINCT` (PG 15+) dump-fidelity round-trip. Parser previously accepted-and-
DISCARDED the clause and pg_index.indnullsnotdistinct was hard-wired false, so a
NULLS NOT DISTINCT unique index dumped as a plain CREATE UNIQUE INDEX — a silent
loss of NULL-dedup semantics on restore. Threaded end to end:
CreateIndexStmt.NullsNotDistinct → catalog.Index.NullsNotDistinct →
pg_index.indnullsnotdistinct (BOTH row builders) → BuildIndexDef re-emits the
clause after the column list (ruleutils.c order). Enforcement of NULLS-equal
uniqueness DEFERRED (ledger): encodeIndexKeyFromCols returns nil on NULL keys;
making NULLs collide needs a NULL-sentinel encoding consistent across
insert-maintain / unique-check / index-scan-probe paths.
Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/catalog/catalog.go, internal/executor/operators_ddl.go,
internal/executor/pg18_user_catalog_rows.go, internal/parser/ddl_test.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/deferral_ledger.md.
Verified: TestParseCreateIndexNullsNotDistinct PASS;
TestPort_PgDumpConnectionSetup PASS (2.50s); catalog+parser pkgs PASS.
Committed + pushed.

Next direction (slice 135): a `UNIQUE NULLS NOT DISTINCT` *table/column
constraint* (CREATE TABLE / ALTER TABLE ADD — separate parser path from CREATE
INDEX; pg_get_constraintdef emits `UNIQUE NULLS NOT DISTINCT (cols)` per
ruleutils.c ~line 2403), OR enforcement of the slice-134 NULLS-equal semantics,
OR an EXCLUDE-constraint dump surface.
