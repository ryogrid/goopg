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

## Slice 61 (2026-10-22): descents start from a cached root, and scans stop building the descent path — TPC-DS Q87 (below the 3 % bar)

**Target.** After slice 60, index probes were 28 % of Q87's instructions: the nested-loop probes into
`customer_pkey`. Per probe, `descendMode` did two things PG does not:

- **A metapage read every time.** `readMeta` pinned the metapage, validated it and decoded it on every descent,
  about 1.5 %. PG's `_bt_getroot` descends from `rd_amcache`, a cached copy of the metapage, and re-reads it only
  when the cached root proves stale.
- **A descent path nobody read.** Every internal block was appended to `path`, so the slice grew and allocated
  on each probe (`growslice` about 1.3 %). Only writers (split propagation) and vacuum read that path, never a
  scan.

**Change.**

- **Cached root.** `BTree.rootCache` packs (level, block) atomically, with 0 meaning uncached. `readRoot` fills
  it whenever the metapage is read. A descent starting from the cache pins the page and requires
  `rootCacheValid`: `BTRoot` set, not deleted or half-dead, at the cached level, and with no left or right
  sibling. Otherwise it drops the cache and re-reads the metapage, as is an unreadable cached block.
  - A root that has split gets a right sibling and loses `BTRoot`.
  - Requiring the true root, rather than PG's fast root, keeps a writer's path complete up to the top for
    `getStackBuf`.
- **No path for scans.** `descendMode` takes `needPath`. `descendToLowerBound`, the scan entry, passes false;
  `descend` (inserts, `descendToLeaf`, vacuum) passes true.

**Pins.** `TestRootCacheTracksTheRoot` runs three stages, and in each, lookups (`Search`) and a cursor scan must
stay exact, while the cache must equal the metapage's root and level afterwards:

1. A fresh 3000-key tree, whose root is internal.
2. The cache poisoned with the rightmost leaf, from which right links can never reach key 0.
3. The pre-split root held in the cache across inserts that grow the tree a level, then 500 more inserts made
   through the stale cache.

A full scan must then return every key in order. Mutation check: a `rootCacheValid` that accepts anything
makes `Search(0)` miss. The nbtree package passes under `-race`.

**Discovery (filed, not fixed).** Growing that tree past three levels trips the existing `walkPivotInvariants`
check, "internal item 0 natts = 1, want 0", with the cache disabled too. `_bt_split` makes the first data item of
a split-created right internal page a zero-attribute (minus infinity) pivot; goopg keeps its key. Searches are
unaffected, because keys stay ordered, but the page differs from PG's on disk. The existing pivot tests only build
two-level trees. Filed as fix_plan M0131-S33 with a deferral-ledger row.

**Measured.** Instructions per query against `fdb607c10`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q87 | 24.12 | 23.79 (−1.4 %) |
| Q38 | 23.96 | 23.65 (−1.3 %) |
| Q72 | 29.23 | 28.62 (−2.1 %) |
| Q14 | 162.5 | 159.8 (−1.6 %) |
| Q16 | 13.90 | 13.87 (−0.2 %) |

