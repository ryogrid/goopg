# M0138-0007 — give `numeric` columns a real `avg_width`

Status: accepted (landed 2026-09-15)

## Task

Per `.ralph/fix_plan.md`'s M0138 milestone: M0138-0005's corpus re-measure
found that `datumVariablePayloadWidth`'s `KindNumeric` case
(`internal/executor/operators_analyze.go`) still returns a literal `0` for
every value on the int64 fast path — the mantissa fits inside `Datum.Int`, so
there is no `Buf`/arena payload to measure the way `KindString`/`KindBytes`
do. The `big.Int` overflow lane (`flagBigNumeric`) already returns a nonzero
byte count. `numeric` is PG's declared type for every TPC-H primary/foreign
key (`hammerdb_tpch_integer_keys_are_numeric`), so the fast path is exercised
by almost every numeric column in both bench corpora — 28/61 TPC-H and
17/120 TPC-DS columns report `avg_width=0`, the ledger row
(`.ralph/deferral_ledger.md`, `m0138-0005`) named at filing.

## What PG actually measures

`compute_scalar_stats`/`compute_distinct_stats` accumulate
`VARSIZE_ANY(DatumGetPointer(value))` for each *raw* sampled attribute Datum
(`postgres/src/backend/commands/analyze.c:2008,2124,2471`) — the full on-disk
varlena size, header included, of whatever the heap tuple actually stored.
That is **not** the typmod-derived worst case
(`numeric_maximum_size`/`NUMERIC_HDRSZ`, which is what
`internal/optimizer/relsize.go`'s `numericHeaderSize` constant already uses
for the planner's row-width *upper bound*) — it is the value's real encoded
size, which for a small on-heap numeric is:

- a 1-byte "short" varlena header (`VARHDRSZ_SHORT`,
  `postgres/src/include/varatt.h:255,257`) whenever the body is `<= 126`
  bytes (`VARATT_SHORT_MAX = 0x7F`, minus the header byte itself) — true for
  every fast-path value here, since it is bounded by int64;
- PG NumericData's own internal header, 2 bytes ("short" form) when
  `NUMERIC_CAN_BE_SHORT(dscale, weight)` holds
  (`postgres/src/backend/utils/adt/numeric.c:500-503`) or 4 bytes ("long"
  form) otherwise;
- 2 bytes per base-10000 digit actually stored — trailing all-zero digit
  groups are stripped (`strip_var`), so digit count tracks the value's real
  significant precision, not its declared scale.

## Oracle verification (not assumed — measured against live PG 18.3)

Before committing to this formula, the ledger's own guess
("`NUMERIC_HDRSZ` plus a digit-group count") was checked against the
TPC-H reference cluster (`:65432`) and found **wrong**: it uses the "long"
4-byte internal header and omits the 1-byte short-varlena wrapper the real
on-heap value gets, which overstates every column's width.

```
tpch=# select pg_column_size(l_quantity) from lineitem limit 1;   -- value 18, dscale 0
 5   -- 1 (short varlena) + 2 (short numeric hdr) + 1 digit * 2 bytes = 5
tpch=# select pg_column_size(l_extendedprice) from lineitem limit 1; -- value 27153.18
 9   -- 1 + 2 + 3 digits * 2 bytes = 9
tpch=# select avg_width from pg_stats where tablename='lineitem' and attname='l_extendedprice';
 8   -- corpus average of the 7-or-9-byte-per-row mix above, PG-reported
```

Both hand-derived values from the formula below reproduce PG's numbers
exactly (`internal/executor/operators_analyze_test.go`'s
`TestNumericFastPathOnDiskWidth`).

## Implementation

`internal/executor/operators_analyze.go`'s `datumVariablePayloadWidth`
`KindNumeric` fast-path arm now calls `numericFastPathOnDiskWidth(mantissa,
scale)`, which:

1. Formats the fast-path `(mantissa, scale)` pair to canonical decimal text
   via the existing `formatNumeric` helper (already used for `numericText`).
2. Feeds that text through `internal/nodes.NumericBodyFromText` — the same
   byte-exact `numeric_in`/`make_result` port `codec.go` already uses to
   encode the heap's on-disk numeric column form — to get PG's actual
   NumericData body (internal header + stripped digit array).
3. Wraps the body in the 1-byte-or-4-byte outer varlena header per the
   `VARATT_SHORT_MAX` threshold above.

Reusing `NumericBodyFromText` rather than re-deriving the base-10000
digit-grouping/stripping rule here keeps this arm and the heap-encode path
from drifting on ndigits/weight rules (`pattern_sibling_paths_must_agree`).

The `flagBigNumeric` arm is unchanged — it reports goopg's *internal*
arena-storage byte count (a sign byte plus `big.Int` magnitude bytes), which
is a different, pre-existing quantity from PG's on-disk NumericData size and
is out of this task's scope (no ledger row named it; M0138-0005's finding
was fast-path-only).

## Sibling-path check

`internal/executor/spill.go`'s `estimatedRowBytes` mirrors
`datumVariablePayloadWidth`'s `KindEnum`/`flagBigNumeric` arms for its own,
deliberately different purpose (in-memory spill-budget sizing, not on-disk
`avg_width`) and its own doc comment already states the two "differ by up to
5x" and are not meant to track exactly. Its `KindNumeric` fast-path arm
correctly stays at `+0`: the int64 mantissa genuinely adds no bytes beyond
the fixed `Datum` struct in memory. `TestEstimatedRowBytesCountsEnumAndBigNumeric`'s
cross-check loop had (by coincidence, both being `0`) also asserted equality
for the fast-path case; updated to exclude it with a comment explaining the
now-intentional divergence, since forcing the two rulers to agree here would
require faking either quantity.

## Gates

- `go build ./...` clean.
- `go test ./internal/executor/... ./internal/optimizer/...` — full package
  green, including the new `TestNumericFastPathOnDiskWidth` and the updated
  `TestDatumVariablePayloadWidth`/`TestEstimatedRowBytesCountsEnumAndBigNumeric`.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — green
  except the pre-existing, already-filed `internal/parser` `GroupedJoinUnaliased`
  AST-drift (60 test functions, unrelated to this change — see fix_plan.md's
  "Manually discovered" entry).
- `scripts/tpch-spotcheck.sh` — `RESULT=PASS`, Q12=2/Q13=34 (canonical
  anchors). `avg_width` does not currently gate any TPC-H/TPC-DS plan choice
  goopg's cost model reads at this scale (confirmed no plan/category shift by
  running the spot-check clean), so this is a pure statistics-fidelity fix,
  not a plan-shape change — corpus-wide re-measurement of any resulting
  movement is M0138-0005-style follow-up work, not required to land this fix.

## Ledger

`.ralph/deferral_ledger.md` row `m0138-0005` (avg_width numeric fast-path)
flipped to `resolved`, citing this doc.
