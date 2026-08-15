# Working set — M0134-0002 alter_table.sql (C8 system-column rejection LANDED)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed **C8**
— reject PostgreSQL system-column names for user columns. New case-sensitive
`isSystemColumn(name)` helper (ctid/xmin/cmin/xmax/cmax/tableoid; NO `oid`, legal
since PG 12) applied at four entry points: `execCreateTable`, `execCreateTableAs`,
`execAlterTableAddColumn`, and the RENAME COLUMN arm (`internal/executor/
operators_ddl.go`); `validatePartitionKey` (`operators_ddl_partition.go`) now reuses
the one helper. The pre-existing RENAME check was also corrected: SQLSTATE
42P20→42701, dropped `oid`, case-sensitive (was `strings.EqualFold`).

**Status:** COMPLETE + committed (code `0f6945bc`, bookkeeping `b7bdfb18`,
state-guard `1d725540`).

**Findings:** diff 4677→4664 (−13) — the sole C8 statement
(`ALTER TABLE attmp ADD COLUMN xmin integer;`) now matches PG (42701 + exact
message, no LINE 1). PG oracle: `tablecmds.c:7673` check_for_column_name_collision
(ADD/RENAME) + `heap.c:481` CheckAttributeNamesTypes (CREATE/CTAS); SysAtt[]
`heap.c:144-228`. Pos left 0 — the PG ereport carries no errposition.

**Files:** `internal/executor/operators_ddl.go` (+helper, 4 sites, RENAME fix);
`operators_ddl_partition.go` (reuse helper); new
`operators_ddl_system_column_test.go` (7 table-driven tests);
`docs/design/0134-0002-alter-table-sql-divergence.md` (C8 row → LANDED);
`.ralph/deferral_ledger.md` (new row: DROP/ALTER-on-system-column gap, 0A000);
`.ralph/fix_plan.md` (M0134-0002 C8 progress note).

**Key symbols:** `isSystemColumn` (operators_ddl.go ~:11163), `execCreateTable`
(:3106 loop), `execCreateTableAs` (:4158), `execAlterTableAddColumn` (:9242),
`execAlterTable` RENAME arm (:8215), `validatePartitionKey` (partition file :168).

**Deferral (recorded):** DROP COLUMN / ALTER COLUMN on a system column still
diverge — PG raises `cannot drop/alter system column "xmin"` (0A000), goopg says
`column "xmin" does not exist` (DROP) or silently accepts (ALTER TYPE). Needs a
system-column model or per-path guards; ledger row appended (resume: tablecmds.c
ATExecDropColumn:9338 / ATExecAlterColumnType:7777).

**Next step:** C5 (btree-inet rejected — `btreeKeyTypeRejectionError`) is the next
single-loop correctness win; needs a researcher pass first to scope whether inet
comparison ops + btree opclass already exist (opclass registration only) or the
comparators must be implemented. C2 (ALTER-TABLE grammar cluster) is the largest
remaining class and needs a researcher decomposition pass before implementing.

**Gates run (this loop):** `go test ./internal/executor/` PASS (7 new tests);
`scripts/pg-regress-runner.sh alter_table` diff 4677→4664; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=35); `go build ./...` clean; pre-commit pgbench smoke PASS (×2);
`make ralph-state-guard` OK (repaired progress.json, then clean).

**Delegation:** researcher `0134-0002-c8-system-columns-research` DONE; implementer
`0134-0002-c8-system-columns-impl` DONE; tester `0134-0002-s1-tpch` DONE (PASS).

**In-flight:** none.