The named target moved less than 3 %, which counts toward the banner's item-10 yield rule (b). The sf025 sweep
sum went from 105.8 to 107.1 s; the instruction counts show this is load. Values are identical (sf025 PASS=99,
61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Left.** Each probe still pins its leaf twice: the descent checks the high key and unpins, then the cursor
re-pins and re-checks it. PG keeps the leaf pinned from `_bt_search` into `_bt_readpage`, but holding the
latch across the executor boundary is unsafe here. Beyond that the probe cost is the comparison chain itself:
`compare` → `ComparePGIndexTuples` → `comparePGIndexTupleAttrsAt` → `compareFixedPrefix` on a tuple-vs-tuple key,
where PG compares a pre-deformed scan key against the index tuple.

## Slice 62 (2026-10-23): an explicit-frame window aggregate keeps its running state while the frame head stays put — TPC-DS Q51

**Target.** TPC-DS Q51, 1.4 s against PG's 0.31 s, at 15.8 G instructions. Its CTEs compute
`sum(...) OVER (PARTITION BY item ORDER BY date ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)`, and its outer
query a `max(...) OVER` with the same frame. Split by stack, the leader held 70 % of the samples, and
`windowOp`'s frame evaluation (`evalExplicitFrameAggFuncs` → `applyAgg` / `aggEvalList` / `evalExprSlot`) was its
largest window-specific cost. The cause is in that function's own comment: every row's frame was re-aggregated from
its head, O(partition × frame), "a deliberate v0 tradeoff". For an UNBOUNDED PRECEDING frame that is quadratic in
the partition size. PG's `eval_windowaggregates` (nodeWindowAgg.c) works differently. While the frame head has not
moved, it keeps the transition state and feeds only the rows between `aggregatedupto` and the new frame tail. When
the head moves, it restarts, unless the aggregate has an inverse transition function. It also restarts for an
EXCLUDE clause.

**Change.** `evalExplicitFrameAggFuncs` tracks `[aggStart, aggEnd)`, the rows already in the running state:

- When the next row's frame has the same head and a tail no shorter, only rows `[aggEnd, end)` are applied.
- Otherwise the state is reset and the frame is aggregated from its head.
- Rows are applied in the same ascending order as the from-scratch loop, so float sums are bit-identical.
- This applies only without EXCLUDE, and only to `sum`, `count`, `avg`, `min` and `max`
  (`frameAggReusesState`). Their state is plain values: `finishAgg` gets it by value, and numeric sums are
  replaced, never updated in place. A finalized value therefore never aliases state that a later row changes.
- Aggregates holding slices or maps (string\_agg, array\_agg, ordered-set, DISTINCT) still recompute every frame.
- `frameAggIncremental` switches the path off, for the equivalence test.

**Pins.**

- `TestWindowFrameIncrementalMatchesRecompute` runs 100 rows in 4 partitions, with duplicate order keys (peer
  groups), NULLs, numerics beyond 64 bits and float8.
  - Ten aggregates (sum, count, count(\*), avg, min, max, and a FILTERed sum) run over ten frames: ROWS and
    GROUPS with an UNBOUNDED PRECEDING head and CURRENT ROW, k FOLLOWING or UNBOUNDED FOLLOWING tails; sliding
    ROWS; RANGE frames; and an EXCLUDE CURRENT ROW frame.
  - Every value must equal the from-scratch path.
  - Mutation check: ignoring head movement fails it on the sliding frame.
- The sf025 sweep records Q51 by row count only (`ck=n/a`), so this test is its value pin.

**Measured** against `edc87a31a`:

| query | HEAD Ginstr | candidate Ginstr | HEAD wall / CPU (s) | candidate wall / CPU (s) |
|---|---|---|---|---|
| Q51 | 15.79 | 12.17 (−22.9 %) | 1.42–1.60 / 2.01–2.27 | 1.14–1.16 / 1.74–1.77 |
| Q47, Q57, Q12, Q98 | — | identical (no explicit frame) | | |

The sf025 sweep sum went from 107.1 to 105.5 s, with Q51 at 1.11 → 0.84 s. Values are identical (sf025 PASS=99,
61 ck; acceptance 24 MATCH), plan shapes are identical, and the regress suite, whose window cases cover frames,
passes.

**Deferred (ledger row).** PG's moving-aggregate mode is not implemented. When the head advances, PG subtracts the
leaving rows through the aggregate's inverse transition function (`invtransfn`, with NaN and overflow fallbacks);
goopg recomputes a sliding frame from its new head. Sliding frames are rare in the corpora.

**Left.** Q51 is still ~2.7× PG. The leader's sorted Groups and Sorts over CTE scans clone every row
(`cloneRowOwned` via `sortOp.Open` ← `cteScanOp`), and the workers' Gather queue clones every row
(`transferRowForQueue`).

## Slice 63 (2026-10-23): a Gather Merge under an aggregate ships only live columns, and live / prefix clones leave dead cells unwritten — TPC-DS Q78

**Target.** TPC-DS Q78, 1.6–2.0 s against PG's 0.46 s, at 27.9 G instructions. Each of its three CTE branches is
a GroupAggregate → Gather Merge → Sort → (Parallel Hash Join + anti join). An instruction profile put
`cloneRowOwned` at 12.5 % and `memclrNoHeapPointers` at 5.3 %. Most of that (9.3 % + 3.6 %) was
`transferRowForQueue` in `gatherMergeOp.runWorker`: every ~50-column join row was deep-copied across the worker
queue for an aggregate that reads six columns. Slice 39 had given plain Gather a live-column mask
(`gatherOp.liveCols` via `buildCompleteChild`), but Gather Merge had no such arm.

**Change.**

- **Live mask on Gather Merge.** `buildCompleteChild` gets a GatherMerge arm. `newGatherMergeNode`, shared with
  `buildNode` as `newGatherNode` is, sets `liveCols` to `consumerLiveColumns` of the consumers plus the
  GatherMerge's own merge keys, because the leader evaluates those keys on every transferred row. Both transfer
  sites take `transferRowForQueueLive`: the worker queue, and the leader's own participating tree. An
  unresolvable shape, such as a ctid key or a sublink, yields nil, which means every column.
- **No writes for dead cells.** `cloneRowOwnedLive` no longer walks every column writing `NullDatum` into dead
  cells: a fresh row's cells are already the zero Datum, which is `NullDatum` (`KindNull` = 0). It copies only
  the live cells within the mask; the armed deform poison still writes every dead cell.
  `cloneRowOwnedPrefix`, the seq scan's survivor-window clone, skips its tail the same way.

**Pins.**

- `TestGatherMergeUnderAggregateShipsLiveColumns`: the live set is exactly the group column, the merge key and
  the aggregate argument. A Finalize aggregate or a ctid merge key gets nil. Mutation check: omitting the merge
  keys fails it.
- `TestNullDatumIsTheZeroDatum` pins the zero-value premise and both clones' NULL tails with the poison off.
  `TestCloneRowOwnedLive` and the existing Gather Merge order / null-ordering / identity tests pass.
- The executor package passes with the poison armed.

**Measured.** Instructions per query against `c6a780f7e`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q78 | 27.86–27.87 | 26.71 (−4.2 %; the live mask alone gave −2.4 %) |
| Q51, Q97, Q14, Q87, Q23, Q1 | — | within ±0.1 % |

Wall time and CPU could not resolve the change at load average 7–9. The sweep's Q78 checksum is identical
(`ck=c06cf981a7819a37`). Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are
identical.

**Left.** The transferred row is still allocated, and therefore zeroed, at full width: the leader expects the
child schema's column positions. The worker's Sort below the Gather Merge stores whole join rows
(`sortOp.Open` → `MaterializedSlot.Materialize`), because a worker tree is built with `buildNode`, not
`buildCompleteChild`, so nothing narrows it to the consumers' columns.
