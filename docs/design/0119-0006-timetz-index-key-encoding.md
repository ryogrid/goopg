# `timetz` B-tree key encoding — a two-part key is still one ordered key (M0119-0006)

Status: accepted
Date: 2026-08-10
Milestone/task: M0119-0006 (pg_amcheck server tier — the type cluster)
Supersedes: the `timetz` refusal recorded by
`docs/design/0119-0006-scalar-index-key-encodings.md` and its deferral-ledger row.

## The claim being retracted

The int2/oid/bool/bytea/time slice added five key encodings and explicitly
declined a sixth:

> `timetz` is deliberately NOT here: its comparison is two-part
> (`timetz_cmp_internal` compares time-minus-zone first and only then the zone
> itself), so a single ordered key column cannot represent it.

The premise is right and the conclusion is wrong. Upstream
(`postgres/src/backend/utils/adt/date.c`):

```c
timetz_cmp_internal(TimeTzADT *time1, TimeTzADT *time2)
{
    /* Primary sort is by true (GMT-equivalent) time */
    t1 = time1->time + (time1->zone * USECS_PER_SEC);
    t2 = time2->time + (time2->zone * USECS_PER_SEC);
    if (t1 > t2) return 1;
    if (t1 < t2) return -1;
    /* If same GMT time, sort by timezone ... */
    if (time1->zone > time2->zone) return 1;
    if (time1->zone < time2->zone) return -1;
    return 0;
}
```

Both parts are fixed-width integers, and each has an order-preserving key
encoding goopg already ships (`btree.EncodeInt8`, `btree.EncodeInt4`). A
lexicographic comparison of `EncodeInt8(gmt) ++ EncodeInt4(zone)` therefore
*is* `timetz_cmp_internal`: the first eight bytes decide unless they are equal,
in which case the next four do — the same structure that makes a two-column
composite index key work. Nothing about "more than one part" is an obstacle.

What actually is an obstacle is a part that is not *individually*
order-preserving, or a comparison that is not a lexicographic refinement at
all. `interval` is the real instance of that in this cluster and stays open:
`interval_cmp_value` (`utils/adt/timestamp.c`) collapses months/days/micros into
one 128-bit span, so `'1 mon'` and `'30 days'` compare EQUAL — a key can be
built (a 16-byte biased span) but it is lossy, so it cannot be decoded, and the
decode side is what index-only scans and amcheck's comparator walk need. That is
a distinct problem from this one and gets its own slice.

## What landed

`internal/executor/btree_scalar_keys.go`:

- `isTimeTzType` — `timetz` / `time with time zone`. Deliberately disjoint from
  `isTimeOfDayType`: if the plain-time predicate claimed `timetz` the key would
  be 8 bytes of local time-of-day and the zone would drop out of the ordering
  entirely, which is a silently wrong index rather than a refused one.
- `timeTzKeyParts` — splits a timetz `Datum` into the two quantities upstream
  sorts by, **in PG's units and sign convention**.
- `encodeTimeTzBTreeKey` — `EncodeInt8(gmtMicros) ++ EncodeInt4(pgZone)`,
  a fixed 12 bytes.
- an `encodeScalarBTreeKey` arm, a `decodeScalarBTreeKey` arm (12 bytes,
  inverting back to a `NewTimeTZDatum`), and a `coerceScalarKeyStringDatum` arm
  so a probe literal (`WHERE k = '12:00:00+02'`, which arrives as `KindString`
  because a bare literal is typed `unknown`) encodes to the same bytes the
  stored row did.

`internal/executor/operators_ddl.go`: `isSupportedBTreeKeyType` accepts it, so
`CREATE INDEX ON t(timetz_col)` and `PRIMARY KEY (timetz_col)` stop raising
`0A000 btree v0 only supports int4 / numeric keys`.

## The sign convention is the load-bearing detail

Upstream `TimeTzADT.zone` is **seconds WEST of UTC**. goopg's carrier is the
opposite: `NewTimeTZDatum(t, offsetSecs)` stores minutes **EAST** of UTC in
`Datum.Scale`, and `TimeTZOffsetSecs()` returns seconds east. So the encoder
negates:

```go
pgZone = int32(-v.TimeTZOffsetSecs())
gmtMicros = pgTimeMicros(v.TimeValue()) + int64(pgZone)*1_000_000
```

Getting that backwards does not break the primary part — it is symmetric there
by accident, since the local time compensates — but it reverses every tie.
Captured from the PG 18.3 reference cluster (port 65432), the three
same-instant values sort:

```
13:00:00+01   (zone -3600 west)
12:00:00+00   (zone     0)
11:00:00-01   (zone +3600 west)
```

Ordering by seconds-east would emit those in exactly the reverse order. This is
asserted directly, on same-instant values, rather than only through the
whole-ordering table — a table can pass while the tie-break is inverted if no
two of its values are the same instant.

The captured oracle ordering used by the tests, in full:

```
00:00:00+14  00:00:00+00  12:00:00+05:30  13:00:00+01
12:00:00+00  11:00:00-01  00:00:00-12     23:59:59.999999-12
```

`00:00:00+14` sorting *first* pins the primary part: its local clock reads
midnight, but GMT-equivalent it is 10:00 the previous day, so the primary key
part is negative.

## Decoding, and why it is required rather than optional

Both key-decode siblings — `decodeIndexKeyColumn` (the composite walk that
amcheck's opclass comparator uses) and `decodeBTreeKeyToDatum` (single-column
index-only scans) — route through `decodeScalarBTreeKey` *before* their own
type switches. That routing is not tidiness: their shared `default:` arm reads
any 8 leading bytes as an enum float8 and never errors, so an unrouted 12-byte
timetz key would decode as a plausible-looking enum and, in the composite walk,
consume 8 bytes instead of 12 — desynchronizing every later key column. That is
precisely the failure the ARRAY decode slice found at HEAD, so the arm is added
in the same change as the encoder rather than deferred (Hard-won Rule #2).

The inversion is exact for everything the heap can carry: `local = gmt −
zone·10⁶`, `offsetEast = −zone`. One pre-existing carrier limit rides along:
`Datum.Scale` holds the offset in whole MINUTES, so a sub-minute zone offset
(historical LMT oddities) is already lost by the heap codec before a key is ever
built — the decode is faithful to what was stored, not to what PG could store.

## Gates

All in `internal/executor/btree_scalar_keys_test.go`; the shared
`scalarKeyCases()` table gains a `timetz` row, so the four table-driven gates
cover it automatically:

| gate | what it pins |
|---|---|
| `TestEncodeScalarBTreeKeyMatchesPGOrder/timetz` | the captured PG 18.3 ordering |
| `TestScalarBTreeKeyProbeMatchesStoredKey/timetz` | probe literal ≡ stored key bytes |
| `TestScalarBTreeKeyDecodeSiblingParity/timetz` | both decode siblings, and the exact 12-byte width |
| `TestScalarIndexBuildAndMaintainKeys/timetz` | both stored-key writers (CREATE INDEX bulk build and the runtime INSERT maintain path) over the physical tree |
| `TestTimeTzIndexKeyIsTwoPart` | the tie-break direction, on same-instant values; the 12-byte shape; and that `isTimeOfDayType` does not claim timetz |
| `TestTimeTzCompositeKeyIsSelfDelimiting` | timetz in a non-final composite position: ordering, and that the composite WALK resynchronizes so the trailing column decodes correctly |

Non-vacuity, each confirmed by mutating the source and observing the named
failures:

- zone sign flipped to seconds-east ⇒ `TestEncodeScalarBTreeKeyMatchesPGOrder`,
  `TestScalarBTreeKeyDecodeSiblingParity`, `TestScalarIndexBuildAndMaintainKeys`,
  `TestTimeTzIndexKeyIsTwoPart` all FAIL.
- `isTimeTzType` removed from `isSupportedBTreeKeyType` ⇒
  `TestScalarIndexBuildAndMaintainKeys`, `TestTimeTzIndexKeyIsTwoPart` FAIL.
- decode arm disabled (key falls to the shared enum-float8 `default:`) ⇒
  `TestScalarBTreeKeyDecodeSiblingParity` FAILs.
- decoded width reported as 8 instead of 12 ⇒
  `TestTimeTzCompositeKeyIsSelfDelimiting` FAILs.

## Still open in this cluster

- `interval` — see above; a lossy 128-bit span key, which needs a decision about
  what the decode siblings do with a non-invertible key before it can land.
- `box`, `int4range` — no key encoding; `box_ops` is not even a total order in
  the B-tree sense (`box` has no default btree opclass upstream), and range
  types compare lower-bound-then-upper with infinite/exclusive flags.
- `timetz` ARRAY elements: `decodeArrayBTreeKey` renders elements per type and
  has no timetz arm, so a `timetz[]` key column encodes but does not decode.
  Unreachable in practice for the same reason the other element gaps are (the
  heap array codec does not store them), and recorded in the ledger.
