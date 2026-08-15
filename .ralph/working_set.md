# Working set — M0134-0002 alter_table.sql (C2 grammar cluster, slice 7 landed)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 7 — DROP COLUMN/CONSTRAINT IF EXISTS**.

**Status:** C2 slice 7 COMPLETE + committed (`e4395f7d`) + pushed. C2 remains OPEN.

**Findings:** The parser's DROP COLUMN arm *appeared* to consume `IF EXISTS` but
never did — its `acceptIdentKeyword("if")`/`("exists")` only matches `TokenIdent`,
never the `KwIf`/`KwExists` keyword tokens, so `DROP COLUMN IF EXISTS` was a
syntax error (`got if`). Fixed both arms with the proven sibling pattern
`acceptKeyword(KwIf)` + `acceptKeyword(KwExists)`, setting the existing
`AlterTableAction.IfExists` flag (no new AST field). Executor: `execAlterDropColumn`
(`operators_ddl.go:21278`) and `execAlterTableDropConstraint` (`:10011`) emit
PG's NOTICE + `return nil` on missing object; the drop-constraint skip is gated on
`pkIdx == nil` (all five kinds miss) so a real constraint of another kind is never
skipped. NOTICE text byte-exact (tablecmds.c:9326-9328 / :14060-14062).

**Files:** internal/parser/{ddl.go,alter_test.go},
internal/executor/{operators_ddl.go,alter_table_drop_if_exists_test.go},
.ralph/fix_plan.md (C2 seventh-slice bullet + M-NIGHTLY filing), .ralph/working_set.md.

**Key symbols:** `parseAlterTableAction` (ddl.go, DROP CONSTRAINT arm ~9300 /
DROP COLUMN arm ~9325); `execAlterDropColumn` (operators_ddl.go:21278);
`execAlterTableDropConstraint` (operators_ddl.go:10011); `AlterTableAction.IfExists`
(ast.go, pre-existing).

**Remaining C2 sub-gaps (ranked):** RENAME `<col>` TO bare (3 sites),
ANALYZE tab(col) (re-route — ANALYZE/VACUUM gap), NOT VALID (2), STORAGE (2),
OF/NOT OF (3), SET WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

**Next step:** C2 slice 8 = **RENAME `<col>` TO bare** (3 sites) — the parser's
RENAME COLUMN arm likely requires `TO <col>` but PG allows `RENAME <col>` without
the column type/schema repetition; investigate whether goopg's `RENAME COLUMN a TO b`
grammar has an arm that mis-parses the bare form. Needs a light researcher round
(the arm's current shape is not yet pinned). Alternatively NOT VALID (2) or
STORAGE (2) are smaller.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/` PASS;
`go test ./internal/executor/` PASS (6.4s); 3 named executor guard tests PASS;
`scripts/pg-regress-runner.sh alter_table` — `drop column if exists non_existing` +
`drop constraint IF EXISTS anothertab_chk` divergence lines 8→0 (overall diff still
FAILs on ~4417 pre-existing unrelated lines); pre-commit pgbench smoke PASS (12278 tps).

**Delegation:** researcher `0134-0002-c2-slice7-research` DONE (1 round);
implementer `0134-0002-c2-slice7-impl` DONE (1 round). No tester needed.

**In-flight:** none.
