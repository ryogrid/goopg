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
