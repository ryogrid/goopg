# M0149\-0046 — Port subscription/t/002\_types.pl

Status: in progress. TestPort\_Subscription002Types is written \(three pairings\) and gated by GOOPG\_TAP\_002. Its
SQL and expected output are 002\_types.pl's, extracted verbatim. The port passes with PG 18.3 on both sides, which
checks the extraction.

The PG nodes come from the test\-module toolchain \(`tapnode.NewPGModules`, tmp/pg183\-inj\), because the test
creates the hstore contrib type and postgres/local\_install has no contrib modules.
`scripts/build-pg183-toolchain.sh` now installs contrib/hstore.

## Blockers on a goopg node

The DDL and DML fail on any goopg node, so both goopg pairings fail before replication starts. Each blocker below
is one link from the TAP test:

| task | blocker |
|---|---|
| M0149\-0046a \(done\) | NULL array elements were refused \("NULL array elements are not supported"\) |
| M0149\-0046b | btree keys on composite types: "btree v0 only supports int4 / numeric keys" for the four composite\-keyed tables |
| M0149\-0046c | composite fields are not coerced to their column types: `ROW(1.0, 'a', 1)` into \(float8, text, int\) prints `(1.0,a,1)`, not `(1,a,1)`; an array field keeps its input spelling `{a, b, c}` |
| M0149\-0046d | range input is not canonicalised: int4range `'[1, 10]'` stays `[1, 10]`, not `[1,11)` |
| M0149\-0046e | range output ignores the session TimeZone: tstzrange bounds print at \+00 under `SET timezone = '+2'` |
| M0149\-0046f | no hstore extension |

## Landed — M0149\-0046a, NULL array elements

PG's ArrayType gets a null bitmap when any element is NULL \(array\_in → construct\_md\_array\):

- The bitmap follows lbound\[0\]; bit i set means element i is present.
- `dataoffset` is ARR\_OVERHEAD\_WITHNULLS = MAXALIGN\(24 \+ ceil\(n/8\)\).
- A NULL element takes no data.
- Only the unquoted token NULL \(any case\) is NULL; a quoted `"NULL"` is the four\-letter string \(ReadArrayStr\).

goopg refused NULL elements outright. Three paths change:

- **Encoder.** `encodeArrayValuePGCtx` writes the bitmap and `dataoffset`, reading quotedness from
  `parseTextArrayElemsDelim`.
- **Renderer.** `array.RenderTextStyled`, shared with the pgoutput decoder, reads them back.
- **Subscript.** A subscript of a NULL element is SQL NULL rather than the text "NULL" \(array\_get\_element\).

On PG 18.3 the stored blob for `'{5,NULL,3}'::int[]` is 40 bytes, and goopg's matches. The arrays regress case
went from 1134 to 760 mismatched lines.
