Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
regexp error-path HINT bucket. Code commit `1c6e8a86`, pushed.

Landed: `evalFuncCall` `case "regexp_replace"` `case 4:` flags branch
(`internal/executor/expr.go:12474`) gained a digit-first guard returning
`&ExecError{Code:"22023", Message:"invalid regular expression option: \"" +
flagsStr + "\"", Hint:"If you meant to use regexp_replace() with a start
parameter, cast the fourth argument to integer explicitly."}`. Mirrors PG oracle
`textregexreplace` (`postgres/src/backend/utils/adt/regexp.c:673-684`). Prints
the WHOLE `flagsStr` (so `"1z"` → `"1z"`, not `"1"`). Single-site — the shared
`pgRegexFlagsToGoModifiers` (8 callers, expr.go:7493) is untouched: PG hints
only in `textregexreplace`; `regexp_matches(...,'1')` etc. raise 22023 with no
hint via `parse_re_flags`. New test
`internal/executor/regexp_replace_hint_test.go` (4 tests incl. the sibling
"must not over-hint" guard).

Key symbols: `evalFuncCall` (expr.go, case "regexp_replace" case 4:),
`pgRegexFlagsToGoModifiers` (expr.go:7493), `ExecError.Hint` (expr.go:46-54).

Hypothesis/Findings: the "~7 lines" bucket was actually ONE missing `-HINT:`
line (`tmp/regress-diffs/strings.diff:193`); the ERROR line above already
matched. `strings.sql` diff 348→347 lines, grep-confirmed zero residual
regexp-HINT lines. No deferral — the fix is complete (whole-opt printing was
handled inline, no unimplemented PG behavior remains for this bucket).

Remaining buckets in the 347-line diff (all named in the fix_plan entry):
`standard_conforming_strings=off` lexing + `escape_string_warning`
(REFACTOR-tier), RE2-vs-ARE regex backrefs/zero-width/SIMILAR-ambiguity,
Unicode-escape error-message/DETAIL text (~16), `char(20) '...'`
typed-literal grammar (~14), SQL99 `SUBSTRING FROM..FOR` + missing CONTEXT
(~8), toasttest `pg_relation_size` hunk 2 (J2).

Next step: re-size the three remaining SMALL contained buckets (Unicode-escape
error-message/DETAIL, `char(20) '...'` typed-literal, SQL99 `SUBSTRING
FROM..FOR`+CONTEXT) via researcher to pick the next contained fix, or open
toasttest hunk 2 (J2). NOTE the "missing CONTEXT" is likely the known
cross-cutting no-CONTEXT-stack gap (see ledger: `2200C` error omits PG's
CONTEXT line) — do not assume it is a self-contained 8-line fix without sizing.

Gates run this loop: `go test ./internal/executor/` PASS (7.2s incl. new
tests); `go build ./...` PASS; pre-commit pgbench smoke PASS (0 failed, via
git hook); `make ralph-state-guard` clean.

Delegation: implementer (m0134-0070-regexp-hint) DONE — all gates PASS,
committed `1c6e8a86`. Researcher (m0134-0070-regexp-hint) DONE pre-slicing.

In-flight: none.
