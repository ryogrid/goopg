# M0119-0006 — `date[]` / `time[]` / `timestamp[]` / `timestamptz[]` / `timetz[]` / `bytea[]` element images

status: accepted
date: 2026-08-12
milestone/task: M0119-0006 (pg_amcheck server tier — the array/type cluster)
supersedes: nothing; successor to `0119-0006-array-element-pg-images.md`
(interval/uuid/numeric elements) and `0119-0006-array-index-key-decodability.md`

## The defect

goopg spells a user array column as `catalog.Type{Name:<ELEMENT>, IsArray:true}`
and stores it as a PG-native `ArrayType` varlena. The element table that decides
the on-disk image of each element — `pgarray.ElemTypeInfo` — had arms for the
integer/float/bool/text family plus `interval`/`uuid`/`numeric` (the previous
slice), and **no arm for any date-time type or for `bytea`**.

With no arm the encoder takes the *unknown element* fallback: it writes an
`ArrayType` whose `elemtype` field says **25 (text)** with 4-byte-varlena TEXT
bodies, while `pg_attribute.atttypid` for the very same column says `_date` /
`_time` / `_timestamp` / `_timestamptz` / `_timetz` / `_bytea` (confirmed at HEAD
on a throwaway cluster: the catalog reported OIDs 1182/1183/1115/1185/1270/1001
over blobs whose bodies were the literal characters the user typed).

The blob and the catalog disagreed about one column. That is the same class of
defect as the `uuid` column slice: **no goopg answer was wrong** — goopg wrote
text and read the same text back — so it is invisible from inside the engine and
visible only to a reader that trusts the descriptor:

- a PG 18.3 standby reading goopg's heap deforms `_date` element bodies as
  4-byte dates and every following element from the wrong offset;
- `pg_amcheck`'s heap tier (the milestone this slice serves) reads the same
  bytes through the same descriptor;
- `internal/wal`'s pgoutput decoder ships the element bodies to a subscriber.

There is a user-visible half too, because the text path echoed the *input*
spelling instead of the type's output function. Against the PG 18.3 oracle
(port 65432, `TimeZone=UTC`), five answers were wrong:

| input | goopg (HEAD) | PG 18.3 |
|---|---|---|
| `'{2020-1-2}'::date[]` | `{2020-1-2}` | `{2020-01-02}` |
| `'{1:2:3}'::time[]` | `{1:2:3}` | `{01:02:03}` |
| `'{04:05:06.100000}'::time[]` | `{04:05:06.100000}` | `{04:05:06.1}` |
| `'{"2020-01-01 10:00:00+02"}'::timestamptz[]` | `+02` kept verbatim | `2020-01-01 08:00:00+00` |
| `'{01:02:03+05:00}'::timetz[]` | `{01:02:03+05:00}` | `{01:02:03+05}` |

## What landed

**1. The element table (`internal/pgarray`).** Six arms, taken from `pg_type`'s
own `typlen`/`typalign` and cross-checked against PG 18.3 `pg_column_size`:

| element | OID | width | align | 2-element array size (PG 18.3) |
|---|---|---|---|---|
| `date` | 1082 | 4 | 4 | 32 |
| `time` | 1083 | 8 | 8 | 40 |
| `timestamp` | 1114 | 8 | 8 | 40 |
| `timestamptz` | 1184 | 8 | 8 | 40 |
| `timetz` | 1266 | 12 | 8 | 56 |
| `bytea` | 17 | varlena | 4 | 44 (`\x01`, `\x0102030405`) |

**2. The encode side delegates (`encodeArrayElem`).** The five fixed-width
date-time arms call `encodeValuePG` with the SCALAR element type rather than
re-deriving the image. That is Hard-won Rule #2 made structural: the element and
the column cannot drift because there is one encoder. It works because every one
of those scalar arms already accepts the `KindString` an array element always is
(`parseCopyTimestamp` + the ±infinity literals for date/timestamp,
`parseTimeString` for time, `parseTimeTZString` for timetz). `bytea` runs
`byteaIn` and stores the RAW bytes — but behind
`array4ByteVarlenaBytes`, not the scalar arm's `varlenaPayloadBytes`: a scalar
bytea may use PG's 1-byte short header, while inside an array body the elements
carry the always-4-byte header at align 4 (pinned: three 1-byte bytea elements
make a 48-byte array).

