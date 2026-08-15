# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 2 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 is the
"ALTER-TABLE grammar cluster" (~60 of 88 syntax-error lines). This loop landed
**C2 slice 2 — `ADD [CONSTRAINT] CHECK ... NO INHERIT`** (commit `a39b75ad`).

**Status:** C2 slice 2 COMPLETE + committed. C2 remains OPEN (12 sub-gaps left).

**Findings:** parser `parseAlterTableAction` ADD CHECK arm now consumes `NO INHERIT`
at BOTH orderings (`acceptIdentKeyword("no")`/`"inherit"`; PG ConstraintAttributeSpec
is order-independent — `check (a=2) no inherit not valid` + trailing) + sets
`AlterTableAction.NoInherit`. Executor `execAlterTable` ADD CHECK arm threads
`act.NoInherit` into `AddCheckFull` and raises 42P16 `cannot add NO INHERIT
constraint to partitioned table %q` on a partitioned target (mirrors CREATE TABLE
sibling :2111). 7 `(got no)` syntax sites closed (80→73); partitioned ERROR
byte-matches PG. Unmasked 2 C3-class gaps (ADD CHECK w/o NOT VALID doesn't
validate existing rows; INSERT/UPDATE doesn't enforce CHECK) → 2 deferral rows.

**Files:** `internal/parser/ddl.go`, `internal/parser/alter_test.go`
(+TestParseAlterTableAddCheckNoInherit), `internal/executor/operators_ddl.go`,
`internal/executor/operators_ddl_check_notenforced_test.go`
(+TestCheckConstraintNoInheritAlterTable); `docs/design/0134-0002-alter-table-sql-divergence.md`
(2nd-slice note); `.ralph/deferral_ledger.md` (2 rows); `.ralph/fix_plan.md` (C2 progress).

**Key symbols:** `parseAlterTableAction` (ddl.go), `AlterTableAction.NoInherit`,
`execAlterTable` ADD CHECK arm (operators_ddl.go:7674), `AddCheckFull`,
`tbl.PartitionKey` (partitioned-table test).

**Remaining C2 sub-gaps (ranked count÷risk):** TYPE…USING (11, C10-entangled —
must THREAD USING, never parse-and-ignore), RENAME CONSTRAINT (11, new kind +
executor RenameConstraint, partial C7), comma multi-action (7, structural), RENAME
`<col>` TO bare (3, one-line), ANALYZE tab(col) (4, re-route — ANALYZE/VACUUM gap),
NOT VALID (2), STORAGE (2), OF/NOT OF (3), DROP COLUMN IF EXISTS (1), DROP
CONSTRAINT IF EXISTS (1), SET WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

**Next step:** implement C2 slice 3 = **RENAME CONSTRAINT** (11 sites, additive:
new `AlterTableRenameConstraint` kind + parser arm at the RENAME branch
(ddl.go:8529-8575) + executor `RenameConstraint` that also renames the backing
unique index; the `onek` block closes C7-independently). Researcher report
`tmp/ralph-handoffs/0134-0002-c2-grammar-research/report.md` §1 has the full row.
Alternative quick sweep (if a lighter slice is preferred): RENAME `<col>` TO bare +
DROP COLUMN/DROP CONSTRAINT IF EXISTS + SET WITHOUT OIDS (6 sites, all one-line).

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/ -p 4`
PASS; `go test ./internal/executor/ -p 4` PASS; `scripts/pg-regress-runner.sh
alter_table` 7 sites closed; pre-commit pgbench smoke PASS; `make ralph-state-guard`
OK (repaired progress.json).

**Delegation:** implementer `0134-0002-c2-noinherit-impl` DONE (PASS, one round).

**In-flight:** none.
