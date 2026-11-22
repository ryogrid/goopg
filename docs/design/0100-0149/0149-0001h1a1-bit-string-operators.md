# M0149\-0001h1a1 — bit\-string operators, functions and casts

**Status:** landed 2026\-11\-17.

## Problem

goopg carries a bit / bit varying value as its `'0'`/`'1'` digit text. Several
things went wrong on top of that representation:

- the `B'…'` / `X'…'` literals were plain string constants typed text, so
  `pg_typeof(B'1')` was `text`;
- `| & # ~ << >>` reached the executor's integer arms ("operator \| requires
  integer operands");
- `x'20000'::int` failed with "value … is out of range for type integer";
- `bit_length` / `octet_length` / `bit_count` / `get_bit` / `set_bit` on a bit
  string ran their text / bytea versions.

The regress cases `privileges` and `largeobject` open every large object with
`x'20000'::int`, so this blocked most of their large\-object steps.

## PG

- A bit\-string constant is A\_Const T\_BitString, typed BITOID with no typmod.
- `&` `\|` `#` call bitand / bitor / bitxor. Operands of different lengths are
  22026 "cannot AND / OR / XOR bit strings of different sizes".
- `~` is bitnot.
- `<<` / `>>` call bitshiftleft / bitshiftright. The length is kept, a
  negative count shifts the other way, and an over\-long shift gives all
  zeros.
- All of the above return bit. `\|\|` is bitcat and returns bit varying.
- An unknown literal beside a bit operand resolves to bit and is read by
  bit\_in, so a non\-binary digit is 22P02 at the literal.
- pg\_cast has bit → int4 \(bittoint4\) and bit → int8 \(bittoint8\):
  - the digits are read as an unsigned pattern into the target's two's
    complement;
  - more digits than the target has bits is 22003;
  - there is no bit → int2 cast and no bit varying → integer cast \(42846\).
- An explicit bit → bit\(n\) cast zero\-pads or truncates \(bit\(\) with
  isExplicit\); bit varying\(n\) truncates. An implicit one keeps the length
  check.
- pg\_proc has bit overloads of octet\_length \(bitoctetlength\),
  bit\_length \(the bit count\), bit\_count \(bit\_bit\_count\), get\_bit /
  set\_bit \(bitgetbit / bitsetbit\). Their errors: 2202E "bit index %d out of
  valid range \(0..%d\)" and 22023 "new bit must be 0 or 1".

## goopg

- **Parser:** decodeBitStringLit sets `StringConst.BitString`. The value is
  unchanged, so every existing path still sees digit text. The field is tagged
  `dump:"omitempty"`, so only the five bit\-literal goldens changed.
  parserExprKey keys it as `b:` so `GROUP BY B'1'` and `'1'` stay distinct.
- **Planner:**
  - exprType / inferExprType type the flagged literal `bit`;
  - bitStringOpCall \(at the three BinaryOp resolvers, beside moneyArithCall\)
    and the three UnaryOp resolvers rewrite the operators into FuncCalls
    carrying `Operator`;
  - an unknown literal operand is wrapped in a varbit cast at its own
    position;
  - bitStringFuncOverload renames the overloaded functions when the first
    argument is a bit string, keeping the SQL name in the new
    `FuncCall.SQLName` for the column label and EXPLAIN;
  - a cast whose operand is an operator call is labelled by its type, as
    FigureColname does for an OpExpr;
  - castTargetLabel maps int → int4, decimal / dec → numeric and
    char / character → bpchar \(gram.y SystemTypeName\).
- **Analyzer:** `\|\|` over bit strings types as varbit instead of raising
  "operator does not exist".
- **Executor** \(bitstring\_ops.go\): the varbit.c functions;
  bitStringCast runs ahead of the generic cast; bitExplicitLength. EXPLAIN
  prints a prefix\-operator call as `(~ x)` and a renamed overload under its
  SQL name.

Probe \(goopg vs PG 18.3, 39 statements over literals, columns, operators,
functions, casts, errors, labels and EXPLAIN\): identical.

Regress:

| case | before | after |
|---|---|---|
| bit | 254 | 27 |
| largeobject | 220 | 46 |
| privileges | 693 | 673 |
| arrays | 1286 | 1281 |

51 cases were A/B'd. Nothing else moved apart from the join ±1 flake.

## Open

- `position(B'' IN B'')` returns 1; PG returns 0 \(bitposition\).
- Column defaults deparse without the `::"bit"` / `::bit varying` cast in
  `\d`.
- pg\_input\_is\_valid / pg\_input\_error\_info ignore the bit\(n\) length
  check.
- `1::dec` is a syntax error \(the DEC keyword spelling is missing\).
- bitStringFuncOverload runs only in the main resolver. A bit\-typed
  overloaded call after aggregation \(HAVING / post\-aggregate target\) still
  takes the text version.

## Explicit casts from strings \(M0149\-0001h1a83\)

coerce\_type reads a string with typmod \-1 \(bit\_in for an unknown literal, CoerceViaIO for text\) and then applies
the length coercion bit\(\) or varbit\(\) with isExplicit:

- bit\(n\) zero\-pads on the right or truncates: `'10'::bit(4)` and `bit(4) '10'` give `1000`.
- bit varying\(n\) truncates: `'111'::varbit(2)` gives `11`.

The cast arm in expr.go now does this for every explicit cast with a typmod, alongside the existing arm for
bit\-typed sources. Implicit assignment keeps the 22026/22001 length errors: `INSERT ... VALUES ('10')` into a
bit\(4\) column still fails, as in PG. An invalid digit is now reported at the literal's position rather than the
cast's.
