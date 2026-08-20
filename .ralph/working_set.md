Task: M0134-0040 (jsonb_jsonpath.sql) — PARKED, no code change this loop.

Files: `.ralph/deferral_ledger.md` (new row), `.ralph/fix_plan.md` (M0134-0040
entry updated to PARKED with sizing).

Key symbols: none touched (bookkeeping-only loop) — the file's own gap is the
absent jsonpath grammar/evaluator subsystem (would live under a new
`internal/executor/jsonpath/` or similar, none exists yet).

Hypothesis/Findings: `researcher` sized jsonb_jsonpath.sql at HEAD via
`scripts/pg-regress-runner.sh --verbose jsonb_jsonpath`: 6175 diff lines, 818
`^+ERROR` / 244 `^-ERROR`, 0/1 PASS. 817/818 (99.9%) trace to ONE gap: no SQL/JSON
jsonpath parser or evaluator exists anywhere in goopg (confirmed via
`grep jsonb_path internal/executor/expr.go` = zero hits; `pg_proc` has the
catalog rows but no dispatch case). Breakdown: 717 `^+ERROR` from
`jsonb_path_query[_tz/_array/_first]`/`jsonb_path_match`/`jsonb_path_exists`
family (uncalled builtins), 87 from `@?`/`@@` operators entirely unlexed
(`internal/parser/lexer.go` has no token for either), 13 downstream
parse-cascade. No CONTAINED bucket exists to peel off (unlike M0134-0037/0038/
0039) — the whole file is REFACTOR-tier. PG oracle: `postgres/src/backend/
utils/adt/jsonpath.c` (grammar), `jsonpath_exec.c` (evaluator). Ledger row
appended with full detail. This is now the SECOND file (after jsonb.sql,
M0134-0039) confirmed dominated by this exact gap.

Next step: per fix_plan.md banner, select **M0134-0041 (jsonpath.sql, status
`failed`)** next. Size it first via `scripts/pg-regress-runner.sh --verbose
jsonpath` (delegate to researcher). Strong prior expectation (by name alone)
that it will ALSO be dominated by the same absent jsonpath subsystem — if
confirmed (3-for-3), stop parking individual files and instead promote
"implement the SQL/JSON jsonpath grammar+evaluator" to its own dedicated
multi-loop epic: write a design doc under `docs/design/` (grammar subset
needed: `$.foo`, array/object accessors, `==`/`&&`/`||`/comparison operators,
filter expressions `?()`, `strict`/`lax` mode), decompose into slices (lexer/
parser for the jsonpath literal type itself, tree-walking evaluator over
decoded jsonb, wire `@?`/`@@` operators + `jsonb_path_*` function family
dispatch), and work it across several loops. That epic would very likely flip
all three files (jsonb.sql's jsonpath bucket, jsonb_jsonpath.sql,
jsonpath.sql) plus unblock jsonb.sql's remaining `?`/`?|`/`?&` bucket at the
same time.

Gates run this loop: none (bookkeeping-only loop, no source/test files
touched) — `make ralph-state-guard` still run before status block per
standing rule.

Delegation: none in flight — the sizing researcher call completed and its
findings are folded into the ledger/fix_plan rows above; no handoff dir left
open.

In-flight: none.
