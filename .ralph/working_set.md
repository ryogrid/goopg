Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop closed the
`LIKE ... ESCAPE` grammar/semantics gap and committed/pushed it (`a5505fbe`).
strings.sql itself remains `failed` overall (diff now 2135 lines, down from
2404 at loop start).

This loop: delegated a researcher round (confirmed 32 self-contained
`LIKE .. ESCAPE ..` lines, zero ESCAPE-keyword handling anywhere in goopg,
and that PG desugars the ESCAPE clause into a runtime-evaluated `a_expr`
via `like_escape()`, not a parse-time constant — cited gram.y + like_match.c
`do_like_escape`). Delegated an implementer round; it converged in 1 round
but needed more than the brief scoped (see below) — still within the 3-round
cap, no SendMessage follow-up needed.

Landed: new unreserved keyword `KwEscape`; new `parser.LikeEscapePattern` /
`optimizer.LikeEscapePattern` wrapper node (deliberately kept OUT of
`BinaryOp` — that struct has 43 existing `case *parser.BinaryOp:` switch
sites across the codebase, auditing all of them for a new field was out of
scope; the wrapper only ever appears as `BinaryOp.Right` when an ESCAPE
clause was present, so every other `BinaryOp` use is unaffected); analyzer +
planner + evaluator wiring; PG-faithful `likeEscapeRewrite` implementing
`do_like_escape` exactly (empty escape doubles literal backslashes, 1-char
substitutes, escape==`\` no-op, >1-char raises SQLSTATE 22025 with PG's
exact HINT), feeding the rewritten pattern into the **unmodified**
`matchSQLLike`. Covers text AND bytea, all 4 forms (LIKE/NOT LIKE/ILIKE/NOT
ILIKE). All 32 fixture lines now byte-identical to the PG oracle.

Deviation from the brief (load-bearing, not scope creep): the brief's
scoped edit site (`evalBinary`, expr.go ~1819) only ever sees already-
evaluated `Datum`s, not AST nodes, so the escape-rewrite had to happen one
level up in `evalExprSlot`'s node-type switch (where `Right` is still an
`Expr`), which required a parallel `optimizer.LikeEscapePattern` twin (the
planner's resolved-expression IR is a separate node set from the parser's
AST) plus `analyzeExpr`/planner/exprwalk cases the brief didn't anticipate.
Verified live: `go build ./...` clean, `go test ./internal/parser/...
./internal/executor/... ./internal/optimizer/...` PASS, `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (per implementer, not re-run — no
reason to distrust), fresh `scripts/pg-regress-runner.sh --verbose strings`
via tester re-verification round: diff 2135, zero hunks touch the ESCAPE
region. Pre-commit pgbench smoke via git hook — PASS (376/696/12831 TPS, 0
failed).

Files this loop: `internal/parser/token.go`, `internal/parser/keywords.go`
(new `KwEscape`), `internal/parser/expr.go` (new `LikeEscapePattern`),
`internal/parser/select.go` (wiring into the 4 LIKE-family parse arms),
`internal/parser/like_escape_test.go` (new), `internal/parser/analyzer/analyzer.go`
(new case arm — required, not in original brief scope), `internal/optimizer/plan.go`,
`internal/optimizer/planner.go`, `internal/optimizer/exprwalk.go`,
`internal/optimizer/exprkey_test.go` (planner-IR twin node + resolver
cases — required, not in original brief scope), `internal/executor/expr.go`
(`evalLikeEscapePattern`/`likeEscapeRewrite` in `evalExprSlot`),
`internal/executor/like_escape_test.go` (new). Handoff dir:
`tmp/ralph-handoffs/m0134-0070-like-escape/` (brief.md + report.md) —
scratch, not system of record.

Key symbols: `parser.LikeEscapePattern` (`internal/parser/expr.go`),
`optimizer.LikeEscapePattern` (`internal/optimizer/plan.go`),
`likeEscapeRewrite`/`evalLikeEscapePattern` (`internal/executor/expr.go`,
new), `matchSQLLike` (`internal/executor/expr.go:2185`, unchanged — fed the
rewritten pattern, single source of truth for backslash matching).

Deferred (ledger row 2026-08-22): expression-to-SQL deparsers
(`internal/executor/operators_ddl.go:defaultExprToSQL`,
`internal/catalog/catalog.go:formatExprForAttrdef` — CHECK-constraint/
DEFAULT-expression round-trip rendering) don't know `LikeEscapePattern` and
would silently drop the ESCAPE clause on round-trip. Not exercised by
strings.sql itself so left open; resume point is in the ledger row.

Next step: pick the next REFACTOR-tier bucket for strings.sql (2135-line
diff remaining). Recommend, in order of increasing cost per the researcher
sizing carried from the prior loop: OVERLAY(... PLACING ... FROM ...)
(parser + one new executor case — no PLACING grammar exists anywhere in
internal/parser/, and `overlay` is only in an unrelated builtin allowlist at
internal/executor/operators_call.go:745 with no eval dispatch) → decompose
the regexp_* family into per-function slices (dominant bucket, needs its
own sizing pass, do not brief as one slice) → SIMILAR TO (parser + new
executor POSIX-conversion helper, biggest lift, no `similar_escape`
equivalent exists) → Unicode-escape `U&'...'`/`UESCAPE` + bit/hex-string
literals (~57 lines, not yet sized in detail) → ascii()/bit_count() spacing
(~4 lines, but root cause looks systemic/shared with domain.diff/
misc_functions.diff — do NOT fix blind in strings.sql alone, needs dedicated
wire-protocol investigation first) → chr(0)/bytea-trim NUL handling (~4
lines, not yet sized). Alternatively PARK M0134-0070 now if remaining
buckets all look too large for one loop — re-check the fix_plan banner at
loop start first; if parking, M0134-0071 (equivclass.sql) is next in ID
order (gated behind CREATE TYPE (INPUT=,OUTPUT=,LIKE=) base/shell type
creation + LANGUAGE INTERNAL function dispatch, both currently
stubbed/absent — get a live tester diff before treating it as "next" if
0070 gets parked).

Gates run this loop: `go build ./...` PASS (implementer + this session, both
verified); `go test ./internal/parser/... ./internal/executor/...
./internal/optimizer/...` PASS (cached, verified fresh); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (implementer report, not re-run);
`scripts/pg-regress-runner.sh --verbose strings` (tester re-verification
round, cgroup-capped) — diff 2135, zero ESCAPE-region hunks confirmed;
pre-commit pgbench smoke via git hook — PASS (376/696/12831 TPS, 0 failed
transactions). `make ralph-state-guard` — ran this loop, found a stale
running/completed mismatch from the prior loop's clean exit, self-repaired
to consistent (in_progress), OK.

Delegation: researcher round (a61efa80a1841d191, sizing/grammar
confirmation) + implementer round (a84f1c26492f7c718,
tmp/ralph-handoffs/m0134-0070-like-escape/, DONE in 1 round, converged) +
tester re-verification round (a278d1439a1a4fa33, DONE, confirmed diff 2135
and ESCAPE-region clean). No SendMessage follow-up needed (1 round,
well within the 3-round cap). No open handoff.

In-flight: none. Commit `a5505fbe` landed and pushed to
`regress-renumbering` (`93dcdf7c..a5505fbe`). No server left running.
