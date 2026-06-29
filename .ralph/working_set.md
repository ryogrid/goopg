(idle — nothing in flight)

Last loop (#44): M0119-0004 **`ALTER TABLE … ENABLE / FORCE ROW LEVEL SECURITY`
round-trip in pg_dump** (DU-002 slice 322) — LANDED, committed.

pg_dump emits RLS state from two pg_class cols: relrowsecurity → getPolicies
(null-polname PolicyInfo) → dumpPolicy `ALTER TABLE <t> ENABLE ROW LEVEL
SECURITY;`, and relforcerowsecurity → dumpTableSchema `ALTER TABLE ONLY <t>
FORCE ROW LEVEL SECURITY;`. goopg hardcoded both to 'f' and swallowed ENABLE as
a trigger no-op, so RLS was dropped and goopg couldn't restore its own dump.

Fix (dump-fidelity only — goopg enforces NO RLS):
- ast.go: 4 new AlterTableActionKind (Enable/Disable/Force/NoForceRowSecurity).
- ddl.go parseAlterTable: detect ENABLE/DISABLE ROW LEVEL SECURITY and [NO]
  FORCE ROW LEVEL SECURITY by token-value lookahead BEFORE the trigger no-op arm.
- catalog.go: Table.RowSecurity/ForceRowSecurity fields; new boolToPGChar helper;
  virtual pg_class builder (registerSystemTables) projects them.
- pg18_user_catalog_rows.go buildUserPGClassRow: emit the flags (heap path).
- operators_ddl.go ALTER loop: 4 cases set flag + delete-old-rows +
  syncTableToCatalogHeap (mirrors REPLICA IDENTITY slice 305).

Files: internal/parser/ast.go, internal/parser/ddl.go,
internal/catalog/catalog.go, internal/executor/pg18_user_catalog_rows.go,
internal/executor/operators_ddl.go, internal/parser/alter_test.go
(TestParseAlterTableRowSecurity), internal/executor/storage_ddl_test.go
(TestDDLAlterTableRowSecurityRoundTrip),
internal/testport/pgdump_connsetup_test.go (slice 322 fixture+assert),
docs/design/0119-0004-row-level-security-enable.md (+README 0119-0004y).

Gates: parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup PASS
(real pg_dump 18.3, both ENABLE+FORCE clauses); go build clean; pgbench smoke =
pre-commit hook.

NEXT loop — remaining pg_dump getter-battery gaps (the big multi-component
features): CREATE POLICY (pg_policy — the per-policy half of RLS, now that the
ENABLE flag round-trips), GRANT/ACL (relacl + dumpACL — needs real ACL storage,
GRANT is a CompatNoopStmt today), CREATE RULE (pg_rewrite + pg_get_ruledef —
CREATE RULE is a full no-op today). Pick one and scope a contained first slice.
