Task: DU-002 slice 295 — COMPLETE, committing + pushing.

Last landed: TEST-ONLY. A function-call generation expression whose string-literal argument's
BODY IS A COMMA — `concat(ka, ',', la)` — inherited onto a partition leaf round-trips end-to-end
vs real pg_dump 18.3. The adversarial complement to slice 294 (which proved body `-`): here the
literal `','` directly collides with the argument-separator comma. Pre-slice-294 the Value-based
switch would have matched the literal's `,` against the separator case AND dropped its quotes →
malformed `concat(ka,,,la)`; slice 294's TokenSymbol gating + renderTok re-quoting render it
distinctly. No production change — pins slice 294's gating on the ORACLE path. Table named `pgkc`
(`pgcc` already taken at L1600).

Files:
- internal/parser/gen_override_test.go — 1 unit case `comma_literal_arg` (concat(fa, ',', fb)).
- internal/testport/pgdump_connsetup_test.go — pgkc fixture (~L1756) + assertion (pgkc_1 block,
  after pglc assertion ~L5042) incl. malformed `concat(ka,,,la)` absence guard.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 295 section + Next (296) note.
- .ralph/fix_plan.md — slice 295 progress (loop #63).

Key symbols: joinGeneratedExprTokens (internal/parser/ddl.go:2479), renderTok closure, TokenStringLit,
TokenSymbol (all unchanged this slice — test-only).

Gates: gofmt clean; go vet clean; TestGeneratedColumnExprCanonicalSpacing PASS (new case);
TestPort_PgDumpConnectionSetup PASS (3.89s vs real pg_dump 18.3); go test ./internal/parser/ PASS;
make ralph-state-guard consistent (auto-repaired prev clean-exit marker); pgbench pre-commit smoke
(enforced by .githooks/pre-commit on commit).

Next (slice 296+): a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path, OR
a string-literal generation expr with an embedded backslash / unicode escape to exercise the literal
re-quoting against standard_conforming_strings edge cases.
