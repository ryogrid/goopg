# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, first slice landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed the
**C2 first slice — `ADD COLUMN IF NOT EXISTS`** (commit `8afe5bf7`). C2 is the
"ALTER-TABLE grammar cluster", the largest remaining class (~60 of 88 syntax-error
lines). A researcher decomposition split it into 14 sub-gaps (11 doc-listed + 3
new: ADD COLUMN IF NOT EXISTS, DROP CONSTRAINT IF EXISTS, ALTER TABLE OF/NOT OF).

**Status:** C2 slice 1 COMPLETE + committed (code `8afe5bf7`, bookkeeping
`1e1013d9`). C2 as a whole remains OPEN (13 sub-gaps left).

**Findings:** parser `parseAlterTableAction` ADD COLUMN arm now consumes
`IF NOT EXISTS` via `acceptKeyword(KwIf)` (NOT `acceptIdentKeyword`, which
silently drops KwIf) + `AlterTableAction.IfExists` flag (field is in `ast.go`,
not ddl.go). `execAlterTableAddColumn` (`operators_ddl.go:9244`) emits PG's
NOTICE `column "c" of relation "r" already exists, skipping` via `ctx.AddNotice`
and skips. Diff 4645→4602, `expected identifier (got not)` 8→0 (syntax-error
total 88→80); NOTICE byte-exact. Deferral row appended: `ctx.AddNotice` carries
no SQLSTATE (PG attaches 42701) + non-IF-NOT-EXISTS 42701 message-text gap
(classes C13/C12).

**Files:** `internal/parser/ast.go` (+IfExists), `internal/parser/ddl.go`
(+IF NOT EXISTS consumption), `internal/parser/alter_test.go`
(+TestParseAlterTableAddColumnIfNotExists), `internal/executor/operators_ddl.go`
(+skip-NOTICE), new `internal/executor/alter_table_add_column_if_not_exists_test.go`;
`docs/design/0134-0002-alter-table-sql-divergence.md` (C2 decomposition note);
`.ralph/deferral_ledger.md` (new row); `.ralph/fix_plan.md` (C2 progress note).

**Key symbols:** `parseAlterTableAction` (ddl.go), `AlterTableAction.IfExists`
(ast.go), `execAlterTableAddColumn` (operators_ddl.go), `ctx.AddNotice`,
`Catalog.LookupColumn`.

**Remaining C2 sub-gaps (ranked by error-site count ÷ risk):** NO INHERIT trailer
(7), TYPE…USING (11, C10-entangled), RENAME CONSTRAINT (11, C7-partial), comma
multi-action (7, structural), RENAME `<col>` TO bare (3), ANALYZE tab(col) (4,
re-route — ANALYZE/VACUUM statement gap), NOT VALID (2), STORAGE (2), OF/NOT OF
(3), DROP COLUMN IF EXISTS (1, one-line), DROP CONSTRAINT IF EXISTS (1), SET
WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

**Next step:** implement C2 slice 2 = **NO INHERIT trailer** (7 sites, additive
parser trailer at the ALTER ADD CHECK arm `ddl.go:9495-9522` + executor
`AddCheckFull(..., false /*noInherit*/, ...)` pass `act.NoInherit`; partitioned
error message after). Researcher report
`tmp/ralph-handoffs/0134-0002-c2-grammar-research/report.md` §5 has the full row.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/ -p 4`
PASS; `go test ./internal/executor/ -p 4` PASS; `scripts/pg-regress-runner.sh
alter_table` diff 4645→4602 (8 sites gone); pre-commit pgbench smoke PASS (×2);
`make ralph-state-guard` OK (repaired progress.json → in_progress).

**Delegation:** researcher `0134-0002-c2-grammar-research` DONE (14 sub-gaps,
named ADD COLUMN IF NOT EXISTS first); implementer
`0134-0002-c2-addcol-ifnotexists-impl` DONE (PASS).

**In-flight:** none.
