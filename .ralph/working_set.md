Task: DU-002 slice 296 — COMPLETE, committing + pushing.

Last landed: TEST-ONLY. A function-call generation expression whose string-literal argument's
BODY IS A SINGLE QUOTE — `concat(ka, '''', la)` — inherited onto a partition leaf round-trips
end-to-end vs real pg_dump 18.3. The adversarial complement to slices 294 (body `-`) and 295
(body `,`), and the ONLY fixture exercising slice 294's quote-DOUBLING (renderTok's
`strings.ReplaceAll(t.Value, "'", "''")`) on the ORACLE path. The lexer stores a literal's
unquoted body, so SQL `''''` (one `'`) is stored as the single byte `'`. Pre-294 raw join →
malformed `concat(ka, ', la)`; forgot-to-double → unbalanced `concat(ka, ''', la)`; the fix →
balanced four-quote `''''`. No production change — the unit `embedded_quote_literal` case
already existed; slice 296 pins it end-to-end. Table named `pgqc`.

Files:
- internal/testport/pgdump_connsetup_test.go — pgqc fixture (~L1783) + assertion (pgqc_1 block,
  after pgkc assertion ~L5118) incl. forgot-to-double `concat(ka, ''', la)` absence guard.
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 296 section + Next (297) note.
- .ralph/fix_plan.md — slice 296 progress (loop #64).
- (NO unit-test change — `embedded_quote_literal` case already present at gen_override_test.go:176.)

Key symbols: joinGeneratedExprTokens (internal/parser/ddl.go:2479), renderTok closure, TokenStringLit
(all unchanged this slice — test-only).

Gates: gofmt clean; go vet clean; TestGeneratedColumnExprCanonicalSpacing PASS (cached — case existed);
TestPort_PgDumpConnectionSetup PASS (4.08s vs real pg_dump 18.3); make ralph-state-guard; pgbench
pre-commit smoke (enforced by .githooks/pre-commit on commit).

Next (slice 297+): a multi-column / NULL-typed DEFAULT variant on the partition-leaf ALTER path, OR a
string-literal generation expr with an embedded backslash / E'' escape to exercise the literal
re-quoting against standard_conforming_strings edge cases.
