# M0134-0092 — `bit.sql`: `B'...'`/`X'...'` literal syntax + bit(n)/varbit(n) column coercion

## Status: PARKED (case still `failed`)

## Summary

`postgres/src/test/regress/sql/bit.sql` sized live against the PG 18.3
oracle (`scripts/pg-regress-runner.sh --verbose bit`) at a 1054-line diff,
0% parity. The file's very first blocker was structural: goopg's parser had
**no support at all** for the SQL99 bit-string literal syntax (`B'1010'`,
`X'A'`) — every `INSERT INTO bit_table VALUES (B'10')` raised a bare parser
syntax error, so nothing past line 1 of the file could even be attempted.

This slice fixes that blocker plus a second, independent gap it exposed:
bit(n)/varbit(n) COLUMNS had no length/digit coercion at all, so once the
literal syntax parsed, values of the wrong length silently stored instead of
raising PG's length-mismatch errors. Both fixes are landed and verified live;
the diff shrank 1054→581 lines. The remainder — the entire bit/varbit
bitwise-operator family (`~ & | # << >>`), `||` concat, `length()`/
`SUBSTRING()` over bit, COPY hex-format decode, and
`pg_input_is_valid`/`pg_input_error_info` routing — is each an independently
large, separately-scoped subsystem; see the deferral ledger row for the full
five-plus-item breakdown. The case stays `failed`/PARKED per the established
M0134 pattern (cf. M0134-0081..0091).

## Fix 1 — lexer/parser: `B'...'`/`X'...'` literal syntax

PG's `scan.l` recognizes `xbstart`/`xhstart` (`[bB]'`/`[xX]'`) as separate
lexer start-conditions from a plain `'...'` string, producing `BCONST`/
`XCONST` tokens that `gram.y`'s `AexprConst` rule turns into an `A_Const`
with `val.type = T_BitString` via `makeBitStringConst`
(`postgres/src/backend/parser/gram.y:17366-17377`). Critically,
`parse_node.c`'s `T_BitString` case (`transformExprRecurse`, around line 452)
calls `bit_in()` **eagerly**, unconditionally, right there while building the
constant — not deferred to assignment/coercion time. That means an invalid
digit (`SELECT b' 0'`) errors immediately with `ERRCODE_INVALID_TEXT_
REPRESENTATION` (22P02) and an `errposition` at the literal's start,
regardless of what — if anything — the literal is later assigned to.

goopg mirrors this shape:

- `internal/parser/token.go`: new `TokenBitStringLit` kind. `Token.Value`
  carries a leading marker byte (`'b'` or `'x'`) ahead of the raw source
  digits — the same convention `scan.l` itself uses internally
  (`addlitchar('b'|'x', ...)`) — so no second `Token` field is needed.
- `internal/parser/lexer.go`: `lexBitOrHexString`, structured exactly like
  the existing `lexEscapeString` (`E'...'`) prefix-detection arm in `next()`
  — a single-character identifier (`b`/`x`, case-insensitive) immediately
  followed by a quote with zero whitespace tolerance. Body scanning reuses
  `scanPlainQuoteInto`/`tryQuoteContinuation`, the same helpers plain string
  literals use for `''`-doubling and newline-gap quote continuation — PG's
  own `xbinside`/`xhinside` rules accept any character in the body
  (validation happens later, not in the lexer).
- `internal/parser/expr.go`: `decodeBitStringLit`, called from
  `parsePrimary` (`select.go`) on a `TokenBitStringLit`. Validates the digit
  set (binary: `0`/`1` only; hex: `0-9A-Fa-f`) and, for hex, expands each
  nibble to 4 bits MSB-first (matching `varbit.c bit_in`'s hex branch,
  `postgres/src/backend/utils/adt/varbit.c:230-270`). An invalid digit
  returns a `*SyntaxError{Code: "22P02", Raw: true, Pos: <literal start>}`
  with PG's exact wording (`"%s" is not a valid binary/hexadecimal digit`).

### Simplification (ledgered): reused as a plain `*StringConst`

