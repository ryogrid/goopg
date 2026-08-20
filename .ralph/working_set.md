Task: M0134-0041 (jsonpath.sql) — PARKED, no code change this loop. Promoted
the 3-for-3 confirmed jsonpath gap to its own milestone **M0135** (design
doc written, 4 slices filed).

Files: `.ralph/fix_plan.md` (M0134-0041 entry PARKED; new M0135 milestone
section with S1-S4 appended at foot), `.ralph/deferral_ledger.md` (2 new
rows: the jsonpath-subsystem gap + the unrelated `unnest(...) str` alias
bug), `docs/design/m0135-0001-jsonpath-subsystem.md` (new, draft),
`docs/design/README.md` (indexed).

Key symbols: none touched (bookkeeping/design-only loop). Future S1 work
lands in a new `internal/executor/jsonpath/` package (lexer.go/parser.go/
ast.go/eval.go), wiring `jsonpath_in`/`jsonpath_out`
(`internal/initdb/pg_proc_seed_data.go:2713-2715`, currently zero executor
dispatch).

Hypothesis/Findings: `researcher` sized jsonpath.sql at HEAD — 1443 diff
lines, 0/1 PASS, only 1 `^+ERROR` / 36 `^-ERROR` (a DIFFERENT shape than
M0134-0039/0040's `+ERROR`-dominated failures). This file never calls
`jsonb_path_query`/`@?`/`@@`; it's pure `::jsonpath` type-I/O
(parse-text→tree→canonical-print), and goopg's cast is a naive passthrough.
~950 lines are canonicalization mismatches (quoting/spacing/numeric
normalization), 36 `^-ERROR` are malformed-jsonpath-text goopg wrongly
accepts. **3-for-3 confirmed**: jsonb.sql (M0134-0039), jsonb_jsonpath.sql
(M0134-0040), jsonpath.sql (M0134-0041) all trace to the same absent
jsonpath subsystem (no lexer/parser/pretty-printer/evaluator anywhere in
goopg, only pg_type/pg_proc catalog scaffolding). Per the standing plan
(recorded in the M0134-0040 fix_plan row), promoted to milestone M0135
rather than parking a fourth isolated file. One incidental unrelated bug
surfaced: `FROM unnest(...) str` single-column-SRF-alias resolution
(`column "str" does not exist"`) — deliberately kept OUT of M0135 (own
ledger row, file as its own M0134 task later).

Next step: per fix_plan.md banner (M-NIGHTLY → M0134), select the next
M0134 task in ascending ID order (M0134-0042 onward — check
`.ralph/fix_plan.md` for the next unchecked entry after 0041). M0135 itself
is NOT auto-prioritized ahead of M0134's remaining single-file tasks; select
M0135-S1 only when the banner names M0135 explicitly, or opportunistically
if a future M0134 task lands back on jsonb.sql/jsonb_jsonpath.sql/
jsonpath.sql and a real fix is wanted instead of another park. M0135-S1
(lexer+parser+pretty-printer, `docs/design/m0135-0001-jsonpath-subsystem.md`
§S1) is sized as a normal implementer-brief-sized slice when selected — it
alone is expected to flip M0134-0041 to `pass`.

Gates run this loop: none (bookkeeping/design-only loop, no source/test
files touched) — `make ralph-state-guard` still run before status block per
standing rule.

Delegation: none in flight — the sizing researcher call (agentId
afe6de915fea438a0) completed and its findings are folded into the ledger/
fix_plan/design-doc rows above; no handoff dir opened this loop.

In-flight: none.
