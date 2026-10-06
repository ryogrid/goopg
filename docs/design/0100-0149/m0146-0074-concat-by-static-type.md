# M0146-0074 — `||` resolves from the operand types

Status: done 2026-10-07 (`dc75eccdc`).

## Symptom

| expression | goopg before | PG 18.3 |
|---|---|---|
| `'a' \|\| '{9}'::text` | `{a,9}` | `a{9}` |
| `'{1}'::text \|\| '{2}'::text` | `{1,2}` | `{1}{2}` |
| `'ab'::varchar \|\| '{c}'` | `{ab,c}` | `ab{c}` |
| `t \|\| u` over two text columns holding `{1}`, `{2}` | `{1,2}` | `{1}{2}` |
| `coalesce(t, '') \|\| '{w}'` | `{p,w}` | `{p}{w}` |
| `'[1]'::jsonb \|\| '[2]'::jsonb` | `[1][2]` | `[1, 2]` |
| `'{"a":1}'::jsonb \|\| '{"b":2}'` | `{"a": 1,"b":2}` | `{"a": 1, "b": 2}` |
| `j \|\| j` over a jsonb column | operator does not exist | `jsonb_concat` |

## Cause

Arrays, jsonb and text all travel through the executor as string Datums.
evalBinary's `||` arm therefore decided from the values alone: an operand
shaped like `{…}` was treated as an array, so it was merged with the other
operand, appended to, or prepended to. The arm had no jsonb case, and the
analyzer rejected `jsonb || jsonb` when both operands were typed.

## PG

`||` is resolved at parse time (`oper()`, parse_oper.c) from the operand
types:

- **Text.** `textcat` (text, text), `anytextcat` (anynonarray, text) and
  `textanycat` (text, anynonarray), all in varlena.c, concatenate bytes.
- **Arrays.** `array_cat`, `array_append` and `array_prepend`
  (array_userfuncs.c).
- **jsonb.** `jsonb_concat` (jsonfuncs.c `IteratorConcat`). Two objects
  merge, and the right operand wins on a shared key. Anything else yields
  an array: the left operand's elements, then the right operand's, where a
  non-array operand counts as one element.
- **Unknown literals.** An unknown literal adopts the other operand's type,
  so `jsonb || '{…}'` is `jsonb_concat`.

## Change

- **`concat_mode.go`.** `concatModeOf(l, r)` maps the static types
  (`optimizer.ExprResultType`) to a mode:
  - `concatText` for character strings and other non-array, non-jsonb
    types, and for jsonb beside a character string;
  - `concatJSONB` for jsonb with jsonb or an untyped literal;
  - `concatGuess` for array, polymorphic or unresolvable types. This is the
    old shape-based path, kept so that an array expression whose type goopg
    cannot resolve still concatenates as an array.
- **`jsonbConcat`.** Implements `jsonb_concat` on the
  `jsonb_canonical.go` value model.
- **`evalConcat`.** Split out of `evalBinary`, it takes the mode:
  - the interpreted twin (`evalExprSlot`) calls `concatModeOf`;
  - the compiled twin (`exprnode.go`) records the mode in `payload[16]`
    bits 8 (text) and 16 (jsonb) at build time;
  - other `evalBinary` callers keep `concatGuess`.
- **Analyzer.** `jsonb || jsonb`, or jsonb with an unknown literal, is
  typed `jsonb`.
- **`ExprResultType`.** Now resolves:
  - `coalesce`, `greatest`, `least`: the first typed argument, or text when
    all arguments are literals;
  - `nullif`: its first argument;
  - `concat`, `concat_ws`, `format`: text. Their VARIADIC `"any"`
    signatures cannot key a `pg_proc` lookup.

## Verification

- `TestConcatResolvesByStaticType`: 21 queries under both builders, every
  want PG 18.3's. They cover text, varchar, untyped literals, columns,
  function results, arrays (literal, typed, column, sublink) and jsonb
  (array, object, scalar, mixed, column, beside text).
- A server probe of 50 statements is identical to PG except two
  pre-existing gaps listed under "Not covered".
- Regress A/B over 13 files:
  - jsonb shrank from 6511 to 6345 lines, with no new divergent line;
  - plpgsql shows its known function-overload flap;
  - the other files are identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - fire set: no fires;
  - ea-ratchet PASS;
  - sf025 96/96, plan shapes 99/99, runtime-moves 0. A first sweep under
    host load timed out Q72 with its plan unchanged; the re-run on a quiet
    host passed it in 176s, its usual time.

## Not covered (ledgered)

- **Element-type checking.** `text || int[]` concatenates; PG raises
  "operator does not exist: text || integer[]" because array_prepend needs
  an element of the array's type. goopg's array path checks no element
  type.
- **Unresolved operand types.** An operand whose static type is still
  unresolved keeps the shape guess.
- **`string_to_array`.** Not implemented; it was already ledgered by
  M0146-0045.
