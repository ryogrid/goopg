# M0134-0070 Round C: `regexp_*` family — shared flags translator + `regexp_matches` multi-flag fix

Status: accepted. Scope: `strings.sql` regress case, Round C of the M0134-0070
sizing pass (Round A `E'...'` Unicode-escape validation, Round B `U&'...'`/
`UESCAPE`, both landed 2026-08-22). This round only — see "Follow-on slices"
below for the rest of the `regexp_*` bucket (~815 of 1804 remaining diff
lines), which stay separate slices by design.

## Problem

The 2026-08-22 sizing pass (`tmp/regress-diffs/strings.diff` at 1804 lines)
found the `regexp_*` family is not one gap but four independent root causes.
This slice targets the smallest, highest-leverage one: **PG regex flag
translation is incomplete and duplicated**. `internal/executor/expr.go`'s
`evalRegexpMatchesSRF` (and its `regexp_split_to_array`/`regexp_replace`
siblings) only recognize `i` (case-insensitive) and `g` (global) out of PG's
full flag set `b/c/e/i/m/n/p/q/s/t/w/x` (`postgres/src/backend/utils/adt/
regexp.c:parse_re_flags`, `:117-200`). In particular `m`/`s`/`n` (line-anchor
modes) are silently dropped — a `regexp_matches(text, pattern, 'mg')` call
anchors `^`/`$` to the whole string instead of per-line, returning 1 row
where PG returns N. Unknown flag characters (e.g. `'z'`) are also silently
accepted instead of raising `22023 invalid regular expression option`.

PG's flag semantics (`regexp.c:319-380 RE_compile_and_cache` /
`parse_re_flags`):
- `g` — global (goopg-side loop semantics, unaffected by this slice)
- `i` — case-insensitive → Go `(?i)`
- `m`/`n` — newline-sensitive matching: `^`/`$` match at each line boundary,
  `.` does not match `\n` → Go `(?m)` covers the `^`/`$` half; Go's `.` never
  matches `\n` unless `(?s)` is given, which already matches PG's default (PG
  `.` also excludes `\n` under `n`), so `m`/`n` both map to Go `(?m)` for our
  purposes (PG distinguishes `m` full newline-sensitive vs `n` partial, but
  neither goopg nor this test exercises the distinction beyond `^`/`$`
  anchoring — documented as a known simplification below, not silently
  dropped).
- `s` — non-newline-sensitive (PG default without `n`/`m`) — no-op relative
  to Go's default; **do not** map to Go's `(?s)` (that flag means something
  different in Go: dot matches newline). This is a naming collision between
  PG's `s` flag and Go's `(?s)` modifier — the translator must not conflate
  them.
- `p` — partial newline-sensitive (like `n` but `.` still matches `\n`) — map
  to Go `(?m)` only, not `(?s)`.
- `w` — inverse partial newline-sensitive — treat as `(?m)` for this slice
  (same reasoning as `p`).
- unknown char → `22023`.

## Design

1. New shared helper in `internal/executor/expr.go` (near the existing
   ad-hoc `strings.Contains(flags, "i")` call sites):
   `func pgRegexFlagsToGoModifiers(flags string) (goFlags string, global bool, err error)`.
   Iterates each rune, maps `i`→`i`, `m`/`n`/`p`/`w`→`m` (deduped), `s`/`c`/
   `e`/`b`/`t`/`q`/`x`→ no-op (accepted, not yet meaningfully implemented —
   NOT flagged unknown, matching PG which accepts them as valid ARE-mode
   selectors), `g`→ sets `global=true` (not folded into `goFlags`, since Go's
   `regexp` has no inline "global" modifier — call sites already loop for
   `g`), anything else → `22023 invalid regular expression option "<ch>"`
   (`errors.New` wrapped via the existing goopg pgerror helper used elsewhere
   in this file — grep `pgerrcode.InvalidRegularExpression` or equivalent for
   the exact call convention already in use for regex compile errors in this
   file, reuse it verbatim).
2. Replace the three duplicated inline flag checks with calls to this helper:
   - `evalRegexpMatchesSRF` (regexp_matches SRF path, `expr.go:7346`)
   - `regexp_split_to_array` case (`expr.go:12820`)
   - `regexp_replace` case's flags handling (`expr.go:11850` region) — flags
     only; the separate start/N extended-arg bug in this same case is
     EXPLICITLY OUT OF SCOPE for this slice (Round D, see below) — do not
     touch arg-count dispatch here.
3. Prepend `goFlags` (e.g. `"(?im)"`) to the pattern before
   `regexp.Compile`/`regexp.CompilePOSIX` (whichever this file already uses —
   confirm at the call site rather than assuming) instead of the current
   ad-hoc `"(?i)"` string literal.
4. Any existing pattern-translation step (`pgPatternToGoRE2`, `expr.go:2375`)
   runs first as today; the flag prefix is prepended to ITS output, not to
   the raw PG pattern — order matters if that function already emits its own
   leading `(?...)` group.

## Acceptance criteria

- `regexp_matches('...multi-line text...', '^...$', 'mg')` returns the same
  row count/content as PG for the `strings.sql` fixture lines (diff lines
  852-927 close).
- `regexp_matches(..., 'z')` (or any single unrecognized flag char) raises
  `22023`, matching PG's error text shape (message wording may still differ
  slightly — that residual is acceptable for this slice; row-shape/error-code
  parity is the bar, not byte-identical message text, per Hard-won Rule #1's
  "row-count" framing — DETAIL/HINT wording gaps get their own ledger row if
  still present after this slice).
- `regexp_split_to_array` correctly rejects `'z'` and reports "does not
  support 'global' option" for `'g'` on split, per the diff's PG-oracle lines.
- No regression in existing `regexp_matches`/`regexp_replace`/
  `regexp_split_to_array` unit tests (`internal/executor/*_test.go` — grep
  for existing coverage before adding new cases; add targeted cases for the
  `m`/`s`/`n`/`p`/`w`/unknown-flag paths since none currently exist).

## Follow-on slices (Round C is this doc; do NOT fold these in)

Per the 2026-08-22 sizing researcher round, in priority order:
- **Round D** — `regexp_split_to_table` SRF wiring (mechanical: mirror
  `planFromRegexpMatches`/`fromRegexpMatchesOp` in
  `internal/optimizer/planner.go:4639`, reusing `evalRegexpMatchesSRF`). ~129
  diff lines, no new engine work.
- **Round E** — `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`
  (all four currently `function ... does not exist`, ~426 diff lines
  combined). Thin wrappers over the same compiled-pattern + flags machinery
  this round builds; do `regexp_instr` first (193 lines, exercises
  subexpr/endoption logic reusable by `regexp_substr`).
- **Round F** — `regexp_replace` extended 6-arg form (`text, pattern,
  replacement, start, N, flags`) — currently misreads `start` as `flags` for
  any call past the 4-arg form. ~81 diff lines.
- **Round G** — `regexp_replace` backreference translator generalization
  (`\1`/`\2`-only → any `\N`/`\&`). ~35 diff lines. The `(.)\1`
  **pattern-side** backreference form is a genuine Go `regexp`(RE2)-vs-PG-ARE
  engine capability gap (RE2 forbids backreferences in the pattern
  structurally) — defer that specific sub-case to the ledger rather than
  blocking Round G; do not attempt a regex-engine swap for this.

PG oracle for all rounds: `postgres/src/backend/utils/adt/regexp.c`
(`regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`/`regexp_replace`/
`regexp_matches`/`regexp_split_to_array`/`regexp_split_to_table`,
`parse_re_flags`).
