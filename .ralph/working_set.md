Task: M-NIGHTLY `numerology — binary/octal/hex integer literals unsupported`
(banner: P0-E6 still `[!]`, items 3-6 and M0143 re-confirmed exhausted this
loop too — see below — so per the banner's "While P0-E6 waits" ordering,
dropped through to M-NIGHTLY). DONE + committed (pending this loop's commit).

Files: `internal/parser/adapter.go` (`mapToken`'s `TokenIntLit` case),
`internal/parser/select.go` (new shared helper `intLiteralOverflowText`,
`parseIntLiteralExpr` simplified to use it), `internal/parser/lexer.go`
(digit-led numeric-literal dot-commit logic rewritten), `.ralph/fix_plan.md`
(task checked off with full root-cause note; parent
`testport/TestPort_RegressSuite` task annotated — now 50/0/183, ALL 4
subtests from that FAILed-run item are fixed).

Key symbols: `mapToken` (`internal/parser/adapter.go:505`),
`parseIntLiteral`/`parseIntLiteralExpr`/`intLiteralOverflowText`
(`internal/parser/select.go:5175-5227`), the digit-branch of `(*lexer).next`
(`internal/parser/lexer.go:265-388`).

Findings: TWO independent bugs were hiding behind one filed task.
(1) The filed task's premise was wrong — the LEXER already fully tokenized
`0b`/`0o`/`0x` (M0097-0003). The real gap: `adapter.go`'s `mapToken` (goyacc
path, what a routed `SELECT` actually uses) always called
`strconv.ParseInt(..., 10, 64)` on `TokenIntLit` text, ignoring the prefix —
`select.go`'s `parseIntLiteral` (legacy hand-written path) already knew the
base. A sibling-path drift (`pattern_sibling_paths_must_agree`): the two
paths silently diverged and only the unrouted one was correct. Fixed by
having `mapToken` call `parseIntLiteral` directly, and factoring the
overflow-to-decimal-text logic (needed because the FCONST/NumericConst path
only understands base 10) into a shared `intLiteralOverflowText` used by
both `mapToken` and `parseIntLiteralExpr`.
(2) Fixing (1) exposed a SECOND bug: `0.a` / `1_000._5` produced `syntax
error at or near "."` instead of PG's `trailing junk after numeric literal`.
The lexer's dot-commit heuristic assumed a digit-led token could be
upstream's qualified-name form (`a.b`) — impossible, since qualified names
start with an identifier, never a digit, and PG's own `scan.l` has no such
carve-out for `{decinteger}'.'`. Fixed: a digit-led token's dot always
starts a numeric literal (except `..` range syntax), and the fractional
loop no longer swallows a *leading* underscore as a fraction digit (PG's
`{decinteger}` requires the fraction to START with a digit).
Both fixes verified byte-for-byte against
`postgres/src/test/regress/expected/numerology.out` on a throwaway scratch
cluster (ports 5533/5534, `/tmp/numerology-scratch-data` — deleted before
finishing, never the shared/reference clusters) across all magnitude tiers
(int4-fits, int4-overflow/int8-fits, int8-overflow) and both dot-junk cases.
Banner re-scan (recorded so the next loop doesn't redo it): M0143 still has
zero selectable non-TPC-H tasks (only `M0143-0007b`, blocked on an owner
decision re: reversing bpchar trimmed-storage). Items 3-6's recon-only
candidates are still implementation-only/blocked per the prior loop's scan
(nothing changed this loop that would unblock them — this loop touched only
`internal/parser`).

Next step: next loop re-reads the banner fresh. P0-E6 still owner-run
(`bench/tpch/runtime_goopg/data.HOLD` still present). If still `[!]`,
re-check `ci/logs/action-items.md` for anything new (last checked run
20260918-010720, already filed+closed as a stale nightly-checkout race —
see fix_plan), then pick the next open M-NIGHTLY item. With `numerology`
and `limit` both now fixed, `testport/TestPort_RegressSuite` shows 50/0/183
— check the fix_plan's "M-NIGHTLY open items" section top-to-bottom for the
next one (several `testport/TestPort_Isolation*` items are open, e.g.
`TestPort_IsolationFkContention`/`TestPort_IsolationFkDeadlock`/
`TestPort_UpdateLockedTuple` from the 20260917-004357 run).

Gates run: `go build ./...` clean (twice, before/after the second fix).
`go test ./internal/parser/...` PASS (goldens unchanged — no golden diff,
confirming no pinned AST shape moved). `go test -v -run
'^TestPort_RegressSuite$/^numerology$' ./internal/testport/` PASS. Full
`TestPort_RegressSuite`: 50 PASS / 0 FAIL / 183 SKIP (was 48/1/183 before
this loop). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (all in-scope packages, including `internal/executor` explicitly).
`make ralph-state-guard` PASS (self-repaired a stale completed-marker from
the prior loop's clean exit, same as last loop — unrelated to this loop's
work). No TPC-H/sf025 gate needed — pure lexer/parser change, no
`internal/executor`/`internal/optimizer` row-count path touched (and the
`internal/executor` unit package itself was re-run green above).

In-flight: none.
