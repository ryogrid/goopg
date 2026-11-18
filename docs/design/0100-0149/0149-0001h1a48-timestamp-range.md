# M0149\-0001h1a48 — the full PostgreSQL timestamp range

## Problem

A `KindTime` Datum keeps its instant as int64 nanoseconds since 1970 in `Datum.Int`, which
ends at 1677 and 2262. PG 18.3 accepts timestamps from 4714\-11\-24 BC to 294276 AD. Text input
is rejected outside goopg's range, and values produced by arithmetic or decoded from disk are not
checked at all.

## Target

`Datum.Int` holds microseconds since 2000\-01\-01, PG's own representation. Microseconds since
1970 would stop near 294247 AD, short of PG's limit. The infinity sentinels stay at the int64
extremes, which are also PG's values.

## Slices

| task | content | state |
|---|---|---|
| h1a48a | every unit\-dependent read goes through helpers in `datum.go` | done |
| h1a48b | switch the unit and epoch, keeping today's accepted range | done |
| h1a48c | replace `time.Duration` arithmetic that overflows past 292 years | open |
| h1a48d | BC and long\-year output in every formatter | open |
| h1a48e | parse long years, apply PG's bounds and errors, range\-check results, open the gate | open |

The gate opens last, so a value that cannot be handled correctly is never accepted.

## Slice h1a48a

`timeToCarrier`, `timeFromCarrier`, `timeCarrierUnitsPerSecond` and `timeTZCompareKey` are the
only code that knows the unit. Spill frames and the subplan hash key use `Int` directly, and
`timezone()` computes through microseconds of the day.

To test that nothing else depends on the unit, the helpers were switched to microseconds in a
throwaway worktree. Every executor, postmaster, optimizer and catalog test passed except
`TestExecCurrentTimestamp`, which compares `now()` at nanosecond precision and is expected to
change. The compiler cannot find such sites, because `Int` remains an int64.

## Slice h1a48b

`Datum.Int` is now microseconds since 2000\-01\-01, the value PG stores, so the heap codec
copies it directly instead of converting through `time.Time`. Digits below a microsecond are
dropped when a `time.Time` becomes a Datum. `timeFromCarrier` splits seconds with floor division
so an instant before 2000 keeps a non\-negative fraction.

The infinity sentinels stay at the int64 extremes. They convert to the same instants as before,
and those instants convert back to the sentinels, so code that passes an infinity through
`time.Time` still gets an infinity.

Hash partitioning was the one persisted use of the raw value: its fallback hashed the low 32 bits
of `Int`. `hashPartitionFallbackWord` keeps the former nanosecond word for time values, so rows
already stored stay in the partition a lookup computes. This is not PG's hash \(M0149\-0001h1a50\).

## Risks for later slices

These come from reading the code and have not been executed.

- `time.Time.Sub` saturates at about 292 years, and `time.Duration` multiplication wraps. Index key
  encoders, binary COPY, date subtraction and interval addition use them.
- Index keys for infinity are derived from the sentinel's `TimeValue`, so `TimeValue` must keep
  returning the old instants for sentinels until those encoders are changed.
- Index\-only decoding must remain the exact inverse of the key encoders.
- `eraDisplay` rebuilds a date with the display year, which looks wrong for a BC leap day.
- Dates after 294276 AD, which PG allows up to 5874897 AD, do not fit in microseconds. Slice h1a48e
  has to decide how to treat them.
