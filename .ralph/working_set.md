Task: DU-002 slice 289 (loop #57) — COMPLETE, committing + pushing.

Last landed: PRODUCTION fix — canonical `GeneratedExpr` deparse. The FIRST parenthesised
generation expression (`(fa + fb) * 2`) inherited onto a partition leaf now round-trips vs
real pg_dump 18.3. Defect: goopg captured the `GENERATED ALWAYS AS (...)` body as raw lexer
tokens and joined with single spaces, so `(fa + fb) * 2` → `( fa + fb ) * 2` (and `upper(fn)`
→ `upper ( fn )`); pg_dump wraps `(%s)` so goopg emitted `(( fa + fb ) * 2)` vs real
pg_get_expr's tight `((fa + fb) * 2)`.

Fix: new `joinGeneratedExprTokens` (parser/ddl.go, inserted before parseColumnDef at ~line
2457) reconstructs pg_get_expr spacing from the captured `[]Token` stream — tight call parens
(prev Kind is TokenIdent/TokenQuotedIdent or prev==`)`), spaced grouping parens, `, ` arg
separators, tight `.` qualified names, spaced binary operators. BOTH capture sites now collect
`[]Token` (was `[]string`) and route through the helper: the column-def path (~line 2576–2613)
and the `PARTITION OF (... WITH OPTIONS GENERATED ALWAYS AS ...)` override path (~line 1566–1586).
Re-parses to same node → evalGeneratedExpr unaffected; flat-chain slices 283–288 keep
byte-identical stored source (verified).

Files:
- internal/parser/ddl.go — `joinGeneratedExprTokens` helper + both capture sites → []Token.
- internal/parser/gen_override_test.go — `TestGeneratedColumnExprCanonicalSpacing` (no server).
- internal/testport/pgdump_connsetup_test.go — pgpp fixture (after pgcc_1) + assertion (after
  pgcc_1 ATTACH assertion).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 289 section + Next (290) note.
- .ralph/fix_plan.md — slice 289 progress (loop #57).

Gates: gofmt clean; go vet clean; go test ./internal/{parser,executor,catalog}/ PASS;
TestPort_PgDumpConnectionSetup PASS (4.07s vs real pg_dump 18.3); pgbench pre-commit smoke
(enforced by .githooks/pre-commit on commit).

Next (slice 290+): function-call generation deparse END-TO-END (`upper(fn)`) — parser
canonicalization is done + unit-tested; remaining is confirming CREATE-TABLE + materialization
accept a function-call generation expr so it can ride the pg_dump oracle. OR a multi-column /
NULL-typed DEFAULT variant on the partition-leaf ALTER path.
