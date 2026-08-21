# M0134-0070 Round B: `U&'...'`/`U&"..."` Unicode-escape literals + `UESCAPE`

Status: accepted. Scope: `strings.sql` regress case, Round B of the two-round
Unicode-escape sizing pass (Round A, `E'...'` `\u`/`\U` validation, landed
2026-08-22, `internal/parser/lexer.go`).

## Problem

goopg's lexer has no recognition at all for the `U&'...'`/`U&"..."` Unicode-
escape string/identifier literal syntax (SQL standard, PG oracle
`postgres/src/backend/parser/scan.l` states `xus`/`xui` + `parser.c:253-527`
`base_yylex`/`check_uescapechar`/`str_udeescape`). A bare `u&'...'` in goopg
today lexes as ident `u`, operator `&`, then a plain string literal — wrong
token stream, not just wrong decoding.

## Design

Implemented entirely inside `internal/parser/lexer.go`'s scan loop — no new
parser grammar production, no new `TokenKind`. Rationale: goopg's lexer fully
materializes tokens up front (there is no streaming multi-token lookahead
layer to hook a `base_yylex`-style wrapper into, unlike PG's two-layer
scanner/parser split), and the UESCAPE clause must collapse into the literal's
already-decoded value before the token is ever placed in the slice — every
downstream `TokenStringLit`/`TokenQuotedIdent` consumer (~40 call sites) must
stay untouched.

1. **Dispatch** (`next()`'s `case isIdentStart(c):`, `lexer.go:184-197`): add
   a sibling branch to the existing `text == "e"` check —
   `text == "u" && l.src[l.pos] == '&' && (l.src[l.pos+1] == '\'' ||
   l.src[l.pos+1] == '"')`, zero whitespace tolerance between `u`, `&`, and
   the quote (mirrors PG's `xuistart`/`xusstart`, `scan.l:301-304`). Routes
   to new `lexUnicodeEscapeQuote(start int, quoteChar byte)`.
2. **Raw body scan**: reuse the existing doubling rules — `''`-doubling
   (`scanPlainQuoteInto`'s loop) for the `'` form, `""`-doubling (inline in
   `next()`'s `case c == '"':`) for the `"` form. No backslash interpretation
   during this pass (matches PG: `<xus>{xqinside}`/`<xui>{xdinside}` are
   *undecoded*, same body-scan as a plain string/ident, `scan.l:636` cf.
   `<xe>{xeinside}` at `:639` which IS decoded inline).
3. **String continuation**: `U&'...'` participates in `tryQuoteContinuation`
   (PG's `<xqs>` lookahead covers `xus` uniformly, `scan.l:574`); `U&"..."`
   does not (identifiers never continue, same as plain `"..."`).
4. **UESCAPE lookahead** (inside `lexUnicodeEscapeQuote`, after the closing
   quote): save `l.pos`, call `skipWhitespaceAndComments`, case-insensitive
   raw match on `uescape` bounded by `isIdentCont`, `skipWhitespaceAndComments`
   again, require a `'`, scan a single-char plain string body. On any
   mismatch at any step, restore `l.pos` to the saved point and fall back to
   the default escape char `\`. No `UESCAPE` keyword is registered in
   `token.go`'s `keywords` map — this is a lexer-local raw-text peek, not a
   parser production, so `uescape` remains an ordinary identifier everywhere
   else (matches PG's `UNRESERVED_KEYWORD` classification: it is not
   special outside this exact context).
5. **Escape-char validation** (`check_uescapechar`, `parser.c:352-362`):
   reject as a custom UESCAPE char any hex digit, `+`, `'`, `"`, or
   whitespace — `42601 "invalid Unicode escape character"`.
6. **Decode** (new `decodeUnicodeEscapes(raw string, escape byte) (string,
   error)`, operates on the captured raw body with its own cursor — not
   `l.pos`, since by this point the whole body is already captured):
   - `<esc><esc>` → literal escape char
   - `<esc>XXXX` (exactly 4 hex digits) → codepoint, via Round A's
     `scanUnicodeEscapeDigits(4)` reused verbatim (repointed at the local
     cursor, not `l.pos`)
   - `<esc>+XXXXXX` (literal `+` then exactly 6 hex digits) → codepoint, via
     `scanUnicodeEscapeDigits(6)` after consuming the `+`. Note: `U&` has
     **no** 8-hex `\U` form — that's `E'...'`-only. `U&`'s wide form is the
     `+`-prefixed 6-hex form.
   - anything else after `<esc>` → `22025 "invalid Unicode escape"`, hint
     `"Unicode escapes must be \XXXX or \+XXXXXX."` (differs from Round A's
     E-string hint text — do not share the literal string constant)
   - codepoint validity (`0 < c <= 0x10FFFF`) and surrogate pairing reuse
     Round A's `isUTF16SurrogateFirst`/`isUTF16SurrogateSecond`/
     `surrogatePairToCodepoint` free functions verbatim (no signature
     change — pure `rune` functions).
7. **`standard_conforming_strings=off` gate**: explicitly SKIPPED. goopg's
   lexer has no functioning off-mode today (the GUC is registered but
   nothing under `internal/parser` reads it — plain `'...'` is always
   standard-conforming), so PG's `ERRCODE_FEATURE_NOT_SUPPORTED` gate on
   `U&'...'` has no reachable state in goopg. Leave a one-line code comment
   at the dispatch site so a future off-mode implementation adds this check
   then; do not add dead code for it now.
8. **Token emission**: `U&'...'` → `Token{Kind: TokenStringLit, Value:
   <decoded>}`; `U&"..."` → `Token{Kind: TokenQuotedIdent, Value: <decoded,
   case preserved>}` — matches PG's own post-decode `cur_token = SCONST` /
   `IDENT` conversion (`parser.c:308-319`). No NAMEDATALEN truncation is
   applied here, matching plain `"..."`'s existing behavior (goopg has no
   lex-time identifier truncation; truncation, if any, happens downstream at
   the catalog boundary, uniformly for all `TokenQuotedIdent` sources).

## Out of scope / deferred

- `standard_conforming_strings=off` gate (see point 7) — dead code until
  goopg implements off-mode string lexing at all.
- Any UESCAPE-adjacent grammar ambiguity: none expected since `uescape` is
  never registered as a keyword.

## PG oracle citations

`postgres/src/backend/parser/scan.l:301-304,574,636-639,794-798`;
`postgres/src/backend/parser/parser.c:253-320` (`base_yylex` UIDENT/USCONST
case), `:352-362` (`check_uescapechar`), `:371-527` (`str_udeescape`);
`postgres/src/include/mb/pg_wchar.h:535-556` (codepoint/surrogate helpers,
shared with Round A).
