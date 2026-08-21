Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop found and
fixed a fresh regression in the earlier-landed string-literal-continuation
lexer feature (not a REFACTOR-tier bucket pick — a genuine correctness bug
introduced same-day). strings.sql itself remains `failed` (diff now 2461,
down from 2501).

This loop: confirmed via researcher (couldn't run the gate itself; used the
existing 2026-08-21 19:40 diff artifact) that a NEW small regression had
appeared at the top of the strings.sql diff: `tryQuoteContinuation()`
(`internal/parser/lexer.go`) wrongly treated a `/* block comment */` inside
the quote-continuation gap as skippable whitespace whose internal newlines
satisfy PG's "must contain a newline" rule — so goopg wrongly concatenated
`'a' 'b' /* c */ 'd'` where PG raises a syntax error at the third fragment.
Root cause: PG's `scan.l` `quotecontinue`/`whitespace_with_newline`/
`comment` macros (~215-239) only ever admit `--` line comments into the
gap; block comments are a wholly separate `<xc>` start-condition with no
reachability into `quotecontinue`. Delegated fix to implementer (1 round,
converged): deleted the block-comment-skip branch from the scan loop so a
`/*` now falls straight to `default: break scan` and fails the lookahead.
Re-measured live via tester: diff shrank 2501→2461 lines. The specific
fixture line is STILL a diff (behavior now matches — both raise a syntax
error — but the error MESSAGE TEXT differs), a narrower new deferral row.

Files this loop: `internal/parser/lexer.go` (`tryQuoteContinuation`, removed
block-comment branch + doc comment update), `internal/parser/string_continuation_test.go`
(rewrote `TestLexStringContinuationWithComments`, added
`TestLexStringContinuationBlockCommentBetweenFragmentsIsSyntaxError` +
`TestBlockCommentAsOrdinaryWhitespaceUnaffected`), `.ralph/fix_plan.md`
(M0134-0070 entry appended), `.ralph/deferral_ledger.md` (new row for the
error-message-text gap). Left untouched (concurrent WIP at loop start):
`.ralph/progress.json`, `ci/logs/scheduler.log`, `third-party/tpcds-postgres`
submodule ref, untracked `postgres` symlink.

Key symbols: `tryQuoteContinuation` (`internal/parser/lexer.go:137`).
No compiled/interpreted-evaluator twin — lexer-only concern, no Rule-4
sibling sync needed.

Hypothesis/Findings: strings.sql's remaining 2461-line diff is still
dominated by the same REFACTOR-tier buckets as last loop (Unicode-escape
`U&'...'`/`UESCAPE` + bit/hex-string literals; `POSITION`/`OVERLAY`/
`LIKE ESCAPE`/`SIMILAR TO` grammar; `regexp_count`/`regexp_like`/
`regexp_instr`/`regexp_substr` family; `regexp_replace` backreferences;
`regexp_matches(...,'g')` multi-match; `regexp_split_to_table`; bytea
trim/overlay edge cases), plus the newly-found small error-message-wording
gap (PG's `syntax error at or near "<token>"` vs goopg's generic "expected
';' or end of input (got <token>)" phrasing for trailing-garbage-after-
statement errors) — worth sizing as a possibly-contained, possibly
cross-cutting slice (could affect many other regress files' false-diff
counts the same way the LINE/caret series did).

Next step: (a) size the error-message-wording gap found this loop — check
whether goopg's "expected ';' or end of input (got %s)" phrasing is used
at ONE raise site or many (grep `internal/parser` for that exact string),
and whether swapping to PG's `syntax error at or near "%s"` shape for this
error class is a small, cross-cutting, contained fix (high-value if so,
same pattern as the LINE/caret series); (b) if that's not small, pick one
REFACTOR-tier bucket from the list above and size it as its own task
(recommend `POSITION`/`OVERLAY` grammar forms — likely smaller than the
regexp builtin family or the Unicode-escape literal work); (c) alternatively
consider PARKING M0134-0070 (same pattern as 0063-0069) since most remaining
gaps are REFACTOR-tier — re-check the fix_plan banner at loop start first.
Also worth noting: M0134-0071 (equivclass.sql, next task after 0070) was
statically sized by researcher THIS loop (no live diff run) and looks
REFACTOR-tier-gated behind `CREATE TYPE (INPUT=,OUTPUT=,LIKE=)` base/shell
type creation + `LANGUAGE INTERNAL` function dispatch (both currently
stubbed/absent) — get a live tester diff before committing to it as "next"
if 0070 gets parked; the static sizing carries real risk of being wrong
(researcher's own caveat).

Gates run this loop: `go build ./...` PASS; targeted
`go test ./internal/parser/... -run 'TestLexStringContinuation|TestBlockCommentAsOrdinaryWhitespaceUnaffected'`
PASS (7 subtests, implementer); full `go test ./internal/parser/...` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(coordinator, direct run, ~429s internal/initdb cold, rest cached);
`scripts/pg-regress-runner.sh --verbose strings` PASS-to-run (tester,
0/1 PASS as expected, 2461-line diff confirmed live); pre-commit pgbench
smoke via git hook — PASS (372/675/12723 TPS, 0 failed transactions).
`make ralph-state-guard` — to run before this status block.

Delegation: researcher round (strings.sql diff confirmation + block-comment
regression discovery + static M0134-0071 sizing) + implementer round
(tmp/ralph-handoffs/m0134-0070-quotecontinuation-blockcomment/, DONE, no
deviation — note: the implementer's Write tool refused to create report.md
under subagent policy; its round summary was relayed inline instead and is
captured here) + tester round (re-measure strings.sql diff post-fix). No
open handoff — all rounds converged in 1 pass each.

In-flight: none. Commit `ae4575a0` landed and pushed to
`regress-renumbering` (`6eca7538..ae4575a0`). No server left running.
