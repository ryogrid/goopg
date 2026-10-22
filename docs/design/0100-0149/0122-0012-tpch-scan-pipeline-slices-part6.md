# M0122-0012 — scan pipeline slices, part 6 (TPC-DS executor gaps)

Continues [0122-0012-tpch-scan-pipeline-slices-part5.md](0122-0012-tpch-scan-pipeline-slices-part5.md) (slices
46–57). It was split out on 2026-10-22 under the 800-line design-doc limit; slice numbering is unchanged.

**How this part measures.** Targets come from the per-query gap against the `:65438` reference, timed plainly:

- `explain (analyze, timing on)` inflates PG's Q97 from 0.12 s to 0.56 s on this host, so timed plans are not
  compared.
- On the goopg side, the A/B instrument is per-query wall time plus the `/proc/<pid>/stat` utime+stime delta over
  N runs (`tmp/ds-perf.sh`). The Go CPU profiler under-samples parallel queries here (slice 57).
- `perf record -g -p <pid>` attributes samples. Splitting them by stack (`runPostStartupLoop` = the leader,
  `runWorker` = a parallel worker) shows where the serial part of a parallel plan goes.

## Slice 58 (2026-10-22): the serial leader path of a sorted Group stops copying and allocating per row — TPC-DS Q97

**Target.** TPC-DS Q97, 0.56 s against PG's 0.12 s. Q97 is a FULL JOIN of two CTEs. Each CTE is a sorted Group
over a Gather Merge of parallel sorts of a hash join, and an aggregate of three CASE sums sits on top. A `perf -g`
profile split by stack showed the leader busy for the whole query, about 530 ms of samples per run, while each
worker was busy about 150 ms. The leader is the wall-time bottleneck. Excluding its own parallel-sort share,
its innermost-operator attribution was:

- the Project between each Gather Merge and its Group: 1041 samples;
- Gather Merge: 1003;
- the sorted Group (`aggregateOp.nextSorted`): 937;
- the merge join's input sources: 735;
- the final aggregate's CASE evaluation: 586.

Two of these did work no other path does:

- **The Project's own frame.** `projectOp.Next`'s hot instructions were 48-byte Datum copies bouncing through a
  stack slot. Each bare-column target went through `evalExprSlot`, whose by-value return is stored and reloaded
  — the store-forwarding stall slice 22's in-place deform removed from the scan.
- **The sorted Group's key.** `nextSorted` called `evalGroupExprs` per input row. That allocates a values row and
  a parts slice and builds one `datumKey` string per column (`slicebytetostring`), only to compare the parts
  with the current group's.

**Change.**

- **Bare columns are copied in place.** `projectOp` maps each bare `ColumnRef` target to its input column
  (`projectColRefs`, filled on the first `Next`). Over a `*MaterializedSlot` it copies `row[c]` straight into the
  output cell, as `evalExprSlot`'s ColumnRef arm would. An out-of-range index still takes that arm, so its
  error is unchanged. The deform-poison check runs when armed.
- **The sorted Group compares keys without allocating.**
  - `nextSorted` evaluates the grouping values into the hashed path's scratch (`evalGroupValsScratch`).
  - `sameSortedGroup` compares each column's key with the group's stored part through one reused buffer.
    `appendGroupColKey` (factored out of `appendGroupKey`) writes datumKey with the gkTrims bpchar strip, and
    `string(buf) == part` allocates nothing.
  - Values are materialized out of the input arena, and the parts kept, only at a group boundary.
  - The decision is `sameGroupKey`'s, column by column.

**Pins.**

- `TestProjectColumnRefCopyMatchesEval`: a Project of reordered, repeated and computed targets over NULL and
  string rows equals `evalExprSlot` per cell. An out-of-range column raises `evalExprSlot`'s error. Mutation
  check: copying the wrong column fails it.
- `TestSameSortedGroupMatchesSameGroupKey`: over NULL, bool, int, numeric, big numeric, padded and empty strings,
  bytes, time and interval values with random gkTrims, `appendGroupColKey` equals the part `evalGroupExprs`
  built, and `sameSortedGroup` equals `sameGroupKey`. Both answers occur over 500 times each. Mutation check:
  ignoring the trim rule fails it.
- The executor package passes with the deform poison armed.

**Measured.** CPU per query from `/proc`, 2GB clone, interleaved against `49da13013`:

| run | HEAD CPU-s | candidate CPU-s |
|---|---|---|
| Q97, parallel (3 rounds × 15 runs) | 1.10–1.10 (one 1.27 cold round) | 1.05–1.07 |
| Q97, serial (`max_parallel_workers_per_gather = 0`, 4 rounds) | 0.91–0.94 | 0.83–0.89 |

That is about −3.5 % parallel and −6 % serial. Wall time on this host scatters ±10 % run to run and does not
resolve the difference. Q51 and Q87 are flat. The sf025 sweep sum went from 110.7 to 108.6 s. Values are
identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Tried and dropped.**

