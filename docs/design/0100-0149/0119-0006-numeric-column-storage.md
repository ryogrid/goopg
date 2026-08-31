# M0119-0006 — the `numeric` column stops being text

**Status:** landed 2026-08-11.
**Predecessors:** `0119-0006-interval-column-storage.md`,
`0119-0006-uuid-column-storage.md` (same class, same five-seam shape).
**Closes:** the M0130-S11.4 B2-a ledger row (`pgIndexKeyImageIsPGFaithful`
refuses numeric).

## The divergence

`encodeValuePG`'s `numeric`/`decimal` arm stored the value's DECIMAL STRING
behind a varlena header — `varlenaTextBytes(coerceTextLikeDatum(...))`.

This is not the uuid failure mode. pg_type agrees numeric is a varlena
(typlen -1, typalign `i`, typstorage `m`), so goopg's own `pg_attribute` row was
never wrong and no following column was misplaced. The PAYLOAD was the
divergence: every reader that trusts the TYPE hands it to `numeric_out` as a
`NumericData`, and reads `"1234"` as `n_header = 0x3231` (NUMERIC_POS, dscale
12849) with `n_weight = 13363`. Three such readers exist today:

- a PG 18.3 standby on goopg's cluster (the M0130 line's whole point);
- `pg_amcheck`'s heap tier, which is what M0119-0006 is building toward;
- goopg's own PG-format index tuples — `PGCompareNumeric` cannot decode an
  ASCII header, falls back to `bytes.Compare`, and then orders `-1000` above
  `0` and `0.5` above `1`. That is a mis-ORDERED index, which is why
  `pgIndexKeyImageIsPGFaithful` had to refuse numeric and keep it on the blob
  key path.

## What landed

PostgreSQL's `NumericData` (numeric.c `make_result_opt_error`): a uint16
`n_header` (short form) or `n_sign_dscale` + int16 `n_weight` (long form),
followed by base-10000 digits, all little-endian, behind the heap's own varlena
framing (1-byte short header when it fits, exactly as `heap_fill_tuple`
chooses).

The serializer was already in the tree. `internal/pgnodes/datum.go` ported
`numeric_in`/`numeric_out` in full for pg_node_tree, where a numeric `Const`'s
`constvalue` is the same on-disk varlena that `outfuncs.c` dumps byte for byte.
`internal/pgnodes/numeric_storage.go` exports three heap-facing entry points
over it — `NumericBodyFromText`, `NumericTextFromBody`,
`NumericTextFromStoredPayload` — taking and returning the varlena PAYLOAD,
because the framing belongs to the heap.

Seams touched (fewer than uuid's five: numeric stays a varlena, so no alignment
or `pgPhysicalTypeIsVarlena` change):

1. `executor.encodeValuePG` — numeric arm writes the NumericData body via the
   new `varlenaBytes` (the non-text twin of `varlenaTextBytes`).
2. `executor.decodePhysicalPGValueMctx` — numeric arm renders text back through
   `NumericTextFromStoredPayload`, then reuses the existing
   `parseNumericFastInt` / known-scale / big.Int tail unchanged.
3. `wal.pgoDecodePhysicalValue` — the SECOND decoder of the heap layout. Left
   unrouted it would have shipped the raw digit array to a subscriber as text
   WITHOUT erroring, since pgoutput declares a value's length, not its spelling.
4. `executor.pgIndexKeyImageIsPGFaithful` — numeric removed; no type fails it
   now. Numeric-keyed indexes move to the PG tuple key format under
   `PGCompareNumeric`.

The in-memory `Datum` is deliberately unchanged (KindNumeric mantissa+scale), so
no comparison, arithmetic, analyzer or planner site moves.

## No on-disk migration: the dual read

Every cluster written before this flip holds text payloads in its numeric
columns — including the TPC-H and TPC-DS benchmark clusters, whose gates filter
and aggregate on them. `NumericTextFromStoredPayload` therefore accepts both
forms, and the discrimination is EXACT rather than heuristic: a payload whose
every byte lies in the decimal-literal set `[0-9+-.eE]` is always legacy text,
because no NumericData body can be spelled from that set.

