Task: M0119-0004 — DU-002 next blocker: NOT NULL pid parser gap in CREATE TABLE INHERITS

Files:
- internal/parser/ddl.go: added NOT NULL colname case in parseCreateTableTail body loop (between parseTableConstraintElement and LIKE), consuming NOT + NULL + column-name, creating ColumnDef{NotNull: true} with empty Type, optional NO INHERIT
- internal/executor/operators_ddl.go: both BodyOrder path and fallback path now check for empty Type.Name → merge NotNull into the already-inherited column instead of adding a duplicate
- internal/parser/ddl_test.go: TestParseStandaloneNotNullColumnConstraint — basic, NO INHERIT, multi, and NOT-NULL-only column list forms

Key symbols:
- parseCreateTableTail: new `if p.cur()==Not && p.peek(1)==Null` branch
- execCreateTable: constraint-only column merge in both BodyOrder and fallback paths

Hypothesis/Findings:
- Root cause: pg_dump emits `NOT NULL colname` as a standalone table element for inherited NOT NULL columns in CREATE TABLE INHERITS. goopg's parser treated it as a ColumnDef, which starts with parseIdent() and fails on the NOT keyword.
- Fix: recognize NOT NULL at table-element level, parse the column name, store as constraint-only ColumnDef. Executor merges into the inherited column.
- Confirmed working: TestPort_PgDumpConnectionSetup no longer fails at NOT NULL pid; next blocker is ALTER TABLE SET ACCESS METHOD.

Next step:
Fix `ALTER TABLE SET ACCESS METHOD` parser gap (the next DU-002 blocker surfaced by the pg_dump round-trip test).

Gates run:
- go build ./...: PASS
- go test ./internal/parser/...: PASS (0.035s)
- go test ./internal/executor/...: PASS (5.841s)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all packages)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- TestPort_PgDumpConnectionSetup: PASS (NOT NULL pid resolved; next blocker = SET ACCESS METHOD)
- make ralph-state-guard: REPAIRED + OK

In-flight: none
