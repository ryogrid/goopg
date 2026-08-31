# 0119-0006 — Binary COPY of `time` / `timetz`

**Milestone:** M0119-0006 (deferral-ledger backlog consumption), 51st slice
**Status:** implemented (2026-08-13)
**Area:** `internal/executor/copy_binary.go`

## The gap

`COPY … WITH (FORMAT binary)` dispatches on the column's type name in two
sibling functions, `datumToCopyBinary` (send) and `copyBinaryToDatum` (recv).
Neither had an arm for `time` or `timetz`, so both types fell through to the
raw-bytes default. On the way out that default emits `Datum.Format()` — the
*text* rendering — under a format declared binary; the value `12:34:56.789012`
left goopg as 26 ASCII bytes where upstream ships 8. On the way back in, the
default hands the payload to `NewStringDatum`, so a real PG binary stream
decoded into a string.

The result was a stream no PostgreSQL client can read and no goopg reader can
round-trip — the same class of defect as the array case that
`rejectBinaryCopyArray` refuses by name, except silent.

## Upstream behaviour

`postgres/src/backend/utils/adt/date.c`:

| function | wire shape |
|---|---|
| `time_send` | `pq_sendint64(time)` — 8 bytes BE, the `TimeADT` microseconds since midnight |
| `time_recv` | 8-byte BE int64; **errors 22008 unless `0 <= t <= USECS_PER_DAY`**; then `AdjustTimeForTypmod` |
| `timetz_send` | `pq_sendint64(time->time)` then `pq_sendint32(time->zone)` — 12 bytes |
| `timetz_recv` | as above; same 22008 range check, plus 22009 when `|zone| >= TZDISP_LIMIT` (16 h, `datatype/timestamp.h:143-144`); then `AdjustTimeForTypmod` |

Two details are load-bearing and easy to get wrong:

- **The range bound is inclusive.** `USECS_PER_DAY` itself is a legal `TimeADT`
  — it is how `24:00:00` is represented — so a decoder that rejects it
  re-introduces the very value the 50th slice made storable.
- **`zone` counts seconds WEST of UTC**, the opposite sign from
  `Datum.Scale`'s east-of-UTC minutes. The heap `timetz` arm in `codec.go`
  already negates on both sides; this slice mirrors it rather than inventing a
  second convention.

## What landed

Four arms, matching the heap codec's twins one-for-one, plus a
`tzDispLimitSecs` constant citing upstream:

- send `time` / `time without time zone` → 8 BE bytes of `pgTimeMicros(...)`
- send `timetz` / `time with time zone` → those 8 bytes plus `int32(-TimeTZOffsetSecs())`
- recv both → range-checked, rebuilt via `pgTimeFromMicros`, tagged
  `NewTimeDatum` / `NewTimeTZDatum` so the value renders as its own type in
  every type-agnostic path

Taking the microseconds from `pgTimeMicros` — rather than from
`Hour()/Minute()/Second()` — is what carries the hour-24 rule (50th slice) into
binary COPY for free. That function is the single authority in
`internal/executor`; this slice adds a fourth consumer to it instead of a fourth
copy of the probe.

The spelled-out SQL names are accepted alongside the short ones because
`operators_ddl.go`'s canonicaliser emits `time without time zone` /
`time with time zone`, and the sibling dispatchers
(`btree_scalar_keys.go`, `pgindex_keydesc.go`) already accept both.

## Verification

- `internal/executor/copy_binary_time_test.go` — six tests, all confirmed
  failing against pre-fix HEAD (the send-shape ones report the 26-byte text
  payload; the encode↔heap agreement test reports the ASCII bytes read as an
  int64). They cover the wire shape, the hour-24 round trip, agreement with the
  heap encode's microseconds across four boundary literals, the timetz zone
  sign, both range checks including the inclusive `USECS_PER_DAY` bound, and
  the spelled-out type names.
- **Oracle E2E** on a capped throwaway server (5533) against PG 18.3 (65432)
  over `00:00:00`, `12:34:56.789012-07`, `23:59:59.999999+05:30`,
  `24:00:00-12` and NULL: `COPY … TO STDOUT WITH (FORMAT binary)` is
  **byte-identical** on the two engines, and PG's own stream loaded through
  goopg's `COPY … FROM … (FORMAT binary)` renders identically to PG's.

## Deferred (ledger rows filed)

- `AdjustTimeForTypmod` remains unported; the binary recv path is its third
  consumer. It also needs `copyBinaryToDatum`'s signature widened — that
  function receives only a `catalog.Type` and so cannot see the declared
  `time(N)` precision today.
- Binary COPY still has no arm for `int2`, `float4`/`float8`, `uuid`,
  `interval`, `jsonb`, `oid` or `bpchar`. `int2` is actively wrong (8 bytes
  where upstream sends 2), the rest ship text; only `text`/`varchar`/`bytea`
  are accidentally correct under the default.
