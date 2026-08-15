# Working set — M0134-0002 alter_table.sql (C2 grammar cluster COMPLETE)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. C2 = "ALTER-TABLE
grammar cluster". This loop landed **C2 slice 12 — SET WITHOUT/WITH OIDS +
duplicate [NOT] ENFORCED** (commit `4cd395f0`). C2 is now COMPLETE (all grammar
sub-gaps closed except ANALYZE tab(col), which is re-routed out of ALTER TABLE).

**Findings:** Both sub-gaps were parser-only. (a) `SET WITHOUT OIDS` hits the
`SET WITHOUT CLUSTER` arm → `AlterTableNoOp` (PG `AT_DropOids` silent no-op,
tablecmds.c:5528-5530). (b) `SET WITH OIDS` → guard emits `syntax error at or
near "WITH"` (no gram.y production; keyword re-uppercased since the lexer
lowercases keyword Values). (c) dup `[NOT] ENFORCED` → `rejectDuplicateEnforced`/
`isEnforcedAttr` helpers emit Raw `multiple ENFORCED/NOT ENFORCED clauses not
allowed` (parse_utilcmd.c:3999-4027) at the 5 CHECK sites + `sawEnforced` check
in `parseFKConstraintAttrs` (3 callers threaded). Researcher reclassified
"ENFORCED dup" C9-masked → pure C2 grammar gap (syntax error, zero inheritance).

**Files:** internal/parser/{ddl.go,alter_test.go} (helpers + SET arms + 3 named
tests), docs/design/0134-0002-alter-table-sql-divergence.md (slice-12 entry),
.ralph/fix_plan.md (slice-12 line), .ralph/deferral_ledger.md (1 row:
`parseAlterConstraintAttrs` dup still overwrites).

**Key symbols:** `rejectDuplicateEnforced`/`isEnforcedAttr` (ddl.go:2818/2828),
`parseFKConstraintAttrs` (now returns err; 3 callers), SET WITHOUT arm
(ddl.go:9404), SET WITH guard (ddl.go:9557).

**Remaining alter_table.sql (after C2):** ANALYZE tab(col) (4 sites, re-route —
ANALYZE/VACUUM statement gap), then correctness classes C3/C4/C9/C10/C11
(larger). sql:1205/1208 `only … add column x` block is C9 (inheritance).

**Next step:** finish the C2-adjacent tail — ANALYZE tab(col). Researcher round
first: locate why the ANALYZE/VACUUM statement parser rejects the `ANALYZE
tab(col)` column-list form (which file/arm), and the PG grammar (gram.y
`VacuumStmt`/`AnalyzeStmt`). Then a small implementer slice. If that proves to be
a real VACUUM-statement milestone-sized gap, reassess toward a correctness class.

**Gates run (this loop):** `go build ./...` PASS; `go test ./internal/parser/`
PASS (3 new named tests); `scripts/pg-regress-runner.sh alter_table` — SET
WITHOUT/WITH OIDS + ENFORCED-dup sites → shared lines (C9 block remains);
pre-commit pgbench smoke PASS (12468 tps select-only); `make ralph-state-guard`
clean (auto-repaired a stale progress.json "completed" marker).

**Delegation:** researcher `0134-0002-c2-slice12-research` DONE; implementer
`0134-0002-c2-slice12` DONE (1 round).

**In-flight:** none.
