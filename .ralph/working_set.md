# Working set — M0134-0002 C7 slice 1 landed; residual re-characterised

**Task:** M0134-0002 (`alter_table.sql`), C7 slice 1. Selected per the Current
Priority banner (M0134 next after M-NIGHTLY). M-NIGHTLY drained —
`ci/logs/action-items.md` is still run `20260817-011734`, all 6 filed and `[x]`;
nothing new to file. M0134-0001 was parked by S22 (no cheap slices remain).

**Landed:** an inline column-level `CONSTRAINT <name> CHECK (...)` now keeps its
user-given name. goopg parsed `con1` and threw it away, then auto-named
`<table>_<col>_check`, so `ALTER TABLE ... RENAME CONSTRAINT con1` failed with
"does not exist" — a *naming* gap that presented as a *rename* gap. PG decides
this at parse time (`parse_utilcmd.c:transformCheckConstraints` keeps `conname`;
`heap.c:ChooseConstraintName` auto-names ONLY when it is NULL). The fix was the
already-shipped sibling pattern: `ColumnDef` carried `UniqueConstraintName` and
`NotNullConstraintName`; CHECK was the missing third.

**Files:** `internal/parser/ast.go` (`ColumnDef.CheckConstraintName`),
`internal/parser/ddl.go` (column `CONSTRAINT <name> CHECK` arm stores it),
`internal/executor/operators_ddl.go` (`execCreateTable` prefers it),
`internal/parser/check_alter_test.go`,
`internal/executor/operators_ddl_named_check_test.go` (guards),
`docs/design/0134-0002-alter-table-sql-divergence.md` (C7 row + "C7 slice 1"),
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (2 rows 2026-08-18).

**Sibling audit:** `LIKE INCLUDING CONSTRAINTS` inherits the fix (copies from the
materialized source catalog); `PARTITION OF` uses a separate
`PartitionCheckConstraint` struct — unaffected; `ALTER TABLE ADD COLUMN` never
reads `col.CheckExpr` at all (`operators_ddl.go:10033`) and **silently drops**
inline CHECKs — ledgered, not fixed.

**KEY FINDING for the next loop — do NOT slice C7/C12/C13/C14 blindly.** The
residual `alter_table` diff is no longer dominated by the formatter tail. Four
classes outside the doc's 14-class frame carry most of it: (a) ownership/ACL
checks absent entirely (`must be owner of ...` never raised), (b) `pg_locks`
always empty, (c) EXPLAIN Append/constraint-exclusion — a *planner* gap, not C14
verbosity, (d) `\d+` describe drift (missing `Compression` + `Access method:
heap`; Index `Definition` renders the full `CREATE INDEX` instead of the
expression) — highest hunk-count single root cause.

**Next step:** file (a)–(d) as classes **C16–C19** in
`docs/design/0134-0002-alter-table-sql-divergence.md` with PG citations, then
select by yield/risk — (d) for yield, (a) for correctness.

**Gates run:** `go build ./...` clean; `go test ./internal/parser/
./internal/executor/` ok; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (~8 min, `internal/initdb` + `cmd/goopg`
cold, rest cached); `scripts/pg-regress-runner.sh --verbose alter_table`
4048/107 → **4039/108** (line drop is the win; the +1 hunk is a large divergent
hunk splitting around newly-matching content, verified by diffing the `.diff`
files — no new divergence).

**Delegation:** `tmp/ralph-handoffs/m0134-0002-s01-formatter-tail/` (researcher
`af1679ca6e2065b46`, 1 round, GO) and
`tmp/ralph-handoffs/m0134-0002-s02-col-check-name/` (implementer
`aa67709a62ad27eea`, 1 round, DONE; tester `a20613bc8647092d4`, PASS).

**In-flight:** none.
