# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 9 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 9 — STORAGE column clause**.

**Status:** C2 slice 9 COMPLETE + committed (`d0030ea3`). C2 remains OPEN.

**Findings:** The 2 STORAGE sites were CREATE TABLE column-definition `storage`
clauses (`col type STORAGE {plain|external|extended|main}`), NOT the ALTER
`SET STORAGE` arm (that already parses ddl.go:8918-8930 + executes
operators_ddl.go:8573-8604). `parseColumnConstraintList` had a COMPRESSION case
but no STORAGE case → `expected ',' or ')'`. Fixed: STORAGE case (mirrors
COMPRESSION) → new `ColumnDef.Storage` field → `execCreateTable` threads it onto
`catalog.Column.Storage` (both BodyOrder `addCol` + fallback paths) + new
`validateColumnStorage` (0A000 `column data type %s can only have storage PLAIN`,
type name via `pgFormatTypeName`). 2 `got storage` sites (diff:2032/2066) → 0.

**Files:** internal/parser/{ddl.go,ast.go,ddl_test.go},
internal/executor/{operators_ddl.go,storage_ddl_test.go},
docs/design/0134-0002-alter-table-sql-divergence.md (slice-9 entry),
.ralph/fix_plan.md, .ralph/deferral_ledger.md (2 rows: TOAST has_toast_table;
STORAGE DEFAULT + runtime-22023 invalid-mode).

**Key symbols:** `parseColumnConstraintList` STORAGE case (ddl.go ~4282);
`ColumnDef.Storage` (ast.go ~1256); `execCreateTable` `addCol` closure +
fallback path (operators_ddl.go ~1917/2169); `validateColumnStorage` +
`columnTypeStorageCode` (operators_ddl.go ~1596/1631); `pgFormatTypeName`.

**Remaining C2 sub-gaps (ranked):** ANALYZE tab(col) (4, re-route — an
ANALYZE/VACUUM statement gap), OF/NOT OF (3, typed-table arms absent in
parseAlterTableAction), NOT VALID (2), SET WITHOUT OIDS (1), ENFORCED dup (1,
C9-masked).

**Next step:** C2 slice 10 — **NOT VALID** (2 sites, FULLY researched in
`tmp/ralph-handoffs/0134-0002-c2-slice9-research/report.md`, no new research
round needed):
- NV1 (`nv_parent`, diff:534): CREATE TABLE `check (false) no inherit not valid`
  — parser-only consume-and-drop in the two table-level CHECK arms (ddl.go:3737-3766
  anonymous + :3900-3943 named). PG auto-validates NOT VALID at CREATE TABLE
  (parse_utilcmd.c:2946 + heap.c:2584); do NOT set convalidated='f'.
- NV2 (`atnnparted`, diff:1075): `ADD CONSTRAINT ... NOT NULL id NOT VALID` —
  parser (ddl.go:9688-9708 add `acceptKeyword(KwNot)`+`acceptIdentKeyword("valid")`
  + `act.NotValid`) + executor (`AddNotNull` NotValid param, catalog.go:253) +
  catalog (pg_constraint contype='n' row[6]='f', catalog.go:6699) + VALIDATE
  CONSTRAINT `NotNullConstraints` loop (operators_ddl.go:7677-7684). NV2 is a
  parser+executor+catalog bundle — bounded, not C3-class (PG excludes
  CONSTR_NOTNULL from the Phase-3 scan, tablecmds.c:9956); may warrant its own
  brief if NV1 is folded in and NV2 split.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/
./internal/executor/` PASS; `scripts/pg-regress-runner.sh alter_table` — 2 `got
storage` lines → 0 (34→32 syntax-error lines; overall still FAIL, unrelated);
pre-commit pgbench smoke PASS (11898 tps select-only).

**Delegation:** researcher `0134-0002-c2-slice9-research` DONE (1 round, report
inline in handoff dir); implementer `0134-0002-c2-slice9-impl` DONE (1 round,
report inline — the env blocked report.md writes). No tester needed.

**In-flight:** none.
