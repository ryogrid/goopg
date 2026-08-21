Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed Round B
of the 2-round Unicode-escape sizing pass (`U&'...'`/`U&"..."` + `UESCAPE`
literal syntax), committed/pushed (`edfa14de`). strings.sql itself remains
`failed` overall (diff now 1804 lines, down from 1848 at loop start).

This loop: delegated a researcher round to confirm goopg's exact lexer
insertion point, UESCAPE-lookahead approach, `standard_conforming_strings`
gate status, and PG oracle semantics for `U&` escape forms (4-hex `\XXXX` /
6-hex `\+XXXXXX`, no 8-hex form — that stays `E'...'`-only) before briefing —
per the prior loop's own "worth a short researcher round first" note. Wrote
a new design doc `docs/design/m0134-0070-uescape-unicode-literals.md`
(indexed in `docs/design/README.md`) documenting the lexer-only approach (no
new parser production, no new `TokenKind` — reuses `TokenStringLit`/
`TokenQuotedIdent`) and why (goopg's lexer fully materializes tokens up
front, unlike PG's two-layer scanner/parser split, so UESCAPE must collapse
into the decoded value before token emission). Delegated one implementer
round; converged in round 1 (within the 3-round cap).

Landed: `internal/parser/lexer.go` gained `lexUnicodeEscapeQuote` (dispatch
sibling to the existing `E'...'` branch in `next()`'s `isIdentStart` case),
`decodeUnicodeEscapes` (local-cursor decoder mirroring PG's `str_udeescape`,
reuses Round A's surrogate-pair helpers verbatim), a shared
`scanUnicodeEscapeDigitsAt` free function factored out of Round A's
`l.pos`-based `scanUnicodeEscapeDigits` (now a thin wrapper — both Round A's
and Round B's call sites share one implementation), and `isValidUescapeChar`
mirroring `check_uescapechar`. `UESCAPE` is a lexer-local raw-text peek
(save/restore `l.pos` on mismatch) — NOT registered in `token.go`'s
`keywords` map, so it stays an ordinary identifier everywhere else, matching
PG's `UNRESERVED_KEYWORD` classification. New test
`internal/parser/unicode_escape_literal_test.go` (14 subtests: default/custom
escape char, 4-hex/6-hex-wide forms, surrogate pairing success/failure,
codepoint-range rejection, malformed-escape 22025 with U&-specific hint text
distinct from Round A's, 4 rejected UESCAPE chars, string+identifier forms,
continuation interaction). Handoff dir:
`tmp/ralph-handoffs/m0134-0070-uescape-literals/` (brief.md +
report.md, report.md written by this session since the implementer role is
blocked from writing report files directly) — scratch, not system of record.

Key symbols: `lexUnicodeEscapeQuote`, `decodeUnicodeEscapes`,
`scanUnicodeEscapeDigitsAt`, `isValidUescapeChar` (all
`internal/parser/lexer.go`).

Deferred (ledger row 2026-08-22, M0134-0070 U&/UESCAPE entry):
`standard_conforming_strings=off` gate (goopg has no functioning off-mode
string lexing anywhere — dead code until that lands generally) and PG's
dedicated "UESCAPE must be followed by a simple string literal" diagnostic
(goopg falls back to a generic syntax error on a malformed UESCAPE clause).
Neither is exercised by strings.sql's own fixture lines.

Next step: pick the next bucket for strings.sql (1804-line diff remaining).
Best candidate: decompose the dominant `regexp_*` family
(`regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`,
`regexp_replace` backreferences, `regexp_matches(...,'g')` multi-match,
`regexp_split_to_table`) — largest remaining bucket by line count (~1215 of
1688 lines per the 2026-08-21 researcher sizing pass, likely still dominant
now), still needs its own sizing/decomposition pass first, do NOT brief as
one slice — spawn a researcher round to split it into per-function slices
before briefing any of them. Smaller remaining buckets (not yet re-measured
post-Round-B): `ascii()`/`bit_count()` spacing (~4 lines, root cause may be
systemic/shared with other regress diffs — do not fix blind here without
checking), `chr(0)`/bytea-trim NUL handling (~4 lines). Re-check the
fix_plan banner at loop start first (M-NIGHTLY has no open selectable items
as of last check; M0134 stays next-priority).

Gates run this loop: `go build ./...` PASS; `go test ./internal/parser/...`
PASS (both this session and implementer, cached-fresh, 14/14 new subtests
verified individually with `-v`); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (re-run by this session, not just
trusted from the implementer — full unit suite green including the 420s
`internal/initdb` package); `GOOPG_CG_UNIT=... scripts/goopg-test-run.sh
scripts/pg-regress-runner.sh --verbose strings` (implementer, cgroup-capped)
— diff 1848→1804, all core U&/UESCAPE decode paths byte-identical to PG
(modulo the two ledgered gaps); pre-commit pgbench smoke via git hook — PASS
(378/701/12955 TPS, 0 failed transactions). `make ralph-state-guard` — not
yet run this loop, will run immediately before the status block per the
protocol (see below).

Delegation: researcher round (confirming lexer insertion point + PG U&
semantics) + implementer round
(tmp/ralph-handoffs/m0134-0070-uescape-literals/, DONE in round 1,
converged). No SendMessage follow-up needed. No open handoff.

In-flight: none. Commit `edfa14de` landed and pushed to `regress-renumbering`
(`9fe8f673..edfa14de`). No server left running.
