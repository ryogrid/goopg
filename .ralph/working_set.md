Task: DU-002 slice 294 (loop #62) — COMPLETE, committing + pushing.

Last landed: **PRODUCTION fix.** A function-call generation expression with a STRING-LITERAL
argument `concat(ka, '-', la)` inherited onto a partition leaf now round-trips end-to-end vs
real pg_dump 18.3. Slices 291–293 pinned the call-paren/comma branches of joinGeneratedExprTokens
for IDENTIFIER args only. A string-literal arg exposed a latent bug: the lexer stores a literal's
UNQUOTED body (`'-'` → Token.Value "-"), and the helper space-joined token values raw, so
`concat(ka, '-', la)` would have become the malformed `concat(ka, -, la)`. Fix: new renderTok
closure re-quotes TokenStringLit (doubling embedded quotes); punctuation spacing rules gated on
TokenSymbol so a literal body of )/,/(/. can't be mistaken for a punctuator. NO-OP for slices
283–293 (their punctuators are all TokenSymbol) → zero regression. Test dumps a LIVE goopg server,
so pg_dump reads goopg's stored source verbatim → asserts goopg's own `concat(ka, '-', la)` (no
::text cast; out-of-scope divergence).

Files:
- internal/parser/ddl.go — joinGeneratedExprTokens PRODUCTION fix (renderTok + TokenSymbol gating).
- internal/parser/gen_override_test.go — 3 unit cases (string_literal_arg, string_literal_operand,
  embedded_quote_literal).
- internal/testport/pgdump_connsetup_test.go — pglc fixture (~line 1726) + assertion (pglc_1 block,
  after pg3c assertion) incl. malformed-form absence guard.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 294 section + Next (295) note.
- .ralph/fix_plan.md — slice 294 progress (loop #62).

Key symbols: joinGeneratedExprTokens (internal/parser/ddl.go:2469), renderTok closure,
TokenStringLit, TokenSymbol.

Gates: gofmt clean; go vet clean; go build ./... OK; TestGeneratedColumnExprCanonicalSpacing PASS
(3 new cases); TestPort_PgDumpConnectionSetup PASS (3.63s vs real pg_dump 18.3); pgbench pre-commit
smoke (enforced by .githooks/pre-commit on commit).

Next (slice 295+): a string-literal generation expr whose body IS pg_get_expr punctuation
(`concat(ka, ',', la)` — literal body a comma) to exercise the TokenSymbol-gated spacing on the
oracle path. OR a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path.
