# M0134-0070 Round C: `regexp_*` family — shared flags translator + `regexp_matches` multi-flag fix

Status: Round C accepted/landed; Round D accepted/landed; Round E
accepted/landed (see addenda at end of file). Scope: `strings.sql` regress
case, Round C of the M0134-0070
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

## Round D (landed 2026-08-22): `regexp_split_to_table` FROM-clause SRF wiring

`regexp_split_to_table(...)` was catalog-visible (`pg_proc` OIDs 2765/2766,
`RetSet: true`) but completely unwired as a table-valued function: any
`FROM regexp_split_to_table(...)` fell through `planTableFuncRangeVar`
(`internal/optimizer/planner.go`) into the generic user-routine lookup and
raised `0A000 "table-valued function \"regexp_split_to_table\" not
supported"`.

Fix mirrors the existing `regexp_matches` FROM-clause SRF wiring end to end,
with three deliberate divergences (split returns plain `text` rows not
`text[]`; default column alias is `"regexp_split_to_table"`; and — since PG's
`regexp_split_to_table`/`regexp_split_to_array` share `setup_regexp_matches`
and are both stricter than `regexp_matches` — an invalid pattern raises
`2201B` rather than `evalRegexpMatchesSRF`'s permissive empty-rows behavior):

- `internal/optimizer/plan.go` — new `FromRegexpSplitToTable` plan node
  (same `StringExpr`/`PatternExpr`/`FlagsExpr` shape as `FromRegexpMatches`,
  `text`-typed single output column).
- `internal/optimizer/planner.go` — dispatch branch in
  `planTableFuncRangeVar` + new `planFromRegexpSplitToTable` (arg validation,
  lateral-aware resolution, `WITH ORDINALITY` wrapping — all mirrored from
  `planFromRegexpMatches`).
- `internal/executor/executor.go` — dispatch case for
  `*optimizer.FromRegexpSplitToTable`.
- `internal/executor/operators_from_regexp_split_to_table.go` (new) —
  `fromRegexpSplitToTableOp`, mirrors `fromRegexpMatchesOp`'s
  Open/Next/Close skeleton.
- `internal/executor/expr.go` — new `evalRegexpSplitToTable`: reuses the
  shared `pgRegexFlagsToGoModifiers` (Round C), rejects explicit `'g'` with
  `22023` (message parameterized as `regexp_split_to_table()`, not copy-pasted
  from the `regexp_split_to_array()` sibling), raises `2201B` on invalid
  pattern, splits via `re.Split(s, -1)` (same algorithm the existing scalar
  `regexp_split_to_array` case already uses — PG's `build_regexp_split_result`
  confirms N matches -> N+1 rows, the same semantics).

New test: `internal/executor/from_regexp_split_to_table_test.go`
(`TestFromRegexpSplitToTable`), mirroring
`from_regexp_matches_test.go`'s `TestFromRegexpMatches` structure.

PG-regress verification: `strings.sql`'s
`regexp_split_to_table('the quick brown fox jumps over the lazy dog',
$re$\s+$re$)` case is now byte-identical context in
`tmp/regress-diffs/strings.diff` (zero `+`/`-` markers) — this function's
diff contribution is fully closed. `strings.sql` overall stays `failed`
(unrelated pre-existing gaps: `standard_conforming_strings=off` lexing,
`chr(0)`, bytea trim/LIKE, `char(N)` literal concat syntax — see ledger).

Deferred (not this round): `string_to_table` — same table-valued-SRF shape,
literal-delimiter instead of regex; natural next follow-on using the same
wiring pattern. Also unexplored: `SELECT regexp_split_to_table(...)` in
SELECT-list (non-FROM) position — the `strings.sql` fixture only exercises
the FROM-clause form, so no evidence either way that SELECT-list position is
broken or fixed by this round.

## Round E (landed 2026-08-22): `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`

The four Oracle-style regexp builtins (`pg_proc.dat` oids 6254-6269) were
entirely unimplemented — `SELECT regexp_instr(...)` etc. raised `42883
function ... does not exist`. This round wires all four as ordinary scalar
`FuncCall` case arms in `internal/executor/expr.go` (no parser change; PG
resolves the optional-arg overload sets purely by arg count, and goopg's
existing convention — see `regexp_split_to_array` — already does the same
arg-count-keyed defaulting at eval time).

- `regexp_count(string, pattern [, start [, flags]])` →
  `regexp.c:1138 regexp_count`. `start<=0` → `22023`. Counts non-overlapping
  matches in the search window via `re.FindAllStringIndex`.
- `regexp_like(string, pattern [, flags])` → `regexp.c:1329 regexp_like`.
  Direct compile+execute, no start/N, bool result.
- `regexp_instr(string, pattern [, start [, N [, endoption [, flags
  [, subexpr]]]]])` → `regexp.c:1198 regexp_instr`, int4 position result.
- `regexp_substr(string, pattern [, start [, N [, flags [, subexpr]]]])` →
  `regexp.c:1904 regexp_substr` — same start/N/subexpr validation as
  `regexp_instr` but flags/subexpr are one arg slot earlier (no `endoption`).
  text result, NULL (not 0) for the "not found" cases.

All four validate numeric args in PG's exact arg-position order (start, N,
endoption [instr only], subexpr — all `22023 invalid value for parameter
"<name>": <n>`, no `errposition`/`Pos` per `regexp.c`'s own bare `ereport`),
then run flags through the shared `pgRegexFlagsToGoModifiers` (Round C) for
`i`/`m`/`n`/`p`/`w` translation and unknown-char rejection, then reject an
explicit `'g'` with `22023` and a per-function message
(`"<name>() does not support the \"global\" option"` — NOT copy-pasted from
`regexp_split_to_array`'s template; each case arm substitutes its own name).

**Shared instr/substr core** (`regexpInstrSubstrLocate` in `expr.go`): both
functions need identical start/N/subexpr match-selection arithmetic (PG's
own C source literally near-duplicates them), so a single helper locates the
n-th non-overlapping match in the search window and selects the requested
capture group's byte-offset span, returning `ok=false` (not an error) when N
exceeds the match count, subexpr exceeds the capture-group count, or the
selected group didn't participate — `regexp_instr` maps that to `0`,
`regexp_substr` to SQL `NULL`, exactly matching `regexp.c:1267-1273` /
`:1965-1971`. This keeps the two "pos" computations from drifting apart, per
the brief's explicit ask.

**start>1 char-position arithmetic**: mirrors the existing
`case "strpos", "position"` byte→char idiom (`runes := []rune(s[:idx]);
pos := len(runes)+1`) and `evalSubstr`'s rune-window slicing — the search
window is `string([]rune(s)[start-1:])`, matches are found within that
window, and `regexpWindowCharPos` adds `(start-1)` runes back onto the
reported byte offset. This is a documented simplification vs. PG's true
start-search-offset-into-the-original-string approach: a `^`-anchored
pattern re-anchors at the window start here rather than the true string
start (untested by `strings.sql`, so not a landed-behavior regression, but a
known gap for a hypothetical `regexp_instr('ab^c...)` case). Verified byte-
identical against the fixture's idiom-correctness check:
`regexp_instr('abcabcabc','a.c',2)` → `4`.

**Newline/whitespace flag semantics — local, not shared.** Two of
`strings.sql`'s `regexp_like` fixture lines
(`regexp_like('a'||CHR(10)||'d','a.d','s')` → `t`;
`regexp_like('abc',' a . c ','x')` → `t`) require behavior
`pgRegexFlagsToGoModifiers` does not (and, per its own pinned unit test
`TestPgRegexFlagsToGoModifiers`'s `"s-is-noop-not-go-dotall"` case, must
not) provide: PG's *default* regex mode (and explicit `'s'`) has `.` match a
newline (`regexp.c` `parse_re_flags`: `cflags` default to `REG_ADVANCED`
only, i.e. `REG_NEWLINE` unset), the opposite of Go RE2's default — and `'x'`
(`REG_EXPANDED`) makes unescaped whitespace/`#`-comments in the pattern
insignificant, which Go RE2 has no flag for at all. Rather than change the
shared translator (whose "`'s'` is a no-op" contract is relied on by every
other `regexp_*` call site and locked by an existing test — changing it was
out of this round's scope per the brief and risked an untested blast radius
across `regexp_match`/`regexp_matches`/`regexp_replace`/`regexp_split_to_*`/
the `~` operators), this round adds two small helpers used **only** by these
four new case arms: `regexpLocalDotMatchesNewline` (computes whether a local
`(?s)` prefix is needed, from `'n'/'m'/'p'` vs `'s'`) and
`regexpApplyExpandedWhitespace` (strips insignificant whitespace/comments
when `'x'` is present — a simplified, non-bracket-aware approximation of PG's
ARE tokenizer, sufficient for this fixture). Both are folded together in
`regexpCompilePattern`, the single pattern-compilation entry point all four
new case arms use.

New test: `internal/executor/regexp_instr_family_test.go`
(`TestRegexpInstrFamily`) — happy paths for all four, the `start>1` idiom
check, N-th match selection, `endoption` 0 vs 1, `subexpr` 0/whole-match vs
>0/capture-group vs non-participating-group (0 for instr, NULL for substr),
all 6 named PG error cases with exact SQLSTATE + message text, and `'g'`
rejection for all four with per-function message text.

`internal/optimizer/planner.go` `exprType`: `regexp_instr`/`regexp_count` →
`int4`, `regexp_like` → `bool`, `regexp_substr` → `text` (`pg_proc.dat`
`prorettype` per oid).

Out of scope for this round (unchanged): `regexp_replace` backreferences,
`regexp_matches(...,'g')` multi-match FROM-clause/SELECT-list SRF wiring, and
any further `'x'`/`'s'` correctness work on the shared
`pgRegexFlagsToGoModifiers` translator or its other call sites.
