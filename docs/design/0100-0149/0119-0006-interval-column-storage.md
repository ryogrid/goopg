# 0119-0006 — the interval COLUMN gets PG's native 16-byte layout

Status: **landed** (2026-08-10)
Task: M0119-0006 (pg_amcheck server tier), interval-column-storage slice
Siblings: [0119-0006-interval-index-key-encoding.md](0119-0006-interval-index-key-encoding.md)
(the B-tree key, landed the loop before this one)

## The defect this slice closes

The previous slice taught goopg to build a B-tree index on an `interval`
column, keyed on upstream `interval_cmp_value`'s signed 128-bit span. Writing
it surfaced something worse than the missing index: **the column itself was
stored as text.**

`encodeValuePG` (`internal/executor/codec.go`) has one `case` per PG physical
type and a varlena `default:` arm. `interval` had no case, so it fell to the
default and goopg wrote the literal characters the user typed. Every runtime
operation on the column was therefore lexicographic. Captured from the PG 18.3
reference cluster on port 65432 against goopg at HEAD, same five rows
(`'1 mon'`, `'30 days'`, `'10 days'`, `'2 hours'`, `'400 days'`):

| query | PG 18.3 | goopg before |
|---|---|---|
| `ORDER BY i` | `02:00:00`, `10 days`, `1 mon`, `30 days`, `400 days` | `1 mon`, `10 days`, `2 hours`, `30 days`, `400 days` |
| `WHERE i > interval '10 days'` | 1, 2, 5 | 2, 4, 5 |
| `WHERE i = interval '30 days'` | 1, 2 | 2 |
| `min(i)` | `02:00:00` | `1 mon` |
| `SELECT i` for `'2 hours'` | `02:00:00` | `2 hours` |

Three distinct wrong answers and a wrong rendering. Note the shape of the
`>` row: it is not "a few rows out of order", it is a *different set* — the
predicate admitted `2 hours` and rejected `1 mon`.

With the index landed and the heap still text, the answer had become
**plan-dependent**: an index scan ordered by `interval_cmp_value` and a
sequential scan ordered by `strcmp`, so the same query returned different rows
depending on which path the planner chose. That is the state this slice ends.

## Why this was invisible until now

goopg already had every interval *mechanism*:

- `KindInterval` — a Datum carrying `(months, days, micros)`, PG's exact triple.
- `compareDatum`'s `KindInterval` arm — a faithful port of
  `interval_cmp_value` (`postgres/src/backend/utils/adt/timestamp.c`), including
  the ±infinity short-circuit.
- `formatInterval` — a byte-verified port of `EncodeInterval` under the default
  `postgres` IntervalStyle.
- `parser.ParseIntervalBody` — the `interval_in` tokenizer.

All four were reachable from *expressions* (`interval '1 day'`, `ts2 - ts1`) and
none of them from a *stored column*, because storage never produced a
`KindInterval`. The bug was a missing routing arm, not a missing algorithm —
which is exactly why it survived: every interval unit test in the tree passed,
because every one of them built its Datum in memory.

## What landed

PG's `Interval` is a fixed 16-byte, 8-byte-aligned struct
(`postgres/src/include/datatype/timestamp.h`):

```c
typedef struct { TimeOffset time; int32 day; int32 month; } Interval;
```

`time` (int64 µs) at offset 0, `day` at 8, `month` at 12. goopg's
`pg_type` seed has said `{OID 1186, typlen 16, typalign 'd', typbyval false}`
since initdb was written — so the catalog was already telling a PG standby to
read 16 fixed bytes while the heap held a varlena. This slice makes the heap
agree with the catalog rather than the other way round.

Five seams, all in the "one case arm per physical type" style the neighbouring
types already use:

1. **`encodeValuePG` `case "interval"`** — writes the three fields little-endian
   at PG's offsets. Accepts `KindInterval` (typed expression) *and* `KindString`
   (the bare `'1 mon'` literal, which is `unknown` upstream and reaches the
   column through `interval_in`); the text path goes through
   `parser.ParseIntervalBody`, the same tokenizer `'…'::interval` uses, so the
   two entry points cannot disagree. Unparseable text now raises `22007` where
   text storage silently accepted anything.
2. **`decodePhysicalPGValueMctx` `case "interval"`** — the sibling, returning
   `NewIntervalDatumFull`. This is the line that makes `compareDatum`'s existing
   `interval_cmp_value` port reachable for a stored value at all.
3. **`physicalPGTypeAlign`** — 8 (`typalign 'd'`).
4. **`pgPhysicalTypeIsVarlena`** — false (`typlen 16`). Also drives the
   `HEAP_HASVARWIDTH` infomask bit that PG18's `nocachegetattr` fast path
   asserts on.
5. **`tryParseStringAs` `case KindInterval`** — `i > '10 days'` has an unknown
   literal on one side. PG coerces it to `interval` in `transformExpr` before
   resolving the operator; goopg has no such pass, and while *both* sides were
   strings the comparison "worked" lexicographically. With the column now
   `KindInterval` the literal must be parsed here, or the pair falls through to
   the `Format()`-vs-`Format()` fallback — text comparison again, one level down.

