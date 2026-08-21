Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed Round
D of the `regexp_*` family sizing pass: `regexp_split_to_table(...)`
FROM-clause table-valued-SRF wiring, committed/pushed (`3f4d7c5a`).
strings.sql remains `failed` overall (many unrelated pre-existing gaps: e.g.
`standard_conforming_strings=off` lexing, `chr(0)`, bytea trim/LIKE,
`char(N)` literal concat syntax — untouched).

This loop: (1) re-checked fix_plan banner (M0134 still next-priority after
M-NIGHTLY per 2026-08-15 directive) and `ci/logs/action-items.md` — same
nightly run (20260822-001356) as last loop, its 5 items were already filed
last loop as AI-20260822-001356-001..005, nothing new to file, none block
M0134's gates. (2) Delegated a researcher round confirming Round D's exact
wiring shape (planner.go:4639 dispatch site, planFromRegexpMatches at
5129-5185 as the mirror target, FromRegexpMatches plan node at
plan.go:1819-1834, fromRegexpMatchesOp operator, plus the reusable split
algorithm already in the scalar regexp_split_to_array case at
expr.go:12876-12914) and the PG oracle (regexp.c:1748-1897, confirms N
matches -> N+1 rows via re.Split(s,-1), and that PG forces glob=true
internally after rejecting explicit 'g'). (3) Delegated one implementer
round; converged in round 1 (within the 3-round cap).

Landed: `internal/optimizer/plan.go` gained `FromRegexpSplitToTable` (mirrors
`FromRegexpMatches`, `text`-typed single column not `text[]`);
`internal/optimizer/planner.go` gained a `planTableFuncRangeVar` dispatch
branch + `planFromRegexpSplitToTable`; `internal/executor/executor.go`
gained the plan-node dispatch case; new file
`internal/executor/operators_from_regexp_split_to_table.go`
(`fromRegexpSplitToTableOp`, mirrors `fromRegexpMatchesOp`'s Open/Next/Close);
`internal/executor/expr.go` gained `evalRegexpSplitToTable` — reuses shared
`pgRegexFlagsToGoModifiers` (Round C), rejects explicit `g` with `22023`
correctly naming `regexp_split_to_table()` (not copy-pasted from the
`_array` sibling's message), raises `2201B` on invalid pattern (stricter
than `evalRegexpMatchesSRF`'s permissive silent-empty, matching PG's shared
`setup_regexp_matches` behavior for both split functions). New test
`internal/executor/from_regexp_split_to_table_test.go`
(`TestFromRegexpSplitToTable`: basic split N->N+1 rows, no-match, column
alias, `g`-rejection, invalid-pattern 2201B, WITH ORDINALITY). Handoff dir:
`tmp/ralph-handoffs/m0134-0070-regexp-split-to-table/` (brief.md +
report.md) — scratch, not system of record.

Key symbols: `planFromRegexpSplitToTable`, `FromRegexpSplitToTable`
(`internal/optimizer/`), `fromRegexpSplitToTableOp`, `evalRegexpSplitToTable`
(`internal/executor/`).

Deferred (ledger row 2026-08-22, M0134-0070 Round D entry): (1)
`string_to_table(string, delimiter[, null_string])` — literal-delimiter twin
of `regexp_split_to_table`, same table-valued-SRF shape, completely unwired
(currently only denylisted in `operators_ddl_partition.go`'s
`isBuiltinSRF`); PG oracle `varlena.c` `text_to_table`/`text_to_array`
family. (2) `SELECT regexp_split_to_table(...)` in SELECT-list (non-FROM)
position — PG allows it via `operators_project_set.go`'s SRF-in-targetlist
machinery (parallel to existing `regexpMatchesResults` handling), but
strings.sql's fixture only exercises FROM-clause form so no evidence either
way this round.

Next step: work Round E from the design doc —
`regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr` (currently all
`function ... does not exist`, ~426 diff lines combined per the 2026-08-22
sizing pass; do `regexp_instr` first at 193 lines, it exercises
subexpr/endoption logic reusable by `regexp_substr`). All four are thin
wrappers over the already-landed compiled-pattern + `pgRegexFlagsToGoModifiers`
machinery (Rounds C/D). After that: Round F (`regexp_replace` extended 6-arg
form, start/N dispatch bug, ~81 lines), Round G (backreference
generalization `\1`/`\2`-only -> any `\N`/`\&`, ~35 lines, explicitly
excluding the `(.)\1` pattern-backreference RE2 gap — ledger that
sub-case). Re-check the fix_plan banner at loop start first (M-NIGHTLY
items AI-20260822-001356-001..005 remain unselected/unworked as of this
loop — none block M0134's gates so stay that way unless the banner or a new
nightly run changes the picture).

Gates run this loop: `go build ./...` PASS (implementer + this session);
`go test ./internal/executor/... ./internal/optimizer/...` PASS
(implementer, cached-fresh); `go test ./internal/executor/ -run
TestFromRegexpSplitToTable -v` PASS (this session, re-verified before
commit); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(implementer); `GOOPG_CG_UNIT=... scripts/goopg-test-run.sh
scripts/pg-regress-runner.sh --verbose strings` (implementer, cgroup-capped)
— confirmed the `regexp_split_to_table` fixture line is now pure
unchanged-context (zero +/- markers) in `tmp/regress-diffs/strings.diff`;
pre-commit pgbench smoke via git hook — PASS (377/701/13004 TPS, 0 failed
transactions). `make ralph-state-guard` — ran this session: found the same
expected running/completed status/progress mismatch from a prior loop's
clean-exit marker (recurring pattern, not a new bug), auto-repaired, green
after repair.

Delegation: researcher round (Round D wiring confirmation) + implementer
round (tmp/ralph-handoffs/m0134-0070-regexp-split-to-table/, DONE in round
1, converged). No SendMessage follow-up needed. No open handoff.

In-flight: none. Commit `3f4d7c5a` landed and pushed to `regress-renumbering`
(`15b59428..3f4d7c5a`). No server left running.
