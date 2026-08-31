# 0119-0006 — `interval[]` / `uuid[]` / `numeric[]` element images

**Milestone:** M0119-0006 (deferral-ledger backlog, pg_amcheck server tier)
**Date:** 2026-08-11
**Status:** landed
**Predecessors:** `0119-0006-interval-column-storage.md`,
`0119-0006-uuid-column-storage.md`, `0119-0006-numeric-column-storage.md`

## The residue three slices left behind

The interval, uuid and numeric **column** slices each flipped a scalar heap
image from "the text the user typed" to the physical image `pg_type` has
asserted since initdb. Each of them stopped at the array boundary, and each
left the same ledger row: the array encoder (`encodeArrayValuePG`) is a
separate path, so `interval[]` / `uuid[]` / `numeric[]` still stored text
elements. Three rows, one fix — this slice.

The defect was worse than "the elements are text". `arrayElemTypeInfo`
(`internal/executor/codec_array.go`) had no arm for any of the three, so all
three fell through to the *unknown element type* fallback:

```go
oid, align, varlena = 25, 4, true   // text
```

which writes an `ArrayType` header whose `elemtype` field says **25 (text)**.
Meanwhile `pg_attribute.atttypid` for the column says `_interval` (1187),
`_uuid` (2951) or `_numeric` (1231) — that mapping lives elsewhere
(`pg18_user_catalog_rows.go`) and was always right. So the on-disk blob and the
catalog disagreed about the element type of the same column. A reader that
trusts the catalog (a PG 18.3 standby on goopg's cluster directory,
pg_amcheck's heap tier, a logical subscriber) hands a 36-character ASCII string
to `uuid_out` under `typlen 16`, or an ASCII decimal string to `numeric_out` as
a `NumericData` whose first two bytes get read as `n_header`.

## What the element layout actually is

The layout was not guessed. PG 18.3's own `pg_column_size` over the identical
arrays (reference cluster, port 65432):

| array | `pg_column_size` | = header + elements |
|---|---|---|
| `ARRAY['1 mon','2 hours']::interval[]` | 56 | 24 + 16 + 16, align 8 |
| two `uuid`s | 56 | 24 + 16 + 16, align 1 |
| `ARRAY['1.50','-2500']::numeric[]` | 44 | 24 + (10 → pad 12) + 8, align 4 |

which is exactly `arrayElemTypeInfo`'s `(oid, size, align, varlena)` contract
for the three new arms:

| element | OID | typlen | typalign | varlena |
|---|---|---|---|---|
| `uuid` | 2950 | 16 | `c` (1) | no |
| `interval` | 1186 | 16 | `d` (8) | no |
| `numeric` | 1700 | −1 | `i` (4) | yes |

`numeric` is the one varlena element whose body is **not** its own text: like
the scalar arm it stores `pgnodes.NumericBodyFromText`'s base-10000
`NumericData` behind the array's always-4-byte varlena header. That is why
`encodeArrayElem`'s `if varlena` short-circuit had to grow a case ahead of it,
and why `array4ByteVarlena` was split into a bytes-taking
`array4ByteVarlenaBytes`.

The three new arms are ports of the scalar arms, not second implementations:
uuid reuses `uuidBytesFromCanonical` / `uuidCanonicalFromBytes`, interval
reuses `parser.ParseIntervalBody` / `formatInterval`, numeric reuses
`pgnodes.NumericBodyFromText` / `pgnodes.NumericTextFromStoredPayload`.

## The visible behaviour change

Only interval moves any user-visible answer, and it moves it **onto** PG:

```
goopg (before)  {1 mon,30 days,2 hours}
goopg (after)   {"1 mon","30 days",02:00:00}
PG 18.3         {"1 mon","30 days",02:00:00}
```

`2 hours` becomes `interval_out`'s `02:00:00` because the value is now stored
as `{time, day, month}` rather than as the typed text, and `1 mon` acquires
array quoting because `array_out` quotes any element containing a space — the
pre-flip path echoed the text back unquoted, which was not re-parsable as the
same array. uuid only lowercases (already canonical on the goopg side) and
numeric only normalises the spelling PG normalises anyway (`-2.5e3` → `-2500`).

## Reading pre-flip data

There is no on-disk migration, so blobs written by the old encoder are still
out there in every cluster that predates the flip. Unlike the scalar numeric
slice — which had to distinguish two byte forms of the *same* field — the array
case needs no analysis at all: **the blob states its own element type**. A
pre-flip `interval[]` / `uuid[]` / `numeric[]` blob carries `elemtype = 25`,
and the only thing that can produce elemtype 25 under one of these columns is
the pre-flip fallback. `decodeArrayValuePG` therefore compares the stored
elemtype against the arm's expected OID and, on the 25 mismatch, decodes the
whole array on the text path it was written on. New writes are never ambiguous
because the encoder now always stamps the real OID.

## Seams

- `internal/executor/codec_array.go`
  - `arrayElemTypeInfo` — three new arms (the OID/size/align table above).
  - `encodeArrayElem` — numeric ahead of the varlena short-circuit; uuid and
    interval fixed-width arms.
  - `array4ByteVarlenaBytes` — new; `array4ByteVarlena` delegates.
  - `decodeArrayValuePG` — elemtype-vs-expected-OID legacy discrimination.
  - `decodeArrayElem` — the three decode arms; interval's rendering goes
    through `quoteArrayTextElem` because `interval_out` can emit spaces.

## Gates

`internal/executor/codec_array_pgtype_test.go`:

- `TestArrayCodecPGTypeElementRoundTrip` — text → blob → text against outputs
  captured from PG 18.3, not from goopg.
- `TestArrayCodecPGTypeOnDiskLayout` — blob length and `elemtype` pinned to
  PG's `pg_column_size`. This is the assertion the round-trip cannot make: a
  self-consistent encoder/decoder pair round-trips text elements just as
  happily; it is the on-disk image that an external reader consumes.
- `TestArrayCodecPGTypeLegacyTextBlob` — hand-built pre-flip blobs still read.
- `TestArrayCodecPGTypeInvalidElement` — bad elements now raise the scalar
  arm's SQLSTATE (22P02 / 22007) instead of being stored verbatim.

Live server (throwaway cluster, port 5533) confirmed the full engine path —
`CREATE TABLE` → `INSERT ARRAY[...]::t[]` → `SELECT`, plus subscripting —
reproduces the PG 18.3 renderings above.

## Left open (ledger)

- `internal/wal/pgoutput.go`'s `pgoDecodePhysicalValue` / `pgoPhysicalAlign`
  switch on `t.Name` and **ignore `t.IsArray`**, so under logical replication
  ANY array column is decoded as its scalar element type. Pre-existing and
  orthogonal to the heap codec, but the new fixed-width uuid/interval arms make
  a wrong read look plausible rather than obviously broken.
- `interval[]` as an **index key** element: `decodeArrayKeyElemText`
  (`btree_array_key.go`) has no interval arm, so an interval array key is
  refused there while the heap now stores it faithfully — the same shape as the
  existing `timetz[]` row.
