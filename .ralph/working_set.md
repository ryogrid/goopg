Task: M0134-0070 (strings.sql) — regress-sql `failed`, still in progress.
This loop fixed the cross-cutting LINE/caret false-diff gap sized last loop
(BinaryExpr/UnaryExpr/shared-arithmetic-helper `ExecError` sites only).

Files this loop: `internal/executor/expr.go` (removed `Pos:` from the
runtime-eval raise sites — division-by-zero, arithmetic overflow, pg_lsn
overflow, invalid-regex, negative-substring-length, timestamp/interval
out-of-range, shared `arithmetic()` int64 helper), `internal/executor/exprnode.go`
(compiled twin, mirrored the int2/int4 overflow fix — Rule 4 sibling sync),
`internal/executor/expr_sibling_parity_test.go` (corrected a `Pos != 0`
assumption that was itself wrong per PG oracle), new
`internal/executor/expr_error_position_test.go`. Also `.ralph/fix_plan.md`
(M0134-0070 entry) and `.ralph/deferral_ledger.md` (new row, 2026-08-21,
LINE/caret entry).

Key symbols: `evalExprSlot`/`evalUnary`/`evalBinary`/`evalPgLSNBinary`/
`timestampOutOfRange`/`intervalOutOfRange`/`intervalDiv`/`arithmetic()`
(all `internal/executor/expr.go`) — the sites that lost `Pos:`.

Hypothesis/Findings: PG server-side never renders "LINE N:" text itself; it
only sets `ErrorData.cursorpos` via `errposition()`
(`postgres/src/backend/utils/error/elog.c:1468`), and the CLIENT
(`fe-protocol3.c:1200` `reportErrorPosition`) draws LINE+caret only when
that field is present. PG sets it for lex/parse errors and for literal-
constant type coercion at parse time (`parse_node.c:140,354-459`'s
`setup_parser_errposition_callback`) — NOT for plain runtime execution of
operator/function C code (confirmed: no `errposition()` calls anywhere in
`int.c`/`int8.c`/`float.c`/`pg_lsn.c`/`timestamp.c`/`regexp.c`'s runtime
arithmetic/function bodies). goopg's `expr.go` set `Pos` unconditionally on
all ~174 `ExecError` sites; fixed the BinaryExpr/UnaryExpr/shared-helper
subset this loop. `strings.sql` diff shrank 2539→2501 lines (live psql
probe confirmed the fix: `SELECT 1/c FROM (VALUES (0)) t(c);` now omits
LINE/caret). Three follow-on gaps discovered, NOT fixed (own deferral-ledger
entries, same row): (1) `internal/executor/unistr.go`'s 3 raise sites are
NOT Pos-less as a prior loop's row incorrectly assumed — same fix pattern,
small/contained, good next-slice candidate; (2) `roundNumericToInt`
(numeric→int8 CAST path) doesn't distinguish bare-literal casts (PG: Pos
present) from column-derived casts (PG: likely Pos-absent, not
independently reverified) — harder, needs literal-vs-column AST
classification; (3) `abs()`/`gcd()`/`lcm()`/`mod()` FuncCall builtin sites
in expr.go are the same "pure runtime, no PG errposition" shape but weren't
in this loop's scope (different call-site family).

Next step: pick ONE of: (a) fix `unistr.go`'s 3 Pos-setting sites (small,
same pattern as this loop — good first candidate); (b) another `strings.sql`
REFACTOR-tier bucket (Unicode-escape/bit-string/hex-string literals;
POSITION/OVERLAY/LIKE ESCAPE/SIMILAR TO grammar; regexp_count/regexp_like/
regexp_instr/regexp_substr family; regexp_replace backreferences;
regexp_matches(...,'g') multi-match; regexp_split_to_table); (c) the
`abs`/`gcd`/`lcm`/`mod` Pos-stripping fast-follow. Re-verify against the
fix_plan banner (M0134 next-priority-after-M-NIGHTLY) at the START of next
loop; also re-check `ci/logs/action-items.md` for new `## AI-` items (none
found this loop — nightly run 20260821-002906 was `status: pass`, `items:
0`, only non-blocking env-drift notices about newly-SKIPPED TestPort_*
tests, no action required).

Gates run this loop: `go build ./...` PASS; `go test ./internal/executor/...
-run 'TestRuntimeEvalErrorsCarryNoPos|TestLiteralCastOverflowStillCarriesPos'`
PASS; `go test ./internal/executor/... -run 'TestExprSiblingParity'` PASS;
full `go test ./internal/executor/...` PASS (7.4s, coordinator re-verified);
`go vet ./internal/executor/...` clean; `scripts/pg-regress-runner.sh
--verbose strings` (tester, cgroup-capped) — diff 2539→2501 lines, confirmed
no brief-target error messages carry `+LINE` noise anymore;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (tester);
`make ralph-state-guard` — same recurring stale completed-marker
inconsistency as every prior loop, auto-repaired, then PASS; pre-commit
pgbench smoke PASS (362-673-12098 TPS, 0 failed).

Delegation: researcher agent `adc568f39c939393d` (2 rounds — first sized
the LINE/caret gap end-to-end with PG-oracle + goopg citations; follow-up
via SendMessage resolved the literal-cast-vs-runtime-eval classification
unknown, confirming the exact site list was safe to strip); implementer
agent `aed8429b621548edc` (1 round — landed the fix cleanly per brief, ran
its own pre-edit verification probe per the brief's mandate, documented 3
deviations and 3 deferral candidates in report.md, all reasonable). Both
agents completed; no further rounds needed.

In-flight: none. Commit `b6524e0b` pushed to `regress-renumbering`. No
server left running.
