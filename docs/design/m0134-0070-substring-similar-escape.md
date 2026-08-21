# M0134-0070: `SUBSTRING(str SIMILAR pattern ESCAPE escape)` (SQL:1999 form)

Status: designed, not yet implemented. Scope: `strings.sql` regress case,
one bucket of the M0134-0070 sizing pass (`tmp/regress-diffs/strings.diff`,
941 lines at time of writing). This bucket: 13 statements, ~121 of 941 diff
lines (~13%, largest identifiable remaining bucket at the time this doc was
written).

## Problem

goopg's parser does not recognize the SQL:1999 three-operand SUBSTRING form
`SUBSTRING(string SIMILAR pattern ESCAPE escape)` at all —
`parseSubstringFuncCall` (`internal/parser/select.go`, `parseSubstringFuncCall`)
only implements the comma form and `FROM start [FOR count]`; it unconditionally
requires `FROM` after the subject expression, so every occurrence of the
SIMILAR form throws a parse error before execution.

PG desugars this form (`postgres/src/backend/parser/gram.y`, `substr_list`
production for `a_expr SIMILAR a_expr ESCAPE a_expr`) to a plain 3-arg call
`substring(text, text, text)`, which is a SQL-language wrapper
(`postgres/src/backend/catalog/system_functions.sql`):

```sql
CREATE OR REPLACE FUNCTION substring(text, text, text) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
    RETURN substring($1, similar_to_escape($2, $3));
```

`similar_to_escape(pattern, escape)` runs the *same* conversion routine as
`SIMILAR TO` — `similar_escape_internal` (`postgres/src/backend/utils/adt/
regexp.c:768-1063`) — with one extra piece of behavior that plain `SIMILAR TO`
never exercises: the **escape-double-quote part-separator convention**
(`regexp.c:920-953`). Outside of any bracket expression, an *escaped* `"`
(i.e. `<escape>"`, e.g. `#"` with `ESCAPE '#'`) acts as a part boundary. A
pattern with zero, one, or two such separators divides into
`part1 #" part2 #" part3`, and the converted POSIX regex becomes:

```
^(?:part1){1,1}?(part2){1,1}(?:part3)$
```

— `part1` non-greedy, `part2` wrapped in a **capturing group** and greedy,
`part3` implicitly non-greedy. With one separator, `part3` is empty; with
none, both `part1` and `part3` are empty (i.e. plain `Convert` output, modulo
the capturing group). A pattern with **more than two** separators raises
`ERRCODE_INVALID_USE_OF_ESCAPE_CHARACTER` (SQLSTATE `2200C`): "SQL regular
expression may not contain more than two escape-double-quote separators."

`goopg`'s existing `internal/utils/adt/similarto.Convert` explicitly does
**not** implement this — see its doc comment at
`internal/utils/adt/similarto/similarto.go:46-54`, which documents the gap
and cites this exact PG line range as the resume point. This is that resume.

The executor side needs **no changes**: `evalSubstrRegex`
(`internal/executor/expr.go`, the `textregexsubstr` port) already returns the
first capturing subexpression's match when the compiled pattern has one, and
NULL on no match — exactly the semantics the SUBSTRING-SIMILAR form needs
once handed a correctly-converted pattern with `part2` in a real capturing
group. The existing 2-arg `substring(str, pattern)` fold path
(`evalSubstr` → `evalSubstrRegex` when the second arg is a `KindString`)
already routes any 2-arg call with a string pattern into `evalSubstrRegex`.

## Explicitly out of scope: the SQL:1999 `FROM pattern FOR escape` overload

The diff hunk containing the 13 `SIMILAR ... ESCAPE` statements also contains
one `SUBSTRING('abcdefg' FROM 'a#"(b_d)#"%' FOR '#')` statement — this is a
*different*, older SQL:1999 spelling of the same semantics
(`postgres/src/backend/parser/gram.y` `substr_list`: `a_expr FROM a_expr FOR
a_expr` maps to the SAME 3-arg `substring(text,text,text)` call as `a_expr
SIMILAR a_expr ESCAPE a_expr` — PG's comment: "In SQL:1999 ... In SQL:2003,
the second variant was changed to `text SIMILAR pattern ESCAPE escape`...
since we still support the SQL:1999 version, we don't [map them
differently]"). PG picks which meaning applies (`start FOR count` vs.
`pattern FOR escape`) via **function overload resolution** on argument types
at plan time (`substring(text,int4,int4)` vs `substring(text,text,text)`),
not via distinct grammar productions — goopg's `parseSubstringFuncCall`
already desugars `FROM ... FOR ...` unconditionally to the 2/3-int-arg form,
so making this variant work needs overload dispatch on the resolved static
type of the FROM/FOR operands, a materially different (and separable)
mechanism from the SIMILAR/ESCAPE grammar addition above. **Deferred** — not
this slice; ledger row to be appended when this slice lands.

## Fix design

### 1. `internal/utils/adt/similarto/similarto.go` — new `ConvertSubstring`

Add a second exported entry point that emits the escape-double-quote logic.
To avoid forking the whole state machine (this package is a shared leaf per
its own doc comment — "no forked sibling logic"), refactor the existing
`Convert` body into an unexported `convert(pattern, escape string,
substringMode bool) (string, error)` and have both `Convert` (wraps,
discards the impossible-in-non-substring-mode error) and the new
`ConvertSubstring` call it with `substringMode` false/true respectively.

