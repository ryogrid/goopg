# `interval` B-tree key encoding — a deliberately lossy key (M0119-0006)

Status: accepted
Date: 2026-08-10
Milestone/task: M0119-0006 (pg_amcheck server tier — the type cluster)
Closes: the `interval` row left open by
`docs/design/0119-0006-timetz-index-key-encoding.md` and its deferral-ledger row.

## The problem

`interval` was the last of the type cluster's named holdouts. At HEAD:

```
CREATE INDEX ON t (interval_col);
ERROR:  0A000: btree v0 only supports int4 / numeric keys, got "interval"
```

so an interval column could not be indexed at all — not as a secondary index,
not as a `PRIMARY KEY`, not as one column of a composite.

The timetz slice named the obstacle: `interval_cmp_value`
(`postgres/src/backend/utils/adt/timestamp.c`) collapses the three fields of an
`Interval` into one signed 128-bit span

```c
days  = interval->month * INT64CONST(30);
days += interval->day;
span  = int64_to_int128(interval->time);
int128_add_int64_mul_int64(&span, days, USECS_PER_DAY);
```

and `interval_cmp` compares nothing else. So a key exists — but it is *lossy*,
and the earlier slices had treated "the decode sibling must exist" as a
precondition for adding an encoding.

## The decision

**The key is the span alone, and the loss is the correct behaviour, not a
shortcut.**

Because upstream compares only the span, two intervals with the same span are
*equal to PostgreSQL* even when their fields differ. Captured from the PG 18.3
reference cluster (port 65432):

```
SELECT '1 mon'::interval = '30 days'::interval;   -- t
SELECT i FROM iv ORDER BY i;
 -infinity | -1 mons | -00:00:00.000001 | 00:00:00 | 1 day | 1 day 00:00:01
 | 29 days 23:59:59 | 1 mon | 30 days | 2 mons | 1 year | 365 days | infinity
```

A key that preserved the fields — say `span ++ month ++ day ++ time` — would
pass an ordering test and still be the *wrong* key three ways: it would order
values PG calls equal, it would let a `UNIQUE` interval index accept a duplicate
PG rejects, and it would make an index probe for `'30 days'` miss a stored
`'1 mon'`. Faithfulness therefore *requires* the many-to-one image.

`interval` is thus the first goopg index key with no decode sibling, and that is
a property to be enforced rather than a gap to be filled later.

## What landed

`internal/access/btree/btree.go`:

- `EncodeInt128(hi int64, lo uint64)` — the 16-byte order-preserving form of a
  signed 128-bit value, the same sign-bit flip `EncodeInt4`/`EncodeInt8` use,
  applied to the high half only (the low half is a pure magnitude continuation
  whose unsigned big-endian order is already the right tiebreak). 128 bits is
  not optional and is why upstream uses `INT128`: the day total reaches
  `2^31*30 + 2^31 ≈ 6.4e10` and scaling it by `USECS_PER_DAY` overflows int64.

`internal/executor/btree_interval_key.go` (new):

