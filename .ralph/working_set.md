(idle — nothing in flight)

Last landed: DU-002 slice 184 (loop #152) — per-column statistics target
(`ALTER TABLE ... ALTER COLUMN c SET STATISTICS <n>`) now round-trips through pg_dump.
Third sibling of slices 182 (SET STORAGE) / 183 (SET COMPRESSION), for pg_attribute.attstattarget.

pg_dump's dumpTableSchema emits `ALTER TABLE ONLY <t> ALTER COLUMN <c> SET STATISTICS <n>;`
when attstattarget >= 0; PG18 default NULL (decoded -1) emits nothing. Parser previously had
NO SET STATISTICS arm in the table ALTER-COLUMN path (only ALTER INDEX expr-column path), so the
clause fell through to the no-op consumer; buildUserPGAttributeRow hardcoded attstattarget=NULL.
Fixed in 3 layers (mirroring 182/183):
  1. Parser: new SET STATISTICS arm in table ALTER-COLUMN path → AlterTableSetStatistics
     {CheckExpr=value, ColumnName}. Leading `-` accepted for `SET STATISTICS -1` reset
     (`-` lexes as TokenOperator, not TokenSymbol — initial bug, fixed).
  2. buildUserPGAttributeRow: emits integer in attstattarget when Column.StatTarget != nil &&
     *>= 0, else NULL.
  3. LOAD-BEARING: AlterTableSetStatistics executor arm (table branch) parses CheckExpr, sets/
     clears Column.StatTarget (*int: nil=unset, so SET STATISTICS 0 distinguishable from never-set),
     flushes via delete-old-rows + syncTableToCatalogHeap re-sync (pg_dump scans persisted heap).
No CREATE TABLE threading (SET STATISTICS is ALTER-only). New field catalog.Column.StatTarget *int.
Dump-fidelity only (goopg doesn't sample per-column targets).

Files: internal/catalog/catalog.go, internal/parser/ddl.go,
internal/executor/pg18_user_catalog_rows.go, internal/executor/operators_ddl.go,
internal/executor/pg18_user_catalog_rows_test.go (TestUserPGAttributeStatTargetOverride),
internal/parser/alter_test.go (TestParseAlterTableSetStatistics),
internal/testport/pgdump_connsetup_test.go (statcol fixture, 2 positive + 1 negative assert),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 184), .ralph/fix_plan.md (loop-152 PROGRESS).
Gates: gofmt OK; go vet parser+catalog+executor clean; full parser/catalog/executor PASS;
TestPort_PgDumpConnectionSetup PASS (3.08s); pgbench pre-commit smoke on commit.

Next (slice 185 candidates): (1) deferred MINVALUE/MAXVALUE keyword-AST-node slice (HIGHER RISK:
partition routing). (2) close validateDefaultExpr array/row/CASE/InExpr recursion gap (executor
semantic change — own gates). (3) other pg_dump per-column attribute gaps (attoptions /
attfdwoptions are NULL today).