**3. The decode side renders through new leaf formatters
(`internal/pgdatetime`).** `FormatDate`, `FormatTime`, `FormatTimestamp`,
`FormatTimestampTZUTC` and `FormatTimeTZ` are ports of upstream `date_out` /
`time_out` / `timestamp_out` / `timetz_out`, over a port of `j2date`
(`postgres/src/backend/utils/adt/datetime.c`) so the pre-Gregorian range
(4713 BC) and the BC era marker come out right. They live in the leaf package for
the same reason `FormatInterval` does: `internal/wal` cannot import the executor,
and both it and the heap codec render the same bytes.

`executor.byteaOutHex` also moved down, to `pgarray.ByteaOutHex` — the array
element decoder needs byteaout and must not hold a second copy.

**4. A fidelity bug this slice surfaced: the missing TRAILING alignment.**
Upstream `construct_md_array` (`postgres/src/backend/utils/adt/arrayfuncs.c`)
re-aligns the running length **after every element, the last one included**, so a
PG array's `VARSIZE` is padded up to the element alignment. goopg stopped at the
final element. No reader could notice (elements are found by count, never by
scanning to the end), which is why it survived — but the blob was a different
SIZE from the one PG writes for the same value: a 1-element `timetz[]` is 40
bytes in PG (`MAXALIGN(24+12)`) and was 36 in goopg, a 1-element `\x01`
`bytea[]` is 32 and was 29. Fixed in `encodeArrayValuePG`. The previously-landed
element types are unaffected (their bodies were already aligned: 56/56/44).

## Compatibility

Read-compat is inherited from the previous slice and is exact rather than
heuristic: `RenderText` already falls back to the text path when the blob's own
`elemtype` field says 25 while the column's element type says otherwise, and
elemtype 25 under a `date[]`/`bytea[]` column can only have come from the
pre-flip encoder. There is no on-disk migration; a cluster written before this
loop keeps rendering correctly through that branch.

The trailing-alignment fix likewise needs none — an older, shorter blob is read
by element count, and the padding bytes are never examined.

## Gates

All expectations captured from the PG 18.3 reference cluster (bench/tpch, port
65432, `TimeZone=UTC` — goopg's own zone), not derived from goopg.

- `TestFormatDateMatchesDateOut`, `TestFormatTimeMatchesTimeOut`,
  `TestFormatTimestampMatchesTimestampOut`,
  `TestFormatTimeTZSignConvention` (`internal/pgdatetime`) — the last is the
  assertion a whole-value table misses: `TimeTzADT.zone` is seconds WEST of UTC,
  so the printed offset is its negation, and getting it backwards still prints a
  plausible timetz.
- `TestArrayCodecDateTimeElementRoundTrip` — the normalisation half (the
  pre-flip path echoed the input spelling).
- `TestArrayCodecDateTimeOnDiskLayout` — elemtype OID + `pg_column_size`; the
  timetz and bytea rows are what pin the trailing alignment.
- `TestArrayCodecDateTimeElementMatchesScalarColumn` — the element bytes are
  byte-identical to the scalar column's for the same value.
- `TestArrayCodecDateTimeLegacyTextBlob` — pre-flip blobs still render.
- `TestArrayCodecDateTimeInvalidElement` — a bad element is now rejected with
  the scalar arm's SQLSTATE instead of stored as text.
- `TestPgoDecodeArrayColumns` gains `date[]` / `timestamp[]` / `bytea[]` rows
  (the subscriber's view of the same bytes).

Three of the new gates were mutation-checked: dropping the trailing alignment,
removing the `date` element arm, and inverting the timetz zone sign each failed
exactly the intended gate. Suite gates: `go build ./...` + `go vet` clean;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`TestPort_RegressSuite` PASS (Hard-won Rule #5 — this is a codec change);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench smoke via the commit
hook. A live throwaway server reproduced all six columns end to end and matched
the oracle byte for byte on the normalisation probe.

## Deferred (see `.ralph/deferral_ledger.md`)

- `timestamptz` array elements render in **UTC**, because the leaf renderer has
  no tz database. Correct for every goopg cluster today (`SHOW TimeZone` = UTC)
  and wrong the moment a session sets another zone.
- The date/timestamp INPUT functions goopg delegates to reject spellings PG
  accepts (`'0001-01-01 BC'`, `'2020-01-01 10:00'`). Pre-existing in the scalar
  column; the array element now INHERITS the strictness it used to bypass.
- The index-key element decoder (`decodeArrayKeyElemText`) still refuses
  `date`/`time`/`timestamp`/`bytea` elements. One of the two reasons the
  decodability slice gave for that refusal — "no heap element image to agree
  with" — is now gone for exactly these types.