Rather than introduce a dedicated bit-typed AST/IR node (which would need
threading through `internal/optimizer/planner.go`'s `resolveExpr`/
`resolveExprAfterAggregate` family at every call site — the same
sibling-path fan-out the M0134-0087 ledger row hit for `StringConst`/
`NumericConst`/etc.), `decodeBitStringLit` returns the decoded canonical
binary-digit text wrapped in a plain `*StringConst`. This was a deliberate,
verified-safe shortcut: goopg's existing `varchar(n)`/`char(n)` string→column
coercion path was already proven to reproduce PG's length-mismatch errors
byte-for-byte for a plain quoted-string literal, so routing the decoded bit
literal through the *same* path (fix 2, below) gets `INSERT`/`SELECT`
round-trips right for free, with no new plumbing.

The cost: the literal ends up typed `UNKNOWN`/text instead of `BITOID`. An
untyped bare `SELECT b'1010'` (no target column) and bit-typed operator
dispatch without an explicit cast are not PG-faithful yet. `bit.sql`'s own
literal-syntax test block doesn't exercise that gap — it only assigns
literals into declared `bit(n)`/`varbit(n)` columns — so this was safe to
defer. Tracked in the deferral ledger, bucket (A).

## Fix 2 — `bit(n)`/`bit varying(n)` column coercion

Even with the literal parsing, the underlying gap was worse than expected:
**plain quoted-string** assignment into a `bit(n)` column had no length or
digit validation either — confirmed by reproducing with `'10'` (no `B`
prefix) against a `bit(11)` column: it stored `"10"` silently instead of
raising `bit string length 2 does not match type bit(11)`. This predates the
literal-syntax work; it's a genuine, independent, pre-existing gap that
`bit.sql`'s literal-syntax fix simply put back on the critical path.

`internal/executor/codec.go`'s `coerceTextLikeDatum` is the existing
chokepoint every column value passes through when a string-shaped datum is
being stored into a text-like column type — it already had `varchar(n)`/
`char(n)`/`bpchar(n)` length-check arms. Added two more, both calling a new
shared `validateBitDigits` helper (digit-set check, 22P02) first:

- `bit(n)`: exact-length check. Length mismatch (either direction) raises
  `ERRCODE_STRING_DATA_LENGTH_MISMATCH` (22026), `"bit string length %d does
  not match type bit(%d)"` — matches `varbit.c`'s `bit_in`,
  `postgres/src/backend/utils/adt/varbit.c:210-216`.
- `bit varying(n)`: upper-bound-only check. Over length raises
  `ERRCODE_STRING_DATA_RIGHT_TRUNCATION` (22001), `"bit string too long for
  type bit varying(%d)"` — matches `varbit_in`,
  `postgres/src/backend/utils/adt/varbit.c:511-514`. Under length is fine
  (no padding — `bit varying` stores exactly what fits, unlike `bit(n)`).

## Verification

Live, throwaway cgroup-capped goopg + psql against the exact `bit.sql`
`BIT_TABLE`/`VARBIT_TABLE` fixture and the four invalid-digit `SELECT`
cases — all now byte-for-byte identical to the PG 18.3 oracle (error
message, SQLSTATE, and `errposition`/`^` column for the digit-validation
cases). `scripts/pg-regress-runner.sh --verbose bit`: diff 1054→581 lines
(still 0% file-level parity — the runner is all-or-nothing per file; the
remaining diff is entirely the operator/function/COPY gaps in the deferral
ledger row, none of which this slice touched).

Tests: `internal/parser/bit_string_literal_test.go`
(`TestParseBitStringLiteral`, `TestParseBitStringLiteralInvalidDigit`),
`internal/executor/bit_string_literal_test.go`
(`TestBitStringLiteralInsertRoundTrip`, `TestBitStringLiteralHexDecode`).

## Deferred (see `.ralph/deferral_ledger.md`, M0134-0092 row, for full detail)

(A) literal typed UNKNOWN not BITOID — untyped/operator-dispatch contexts.
(B) bitwise operator family (`~ & | # << >>`) entirely missing for bit/varbit.
(C) `||` concat operator has no bit/varbit arm.
(D) `length()`/`SUBSTRING()` don't recognize bit/varbit arguments.
(E) `get_bit`/`set_bit`/`bit_length`/`octet_length`/bit↔integer casts unaudited.
(F) COPY-in of hex-format bit/varbit values doesn't decode.
(G) `pg_input_is_valid`/`pg_input_error_info` don't route through the new
    validation for bit/varbit.
