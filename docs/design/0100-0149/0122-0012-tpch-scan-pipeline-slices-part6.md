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

## Slice 64 (2026-10-23): string literals compile to constants, and a plain string pair skips the character-string ambiguity test — TPC-DS Q59, Q2

**Target.** TPC-DS Q59, 1.9 s against PG's 0.74 s, at 20.1 G instructions. It runs serially (no Gather). Its
`wss` CTE is seven `sum(CASE WHEN d_day_name = 'Sunday' THEN ss_sales_price ELSE NULL END)`-style aggregates, and
`d_day_name` is char(9). Every input row therefore made up to seven bpchar comparisons against string literals. An
instruction profile split as follows:

- `evalFastExpr` 13 %, `projectOp` 10.5 % and `evalExprSlot` 5.9 %.
- `comparisonOperandsAsBpchar` 5.1 %, `evalFastBinaryApply` 4.7 %, `StringValue` 1.9 %, `indexbytebody` 1.3 %
  and `textShapeAmbiguous` 1.0 %.

Two causes sat in the compiled expression path:

- **The literals were interpreted.** The compiled slab had no StringConst kind. Aggregate arguments compile through
  `buildExpr`, without a Context and so without the constant fold, so every `'Sunday'` was an `ExprAdapter`: an
  `evalExprSlot` call per row per arm.
- **The plain-string shortcut ran after the expensive test.** For a comparison of two character-string operands
  (payload bit 4), `evalFastBinaryApply` ran `textShapeAmbiguous` first: two `isValidUUIDStr` scans, a pg\_lsn
  sniff and the row/array test. Only then did it reach slice 30's plain-string shortcut, which re-normalised both
  operands through `comparisonOperandsAsBpchar`'s Datum round trip.

**Change.**

- **StringConst compiles to ExprConstVal.** The value is `NewStringDatum(Value)`, the interpreted arm's own
  result, a constant. A BinaryOp still reads the literal-ness of its operands from the planner expression
  (payload bits 0/1), not from the child node, so literal handling is unchanged.
- **The plain pair goes first.** The plain-string shortcut now runs before the ambiguity test. Two
  `plainCompareString` strings are never `textShapeAmbiguous`: a uuid needs 32 or more bytes, a pg\_lsn a '/', and a
  row or array a leading '(' or '{'. That test could therefore only answer false for them. The bpchar blank rule
  is applied directly to the strings: with either side bpchar-typed, a bpchar side or a bare literal drops its
  trailing blanks.

**Pins.**

- `TestCompiledStringComparisonMatchesInterpreted` runs all six comparison operators over a char(9) column, a text
  column and bare literals (plain, padded and row-shaped), in both operand orders. Values are plain, padded,
  blank-only, uuid-, pg\_lsn-, row- and array-shaped. The compiled per-row and batch evaluators must equal the
  interpreted `evalExpr` on every pair (over 10 000 checks). Mutation check: dropping the bare-literal trim fails
  it.
- `TestBuildExprSlabCommonKinds`' "unknown kind falls back to the adapter" case used StringConst as its example.
  It now uses a function call, since StringConst no longer falls back.

**Measured.** Instructions per query against `ecbfe6696`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q59 | 20.14 | 17.19 (−14.7 %; the StringConst compile alone −1.9 %) |
| Q2 | 14.65 | 11.53 (−21.3 %) |
| Q43 | 8.68 | 8.10 (−6.7 %) |
| Q19, Q3, Q10 | — | identical |

The sf025 sweep put Q59 at 1.60 → 1.30 s and Q2 at 0.46 → 0.42 s, with Q2 and Q43 checksums identical. Values are
identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical. The gates ran after the
nightly batch (fired 00:51, ended 02:09), because the acceptance arm refuses to overlap it.

**Left.** PG compiles `bpchareq` against a constant without re-deriving the constant's blank-stripped length per row.
goopg still trims the literal per call, a short `TrimRight`, and the column value too.

## Slice 65 (2026-10-23): a subquery IN stops allocating per row, and a nested loop copies its outer tuple once per outer row — TPC-DS Q35

**Target.** Ranking the sweep's gaps by absolute seconds put Q25 (3.1 s against PG's 0.14 s) first, but on the
2GB clone Q25 runs in 0.20 s: its sweep time is the corpus's 128MB shared\_buffers. Q35 is a real gap: 2.3–3.1 s
against PG's 0.354 s on the 2GB clone. An instruction profile put about 13 % in Go stack unwinding (`runtime.step`,
`pcvalue`, `findfunc`). That was the heap profiler's sampling stack walks (`mallocgc` → `profilealloc` →
`mProf_Malloc` → `callers`), triggered by large per-row allocations, plus 7–10 % in `memmove`. Two sources:

- **Subquery IN metadata.** Q35's EXISTS clauses become IN subqueries (EXISTS→ANY). For every outer row,
  `inListItemsEval` allocated its two per-item metadata slices, `itemBP` and `itemLit`, at the subquery's
  result size: thousands of customer keys. The uncorrelated subquery's values come from the cache and are then
  probed through the hash (`evalInHashProbe`), so the allocations were all that scaled with the result.
- **Nested-loop pairs.** `nlJoinStream.pairMatches` copied the whole outer tuple into the pair buffer for every
  inner tuple, although it is the same for the entire inner loop.

**Change.**

- **Metadata built once.** For a subquery IN the metadata is uniform: every item shares the inner plan's first
  output column type, so `itemBP` is that column's bpchar flag and `itemLit` is false throughout.
  `subqueryInListMeta` builds it once per InExpr on the Context (`inListPlanMeta`), grows it when a longer
  result arrives, and returns a capped prefix for each row. Without a Context it builds per call as before.
  Callers only read it.
- **Outer half copied once.** `pairMatches` copies the outer half only when `pairOuterValid` is unset. The flag is
  cleared when `stepOuter` reads a new outer tuple, when the buffer is reallocated, and, defensively, when the
  pair is emitted borrowed (`nlEmitBorrowed`), since the consumer then holds it until the next call.

**Pins.**

- `TestSubqueryInListMetaIsUniformAndReused`: for bpchar and text inner columns, the prefix carries the uniform
  flag at lengths 3, 3, 10, 2 and 0. A same-length call reuses the slice, and the no-Context path builds it.
- `TestInSubqueryBpcharSemantics`: `c IN (SELECT c ...)` over char(5) is blank-insensitive and over text
  byte-exact, with a NULL operand, across rows.
- The pair-copy flag is pinned by the existing nested-loop suites. With the new-outer reset removed,
  `TestExecJoinVariants`, `TestNestedLoopKeylessSemiAntiEarlyOut` and `TestNumericHashJoinIntLane` fail.

**Measured** against `302e0463d`:

| query | HEAD Ginstr | candidate Ginstr | HEAD wall / CPU (s) | candidate wall / CPU (s) |
|---|---|---|---|---|
| Q35 | 12.43 | 10.39 (−16.4 %; the metadata alone −9.6 %) | 2.32–3.10 / 2.85–3.16 | 0.62 / 1.14 |
| Q10 | 10.44 | 10.31 (−1.3 %) | | |
| Q16, Q72, Q94, Q64 | — | within ±0.2 % | | |

Q35's wall time fell about 75 %, far more than its instruction count. The per-row allocations cost mostly outside
user-mode instructions: zeroed pages, page faults and collector work, which `instructions:u` does not count. The
sf025 sweep sum went from 98.9 to 95.5 s, with Q35 at 1.37 → 0.81 s and Q10 at 1.07 → 0.83 s. Values are
identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Left.** PG evaluates `EXISTS … OR EXISTS …` as a hashed SubPlan, with no per-row list at all. goopg still
materialises the cached value slice and probes its hash per row.

## Slice 66 (2026-10-23): liveness crosses a Project and a join's probe side, and a Gather worker carves its shipped rows from chunks — TPC-DS Q39

**Target.** Q39 is the largest remaining gap on the 2GB clone: 1.94 s for its two statements against PG's 0.59 s.
Each statement is `HashAggregate → Project → Hash Join (item) → Gather → Hash Join (warehouse) → Parallel Hash
Join (date_dim) → Parallel Seq Scan on inventory`, and the Gather carries 2.34 M rows per statement (PG's own
EXPLAIN ANALYZE count). An instruction profile put 30 % of all instructions in the workers' `transferRowForQueue`:
it gathered every cell of the 9-column join row through `VirtualSlot.Row()` into a scratch row, then deep-copied
that row again for the queue. Slices 39 and 63 ship only live columns when a Gather sits directly under a
consumer-complete parent (an aggregate or a sort), but here a Project and a join stand in between, so
`liveCols` stayed nil.

**Change.**

- **A Project's child is consumer-complete.** A projection reads its child's rows only through its live
  targets, so `buildProjectNode` builds its child through `buildCompleteChild` with those targets as the
  consumers. Slice 53 did this only for a child that is itself a Project; every arm of `buildCompleteChild`
  (Sort, IncrementalSort, Gather, GatherMerge, now Join) is correct under that rule, and any other child kind
  still builds through `buildNode`.