- short form: `n_header` has NUMERIC_SHORT (0x8000) set, so its high byte
  (`body[1]`, little-endian) is ≥ 0x80 — above every byte in the set (max
  `'e'` = 0x65);
- special (NaN/±Inf): header 0xC000/0xD000/0xF000, same argument;
- long form with ≥1 digit: every NBASE digit is 0..9999, so its high byte is
  ≤ 0x27 — below every byte in the set (min `'+'` = 0x2B);
- long form with no digits: that is zero (`strip_var` empties the digit array
  only for zero), whose header high byte is `dscale>>8`, and the long form is
  only chosen for dscale > 63, so the byte is 0x00 for every dscale below
  11008 — far past NUMERIC_MAX_DISPLAY_SCALE.

`TestNumericStoredFormsAreDisjoint` / `TestNumericStoredFormsCannotCollide`
sweep the argument rather than trusting it.

The dual read is goopg-side only. A PG standby or `pg_amcheck` still misreads a
pre-flip row; only rows rewritten after the flip are PG-faithful. Ledger row
2026-08-11.

## The index-format consequence

Because `buildPGIndexKeyDesc` now describes a numeric index, a numeric-keyed
btree resolves to the TUPLE key format, and the format is recomputed at open
time from the catalog — it is not recorded on disk. A numeric-keyed index built
before this loop is blob-format bytes read under a tuple-format descriptor and
must be REINDEXed. This is the same one-time break every other describable type
took at the S11.4 flip (3b-2c-ii-B2-c), extended to the last type; ledger row
2026-08-11.

Six executor tests probed numeric indexes by hand-building blob keys
(`btree.Open` + `btree.EncodeNumericKey`), which is the blob format's layout
asserted in a test that is about the TYPE. They now go through the engine's own
funnels — `openIndexTreeForTest` / `indexProbeForTest` /
`indexProbeMultiForTest` (new, the compound-key twin) / `compositeUpperBound` —
so they track whichever format the index resolves to.

## Gates

- 9 new unit tests across three packages: `TestNumericColumnStoresNumericData`
  (bytes pinned directly — the old text form round-tripped through goopg's own
  decoder perfectly, so only a byte assertion catches a regression),
  `TestNumericColumnRoundTrip`, `TestNumericColumnDecodesLegacyTextPayload`,
  `TestNumericStoredFormsAreDisjoint`, `TestPgoDecodeNumericMatchesPGNativeLayout`,
  `TestPgoDecodeNumericAcceptsLegacyTextPayload`, `TestNumericBodyTextRoundTrip`,
  `TestNumericBodyHasNoVarlenaHeader`, `TestNumericTextFromStoredPayloadAcceptsBothForms`,
  plus `TestPGIndexKeyImagesStayPGFaithful` (the old refusal guard, inverted)
  and numeric's new row in `TestPGIndexTupleKeyOrdersEveryDescribableType`.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35) — on the pre-flip cluster,
  i.e. the legacy read path against real data.
- `scripts/tpcds-sf05-regression.sh sweep` PASS=95 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0 (57 checksum-verified) — TPC-DS is decimal-heavy, so the
  57 value checksums are the strongest available evidence that the dual read
  reproduces the pre-flip answers exactly.

## Still open (ledger rows 2026-08-11)

- `numeric[]`: an array column's elements are still text, and worse than
  `uuid[]`'s — `arrayElemTypeInfo` has no numeric case, so the array is built
  with elemtype OID 25 (text).
- NaN / ±Infinity in a numeric COLUMN: the encoder writes the NUMERIC_SPECIAL
  headers correctly, but goopg's KindNumeric Datum cannot represent them, so
  the decode tail rejects the text — exactly as it rejected the legacy stored
  text. Pre-existing, unchanged.
- No on-disk migration for either the heap payload or the numeric index key
  format (above).
