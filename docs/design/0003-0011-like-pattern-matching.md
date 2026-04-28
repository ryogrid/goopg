# LIKE Pattern Matching (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [0003-0001-planner-overview.md](0003-0001-planner-overview.md) |
| Supersedes | —                                                      |

## Problem

Six TPC-H queries lean on SQL `LIKE`:

- Q2: `p_type like '%:2'`
- Q9: `p_name like '%:1%'`
- Q13: `o_comment not like '%:1%:2%'`
- Q14: `p_type like 'PROMO%'`
- Q16: `p_type not like ':1%'`
- Q20: `p_name like ':1%'`

Without LIKE, every one of these planner-rejects at parse time. The
goal of this loop is to recognise `expr [NOT] LIKE pattern` end-to-end
through parse → analyze → plan → execute.

## Upstream reference

- `postgres/src/backend/utils/adt/like.c` — `MatchText` and the
  byte-by-byte recursive descent that backs `texteq` / `bpchareq`'s
  pattern variant.
- `postgres/src/include/parser/kwlist.h` — keyword registration for
  `LIKE` / `NOT LIKE`.

## Decisions

### Parser: postfix at comparison precedence

`LIKE` parses as a postfix-style binary at `precCompare`, mirroring
the existing `[NOT] IN` handling: in `parseExprPrec` we test for
`KwLike` (and the `KwNot` + `KwLike` two-token lookahead) before
peeking the standard binary operator table. The right-hand operand
parses at `precCompare + 1` so `expr LIKE 'x' AND y` groups as
`(expr LIKE 'x') AND y` — the same precedence as `=`, `<`, etc.

The result is a `parser.BinaryOp{Op: "LIKE"}` (or `"NOT LIKE"`).
v0 doesn't carry an explicit `LikeExpr` node; the binary-op shape is
enough for the analyzer + executor to dispatch and matches how
upstream's parser keeps `OPERATOR ~~` for LIKE internally.

### Analyzer: text-on-text → bool

`analyzeBinaryOp` maps `LIKE` / `NOT LIKE` to
`isStringLike(left) && isStringLike(right) → bool`. `unknown` (the
analyzer's catch-all for untyped literals) is allowed on either side
to keep `'foo' LIKE '%foo%'` and `col LIKE 'PROMO%'` both well-typed.

### Planner: pass-through

The planner's `resolveExpr` walks BinaryOp without knowing about
specific operator strings — `Op` is preserved as-is into
`planner.BinaryOp`. No changes were needed in the planner.

### Executor: byte-level recursive matcher

`matchSQLLike(s, pat string) bool` lives in `internal/executor/expr.go`
and implements the upstream semantics:

- `%` matches any (possibly empty) byte sequence.
- `_` matches exactly one byte.
- `\` escapes the next pattern byte (so `\%` matches a literal `%`).
- All other bytes match themselves byte-for-byte.

The implementation is the standard "two-cursor with star-anchor
backtracking" loop (no regex translation). Avoiding regex sidesteps
the trap where pattern bytes happen to be regex metacharacters: in
upstream, `'a.b' LIKE 'a.b'` matches because `.` is not special in
LIKE, but a naive translation to `regexp.MatchString` breaks that
contract.

`evalBinary` dispatches on `LIKE` / `NOT LIKE`: it requires both
operands to be `KindString`, runs `matchSQLLike`, and inverts the
result for `NOT LIKE`. NULL operands propagate to NULL via the
existing `IsNull` short-circuit at the top of `evalBinary`.

## Verification

Unit: `TestMatchSQLLike` in `internal/executor/like_test.go` pins
the matcher truth table across plain, `%`, `_`, mixed, escape, empty
pattern, and TPC-H Q9/Q14 shapes.

End-to-end against `goopg start -D <dir>` with upstream psql 18.3:

```sql
CREATE TABLE part (p_partkey int4, p_name text, p_size int4);
INSERT INTO part VALUES
  (1, 'PROMO BURNISHED COPPER', 10),
  (2, 'STANDARD POLISHED COPPER', 15),
  (3, 'PROMO PLATED STEEL', 20),
  (4, 'forest green widget', 5);

SELECT p_name FROM part WHERE p_name LIKE 'PROMO%';
-- PROMO BURNISHED COPPER, PROMO PLATED STEEL

SELECT p_name FROM part WHERE p_name NOT LIKE 'PROMO%';
-- STANDARD POLISHED COPPER, forest green widget

SELECT p_name FROM part WHERE p_name LIKE '%COPPER';
-- PROMO BURNISHED COPPER, STANDARD POLISHED COPPER

SELECT p_name FROM part WHERE p_name LIKE '____%';
-- all 4 rows (>=4 bytes)

SELECT 'PROMO%' LIKE 'PROMO\%';
-- t
```

## Out of scope (deferred)

- `ILIKE` — case-insensitive variant. Mechanically the same matcher
  with a `strings.EqualFold`-style byte comparison; left for a
  future loop.
- `ESCAPE` clause to override the default `\` escape character.
- Operator-class-driven LIKE (collation-aware comparison). v0 keeps
  byte-wise semantics; collations land with the type-system milestone.
- Planner-side prefix-anchor extraction for `LIKE 'PROMO%'` →
  index range scan. Today every LIKE goes through Filter on a
  full sequential scan.
