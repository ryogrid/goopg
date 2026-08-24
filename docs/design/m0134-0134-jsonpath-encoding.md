# M0134-0134 — `jsonpath_encoding.sql`: escape lexing for `::jsonpath`

**Case:** `postgres/src/test/regress/sql/jsonpath_encoding.sql`
**Status at loop start:** `not-tried`
**Status after this loop:** `pass` (full pass, 100% parity)

## Sizing

`scripts/pg-regress-runner.sh --verbose jsonpath_encoding` at HEAD: 0/1 pass,
0% parity — every one of the 23 `::jsonpath` cast statements diverged. Two
distinct symptoms:

- Invalid escapes (`'"\u"'::jsonpath`, `'"\u00"'::jsonpath`,
  `'"\u000g"'::jsonpath`, orphan/misordered UTF-16 surrogate pairs) were
  silently ACCEPTED where PG raises `22601`/`22P02`.
- Valid escapes (`'"ꯍ"'::jsonpath`, `'"😄..."'::jsonpath`,
  `'"the Copyright © sign"'::jsonpath`) were returned with the escape
  spelling UNCHANGED where PG decodes them to the literal character.

Root cause: `evalCast` (`internal/executor/expr.go`) had no `"jsonpath"` case
at all — it fell to the function's final `return d, nil // pass-through for
unknown types` line. goopg has no jsonpath grammar parser of any kind (path
navigation, filters, operators all pass through unvalidated — pg_proc has
`jsonpath_in`/`jsonpath_out` seeded, `internal/catalog/codec.go` maps the OID,
but nothing ever consults PG's real jsonpath scanner/grammar
(`postgres/src/backend/utils/adt/jsonpath_scan.l` /
`jsonpath_gram.y`/`jsonpath.c`)). This is a large, multi-milestone gap (see
"Deferred" below); this loop closes only the narrow slice
`jsonpath_encoding.sql` actually exercises — the double-quoted-string escape
rules — because that slice happened to be a near-drop-in reuse of the
lexer M0134-0133's ledger row had already scoped out as future work for
`json_encoding.sql`.

## Fix

New file `internal/executor/jsonpath_encoding.go`:

- `scanJSONPathQuotedString` hand-ports `jsonpath_scan.l`'s string-escape
  states (`xq`/`xnq`/`xvq`) and their helper functions
  (`parseUnicode`/`addUnicode`/`addUnicodeChar`/`hexval`): `\b \f \n \r \t \v`,
  `\xXX`, `\uXXXX`, `\u{XXXXXX}`, and the `\X` → literal-`X` catch-all, plus
  UTF-16 surrogate-pair combination with PG's exact three error messages
  ("high surrogate must not follow a high surrogate" / "low surrogate must
  follow a high surrogate" / the zero-codepoint "cannot be converted to text"
  case).
- `scanJSONPathUnicodeToken` decodes one `\uXXXX`/`\u{...}` token and reports
  "invalid Unicode escape sequence at or near %q of jsonpath input" with the
  exact matched-prefix text flex's `{unicode}*{unicodefail}` rule would show
  (e.g. `\u000g` reports near-text `\u000`, not `\u000g` — the scan stops at
  the first non-hex byte).
- `rewriteJSONPathText` is the entry point: it scans the WHOLE jsonpath input
  once, and for every `"..."` span (both a quoted path value `"foo"` and a
  quoted key after `.`, `$."foo"` — PG's scanner uses the identical
  string-escape state for both, so one quote-scan pass over the raw text
  covers both without needing the surrounding grammar) it decodes escapes via
  `scanJSONPathQuotedString` then RE-ENCODES through `appendJSONBEscaped`
  (`jsonb_canonical.go` — the same `escape_json_char` rule set jsonb
  canonicalisation already implements). Everything outside a quoted string
  (`$`, `.`, `[`, `]`, `?()`, operators, numbers, …) passes through
  byte-for-byte unchanged.
- Wired into `evalCast`'s new `case "jsonpath":` arm
  (`internal/executor/expr.go`), following the exact `ee.Pos = pos` /
  `NewStringDatum` convention every other typed cast in that switch uses.

**Why re-encode, unlike `::json`/`::jsonb`'s M0134-0133 precedent:** `::json`
preserves the input's exact spelling verbatim because PG's `json` type has no
re-serialising printer (`json_in` just validates). `::jsonpath` is different:
PG round-trips it through its own printer (`jsonPathToCstring` /
`printJsonPathItem`, `jsonpath.c`), so `'"dollar $ character"'::jsonpath`
prints back as `"dollar $ character"` (decoded) while
`'"dollar \\u0024 character"'::jsonpath` (a literal backslash, not an escape)
prints back RE-escaped as `"dollar \\u0024 character"` — confirmed against
`postgres/src/test/regress/expected/jsonpath_encoding.out`.
`rewriteJSONPathText`'s decode-then-`appendJSONBEscaped` shape mirrors that
round trip exactly.

## Result

`scripts/pg-regress-runner.sh --verbose jsonpath_encoding`: 1/1 PASS, 100%
parity (0 diff lines). New test `TestJSONPathTypedLiteralEscapes`
(`internal/executor/jsonpath_encoding_test.go`, 15 subtests covering every
distinct case in the regress fixture) passes. CSV row flipped
`not-tried` → `pass` / `pass_required=yes` via `make regen-testport`.

## Deferred

The rest of PG's jsonpath grammar remains completely unvalidated by goopg —
`'$.a[*] ? (@ > 5)'::jsonpath` round-trips through `rewriteJSONPathText`'s
quote-only scan untouched, with no syntax checking of the path-navigation,
filter, or operator grammar at all, and no jsonpath EVALUATION (`@?`, `@@`,
`jsonb_path_query`, etc. — separate from the `#>`/`#>>` JSON path-GET
operators `evalJSONPathGet` already implements, which are unrelated to the
SQL/JSON `jsonpath` TYPE). This is tracked as a candidate for its own
milestone in `.ralph/fix_plan.md`'s M0134 standing-recommendation list
(item 1's GIN/GiST-adjacent "physical index integration" note already lists
jsonpath as unimplemented at the type level; this loop only fixes the
STRING-ESCAPE slice, not the grammar). Resume point: a real hand-port of
`jsonpath_scan.l`/`jsonpath_gram.y` — likely reusing the parser-combinator
style already used elsewhere in `internal/parser/` — would need to replace
`rewriteJSONPathText`'s quote-only scan with a full tokenizer + recursive-
descent (or table-driven) parser producing an AST, which `jsonpath_out`
re-prints and `jsonb_path_query`/`@?`/`@@` evaluate against a jsonb document.

Separately, `json_encoding.sql` (M0134-0133) is STILL `failed`: its remaining
gap — Go's `encoding/json` accepting unpaired/misordered UTF-16 surrogates
(emitting U+FFFD) instead of raising `22P02`, and not rejecting an unescaped
U+0000 in `->>`/jsonb-assembly text conversion — is now trivially
close-able by reusing `scanJSONPathQuotedString`'s escape logic (it already
implements PG-faithful surrogate-pair and NUL rejection) inside
`canonicalizeJSONB`/`validateJSONText`'s string-decode path instead of
delegating to `encoding/json`. Left as a separate follow-on (not folded into
this loop, to keep the M0134-0134 change bounded to the file it targets) —
ledgered below.
