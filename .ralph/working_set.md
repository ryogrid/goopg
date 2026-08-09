Task: DU-002 — EXCLUDE + NULLS NOT DISTINCT + PK DEFERRABLE parser gaps for ALTER TABLE (3 parser fixes)

Files:
- internal/parser/ast.go: added AlterTableAddExclude kind, ExclusionOp/ExclusionMethod/ExclusionWhere fields, NullsNotDistinct field on AlterTableAction
- internal/parser/ddl.go: added EXCLUDE case + NULLS NOT DISTINCT parsing + PK DEFERRABLE parsing in parseAlterTableAction
- internal/parser/ddl_test.go: added TestParseAlterTableAddExclude (8 cases)
- internal/executor/operators_ddl.go: added execAlterTableAddExclude, wired NullsNotDistinct through execAlterTableAddUnique, wired Deferrable/InitiallyDeferred through execAlterTableAddPrimaryKey

Key symbols:
- AlterTableAddExclude: new AlterTableActionKind for ALTER TABLE ADD EXCLUDE
- execAlterTableAddExclude: mirrors CREATE TABLE EXCLUDE path (btree "=" or stub)
- parseConstraintDeferrable: now called for PK too (was only UNIQUE + FK)

Hypothesis/Findings:
- Root cause: parseAlterTableAction's switch had no EXCLUDE case → fell through to default → AlterTableNoOp (silently skipped). Same for NULLS NOT DISTINCT after UNIQUE, and DEFERRABLE after PRIMARY KEY.
- Each fix is a small parser-side addition + executor wiring.
- Three DU-002 blockers cleared this loop: EXCLUDE constraint, UNIQUE NULLS NOT DISTINCT, PRIMARY KEY DEFERRABLE.

Next step: Next DU-002 blocker is CONSTRAINT TRIGGER parsing (`CREATE CONSTRAINT TRIGGER ... NOT DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION ...`). Error: `expected EXECUTE in trigger definition (got initially)`.

Gates run:
- go build ./...: PASS
- go test ./internal/parser/...: PASS (including new TestParseAlterTableAddExclude)
- go test ./internal/executor/...: PASS
- RALPH_PRECOMMIT_SCOPE=units: PASS (all packages)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- TestPort_PgDumpConnectionSetup: PASS (next = CONSTRAINT TRIGGER parsing)

In-flight: none
