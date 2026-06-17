(idle — nothing in flight)

Last landed: DU-002 slice 185 (loop #153) — per-column attribute options
(`ALTER COLUMN c SET (n_distinct=0.5, …)`) now round-trip through pg_dump.
Fourth sibling of slices 182 (SET STORAGE) / 183 (SET COMPRESSION) / 184
(SET STATISTICS), for `pg_attribute.attoptions`.

pg_dump's dumpTableSchema renders `array_to_string(a.attoptions, ', ')` and emits
`ALTER TABLE ONLY <t> ALTER COLUMN <c> SET (...);` when non-empty. The parser
already had a `SET (` arm but DISCARDED the parenthesized contents (brace-depth
consume) → bare no-op action; buildUserPGAttributeRow hardcoded attoptions=NULL.
Fixed in 3 layers (mirroring 182/183/184):
  1. Parser: parseColumnSetOptions replaces the discard loop, captures each
     `name [=] value` normalized to `name=value` (leading `-` for negative
     n_distinct lexes as TokenOperator, concatenated verbatim). New field
     AlterTableAction.SetOptions []string + ColumnName now set.
  2. buildUserPGAttributeRow: emits PG text-array literal `{opt1,opt2}` in
     attoptions when len(Column.Options) > 0, else NULL. goopg's array_to_string
     (→ parseTextArray) consumes the `{…}` literal.
  3. LOAD-BEARING: AlterTableAlterColumnSet executor arm (was no-op) copies
     act.SetOptions → catalog.Column.Options, flushes via delete-old-rows +
     syncTableToCatalogHeap (pg_dump scans persisted heap).
RESET (...) left as pre-existing no-op (pg_dump never emits it). New field
catalog.Column.Options []string. Dump-fidelity only (goopg ignores n_distinct).

Files: internal/parser/ast.go, internal/parser/ddl.go, internal/catalog/catalog.go,
internal/executor/pg18_user_catalog_rows.go, internal/executor/operators_ddl.go,
internal/executor/pg18_user_catalog_rows_test.go (TestUserPGAttributeOptionsOverride),
internal/parser/alter_test.go (TestParseAlterTableSetColumnOptions),
internal/testport/pgdump_connsetup_test.go (optcol fixture, 2 positive + 1 negative),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 185), .ralph/fix_plan.md (loop-153 PROGRESS).
Gates: gofmt OK; go vet parser+catalog+executor clean; full parser/catalog/executor PASS;
TestPort_PgDumpConnectionSetup PASS (3.23s); pgbench pre-commit smoke on commit.

Next (slice 186 candidates): (1) deferred MINVALUE/MAXVALUE keyword-AST-node slice
(HIGHER RISK: partition routing). (2) close validateDefaultExpr array/row/CASE/InExpr
recursion gap (executor semantic change — own gates). (3) attfdwoptions (foreign-table
only, NULL today — needs RELKIND_FOREIGN_TABLE support).
