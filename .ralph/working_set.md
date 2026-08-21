Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop closed the
POSITION(sub IN str) grammar gap and committed/pushed it (`c13eba8c`).
strings.sql itself remains `failed` overall (diff now 2404 lines, down from
2454 at loop start).

This loop: delegated a researcher round to size the 5 remaining REFACTOR-tier
buckets in strings.sql's diff (POSITION/OVERLAY/LIKE-ESCAPE/SIMILAR-TO
grammar ~376 lines, ascii()/bit_count() spacing ~4 lines but systemic/shared
with domain.diff+misc_functions.diff — NOT picked, root cause unverified,
don't bandaid it — Unicode-escape literals ~57 lines, regexp_* function
family ~1215 lines — by far the dominant bucket, chr(0)/bytea NUL ~4 lines).
Selected POSITION(x IN y) as the smallest fully-contained slice (pure parser
desugar to the existing `position(sub,str)` FuncCall dispatch, zero executor
change) and delegated an implementer round.

Round 1 implementer output built `parsePositionFuncCall` (mirrors
`parseSubstringFuncCall`'s dual-form pattern) but got the IN-form arg order
backwards (`Args: []Expr{sub, str}` when the executor's `strpos`/`position`
case expects `{str, sub}`, i.e. haystack-first). Caught this via the tester's
gate-4 live regress run — a real semantic bug, not a parse failure (every
POSITION(x IN y) call returned the wrong boolean). Sent a round-2 SendMessage
to the same implementer with the exact fix; it corrected only the IN-form
desugar site (correctly declined to also "fix" the comma-form site per the
brief's literal wording — verified empirically that the comma form was
already correct in source order and swapping it would have been a
regression; flagged as a deliberate, verified deviation in report.md).
Re-ran gates live: diff now 2404, POSITION(x IN y) block (both text and
bytea variants) is byte-identical clean context vs the PG oracle.

Files this loop: `internal/parser/select.go` (new `parsePositionFuncCall`,
intercept in `parseColumnOrCall`'s function-call chain), new
`internal/parser/position_test.go` (3 tests: IN form, comma-form regression
guard, missing-close-paren error). Handoff dir:
`tmp/ralph-handoffs/m0134-0070-position-in-desugar/` (brief.md + 2-round
report.md) — scratch, not system of record. No design doc needed (small,
template-following parser slice — same scope precedent as the `eba6009e`
errSyntaxAtCur fix).

Key symbols: `parsePositionFuncCall` (`internal/parser/select.go:4130`,
new), `parseColumnOrCall` intercept chain (`internal/parser/select.go:~4277`),
executor dispatch `case "strpos", "position":`
(`internal/executor/expr.go:11514`, unchanged — confirmed its
haystack-first/needle-second convention this loop, worth remembering for any
future OVERLAY/SIMILAR-TO desugar work in the same family).

Hypothesis/Findings: strings.sql's remaining 2404-line diff, per this loop's
researcher sizing (re-verify live before trusting exact numbers, they shift
as buckets are fixed):
- regexp_count/regexp_like/regexp_instr/regexp_substr/regexp_replace
  backreferences/regexp_matches(...,'g')/regexp_split_to_table family —
  **dominant bucket, ~1215 of 1688 diff +/- lines (~72%)**. Likely many
  independent semantic gaps (new PG15+ functions), not one contained fix —
  needs its own sizing pass to decompose into per-function slices before
  briefing.
- OVERLAY(... PLACING ... FROM ...) — no PLACING grammar exists anywhere in
  internal/parser/; needs both a new parser production (same
  parseSubstringFuncCall-family template) AND a new executor
  `case "overlay":` (currently `overlay` is only in an unrelated builtin
  allowlist at internal/executor/operators_call.go:745, no eval dispatch).
- LIKE ... ESCAPE — `LIKE` itself parses fine (OpLike BinaryOp,
  internal/parser/select.go:1939-1963) but has no ESCAPE-clause consumption
  after the RHS.
- SIMILAR TO — zero support, not even a keyword (`SIMILAR`/`KwSimilar`
  absent from token.go); needs new grammar AND likely a new executor-side
  POSIX-pattern-conversion helper (PG's `similar_escape()` equivalent — none
  found under that name in goopg).
- Unicode-escape `U&'...'`/`UESCAPE` + bit/hex-string literals (~57 lines,
  not yet sized in detail).
- ascii()/bit_count() spacing (~4 lines in strings.sql, but root cause is a
  shared/systemic RowDescription or column-width wire-protocol issue also
  present in domain.diff/misc_functions.diff, absent from int2/int4/int8 —
  do NOT fix blind in strings.sql alone, needs dedicated investigation of
  internal/postmaster/{query,extended}.go or internal/libpq/{protocol,
  messages}.go first).
- chr(0)/bytea-trim NUL-byte handling (~4 lines, not yet sized in detail).

Next step: pick the next REFACTOR-tier bucket. Recommend, in order of
increasing cost per this loop's researcher: LIKE...ESCAPE (parser-only, plumb
an escape char into the existing OpLike/OpNotLike BinaryOp or a new node) →
OVERLAY (parser + one new executor case) → decompose the regexp_* family
into per-function slices (large, needs its own sizing pass — do not brief it
as one slice) → SIMILAR TO (parser + new executor regex-conversion function,
biggest lift). Alternatively PARK M0134-0070 now if the remaining buckets all
look too large for one loop — re-check the fix_plan banner at loop start
first; if parking, M0134-0071 (equivclass.sql) is next in ID order (see prior
loop's note: gated behind CREATE TYPE (INPUT=,OUTPUT=,LIKE=) base/shell type
creation + LANGUAGE INTERNAL function dispatch, both currently
stubbed/absent — get a live tester diff before treating it as "next" if 0070
gets parked).

Gates run this loop: `go build ./...` PASS (implementer + tester, both
rounds); `go test ./internal/parser/...` PASS (implementer + tester, both
rounds, cached, verified non-stale via `-run Position -v`); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (tester, ~9min, internal/initdb cold
~424s as expected, not a regression); `scripts/pg-regress-runner.sh
--verbose strings` (tester, cgroup-capped) — run twice: round-1 diff 2439
(revealed the arg-order bug), round-2 diff 2404 (bug fixed, POSITION block
byte-identical clean); pre-commit pgbench smoke via git hook — PASS
(376/690/12783 TPS, 0 failed transactions). `make ralph-state-guard` — run
after this status block per protocol.

Delegation: researcher round (a35454581d13cf1ef, sizing) + implementer round
1 (a246f46fbf510e295, tmp/ralph-handoffs/m0134-0070-position-in-desugar/,
DONE but with the arg-order bug) + tester round (ad155b259659bcca9,
gate-4 caught the bug, BLOCKED verdict) + implementer round 2 (SendMessage
to a246f46fbf510e295, fixed + re-verified, DONE). 2 rounds on the
implementer (within the 3-round cap), converged. No open handoff.

In-flight: none. Commit `c13eba8c` landed and pushed to
`regress-renumbering` (`eba6009e..c13eba8c`). No server left running.
