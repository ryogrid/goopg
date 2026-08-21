Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop closed the
`SIMILAR TO`/`NOT SIMILAR TO [ESCAPE ...]` grammar/semantics gap and
committed/pushed it (`3a4d9788`). strings.sql itself remains `failed` overall
(diff now 1915 lines, down from 2076 at loop start).

This loop: delegated a researcher round (confirmed PG's `similar_escape_internal`
conversion algorithm, gram.y's `similar_to_escape` desugar, and — critically —
that SIMILAR TO's fixture includes EXPLAIN output showing the ALREADY-CONVERTED
POSIX pattern, ruling out a runtime-wrapper-only approach like the prior
LikeEscapePattern template). Delegated one implementer round; it converged in 1
round (well within the 3-round cap), landed the full brief scope plus one
justified deviation (Hint field on SyntaxError, needed for exact PG wire shape
on the 22025 error).

Landed: new `KwSimilar` keyword + grammar production
(`internal/parser/select.go`, same precedence level as LIKE/ILIKE) with
parse-time constant-folding (`buildSimilarTo`) that performs PG's
`similar_escape_internal` SQL-pattern→POSIX-ERE conversion (new shared leaf
package `internal/utils/adt/similarto`, byte-for-byte port of
`postgres/src/backend/utils/adt/regexp.c:768-1063`) immediately when
Pattern/Escape are literal, emitting a plain `BinaryOp{Op:
OpRegexMatch/OpRegexNoMatch, Right: &TypedStringLit{...}}` — same shape PG's
own planner constant-folding produces, so `EXPLAIN (COSTS OFF)` renders
`Filter: (f1 ~ '^(?:...)$'::text)` byte-identically. `ESCAPE ''`/`ESCAPE
NULL`/`ESCAPE '##'` (22025, PG hint text, no LINE/position) all handled at
parse time. All SIMILAR TO/NOT SIMILAR TO statements in `strings.sql:188-221`
now byte-identical to PG oracle.

Files this loop: `internal/parser/token.go` (`KwSimilar`),
`internal/parser/keywords.go`, `internal/parser/select.go` (grammar +
`buildSimilarTo`), `internal/parser/expr.go` (new unwired `SimilarToPattern`
AST node, runtime-fallback placeholder), `internal/parser/parser.go`
(`SyntaxError.Hint` field), `internal/postmaster/copy.go` +
`dispatch_extended.go` (Hint wire-field plumbing, simple+extended twin sync),
`internal/optimizer/exprkey_test.go` (registered new AST type),
`internal/utils/adt/similarto/similarto.go` (new leaf package, shared
Convert/ValidateEscape), tests: `internal/parser/similar_to_test.go`,
`internal/executor/similar_to_test.go`,
`internal/utils/adt/similarto/similarto_test.go`. Handoff dir:
`tmp/ralph-handoffs/m0134-0070-similarto/` (brief.md + report.md) — scratch,
not system of record.

Key symbols: `buildSimilarTo` (`internal/parser/select.go`),
`similarto.Convert`/`similarto.ValidateEscape`
(`internal/utils/adt/similarto/similarto.go`), unwired
`parser.SimilarToPattern` (runtime-fallback placeholder, not reachable from
any eval path yet).

Deferred (ledger row 2026-08-22, M0134-0070 SIMILAR TO entry): (1) non-literal
SIMILAR TO pattern/escape has no runtime-eval wiring — `parser.SimilarToPattern`
exists but isn't threaded through analyzer/optimizer/executor, so it fails
cleanly with 0A000 rather than evaluating (not exercised by strings.sql); (2)
`SUBSTRING(str SIMILAR pattern ESCAPE esc)` is a wholly separate unimplemented
builtin (exercised by strings.sql:180-183,305-420, still failing).

Next step: pick the next REFACTOR-tier bucket for strings.sql (1915-line diff
remaining). Per the researcher/implementer sizing carried across loops, in
rough order: decompose the dominant `regexp_*` family into per-function
slices (largest remaining bucket by line count — `regexp_count`/
`regexp_like`/`regexp_instr`/`regexp_substr`, `regexp_replace` backreferences,
`regexp_matches(...,'g')` multi-match, `regexp_split_to_table` — needs its own
sizing pass, do NOT brief as one slice) → Unicode-escape `U&'...'`/`UESCAPE` +
bit/hex-string literals (~57 lines, not yet sized in detail) →
`SUBSTRING(... SIMILAR ... ESCAPE ...)` (could reuse
`internal/utils/adt/similarto` if the quote-separator convention is added) →
`ascii()`/`bit_count()` spacing (~4 lines, but root cause looks
systemic/shared with domain.diff/misc_functions.diff — do NOT fix blind in
strings.sql alone) → `chr(0)`/bytea-trim NUL handling (~4 lines, not yet
sized). Alternatively PARK M0134-0070 now if remaining buckets all look too
large for one loop — re-check the fix_plan banner at loop start first; if
parking, M0134-0071 (equivclass.sql) is next in ID order (gated behind CREATE
TYPE (INPUT=,OUTPUT=,LIKE=) base/shell type creation + LANGUAGE INTERNAL
function dispatch, both currently stubbed/absent — get a live tester diff
before treating it as "next" if 0070 gets parked).

Gates run this loop: `go build ./...` PASS (implementer + this session
re-verified); `go test ./internal/parser/... ./internal/executor/...
./internal/optimizer/... ./internal/utils/adt/similarto/...` PASS (implementer
report + this session re-verified, cached-fresh); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (implementer report, not re-run — no
reason to distrust); `scripts/pg-regress-runner.sh --verbose strings`
(implementer, cgroup-capped) — diff 2076→1915, zero SIMILAR TO-region hunks
remain, no other regressions introduced; pre-commit pgbench smoke via git hook
— PASS (372/695/12626 TPS, 0 failed transactions). `make ralph-state-guard` —
ran this loop, found the same stale running/completed mismatch pattern as the
prior 3 loops (prior loop's clean-exit marker misread as a completion
marker), self-repaired to consistent (in_progress), OK.

Delegation: researcher round (a7dae2a1c4518e430, similar_escape_internal
algorithm + EXPLAIN-fold-vs-runtime-wrapper distinction) + implementer round
(a1188b4f7946b5730, tmp/ralph-handoffs/m0134-0070-similarto/, DONE in 1
round, converged). No SendMessage follow-up needed. No open handoff. (Note: a
stale IDE-diagnostics system-reminder flagged "undefined: SimilarToPattern" in
select.go mid-implementer-session — verified false/stale via a fresh `go build
./...` in this session after the implementer finished; not a real issue.)

In-flight: none. Commit `3a4d9788` landed and pushed to `regress-renumbering`
(`face6120..3a4d9788`). No server left running.
