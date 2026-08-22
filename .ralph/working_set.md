Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
Unicode-escape error-message wording bucket. Code commit `f6557f64`, pushed.

Landed: `internal/parser/lexer.go` — `decodeUnicodeEscapes` dropped the E-string
`at or near "…"` suffix from its six 42601 surrogate-pair/escape-value messages
(bare text, PG `str_udeescape`/`check_unicode_value` parser.c:341-348,371-527);
`lexUnicodeEscapeQuote` gained the dedicated `UESCAPE must be followed by a simple
string literal at or near "<token>"` fallthrough for a non-SCONST third token
(parser.c:271-274) and appends the raw SCONST text to the `invalid Unicode escape
character` message. Siblings `scanEscapeQuoteInto` (E-string) and `unistr.go`
untouched (different PG convention). Tests updated in
`internal/parser/unicode_escape_literal_test.go`. strings.sql diff 327→279 lines,
`@@ -56,39 +56,39 @@` hunk closed.

Key symbols: `decodeUnicodeEscapes` (lexer.go ~748-850),
`lexUnicodeEscapeQuote` (lexer.go ~622-745), `SyntaxError` (Pos/Raw/Code/Message).

Hypothesis/Findings: researcher re-ran the gate (the 11:41 diff was stale —
predated bytea-LIKE/toasttest-hunk-1/regexp-HINT). Fresh 327-line diff. Three
small buckets were sized: Bucket A (Unicode-escape msg wording ~16, CONTAINED —
this loop, landed), Bucket B (`char(20) '...'` typed-literal ~13, CONTAINED,
parser-only), Bucket C (SQL99 SUBSTRING + CONTEXT — CROSS-CUTTING: C1 7-line
contained half via `similarto.ConvertSubstring` fold, C3 CONTEXT-line is the known
`sql_exec_error_callback` gap + separate RE2-vs-ARE `bcdefg` 2-liner). J2 toast
hunk 2 reconfirmed deferred (wider toast-path blast radius). No deferral this loop.

Next step: brief Bucket B — `char(20) '...'` typed-literal grammar
(`tryTypedLiteral`, `internal/parser/select.go:3172-3294`): mirror the
`interval ( p ) 'lit'` arm (3227-3239) to accept `type ( typmod ) 'lit'` when
peek(1)='(' peek(2)=intlit peek(3)=')' peek(4)=stringlit; consume 5 tokens →
`TypedStringLit{Type: name, Value: strTok.Value}`. Executor already ready
(`evalTypedStringLit`, expr.go:3550). Ignore the typmod value (bpchar
padding/truncation deferred). ~13 diff lines.

Gates run this loop: `go test ./internal/parser/` PASS (implementer); `go build
./...` PASS; `scripts/pg-regress-runner.sh --verbose strings` exit 0 (279-line
diff); pre-commit pgbench smoke PASS (0 failed, via git hook); `make
ralph-state-guard` clean.

Delegation: researcher (m0134-0070-resize-small-buckets) DONE — sized A/B/C/J2.
implementer (m0134-0070-unicode-msg-wording) DONE — all gates PASS, committed
`f6557f64`. Report.md could not be written by the implementer (tool guard);
content folded into fix_plan.md + this file.

In-flight: none. (Observed, not mine: a stale PG oracle process
`tmp/oracle-74-pgdata` @ port 5534 PID 291764 predates this session; left alone.)