The ±infinity sentinels need no special case. `INTERVAL_NOEND` / `INTERVAL_NOBEGIN`
*are* all three fields at their signed extreme, so field-wise storage
round-trips them exactly — verified, including `-infinity` sorting below every
finite interval after a restart.

### The formatter had to move

`formatInterval` lived in `internal/executor/datum.go`. The 16-byte layout has
**two** decoders — the executor's and the logical-replication row decoder
`pgoDecodePhysicalValue` in `internal/wal/pgoutput.go` — and `internal/wal`
cannot import the executor. Left unrouted, pgoutput would have read the
interval's microsecond field as a varlena length header and shipped garbage of
an arbitrary length to the subscriber, *without* an error. So the renderer moved
down to the leaf `internal/pgdatetime` package as `FormatInterval`, and both
decoders now call it; the executor keeps a one-line `formatInterval` wrapper so
its call sites are unchanged. Hard-won rule #2 (sibling paths change together).

## Verification

Every expectation below is PG 18.3 output captured from the reference cluster,
not hand-reasoned.

- **Oracle diff, byte-identical** — a mixed-column table
  (`int2, interval, text, int8, interval`) covering NULL, `-infinity`,
  `infinity`, `2147483647 days`, a negative mixed interval, `1 microsecond`,
  `UPDATE … SET i = i + interval '1 day'`, `ORDER BY … NULLS FIRST`, and
  `ORDER BY` on two different interval columns: goopg's psql output `diff`s
  clean against PG's.
- **Index path** — a 52-row table with `CREATE INDEX ON t(i)`: equality,
  range, `BETWEEN`, and `ORDER BY i LIMIT 5` all `diff` clean. The heap and the
  index now agree, which is the plan-dependence this slice was for.
- **Durability** — values written by `INSERT` and by `COPY` survive a stop/start
  and still compare as intervals afterwards.
- **Unit gates**, each shown non-vacuous under a source mutation (7 mutations,
  7 caught): `TestIntervalColumnUsesPGNativeStructLayout` (field offsets pinned
  directly — a day/month swap round-trips through goopg's own decoder and would
  otherwise pass), `TestIntervalColumnIsFixedWidthNotVarlena`,
  `TestIntervalColumnRoundTripsEveryFieldShape`,
  `TestIntervalColumnAcceptsUnknownLiteral`,
  `TestIntervalColumnRejectsUnparseableText`,
  `TestIntervalColumnComparesAsIntervalNotText`,
  `TestIntervalColumnComparesAgainstUnknownLiteral`,
  `TestIntervalColumnRowRoundTripKeepsNeighbourColumns`,
  `TestPgoDecodeIntervalMatchesPGNativeLayout`, `TestPgoPhysicalAlignInterval`.
- **Suite gates**: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
  `TestPort_RegressSuite` PASS (hard-won rule #5 — full regress port after a
  codec change).

One honest limit, recorded in the row test's own comment: `EncodeRowPG` and the
decode walker consult the *same* align/varlena predicates, so a matched pair of
wrong answers round-trips through goopg perfectly and diverges only from what a
PG reader computes from the TupleDesc. Those two values are therefore pinned
against `pg_type` directly, in `TestIntervalColumnIsFixedWidthNotVarlena`.

## What this does for the PG-standby story

`pgindex_keydesc.go` documents a class of defect it calls *heap-side
divergence*: a type whose `pg_type` row says one physical width while
`encodeValuePG` writes another, so "a real PG 18.3 reading a goopg user table
with such a column already misreads it". It names `numeric` and `uuid`.
`interval` belonged in that list and nobody had noticed — a PG standby reading a
goopg `interval` column would take a 16-byte window onto the stored text. This
slice removes `interval` from the class; `numeric` and `uuid` remain.

## Deferred (ledger rows, 2026-08-10)

1. **`interval(3)` typmod is not applied at storage.** PG's `interval_in` runs
   `AdjustIntervalForTypmod` and rounds the sub-second field to the declared
   precision — `'01:02:03.123456'` into `interval(3)` stores and prints
   `01:02:03.123`. goopg ignores `Type.Args` in the new encode arm and keeps all
   six digits. Resume point: `encodeValuePG`'s `case "interval"`, port
   `AdjustIntervalForTypmod` (`timestamp.c`) and apply it to `t.Args[0]`.
2. **`interval hour to minute` is a syntax error in a column-type position.**
   The field-qualifier grammar exists on the cast path (`expr.go` around the
   `Typmod != 0` interval branch) but the CREATE TABLE type parser rejects it
   (`42601`), so the range-qualified column type cannot be declared at all —
   which is also why deferral 1 above can only be observed through the
   `interval(N)` spelling. Resume point: the type-name parser in
   `internal/parser`.
3. **`interval[]` elements are still text.** The array path
   (`encodeArrayValuePG`) is a separate encoder and was not touched, so
   `c[1] = c[2]` on `ARRAY['1 mon','30 days']::interval[]` returns `f` where PG
   returns `t` — the same lexicographic defect this slice fixed for the scalar,
   one container down. Resume point: `encodeArrayValuePG` /
   `decodePGTextArrayElements` in `internal/executor/codec.go`.
