Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop implemented
the SQL:2003 `SUBSTRING(str SIMILAR pattern ESCAPE escape)` form, the
sizing researcher's largest identified remaining bucket.
Committed/pushed: code `0a04e518`, bookkeeping `a4e7b471`
(`e59785b1..0a04e518..a4e7b471` on `regress-renumbering`).

Landed: goopg's `parseSubstringFuncCall` had no `SIMILAR`/`ESCAPE` grammar
branch at all (hard-required `FROM`). Added a mandatory-`ESCAPE` `SIMILAR`
branch (`internal/parser/select.go`) that parse-time constant-folds literal
str/pattern/escape into a plain 2-arg `substring(text, <POSIX ERE>)` call
(mirrors `buildSimilarTo`'s literal-fold shape, adapted for 3-operand STRICT
NULL propagation) — lands unchanged on the existing `evalSubstrRegex`
capturing-group executor path, zero executor changes needed.
`internal/utils/adt/similarto.Convert` refactored into a shared
`convert(pattern, escape, substringMode)`; new `ConvertSubstring` adds the
escape-double-quote (`#"..#"`) part-separator convention (PG oracle
`regexp.c:920-953`) the package's own doc comment had flagged as
deliberately unported since the original SIMILAR TO work. A third separator
raises `ErrTooManyQuoteSeparators` → SQLSTATE `2200C`. New tests:
`similarto_test.go` (ConvertSubstring ERE-shape pins),
`internal/parser/substring_similar_test.go` (constant-fold pins + 2200C +
NULL-propagation), `internal/executor/substring_similar_escape_test.go`
(11/13 end-to-end byte-exact pins vs PG 18.3 + 2 documented-gap tests).
`strings.sql` diff shrank **941→857 lines**. Case still `failed`.

Deferred (ledger row appended 2026-08-22, 2 items): (1) Go's `regexp`
package is RE2 leftmost-first, not PG's POSIX-ARE leftmost-longest — 1/13
SIMILAR/ESCAPE statements (`'a*#"%#"g*'`) picks the wrong non-greedy
division; `regexp.CompilePOSIX` can't compile PG's `{1,1}?` syntax, so this
needs either an engine swap (cross-cutting) or a targeted rewrite — pinned
as a documented-divergence test, not fixed. (2) the `2200C` error case
omits PG's `CONTEXT:  SQL function "substring" statement 1` line — no
CONTEXT-stack mechanism exists anywhere in goopg for constant-folded
builtin-SQL-wrapper errors (not substring-specific, out of this slice's
scope). Both are 1-line-of-diff residuals.

Key symbols: `similarto.convert`/`ConvertSubstring`/`Convert`
(`internal/utils/adt/similarto/similarto.go`), `parseSubstringFuncCall`'s
new `SIMILAR` branch + `buildSubstringSimilar`
(`internal/parser/select.go`, grep for `KwSimilar` inside
`parseSubstringFuncCall`).

Next step: re-check the fix_plan banner at loop start first (M-NIGHTLY
filing unconditional). Then continue sizing the now-857-line `strings.sql`
diff. Named candidates, none yet sized in detail this loop: (a) the
cross-cutting `psql` aligned-output column-width bug affecting
`ascii()`/`bit_count()` (also present in `misc_functions.diff`/`stats.sql`
per the 2026-08-22 sizing researcher — needs protocol-logging/packet-capture
investigation, not a normal implementer round); (b) residual Unicode-escape
parser error-message/DETAIL mismatches; (c) `scripts/pg-oracle-diff.sh
--auto-start`'s `initdb -q` breakage (ledger row dated 2026-08-22, M0134-0070
infra entry) — none of (a)/(b)/(c) sized this loop, run a researcher sizing
pass on the 857-line diff before picking the next bucket (the SIMILAR/ESCAPE
bucket that was previously largest is now mostly closed).

Gates run this loop: `go build ./...` PASS; `go test ./internal/parser/...
./internal/executor/... ./internal/utils/adt/similarto/...` PASS (all
cached); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/pg-regress-runner.sh --verbose strings` 941→857 lines (coordinator
re-ran directly to verify before commit, not just trusting the worker's
report); pre-commit pgbench smoke via git hook — PASS twice (code commit:
376/697/13180 TPS; bookkeeping commit: 376/697/13179 TPS, 0 failed both
times). `make ralph-state-guard` — pending, run before final status block.

Delegation: researcher round (sizing SUBSTRING SIMILAR ESCAPE, inline
in-conversation, no handoff dir) DONE. implementer round
(`tmp/ralph-handoffs/m0134-0070-substring-similar-escape/`) DONE in one
round — hit the same recurring tooling friction as recent prior loops (could
not write report.md via its own tools; relayed full report text in its
final message instead). Coordinator persisted the ledger row from the
relayed report and independently re-ran the regress-runner + precommit gates
before committing (did not just trust the relayed numbers). No open
handoff — brief.md remains on disk as a completed record, no report.md file
(report text preserved in this working_set entry + the ledger row).

In-flight: none. Commits `0a04e518` (code) and `a4e7b471` (bookkeeping)
landed and pushed to `regress-renumbering`
(`e59785b1..0a04e518..a4e7b471`). No server left running.