- **A join's left side is consumer-complete.** A new `*optimizer.Join` arm of `buildCompleteChild` builds the
  left subtree with the consumers `joinLeftConsumers` derives: one ColumnRef per live left column of the merged
  output, where liveness covers the parent's consumers and the join's merged-space predicate, plus the left
  halves of the equi-key pairs, which evaluate positionally against left rows. The right subtree and the join
  itself are built as in `buildNode`, through the shared `finishJoinNode`. The arm declines, and the join builds
  normally, for a join type whose output is not the left++right layout (only INNER, LEFT and CROSS qualify), a
  LATERAL or null-aware join, a layout the side widths do not account for, or consumers whose reads
  `consumerLiveColumns` cannot enumerate (a sublink, a whole-row or ctid reference).
- **Live cells read in place.** `transferRowForQueueLive` reads a VirtualSlot cell by cell, live cells only,
  instead of materialising the whole row first. Any other slot is copied from its `Row()` view as before.
- **Shipped rows come from chunks.** A Gather or Gather Merge worker carves its shipped rows from a `rowChunk`,
  one allocation per 32 rows of a width. It is not a pool: every row is fresh and zeroed, owned by its
  consumer, and capacity-capped so an append cannot reach a neighbour. A consumer that keeps one row keeps its
  chunk alive, and the 32-row size bounds that. The leader's own Gather Merge rows still allocate one by one.

**Pins.**

- `TestGatherUnderJoinShipsLiveColumns`: Aggregate → Project → Join → Gather ships exactly the join key, the
  residual's left column and the aggregate's column, for INNER and LEFT. Dropping the predicate from the live
  set, or bypassing the arm, fails it.
- `TestGatherUnderJoinKeepsEveryColumnWhenUnsure`: a semi join, a LATERAL join, a mismatched layout and a
  sublink predicate each leave `liveCols` nil.
- `TestRowChunkRowsAreIndependent`: rows are zeroed and length-capped, refill past the chunk works, and an
  append on one row leaves its neighbour intact. Removing the capacity cap fails it.
- `TestTransferRowForQueueLiveReadsLiveCellsOnly`: for a VirtualSlot and a materialised slot, live cells carry
  the source values and dead cells are NULL.
- The executor package passes with the deform poison armed, and so does an SF0.25 sweep run on a poison-armed
  binary (PASS=99, 61 ck), so no consumer reads a cell this slice leaves dead.

**Measured** against `d4a855738`:

| query | HEAD Ginstr | candidate Ginstr | HEAD wall / CPU (s) | candidate wall / CPU (s) |
|---|---|---|---|---|
| Q39 | 29.06 | 26.86 (−7.6 %; the liveness alone −6.5 %) | 1.99 / 3.80 | 2.10 / 3.38 |
| Q22 | 22.64 | 22.48 (−0.7 %) | | |
| Q21, Q7, Q26, Q48, Q13 | — | within ±0.05 % | | |

Q39's CPU fell about 11 %, but its wall time did not move: wall stays within run-to-run noise (1.94–2.10 s on
either binary). The query is bound by its serial leader, which probes the item hash, projects, and aggregates
2.34 M rows into 540 K groups per statement, then finalises `stddev_samp` and `avg` through big-integer numeric
arithmetic. The workers' savings only shorten the time they spend waiting on the leader. The sf025 sweep sum is
unchanged at 95.5 s, values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are
identical.

**Left.**

- PG's workers send the projected tuple, its targetlist narrowed by the planner at every level. goopg still
  ships full-width rows with NULL dead cells, and narrows only along the consumer-complete chains above: a
  join's right side, semi and anti joins, and RIGHT and FULL joins are not narrowed.
- Q39's wall time is the leader's serial work. Its leader profile is flat: per-group allocation, string-keyed
  group lookups, and the numeric finalisation of 540 K groups (`exactIntVariance`, `numericDiv`,
  `pgSqrtScaled`). PG accumulates `int4` input in 128-bit integers (`int4_accum`, `Int128AggState`) and converts
  to numeric only in the final function.

## Slice 67 (2026-10-23): numeric division and variance stop formatting and exponentiating, and int4 variance input accumulates in 128 bits — TPC-DS Q39

**Target.** After slice 66, Q39's wall time is its serial leader. Per statement the leader aggregates 2.34 M rows
into 540 K groups, finalises `stddev_samp` and `avg` for each group, and evaluates the HAVING filter
`CASE avg(...) WHEN 0 THEN 0 ELSE stddev_samp(...) / avg(...) END > 1` on each. An instruction profile split by
leader stack put 31 % of the leader in `finalizeGroup` and 15 % in the filter's `numericDiv`. Three costs ran
per call or per row, and none of them was arithmetic PG performs:

- **Powers of ten** were recomputed with `big.Int.Exp`: in `pow10Big` (variance, square root, weight helpers) and
  in `numericDiv`'s rescaling.
- **Digit counting formatted the number.** `pgNBaseWeightFirst` took `len(v.String())`, and `numericDivScale`
  converted both operands with `Text(10)`, only to read the digit count and the leading NBASE digit.
- **Big-integer accumulation for int4 input.** Every `stddev_samp(int4)` group allocated two `big.Int` sums, and
  every row added to them with a `big.Int` square. PG's `int4_accum` accumulates in `int128`
  (`do_int128_accum`, `Int128AggState`), and so does `int2_accum`; x² < 2^62 there, so neither sum can overflow.

**Change.**

- **Powers of ten from tables.** `pow10Big` serves exponents below 160 from a table built once (callers only read
  the result) and falls back to `Exp` above that. `numericDiv` rescales with it. `pow10Uint64` covers powers
  that fit a uint64.
- **Digit counts without formatting.** `decimalDigitsUint64` is a table comparison. `decimalDigitsBig` estimates
  from the bit length, which is within one digit, and settles with exact power comparisons.
  `pgNBaseWeightFirst` and `nbaseWeightAndFirstDigit` read the leading NBASE digit with one division, through
  uint64 when the value fits. `nbaseWeightAndFirstDigit` now takes the mantissa (any sign), not its decimal
  string. Its negative-scale floor and the zero-padding of a short leading chunk are unchanged.
- **The 128-bit lane.** A variance aggregate whose declared input is int2 or int4 (`inputIsInt32`), with an
  in-range value, accumulates Σx and Σx² in `int128` fields (`sx128`, `sxx128`; `bits.Add64`). `intSums` folds
  the lane into the existing `intSx` and `intSxx` big.Int sums for every reader: the four finalisers, the
  partial-state serialiser and the parallel combine. It allocates fresh results and never modifies the state,
  because a copied state may share the big.Int pointers. int8 input keeps the big.Int lane, as PG's
  `int8_accum` keeps numeric.

**Pins.**

- `TestDecimalDigitsMatchesString` and `TestNBaseWeightHelpersMatchStringOracle` check the new helpers against
  the string-based versions they replace, kept in the test as oracles. The values cover every power-of-ten
  boundary to 10^60, the uint64 seam, and 2000 random values up to 200 bits; scales run −9..40 and both signs
  are tested. Removing the downward adjustment in `decimalDigitsBig` fails them.
- `TestPow10BigTableAndFallback` covers the table, its boundary and the `Exp` fallback, including `Exp`'s answer
  for a negative exponent.
- `TestInt128LaneMatchesBigInt`: lane sums and all four variance / stddev results equal the big.Int path's. The
  inputs include 70 000 repeated `MinInt32`/`MaxInt32` values that carry the low word and drive Σx negative.
  Dropping the sign extension fails it.
- `TestIntSumsFoldsWithoutMutating` and `TestInt128LaneSerialisesAndCombines` cover the fold into an existing
  big.Int lane, shared pointers left untouched, a serialise round trip, and combines from a lane state, a
  deserialised state and into an empty destination. Each of the serialiser, combine and fold arms was
  mutation-checked.

**Measured** against `3a50b12ed` (slice 66):

| query | HEAD Ginstr | candidate Ginstr | HEAD wall / CPU (s) | candidate wall / CPU (s) |
|---|---|---|---|---|
| Q39 | 26.86 | 22.42 (−16.5 %; powers + digits −9.6 %, division −5.5 %, int128 −1.4 %) | 2.16–2.25 / 3.48–3.57 | 1.56–1.59 / 2.76–2.80 |
| Q17 (stddev\_samp), Q7, Q22, Q21, Q26 | — | within ±0.1 % | | |