Substring-mode-only behavior to add inside the `afterEscape` case (mirrors
`regexp.c:920-953` exactly):
- Track `nquotes int` (starts at 0).
- When `substringMode && c == '"' && bracketDepth < 1`: on `nquotes == 0`
  emit `){1,1}?(`; on `nquotes == 1` emit `){1,1}(?:`; on `nquotes >= 2`
  return `ErrTooManyQuoteSeparators` (new sentinel error, SQLSTATE `2200C`,
  message "SQL regular expression may not contain more than two
  escape-double-quote separators" — no `Pos`/errposition, matching PG which
  raises this with no cursor location). Then `nquotes++`.
- Otherwise (normal escaped-char case): unchanged from `Convert` today.
- Non-substring mode (`Convert`) must behave byte-identically to today —
  verify with the existing `similarto_test.go` cases before/after the
  refactor (no behavior change expected for `substringMode=false`).

At the end (`convert`'s trailer, after the loop, replacing today's
unconditional `b.WriteString(")$")`): in substring mode, if `nquotes == 0`,
output is `^(?:` + body + `)$` unchanged (no capturing group — PG's own
comment: "with none, we act as though part1 and part3 are empty... both
behaviors fall out of omitting the relevant part separators", meaning the
WHOLE match becomes the implicit "part2" — re-check `regexp.c`'s trailer
handling, `regexp.c:1033-1063`, before assuming this is literally
`Convert`'s output; the safest implementation mirrors the C trailer byte for
byte rather than special-casing `nquotes==0`/`==1`/`==2` after the fact).

### 2. `internal/parser/select.go` — grammar + constant fold

In `parseSubstringFuncCall`, after the comma-form check and before the
mandatory `FROM`: if the next token is `KwSimilar`, branch into a new path
mirroring the boolean `SIMILAR TO` handling at `select.go:2076-2114`:
- `p.advance()` past `SIMILAR`.
- Parse `pattern` via `p.parseExpr()`.
- Require `KwEscape` (reuse `p.parseOptionalEscape()` — check its signature;
  PG's SUBSTRING form makes `ESCAPE escape` non-optional syntactically once
  `SIMILAR` is seen, unlike the boolean operator's optional clause, so this
  may need a small variant that requires the keyword rather than treating it
  as optional — confirm against `gram.y`'s `substr_list` production, which
  has a mandatory `ESCAPE` third operand for this alternative).
- `p.acceptSymbol(")")` to close.
- Constant-fold like `buildSimilarTo` (`select.go:3934-3970`): if `str`
  itself is also involved in the NULL-propagation (PG's wrapper is `STRICT`,
  so ANY of the three arguments being SQL NULL makes the whole call NULL —
  note this differs from `buildSimilarTo`'s left/pattern folding, since here
  `str` is also a call argument, not a separate operand) and pattern/escape
  are literals: validate escape via `similarto.ValidateEscape`, convert via
  the new `similarto.ConvertSubstring`, surfacing `ErrTooManyQuoteSeparators`
  as SQLSTATE `2200C` (`Pos: -1`, same no-errposition convention as
  `buildSimilarTo`'s 22025 case), and emit `&FuncCall{Name: "substring",
  Args: []Expr{str, <TypedStringLit text, converted>}}` — lands directly on
  the existing `evalSubstrRegex` path, zero executor changes needed.
- When pattern/escape aren't both literal: this path isn't exercised by
  strings.sql (all 13 cases use literal patterns/escapes) — a runtime
  fallback is out of scope for this slice; if hit, return a clear "not yet
  supported" parser error rather than silently mis-parsing (do not guess at
  a runtime node shape here — that's a separate slice if ever needed).

### Test coverage

New `internal/parser` and/or `internal/executor` test(s) pinned byte-exact
to the 13 corresponding `postgres/src/test/regress/expected/strings.out`
cases (same idiom as `to_hex_test.go`, `regexp_flags_test.go`, etc.) — cover
at minimum: zero separators, one separator, two separators, three separators
(error), NULL pattern, NULL escape, NULL string, multi-byte escape char if
exercised by the oracle file.

## Verification

- `go build ./...`
- `go test ./internal/parser/... ./internal/executor/... ./internal/utils/adt/similarto/...`
- `scripts/pg-regress-runner.sh --verbose strings` — expect diff to shrink by
  the SUBSTRING-SIMILAR-ESCAPE block (~121 lines); confirm no new failures
  introduced elsewhere in the same file.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- pre-commit pgbench smoke (mandatory git hook, runs automatically on commit)

## Sibling paths

- `similarto.Convert` (boolean `SIMILAR TO`) and the new `ConvertSubstring`
  share the same state machine by design (`convert(..., substringMode
  bool)`) — do not fork into two independent implementations.
- `buildSimilarTo` (constant-fold entry point for `~`/`!~` desugar) is the
  direct template for the new SUBSTRING-SIMILAR constant-fold path — same
  literal-check → validate-escape → convert → fold shape, adapted for the
  three-argument NULL-STRICT semantics.