- `sortOp`'s `sort.SliceStable` (insertion sort plus symMerge through a reflected swapper; `rotate`, `symMerge`
  and `reflectlite.Swapper` showed in the profile) was replaced by a pdqsort with an index tiebreak. That gives
  the same sequence, pinned against SliceStable. Q87 got ~8 % slower: its presorted runs make SliceStable's
  insertion blocks near-linear, and the tiebreak removes pdqsort's equal-key shortcut.
- `slices.SortStableFunc` (the same stable algorithm without reflection) was flat on Q97, Q87 and Q51. Sorting is
  not Q97's cost.

**Left.**

- Q97 is still ~4.5× PG.
- The leader's remaining serial work: Gather Merge's heap and its per-row re-evaluation of sort keys through
  `evalExprSlot`; the merge join's input sources (`mergeSortedSource.fill` / `keyRow`, which buffer and re-key
  rows the Group already ordered); and the final aggregate's CASE, whose `IS NULL` / `AND` arms fall back from
  `evalFastExpr` to the interpreted `evalExprSlot`.
- PG's leader runs this chain in under 0.12 s.

## Slice 59 (2026-10-22): IS NULL compiles, and the sort and merge comparators take the same-kind fast path — TPC-DS Q97

**Instrument.** Host load (other agents' builds, load average 7–10) moved Q97's per-query CPU by ±10 % between
identical rounds, which hides a few-percent change. From this slice the A/B also counts **user-mode
instructions retired per query**: `perf stat -e instructions:u -p <pid>` across a fixed number of runs
(`tmp/ds-instr.sh`). Repeat rounds agree to ±0.005 G, and builds that differ only off a query's path give the
same count, so unrelated sweep swings can be checked directly.

**Target.** TPC-DS Q97, the leader-serial chain of slice 58. An instruction-sampled profile of Q97 run serially
split as follows:

- About 20 % sat in the sort comparator: `sortOp.lessKeyVals` → `compareDatumTyped` → `compareDatum` →
  `promoteCrossKind`. Every integer key pair went through the generic cross-kind path.
- The Gather Merge heap (`mergeKeysLess`) and the merge join's key comparison (`compareMergeKeys`) take the
  same route.
- The final aggregate's `CASE WHEN a IS NOT NULL AND b IS NULL` arms ran `IsNullExpr` through an `ExprAdapter`,
  i.e. the interpreted `evalExprSlot`, for every row. The compiled slab has no NullTest kind.

**Change.**

- **Comparator fast path.** `sortOp.lessKeyVals`, `mergeKeysLess` and `compareMergeKeys` first try
  `fastCompareDatum`, the compiled comparison's existing same-kind int / time / non-big numeric shortcut.
  `TestFastCompareDatumMatchesCompareDatum` pins it equal to `compareDatum` whenever it answers. Both
  `compareDatumTyped` and `compareDatumPlain` reduce to `compareDatum` for every non-string pair, and
  `fastCompareDatum` declines strings. Everything else takes the old call, so NULL placement, DESC and errors are
  untouched. `sortOp.lessRows`, the defensive fallback, is left alone.
- **ExprNullTest.** `IsNullExpr` compiles to `ExprNullTest`: the compiled operand, then
  `isNull != negated`, which is exactly the interpreted scalar arm. It never yields NULL, and an operand error
  propagates. A `RowExpr` operand keeps the adapter, because its field-wise rule (`evalRowNullTest`) is not the
  scalar test. The batch evaluator reaches it through its per-row fallback.

**Pins.**

- `TestNullTestCompiledMatchesInterpreted` compares IS NULL / IS NOT NULL over int, text and computed operands,
  alone, under AND, and inside a Q97-shaped CASE, on the per-row and batch evaluators, with the interpreted
  `evalExpr`. It also checks that a row-valued IS NULL stays on the adapter and still matches. Mutation check:
  flipping the negation fails it.
- The comparator fast path rests on `TestFastCompareDatumMatchesCompareDatum`. The executor's
  sort / merge / gather tests pass.

**Measured.** Instructions per query (load-insensitive), interleaved against `0f365344a`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q97 serial | 7.882–7.887 | 7.068–7.071 (the IS NULL compile alone: 7.788) |
| Q97 parallel | 8.587 | 7.724–7.729 |
| Q51 | 17.28 | 16.18 |
| Q87 | 27.23 | 27.06 |
| Q16, Q72, Q23, Q95, Q1 | — | identical |

That is −10 % on Q97 and −6.4 % on Q51. `/proc` CPU agrees: Q97 went from 1.09–1.19 to 0.94–1.02 CPU-s and Q51
from 2.20–2.25 to 2.08–2.10. The sf025 sweep sum went from 108.6 to 109.9 s. That came from Q1, Q72, Q23 and Q95
rising and Q74, Q39 and Q9 falling, all by seconds, and the risers' instruction counts are unchanged, so it is
host load. Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Left.**

- Q97's leader still runs the merge join's `mergeSortedSource` re-keying of rows the Group already ordered.
- Gather Merge still re-evaluates sort keys per row through `evalExprSlot`.
- The `sortOp` stable sort still uses the reflected swapper. It is ~3 % of Q97's instructions; slice 58's
  `SortStableFunc` attempt was flat on wall time, but this instrument could now resolve it.
- `exprnode.go`'s header comment still lists only the original six compiled kinds.

## Slice 60 (2026-10-22): string sort keys compare plainly, and DISTINCT ON keys stop allocating per row — TPC-DS Q87, Q38

**Target.** TPC-DS Q87, 0.95 s against PG's 0.31 s. Its three branches each take the DISTINCT
`(c_last_name, c_first_name, d_date)` of a sales ⋈ date_dim ⋈ customer join, through a Sort and a Unique
(`distinctOnOp`), and two EXCEPTs combine them. Q38 is the same shape with INTERSECT. An instruction-sampled
profile at `5d6e91613` showed two costs:

- **String sort keys.** Every comparison of two strings ran `compareDatumTyped`, whose `textShapeAmbiguous`
  check tests both values for row, array, pg_lsn and uuid shapes (1.6 %, plus `StringValue` 1.5 %). The values
  then went through `compareDatum`'s generic path.
- **DISTINCT ON's key.** `distinctOnOp.Next` built each input row's key as a fresh string, `key +=
  datumKey(v) + "\x00"` per column, with `trimStringDatum` for bpchar columns (`TrimRight` 2 %,
  `trimStringDatum` 1.4 %, plus the allocations). It then compared that string with the previous key.

**Change.**

- **String key fast path.** `sortOp` keeps `keyText[i]`, set by `newSortOp` through `sortKeyTextFlags`, which
  records whether the key's static type is a character string. When `fastCompareDatum` declines a pair and
  both values are `KindString` for such a key, `lessKeyVals` uses `strings.Compare`. That is
  `compareDatumTyped`'s answer whatever the values' shape:
  - an ambiguous pair takes `compareText`;
  - otherwise `compareDatum`'s KindString arm reaches the same `strings.Compare`. Its shape rules are exactly
    the ones `textShapeAmbiguous` tests, and `promoteCrossKind` leaves same-kind pairs alone.

  `compareMergeKeys` takes the same shortcut from its existing `text` flags. A key that is not a character
  string keeps the shape rules. Arena strings are `KindString` since M0107-0002, so they are covered. The int
  fast path runs first, so integer keys pay nothing extra.
- **DISTINCT ON key buffer.** `appendDistinctOnKey` appends the same bytes into a reused buffer. It uses
  `appendDatumKey`, whose equality to `datumKey` is already pinned, and an inline `"s:"` + `TrimRight` for a
  trimmed `KindString`, which is `trimStringDatum`'s result. `Next` compares `string(buf)` with the previous key,
  or looks it up in the hashed set, without allocating; only a row that starts a new key allocates.
- **Housekeeping.** `exprnode.go`'s header now lists every compiled kind (left over from slice 59).

**Pins.**

- `TestSortStringKeyFastPathMatchesCompareDatumTyped` runs every pair of 16 strings: plain, padded, row-,
  array-, pg_lsn- and uuid-shaped. For a text key and a record key, ASC and DESC, `lessKeyVals` must match
  `compareDatumTyped` and `compareMergeKeys` must match `compareDatumPlain`. Mutation check: taking the
  shortcut for every key fails it on the record key's uuid normalisation.
- `TestDistinctOnKeyMatchesConcatenatedDatumKeys`: over 3000 random rows of nine value kinds, with and without
  trims, and with an out-of-range key column, the buffer's bytes equal the old concatenation. Mutation check:
  dropping the trim fails it.

**Measured.** Instructions per query against `5d6e91613`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q87 | 27.06 | 24.01–24.12 (−11 %) |
| Q38 | 26.90 | 23.86 (−11 %) |
| Q35 | 12.81 | 12.48 (−2.6 %) |
| Q97 | 7.73 | 7.755 (+0.3 %) |
| Q51 | 15.75 | 15.77 (+0.1 %) |

The +0.3 % on Q97 and +0.1 % on Q51 persisted after the integer fast path was moved first; they are integer-key
queries, so this is layout cost. The sf025 sweep sum went from 109.9 to 105.8 s (Q87 1.01 → 0.84 s, Q38 0.87 →
0.83 s). Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Left.**

- Q87 is still ~3× PG.
- `mergeKeysLess` (Gather Merge) still takes `compareDatumTyped` for strings, because it has no per-key text
  flag.
- `distinctOp` (plain DISTINCT) still hashes a `rowKeyTrimmed` string per row and sorts through
  `compareDatumPlain`.