Q39's wall time fell about 30 % because this work was on the serial leader. In the sf025 sweep Q39 went from
1.75 to 1.37 s with an identical checksum, and the sum went from 95.5 to 95.1 s. Values are identical (sf025
PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Left.** Q39 still takes 1.56 s against PG's 0.59 s. Each group allocates a 664-byte `aggRuntime` per aggregate
(PG's per-group state is a transition value and two flags), plus its group key string in a string-keyed map.
The leader also probes the item hash and projects every row before aggregating.

## Slice 68 (2026-10-23): numeric division takes a 128-bit path and drops redundant big.Int copies, and hash-aggregate groups come from chunks — TPC-DS Q39, Q4

**Target.** After slice 67, a leader-only profile of Q39 put 9 % of the leader in founding groups (`newGroup`:
a `groupRuntime`, a key row and two 664-byte `aggRuntime` states per group, three allocations) and 10.6 % in
the HAVING filter's `numericDiv`. Within `numericDiv`, 20 % was `numericMant` building `big.Int` operands, 15 %
`newNumeric` and 11 % the int64 fast path being tried and failing. The mean is a 16-decimal numeric whose
mantissa passes int64, so it lives in the big-mantissa lane, which skips the int64 fast path entirely.

**Change.**

- **`numericDivUint128`.** It runs after the int64 fast path, for operands of either lane whose mantissa
  magnitudes fit a uint64. A big-lane payload is read in place (`numericMagUint64`: sign byte plus big-endian
  magnitude, the encoding `newBigNumericInCtx` writes). The rscale is `select_div_scale`'s, through
  `nbaseWeightAndFirstDigitUint64`. The shifted numerator is one `bits.Mul64` and the quotient one `bits.Div64`,
  rounded half away from zero, with the result in the int64 lane. It declines a zero operand, a shift past
  10^19, a quotient past 64 bits or past int64, and a magnitude past uint64; all of these go to the big path.
  For Q39 it covers the groups whose coefficient of variation is at least 1 (rscale 16). Below 1, rscale 20
  needs a 20-digit quotient, and the big path keeps those.
- **The big path without redundant copies** (`numericDivBig`, the former body):
  - `numericMant` returns `NumericBigValue()`'s already-fresh `big.Int` instead of copying it again.
  - The numerator is scaled in place.
  - The rounding test runs in the remainder's own storage, and the ±1 bump uses a constant.
  - `newNumeric` passes its argument straight to `newBigNumericInCtx`, which only copies the bytes out.
  
  `numericMant` and `newNumeric` are shared by every numeric operator, so those two help all of them.
- **Groups from chunks.** The hash aggregate's drain carves `groupRuntime`s, `aggRuntime` state slices
  (capacity-capped) and key rows (`rowChunk`) from 32-group chunks. Groups live until the aggregate finishes,
  so a chunk pins nothing that would otherwise be freed. A fresh key row is already NULL throughout, so the
  NULL-fill loop for rolled-up columns is gone.

**Pins.**

- `TestNumericDivUint128MatchesBigPath`: 200 000 random pairs over both lanes, both signs, scales 0..20 and
  magnitudes just past int64. Every answered case equals `numericDivBig` in digits and scale, and the test
  fails if fewer than 50 000 cases take the new path.
- `TestNumericDivUint128RoundingAndDeclines`: round-half-away-from-zero for both signs, an exact quotient, the
  five declines, and division by zero still raising. Dropping the tie rounding, the sign, or the big-lane sign
  byte fails these tests.
- The existing int64 fast-path fuzz tests now compare against `numericDivBig` directly. They had forced the
  slow path through big-lane operands, which the new path would otherwise answer.
- The chunked groups are covered by the aggregate suites: the executor package (also with the deform poison),
  RegressSuite (aggregates, groupingsets) and sf025's 61 value checks.

**Measured** against `b3f708aee` (slice 67):

| query | HEAD Ginstr | candidate Ginstr | HEAD CPU (s) | candidate CPU (s) |
|---|---|---|---|---|
| Q39 | 22.48 | 21.67 (−3.6 %; division −2.9 %, chunks −0.7 %) | 2.88–2.94 | 2.66–2.68 |
| Q4 | 48.38 | 46.38 (−4.1 %) | | |
| Q67 | 29.68 | 29.43 (−0.8 %) | | |
| Q22, Q11, Q87, Q17 | — | within ±0.3 % | | |

Q39's wall time is bimodal on both binaries: each 3-run sample averages either about 1.45 s or 2.2–2.7 s, with
the same CPU, so wall A/Bs on it need several samples or the CPU column. The sf025 sweep's per-query times move
by seconds on unrelated queries (Q74, Q25, Q1) between runs, so its sum is not a usable instrument at this
scale. Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH), and plan shapes are identical.

**Discovery (filed M0122-0016, ledger row).** Every big-mantissa numeric result goes to `mmgr.Perm()`, the
process-global context documented as never reset: 100 000 big-lane divisions grew its usage by 1.18 MB. Each
write also takes the process-wide `permMu` write lock. PG pallocs a numeric in the current memory context
and frees it at reset.

**Left.** Q39 still takes about 1.45 s against PG's 0.59 s. Remaining costs:

- The rscale-20 divisions in the big path.
- The finalisation's big-integer variance and square root.
- The string-keyed group map.
- The leader's per-row item probe and projection.
