Task: M0134-0065 (rules.sql) — sized + landed one CONTAINED fix (ALTER RULE
RENAME TO), PARKED (case still `failed`, CSV row unchanged). Committed &
pushed (fcfb60f9). Next: select M0134-0066 (date.sql).

Files this loop: `internal/parser/ast.go` (new `AlterRuleRenameStmt`),
`internal/parser/ddl.go` (`parseAlter` — ALTER RULE ... RENAME TO dispatch),
`internal/executor/operators_ddl.go` (`execAlterRuleRename`),
`internal/optimizer/planner.go` (DDL passthrough type-switch entry),
`internal/parser/alter_rule_rename_test.go` (new, `TestParseAlterRuleRename`),
`internal/executor/storage_ddl_test.go` (added `TestDDLAlterRuleRename`),
`.ralph/deferral_ledger.md` (new row, M0134-0065), `.ralph/fix_plan.md`
(M0134-0065 marked PARKED with bucket summary, still unchecked).

Key symbols: `parseAlter` (ddl.go ~7075), `execAlterRuleRename`
(operators_ddl.go), `catalog.RuleInfo` (catalog.go:1412),
`parseCreateRuleTail` (ddl.go:1826 — only reifies DO-NOTHING rules).

Hypothesis/Findings: `rules.sql` dominant gap (~35+ of ~90 diverging blocks,
3403-line diff, 51 `^+ERROR`/18 `^-ERROR`) is architectural — goopg has ZERO
query-rewrite/rule-execution subsystem; `CREATE RULE` only persists the
trivial DO-NOTHING form, any real DO INSTEAD/DO ALSO action rule is a no-op,
and `pg_rules`/`pg_views` don't exist. REFACTOR-tier (parser+catalog+new
planner/rewrite phase+executor), same tier as the rowtypes.sql (M0134-0064)
and returning.sql (M0134-0063) precedents. Secondary independent gaps found
but NOT landed: `session_replication_role` GUC missing, `int4smaller`/
`pg_get_function_arg_default` builtins missing, a `SubPlan parameter $0 read
before assignment` planner/executor bug (correlated-subquery eval-order,
worth its own investigation later), MERGE `\sf` deparse losing PG18
`RETURNING WITH (OLD/NEW)`. All ledgered under M0134-0065.

Next step: select **M0134-0066 (date.sql)** per the fix_plan
task-ID-ascending selection rule — size via researcher first.

Gates run this loop: `go build ./...` PASS; `go test ./internal/parser/...
./internal/executor/...` PASS (both new tests + full package suites);
`make ralph-state-guard` PASS (auto-repaired the same recurring stale
clean-exit-marker mismatch as prior loops — known benign); pre-commit
pgbench smoke PASS (701/12992 TPS, 0 failed transactions).

Delegation: researcher agent `a97b3841495548d70` (1 round — full bucket
breakdown + PARK recommendation with the specific contained slice, accepted
as-is); implementer agent `af8c7375d5c9e373f` (1 round — DONE, no escalation,
report relayed inline since Write of report.md was blocked by tool policy —
content captured verbatim into the deferral ledger row instead).

In-flight: none. Commit `fcfb60f9` pushed to `regress-renumbering`. No
server left running.
