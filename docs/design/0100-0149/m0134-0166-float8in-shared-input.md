# M0134-0166 — `float8in`/`float4in`: one PG-faithful float text-input path

Status: LANDED 2026-08-29
Task: `.ralph/fix_plan.md` M0134-0166 (`postgres/src/test/regress/sql/float8.sql`)
Upstream oracle: `postgres/src/backend/utils/adt/float.c` — `float8in` (:364),
`float8in_internal` (:395), `float4in` (:180).

## What the regress case exposed

`float8.sql` was sized live for the first time this loop: 1311 diff lines,
56 `^+ERROR`, 33 `^-ERROR`. Its opening "test for underflow and overflow
handling" block failed in the worst possible way — it did not error, it
*succeeded*:

```
SELECT '10e400'::float8;    -- PG: ERROR 22003 "10e400" is out of range …
                            -- goopg:  10e400        ← the raw TEXT, in a float8
SELECT 'N A N'::float8;     -- PG: ERROR 22P02 invalid input syntax …
                            -- goopg:  N A N
SELECT '  NAN  '::float8;   -- PG: NaN
                            -- goopg:  NAN
```

The cause is not float8-specific and not case-local. `evalCast`
(`internal/executor/expr.go`) had **no `KindString` arm at all** for
`float4`/`real`/`float`/`float8`/`double precision`: a text datum fell through
the arm's integer/numeric normalisation to the closing `return d, nil` and was
handed back *unchanged and unvalidated*. Every `'…'::float8` anywhere in goopg
— not just in this test — accepted arbitrary text and stored it in a float
column. This is the same shape as M0134-0087 (`'…'::xid` had no arm either) and
M0134-0098 (`circle` was a raw-varlena pass-through), and it is a
silent-wrong-value bug, not a cosmetic one.

## The four siblings

Float text input existed in goopg four times, in four different states — a
textbook Hard-won Rule #2 (`pattern_sibling_paths_must_agree`) cluster:

| path | site | state before this change |
|---|---|---|
| `'x'::float8` | `evalCast`, `expr.go` | **no string arm** — raw pass-through |
| `float8 'x'` | `evalTypedStringLit`, `expr.go` | validated with `strconv.ParseFloat`, but on `parseNumeric` failure returned the RAW spelling, so `float8 'NAN'` rendered `NAN`; no 22003; no float4 narrowing |
| `INSERT INTO t(f float8) VALUES ('x')` | `pgFloatFromDatum`, `codec.go` | `TrimSpace`d first and reported the **trimmed** text (`'  '` → `… : ""`); mapped `strconv`'s `ErrRange` to 22P02 instead of PG's 22003 |
| `pg_input_error_info('x','float8')` | `operators_pg_input_error_info.go` | its own third message spelling, always 22P02 |

They now all call one function.

## `internal/executor/float_in.go`

`floatIn(orig string, bits int) (float64, *ExecError)` is `float8in_internal`
with `endptr_p == NULL` (`bits == 64`) / `float4in_internal` (`bits == 32`).
The order of operations is upstream's and matters:

1. skip leading whitespace; an empty remainder is 22P02 up front
   (float.c:402, "to avoid the vagaries of strtod() on different platforms");
2. `strtodToken` returns the prefix `strtod` would consume — sign, the
   `inf`/`infinity`/`nan` spellings float8in_internal checks by hand, or
   digits/point/exponent (an `e` with no exponent digits is left as junk).
   Hex float input is deliberately not accepted, per float8in_internal's
   "we consider these forms unportable" comment;
3. **range check before the junk check** (float.c:469 precedes :494), so
   `'10e400junk'` reports the overflow, not the syntax error;
4. an out-of-range result is 22003 and names only the **token** — leading
   whitespace and trailing junk already stripped — and says
   `double precision` / `real` per the width;
5. trailing whitespace is skipped, then any remainder is 22P02 reporting the
   **original, untrimmed** string (`'  - 3'` → `… : "  - 3"`);
6. NaN is canonicalised to `get_float8_nan()`'s `0x7ff8000000000000`, which is
   byte-visible to a PG standby reading goopg's heap and to `float8send`.

### The one place Go is not glibc

`strconv.ParseFloat` reports `ErrRange` only on **overflow**. A value that
underflows all the way to zero comes back as a clean `0, nil`, where glibc's
`strtod` sets `ERANGE` and float8in_internal's `val == 0.0` branch fires. So
`SELECT '10e-400'::float8` is an ERROR upstream and would have been `0` here.
`floatTokenIsZero` reconstructs the distinction: a token whose significand has
a nonzero digit but evaluates to zero underflowed. A genuine zero (`0`,
`-0.0000`, `0e500`) is not an error, and a denormal (`5e-324`) keeps its value
— upstream explicitly declines to error on denormals.

`floatInDatum` wraps the value in goopg's float carrier (`floatTextDatum` over
`PGFloatOut`), so the stored/rendered text is `float8out`'s canonical
shortest-round-trip form rather than whatever the user typed.

## Result

20-case regress A/B against a HEAD worktree:

| case | HEAD | after | Δ |
|---|---|---|---|
| `float4` | 844 | 652 | **−192** |
| `float8` | 1311 | 1204 | **−107** |
| 18 others | — | — | **byte-identical** |

Net **−299 lines, zero regressions**. Every "bad input" and
"underflow/overflow" assertion in float8.sql's opening section now matches PG
byte-for-byte, including the exact error strings.

Guard: `internal/executor/float_in_test.go` (revert-checked — 5 failures when
the untrimmed-`orig` reporting and the underflow branch are perturbed).

## Deferred (ledgered)

`float8.sql` remains `failed` at 1204 lines. The residual buckets, none of
which is float-input:

- the hyperbolic / degree-trig / gamma / erf function family
  (`sinh cosh tanh asinh acosh atanh sind cosd tand asind acosd atand atan2d
  erf erfc gamma lgamma`) is entirely unimplemented — ~40 of the 52 remaining
  `^+ERROR`s;
- the `@` (abs) and `|/` (sqrt) prefix operators are unlexed;
- `trunc`/`ceil`/`ceiling`/`floor` of a large float8 go through int64 and
  return `-9.223372036854776e+18` — a second silent-wrong-value bug;
- float8 division is evaluated in goopg's decimal `KindNumeric`, not float64
  (`1004.3 / -10` → `-100.43`, PG `-100.42999999999999`);
- `float8send` renders an empty/garbled bytea;
- `nan / 0` raises "division by zero" where PG returns NaN;
- the `LINE n: … ^` errposition echo is missing for a literal-validation error
  raised during `INSERT … VALUES` evaluation (cross-cutting; already ledgered
  with box/circle).
