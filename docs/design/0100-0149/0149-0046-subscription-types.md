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
| M0149\-0046b \(done\) | btree keys on composite types: "btree v0 only supports int4 / numeric keys" for the four composite\-keyed tables |
| M0149\-0046c \(done\) | composite fields are not coerced to their column types: `ROW(1.0, 'a', 1)` into \(float8, text, int\) prints `(1.0,a,1)`, not `(1,a,1)`; an array field keeps its input spelling `{a, b, c}` |
| M0149\-0046d \(done\) | range input is not canonicalised: int4range `'[1, 10]'` stays `[1, 10]`, not `[1,11)` |
| M0149\-0046e \(done\) | range output ignores the session TimeZone: tstzrange bounds print at \+00 under `SET timezone = '+2'` |
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

## Landed — M0149\-0046b, btree keys on composite types

PG indexes a composite column with btree record\_ops, whose comparison is record\_cmp \(rowtypes.c\):

- fields compare one by one, each with its type's default btree comparison;
- a NULL field sorts after every non\-NULL value.

goopg refused composite key columns. The record key now mirrors the array\_ops key \(`btree_record_key.go`\): each
field is a present tag followed by that field's own key, or a NULL tag \(0xFF\), and an end marker closes the
record. Each field key comes from `encodeBTreeKeyForColumn` with the field's type, so:

- nested composites and composite arrays recurse through it;
- the key is value\-based: `ROW(1.0,'a',1)` and `ROW(1,'a',1)` collide in a unique index, as on PG.

Field types come from the composite's catalog field list; a schema\-qualified field type has its qualifier
dropped.

**Decoding is not available.** The key decoders have no catalog and cannot rebuild a record from its key, so
`optimizer.indexCoversColumns` never lets an index\-only scan cover an index with a composite key column. The heap
supplies the value instead.

Two gaps remain \(deferral ledger\):

- equality on a composite column plans a seq scan, where PG uses the index;
- skip scans, trailing\-SAOP walks and amcheck's key walk over an index whose leading key is composite would
  decode it.

Unit test TestRecordBTreeKeyOrder. All of 002's DDL except hstore and all its inserts now succeed on a goopg node.

## Landed — M0149\-0046c, composite fields coerced to their types

PG's record\_in runs every field of a composite value through that field type's input function, and record\_out
re\-renders the result. goopg kept a composite as the text it was built from, so `ROW(1.0, 'a', 1)` into a
\(float8, text, int\) type stored and printed `(1.0,a,1)`, where PG has `(1,a,1)`. `record_in.go` adds the
normalisation:

- **Fields.** `recordIn` parses the record, runs each non\-NULL field through `typedFieldInput` and quotes it with
  `recordFieldText`. A wrong field count is 22P02 "malformed record literal", DETAIL "Too few columns." or "Too
  many columns.".
- **Field kinds.** `typedFieldInput` handles three cases: an array field is re\-spelled element by element
  \(`arrayInElems`, unquoted NULL kept\), a composite field recurses, and every other field goes through
  `evalCast`.
- **Casts.** `evalCast` applies it when the target names a composite type or an array of one.
- **Assignment.** `coerceRowForConstraintChecks`, the INSERT / UPDATE assignment coercion shared by every new\-row
  path, applies it to composite and composite\-array columns.

A built\-in target name skips the catalog lookup. `TypeNameToOID` falls back to text for unknown names, so only a
name that maps elsewhere, or text itself, counts as built\-in.

**Enum fields in keys.** An enum label that reaches `encodeBTreeKeyForColumn` as text, from a composite field or an
array element, is now resolved to its sort order \(enum\_in\). Before, such a key failed to encode, and the
row\-insert path silently left the row out of the index. An ordered scan of `tst_comp_enum_array` returned nothing,
and uniqueness was not enforced. The swallowed index\-insert error is filed as M0149\-0098.

All of 002's array, enum and composite tables now read exactly as on PG on a goopg node. Ranges and hstore remain
\(0046d–f\).

## Landed — M0149\-0046d, range\_in at assignment and in range arrays

goopg's `rangeIn` already canonicalised a discrete range: `'[1, 10]'::int4range` is `[1,11)`, via
int4range\_canonical, int8range\_canonical and daterange\_canonical. It was missing in two places:

- **Assignment.** INSERT / UPDATE stored a range column's literal as typed \(`[1, 10]`\).
  `coerceRowForConstraintChecks` now runs range columns through evalCast and range\-array columns through
  `arrayInElems`.
- **Array casts.** `'{"[1,2]"}'::int8range[]` kept its elements. evalCast's array\-target arm now runs range
  elements through range\_in.

`rangeColumnType` covers the six built\-ins and user range types. Built\-in scalar names skip the catalog lookup.

The rangetypes regress case went from 463 to 444 mismatched lines. 002's range tables match PG, except tstzrange
output in the session TimeZone \(0046e\).

## Landed — M0149\-0046e, date\-time range output in the session style

A range is stored as its text, but PG's range\_out renders each bound through the subtype's output function:
timestamptz\_out in the session TimeZone and DateStyle, timestamp\_out and date\_out in the DateStyle. goopg returned
the stored text. 002 runs `SET timezone = '+2'` before its SELECTs, then read `2014-08-03 22:00:00+00` where PG
reads `2014-08-04 00:00:00+02`.

Two changes fix it:

- **Decode arm.** `decodePhysicalPGValueLowered` now decodes a tstzrange / tsrange / daterange value and
  re\-renders it with `restyleRangeText`: each bound is parsed through its subtype and formatted with the scan's
  `array.OutputStyle`, then the range is deparsed. Infinite or absent bounds and `empty` are kept.
- **Style resolution.** A scan resolves the session style only when `colsHaveArray` says a column needs it. That
  check now includes the three date\-time range types.

The rangetypes regress case is unchanged \(444\).

**Filed: M0149\-0099.** A range column reloads as text after a server restart, because pg\_attribute records
atttypid = text for it and the reload follows. Range\-specific input and output are lost after a restart.
