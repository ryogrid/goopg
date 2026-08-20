# M0134-0019 — the `^` exponentiation operator (parser → evaluator → type inference)

Status: accepted
Task: M0134-0019 (`indexing.sql`)
Date: 2026-08-20

## Context

`indexing.sql` was sized at HEAD (still genuinely `failed`, not a stale status):
1517 diff lines / 39 hunks / 41 `^+ERROR`. Five root causes; the dominant one
(partitioned-index ATTACH/DETACH semantics, ~700+ lines) is REFACTOR-tier and is
ledgered, not shipped. This doc covers the one bucket that is both contained and
**not specific to the case at all**: goopg has no `^` operator.

`SELECT 2^16` fails with a syntax error. Three `indexing.sql` statements
(`sql/indexing.sql:708,709,712`) use `2^16` / `2^g` / `a between 2^16 and 2^19`
and abort. The gap is engine-wide: any query anywhere using `^` fails to parse.

The state at HEAD is a half-built feature, which is why it read as supported:

| layer | site | state |
|---|---|---|
| lexer | `internal/parser/lexer.go:446-468` | **works** — `^` emits its own `TokenOperator{Value:"^"}`; no 2-char operator starts with `^`, so no conflation with `#` or others |
| parser | `peekBinaryOp()`, `internal/parser/select.go:2812` | **missing** — no `case "^"`; every other binary operator is listed |
| opcode | `internal/parser/op.go` | **missing** — no `OpPow` |
| evaluator | `evalBinary`, `internal/executor/expr.go:1472` | **missing** — no arm |
| type inference | `exprType()`, `internal/optimizer/planner.go:11948` | **missing** — no arm |
| catalog seed | `internal/catalog/pg_operator_seed_data.go:284,291`; `internal/initdb/pg_proc_seed_data.go:174,1080` | **present but dead** — `dpow` / `numeric_power` are seeded as metadata with no Go implementation behind those HandlerNames |

## PG semantics being ported

- **Precedence and associativity.** `gram.y:887` declares `%left '^'`, sitting
  strictly between `* / %` and unary minus. goopg's precedence enum leaves slot
  `9` unused between `precMulDiv=8` and `precUnary=10` — an exact match, so
  `OpPow` takes slot 9 with no renumbering. goopg's precedence-climbing loop
  recurses at `prec+1` (`select.go:2356`), which is inherently left-associative;
  PG's `^` is also left-associative (unusually — most languages make it right-
  associative), so no special-casing is needed. A guard test must pick digits
  where the two readings actually diverge (`2^3^2` is 64 either way is **wrong**
  — left-assoc `(2^3)^2 = 64`, right-assoc `2^(3^2) = 512`; use that pair).
- **Result type.** PG has no `int ^ int` operator; integer operands are
  implicitly cast and the resolution lands on `float8 ^ float8` → `dpow`
  (`postgres/src/backend/utils/adt/float.c:dpow`), yielding **float8**. This is
  why upstream `expected/indexing.out:1327` shows `Key (a)=(65536)` — the float8
  `65536` is assignment-cast into the `int` column `a`.
- **`dpow` error and NaN rules** (`float.c:dpow`), ported verbatim:
  - `NaN ^ 0 = 1`, `1 ^ NaN = 1`; any other NaN input yields NaN with no error.
  - `0 ^ negative` → ERROR `22023` "zero raised to a negative power is undefined"
    (explicitly *not* a divide-by-zero code).
  - `negative ^ non-integer` → ERROR `22023` "a negative number raised to a
    non-integer power yields a complex result".

## Design decisions

**1. Do not reuse the existing `power`/`pow` builtin body.** `expr.go:11426-11445`
implements `power(a,b)` by text round-trip: `strconv.ParseFloat(datum.Format())`,
then returning `KindInt` when the result is integral and otherwise a numeric
formatted to exactly 6 decimal places. That is not PG's `power(float8,float8)`
(which returns float8) and it silently loses precision. Reusing it would spread a
non-PG-faithful representation into a second operator — the inverse of the
standing "prefer PG-faithful binary over text-for-convenience" rule. `OpPow`
therefore gets a fresh, `dpow`-faithful helper operating on float64 and returning
a float8 datum. The existing `power`/`pow` builtin is left untouched this loop
(changing its result kind is a separate, wider-blast-radius change) and its
divergence is ledgered.

**2. Ship the float8 operator only; defer `numeric ^ numeric`.** PG also has
`numeric ^ numeric` → `numeric_power` (`utils/adt/numeric.c:numeric_power`), with
its own scale-selection and error rules. Porting it faithfully is a separate
piece of work with no bearing on this case (whose operands are integers, so PG
itself takes the float8 path). Numeric-typed operands consequently route through
the float8 path in goopg for now — a real divergence from PG, ledgered with
`numeric_power` as the resume point.

**3. `exprType()` is a third site, beyond the two the sibling rule implies.**
The compiled evaluator (`evalFastExpr`, `internal/executor/exprnode.go:250`) and
the interpreted one (`evalExprSlot`/`evalExpr`, `expr.go:331/353`) are the classic
twin pair, but they **share a single `evalBinary` call** (`exprnode.go:367`) —
verified against the existing `expr_sibling_parity_test.go` harness — so one arm
covers both. The genuinely separate site is `exprType()`
(`internal/optimizer/planner.go:11948`), an independent type-inference function
(not the `ExprResultType`/`funcCallResultType` pair) with no catalog fallback; it
needs its own `OpPow` arm or the wire type is advertised wrong. `OpPow` is
deliberately kept **out** of the int8-overflow-trigger set at
`planner.go:13002-13003`: PG's `^` is float8 and is not overflow-checked.

## Scope

Four edit sites, all additive: `op.go` (`OpPow` + `String()`), `select.go:2812`
(`case "^"` at precedence 9), `expr.go:1472` (`case parser.OpPow` + dpow helper),
`planner.go:11948` (`case parser.OpPow` → float8).

## Non-goals (ledgered)

`numeric ^ numeric` / `numeric_power`; the non-PG-faithful `power`/`pow` builtin
result kind; and the four unshipped `indexing.sql` buckets — partitioned-index
ATTACH/DETACH semantics (REFACTOR, ~700+ lines), partition-key coverage
validation for unique/PK/EXCLUDE on partitioned tables, `pg_get_indexdef(oid)`
as a `LATERAL`/FROM item, and TOAST on oversized `pg_index` rows.