- `intervalKeyParts` — the two runtime shapes an interval arrives in.
  `KindString` is the one that actually reaches **both** stored-key writers:
  goopg holds an interval column as *text* (the codec has no interval branch, so
  a stored row decodes to `KindString`), and an unknown-literal probe
  (`WHERE i = '30 days'`) is a string too. `KindInterval` arrives from
  expression evaluation (`interval '1 day'`, timestamp subtraction). Text is
  parsed with `parser.ParseIntervalBody`, the *same* entry point `'…'::interval`
  uses (Hard-won Rule #2) — including the `infinity` sentinels — so a value
  goopg can store as an interval is a value this key can encode.
- `intervalSpan128` — a transcription of `interval_cmp_value`, month/day
  combined in int64 exactly as upstream does ("Because the inputs are int32,
  int64 arithmetic suffices here"), only the µs scaling widened, negative day
  totals handled by a two's-complement negation of the 128-bit magnitude.
- `encodeIntervalBTreeKey` — 16 bytes; unparseable text raises PG's `22007`
  rather than indexing something else (the bulk build surfaces that error, but
  the runtime maintain path *swallows* key-encode errors by design, so a silent
  success would leave a permanently under-populated index — the failure mode the
  array slice found).
- `intervalKeyNotDecodable` — the refusal both decode siblings return.

The infinity sentinels need no special case, unlike the timestamp ones: PG
spells `INTERVAL_NOEND`/`NOBEGIN` as the field extremes
(`postgres/src/include/datatype/timestamp.h`), so their spans are already the
largest and smallest any interval can produce, and the plain ordering puts them
at the ends — where the reference cluster puts them.

Routing (three seams, matching the timetz slice): `encodeScalarBTreeKey`,
`decodeScalarBTreeKey`, `isSupportedBTreeKeyType`. No `coerceScalarKeyStringDatum`
arm is needed because the encoder itself accepts text.

### Why the decode arm is not optional

It would be tempting to simply *not* route interval on the decode side. That is
the exact latent misread the array-decode slice found at HEAD: both siblings
share a `default:` arm that reads any 8 leading bytes as an enum float8 and
never errors. An unrouted 16-byte interval key would therefore decode to a bogus
enum *and* consume half the key, desynchronizing every later column of a
composite walk. Refusing is what re-arms the callers' fallbacks:

- the amcheck operator-class comparator falls back to byte order for the column,
  which *is* the interval ordering (missed detection only, never a false
  positive);
- the index-only scan declines its decode-from-key fast path.

### The index-only-scan seam

`indexOnlyScanOp.indexKeyIsDecodable` (`operators_indexonly.go`) is new. The IOS
fast path decodes the row from the key when the heap page is `ALL_VISIBLE`;
with an interval key there is nothing to decode, and letting `decodeRowFromKey`
error would fail the whole query — confirmed by mutation:

```
XX000: IOS decode: btree: interval key is the comparison span
       (interval_cmp_value) and cannot be decoded back to month/day/time
```

So the scan reads the heap instead. That is always correct — the heap path is
what `ALL_VISIBLE` is an optimization *over* — and it is decided once per scan,
since decodability is a property of the index's column types. The whole index is
declined rather than the single column because the composite walk decodes in
order and cannot skip a column whose byte width it does not know. Cost: an
interval index reports non-zero `Heap Fetches` where PG can answer index-only
(PG's index tuples hold real datums). Ledger row.

## Verification

`internal/executor/btree_interval_key_test.go`, all mutation-verified:

| gate | pins |
|---|---|
| `TestEncodeIntervalBTreeKeyMatchesPGOrder` | the 12-value reference-cluster ordering, incl. `29 days 23:59:59 < 1 mon` and `1 year < 365 days` |
| `TestIntervalKeyIsTheComparisonSpan` | equal spans encode to *identical* bytes (`1 mon`/`30 days`, `1 year`/`360 days`, `1 day`/`24:00:00`) |
| `TestIntervalKeyStringAndIntervalDatumAgree` | probe symmetry across `KindString` and `KindInterval` |
| `TestIntervalKeyRejectsUnparseableText` | `22007`, not a silent key |
| `TestIntervalKeyDecodeIsRefused` | both decode siblings refuse |
| `TestIntervalIndexBuildAndMaintainKeys` | both stored-key writers, physical `btree.RangeScan` counts and bytes |
| `TestIntervalCompositeKeyIsSelfDelimiting` | fixed 16-byte width in a non-final composite position |
| `TestIntervalIndexOnlyScanReadsHeap` | the IOS decline, over an actually-`ALL_VISIBLE` page |

Mutations that each turned a gate red: removing the IOS gate; removing the
decode refusal (both siblings then *silently* returned an enum); dropping the
`month*30` term; appending a month tiebreak to the key; removing the 128-bit
negation.

## Deferred (ledger rows, same date)

1. **Interval columns compare as TEXT at runtime.** goopg stores an interval
   column as text and the evaluator compares the stored strings, so on the
   seq-scan path `WHERE i > '10 days'` drops a stored `'1 mon'` and `ORDER BY i`
   is alphabetical. This slice makes the *index* order correct, which makes the
   answer plan-dependent: with an index, `WHERE i = '30 days'` now returns the
   stored `'1 mon'` too (PG-correct); without one it does not. The defect is the
   text comparison, not the key — but it is now visible and is its own slice.
2. **No index-only scan for interval indexes** — the fast path is declined, see
   above; it becomes available when index tuples carry per-attribute datums
   (the M0130-S11.4 direction).
3. Still open in the type cluster: `box` and `int4range` key encodings, the
   posting-list duplicate coverage in the `checkunique` tier, and the
   whole-database (unscoped) `pg_amcheck` run.
