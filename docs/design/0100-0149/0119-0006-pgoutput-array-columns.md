# pgoutput decodes array columns — and the renderer stops having two copies (M0119-0006)

Status: accepted
Date: 2026-08-11
Milestone: M0119-0006 (pg_amcheck server tier — deferral-ledger consumption)
Closes: the ledger row `0119-0006p` opened —
*"`internal/wal/pgoutput.go`'s `pgoDecodePhysicalValue` and `pgoPhysicalAlign`
switch on `t.Name` alone and IGNORE `t.IsArray`"*.

## The defect

goopg spells a user array column as `catalog.Type{Name:<ELEMENT type>,
IsArray:true}` — `Name` holds the ELEMENT's name, never `int4[]` and never
`_int4`. The logical-replication decoder switched on `Name` alone, so **every**
array column was decoded as its scalar element type.

For an `int4[]` that was merely wrong: the first four bytes of the ArrayType
varlena header decode as the integer `128` (a 32-byte blob's `len<<2`). For the
element types whose scalar storage was flipped to a PG physical image by the
preceding slices it is worse, because a plausible value comes back:

| column | pre-fix pgoutput text | correct (PG 18.3 `array_out`) |
|---|---|---|
| `int4[]` `{1,2}` | `128` | `{1,2}` |
| `uuid[]` | `e0000000-0100-0000-0000-0000870b0000` | `{a0eebc99-…,00112233-…}` |
| `interval[]` `{1 mon,2 hours}` | `98 years 11 mons 01:11:34.96752` | `{"1 mon",02:00:00}` |

And the half a per-value comparison cannot catch: the decoder also reports how
many bytes the column spans. A `uuid[]` read as a scalar `uuid` consumes 16
bytes instead of the blob's full length, so **every following column in the
tuple decodes from the middle of the array body** — one array column corrupts
the whole replicated row. `pgoPhysicalAlign` had the mirror-image bug: an
`interval[]` was aligned to the element's typalign `'d'` (8) where an ArrayType,
being a varlena, is `'i'` (4).

A third seam, found while gating the first two: the `R` (Relation) message
advertised `pgoTypeOIDFor(col.Type.Name)` = the ELEMENT's pg_type OID. The
subscriber was told an `int4[]` column is a plain `int4` (23, not `_int4` 1007)
while the values on the wire are array text — an apply worker parsing `{7,8}`
as an int4 errors out.

## Why a new package rather than a second decoder

`internal/wal` cannot import `internal/executor`, which is exactly why the
renderer was missing here in the first place. Re-porting it would have created a
third copy of the ArrayType element table and a fourth of `array_out`'s quoting
rules — precisely the "sibling paths must change together" failure mode this
milestone keeps rediscovering (the interval / uuid / numeric column slices each
had to remember to update this same file).

So the renderer moved down to a new leaf package **`internal/pgarray`**
(imports `catalog`, `pgdatetime`, `pgnodes` — all leaves both callers already
depend on):

- `ElemTypeInfo(elemName)` — the element pg_type OID / typlen / typalign table,
  pinned to PG 18.3 `pg_column_size` measurements by slice `0119-0006p`.
- `RenderText(elemName, payload)` — the ArrayType-payload → `{e1,e2}` renderer,
  including the legacy-blob compatibility path (a pre-flip `interval`/`uuid`/
  `numeric` array states `elemtype 25` in its own header and decodes on the text
  path it was written on).
- `DecodeElem`, `QuoteTextElem` — the per-element and `array_out`-quoting halves.

`executor.decodeArrayValuePG` now strips its varlena header and calls
`RenderText`; `executor.arrayElemTypeInfo` and `quoteArrayTextElem` delegate, so
the encoder is unchanged and there is exactly one element table in the tree.
This is the same move `formatInterval` → `pgdatetime.FormatInterval` made for
the same reason.

`internal/wal/pgoutput.go` gains an `if t.IsArray` guard **ahead of** both
switches (align 4; strip varlena, then `pgarray.RenderText`) and a new
`pgoColumnTypeOID` that folds the element OID through
`catalog.ArrayOIDForBase`.

## Gates

`internal/wal/pgoutput_array_test.go`:

- `TestPgoDecodeArrayColumns` — `int4[]`, `text[]` (with `array_out` quoting and
  the inter-element alignment padding), `uuid[]`, `interval[]` and the empty
  array. All five expected texts were captured from the PG 18.3 reference
  cluster on port 65432, not derived.
- `TestPgoPhysicalAlignArrayIsVarlenaAlign` — `interval[]`/`int8[]` must align 4,
  not 8; `uuid[]`/`bool[]`/`int2[]` must round 5 up to 8, not accept 5/6.
- `TestEncodePgoTuplePhysicalArrayDoesNotShiftFollowingColumn` — the offset
  carry, over a two-column tuple.
- `TestPgOutputRelationAdvertisesArrayTypeOID` — the full plugin stream: the `R`
  message carries 1007 and the `I` message carries `{7,8}`, `42`.

All four were confirmed non-vacuous by disabling each guard in turn; the
disabled run reproduces the exact defects tabulated above, including the
following column decoding as `1` instead of `42`.

Also run: `go test ./internal/wal/ ./internal/executor/` (the delegation must not
move the heap codec), plus the standard commit gates.

## Deferred

- No end-to-end subscriber round-trip over a publication on an array column —
  the gates here stop at the wire bytes the plugin emits. A real apply worker
  consuming an array column is a separate surface (`internal/testport`).
- `pgoDecodePhysicalValue` still refuses an external TOAST pointer, so a large
  array that toasts out of line is not replicable; unchanged by this slice.
- Multi-dimensional arrays and arrays with NULL elements have no writer in
  goopg, so `RenderText` reads `ndim`/`dims[0]` but ignores `dataoffset`'s null
  bitmap — inherited from the heap codec, not introduced here.
