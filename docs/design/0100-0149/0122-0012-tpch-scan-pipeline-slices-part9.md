# M0122-0012 — scan pipeline slices, part 9 (TPC-DS executor gaps, continued)

Continued from [part 8](0122-0012-tpch-scan-pipeline-slices-part8.md) (slices 80–89), which was split before the
800-line limit.

**Measurement method.** As in part 8: the SF0.25 clone `tmp/q64-clone`, with instructions:u per query
(`tmp/ds-instr.sh`) as the instrument and allocation volume (`tmp/ds-alloc.sh`) alongside it.

**Reference timings.** Gaps are ranked against release PG 18.3, whose per-query medians are in
`analysis/timing-after-planprity-match/data/tpcds-sf025-pgrel.tsv`.

Wall-clock ratios from one long warm sweep are unreliable for queries that follow a heavy one (part 8, slice 89).
Warm each query on its own, or rank by instructions.

## Slice 90 — the plain-string test and the string read inline at the comparison

**Target.** Q81, and its sibling Q30, still spent their time in the slice-89 lane. Their correlated SubPlan compares
`ctr1.ctr_state = ctr2.ctr_state` for every inner CTE row, as PG's does. That made the lane's two support calls the
largest costs in Q81:

- `indexbytebody` at 26 %: `plainCompareString`'s `strings.IndexByte` on a two-letter state code, where the call into
  the vector routine costs more than the scan;
- `Datum.StringValue` at 6.5 %: its arena arm puts it over the inlining budget (cost 143 against 80), so every read is
  a call.

**Change** (`exprnode.go`).

- **`plainCompareString`.** It tests the leading '(' or '{' and the 32-byte cut first, then scans for '/' with a byte
  loop, and now inlines. The result is the same as the `IndexByte` form: the string is under 32 bytes whenever the
  loop runs.
- **`stringValueInline(d)`.** It reads an owned string straight from `Buf`; `unsafe.String(unsafe.SliceData(Buf),
  len(Buf))` is "" for an empty or nil buffer, as `StringValue` returns. An arena-backed string calls `StringValue`. It
  inlines (cost under 80) and replaces the two `StringValue` calls in both plain-string compares: the slice-89 lane and
  `evalFastBinaryApply`'s branch.

**Why the plain test stays.** For a character-string pair (payload bit 4), most non-plain strings compare as text on
the general path too:

- `textShapeAmbiguous` sends shape-alike pairs to `binaryTextComparison`, which is `strings.Compare` after the bpchar
  trim.

The exception is a pair that is not shape-ambiguous but has one side carrying a '/':

- it reaches `evalPgLSNBinary` on the untrimmed values;
- a padded bpchar that parses as a pg\_lsn there would compare numerically.

So the guard is required, and only its cost changed.

**Pins** (in `string_compare_inplace_test.go`).

- `TestPlainCompareStringMatchesIndexByteForm` checks the byte loop against the `IndexByte` form, with a '/' at every
  position, leading and non-leading brackets, and lengths 30 to 33. Starting the loop at index 1 fails it.
- `TestStringValueInlineMatchesStringValue` covers owned, empty, nil-buffer and arena-backed strings. Returning "" for
  the arena arm fails it.
- Slice 89's `TestStringCompareInPlaceMatchesInterpreted` still passes.

**Measured** against `e8bf90456` (instructions:u per run):

| query | before | after | change |
|---|---|---|---|
| Q81 | 36.90 G | 31.34 G | −15.1 % |
| Q30 | 8.45 G | 7.24 G | −14.3 % |
| Q59 | 12.09 G | 11.86 G | −1.9 % |
| Q2 | 8.93 G | 8.88 G | −0.6 % |

Two steps make up Q81's −15.1 %: the byte loop alone was −9.1 %, and the inlined read another −3.4 % before its last
budget trim.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

## Slice 91 — a correlated sublink over a CTE scan is lowered, so its result cache keys on the parameters

**Target.** TPC-DS Q1, Q30 and Q81 filter a CTE scan inside a correlated scalar sublink:
`ctr1.ctr_total_return > (SELECT avg(ctr_total_return) * 1.2 FROM ctr ctr2 WHERE ctr1.k = ctr2.k)`. After slice 90,
Q81 still ran 31 G instructions.

EXPLAIN ANALYZE showed why: `SubPlan 2 (calls=6893 rebuilds=6893 hits=0 misses=6893)`. The sublink was not lowered to
PARAM\_EXEC slots (D4.1, `subplan_lower.go`), so its result cache was keyed by the whole outer row (`subqueryCacheKey`)
and missed on every call. Each call rescanned the whole CTE.

**Root cause.** The lowering walk (`lowerTraverseNodeHooked`) had no CTEScan arm, and it bails on every node kind it
does not model, as its soundness contract requires. A sublink over a CTE scan therefore always stayed on the
OuterRows stack path.

The CTE body (`CTEScan.Child`) cannot simply be walked, for two reasons:

- it is shared by every reference to the CTE (the executor fills its row cache once per CTE), so a rewrite inside it
  would rewrite every reference;
- Q81's body holds a parameterised nested loop whose inner index probe reads the join's left row as a Level-1
  `OuterColumnRef`. That reference is local to the body, but a flat "no outer refs" test (`excludedRefsWithin(body,
  0)`, the first attempt) rejects it.

**Change** (`internal/optimizer`).

- **`selfContainedPlan(plan, frames)`** (subplan\_lower.go) is a non-mutating check on the lowering walk itself, so an
  unmodelled kind still refuses:
  - an `OuterColumnRef` must have `Level <= frames`;
  - a lateral Join's right child is walked one frame deeper, as `keptRebase.rebaseNode` walks it, because the join
    pushes its left row there;
  - a nested sublink's plan is walked one frame deeper, and an IN's operand and list in the current frame;
  - an `ExecParamRef` refuses, because its slot's binder cannot be told from here.
- **The CTEScan arm** of `lowerTraverseNodeHooked` (subplan\_lower\_walk.go) returns `selfContainedPlan(x.Child, 0)`
  and never enters the body. The analysis and rewrite phases, and the keptRebase pass, all treat a self-contained CTE
  scan as a leaf.

PG's `SS_replace_correlation_vars` turns every correlated Var into a PARAM\_EXEC regardless of what lies below. This
brings goopg's lowering closer to it, and executor caching is unchanged.

**Plans are unchanged.** Census on this tree: TPC-H 22/22, SF0.25 99/99 and SF1 99/99 MATCH, and the captured plan
text is byte-identical to the previous capture's (only headers differ). The fire-set gate saw no changed query.
`EA-RATCHET: PASS`.

**Pins.**

- `TestLowerScalarSublinkOverCTEScan` (optimizer): a sublink over a MATERIALIZED CTE scan is lowered to one param
  whose Arg is the host's column, and the CTE body carries no ExecParamRef or OuterColumnRef afterwards.
- `TestSublinkOverCorrelatedCTEStaysOnStack`: a CTE defined inside the sublink over the outer row keeps the sublink
  correlated and unlowered. Lowered, it would have no params and would be marked non-correlated, so one cached result
  would serve every outer row. A CTEScan arm that accepts without the check fails this test.
- `TestSelfContainedPlan` covers ten plan shapes: local and escaping refs, lateral and plain joins, nested sublinks,
  ExecParamRef, and an unmodelled node.
- `TestCorrelatedSublinkOverCTEValues` (executor) checks Q81's shape over 60 rows, including NULL keys and bpchar
  padding, against values computed in Go, for both the lowered query and the stack-path query. The accept-always
  mutation makes the stack-path query return wrong rows there too.
- `selfContainedPlan` is pinned `walkerPending` in `exprSwitchInventory`, with a ledger row.

**Measured** against `4cf358ae6`:

| query | instructions before | instructions after | warm wall time | release PG |
|---|---|---|---|---|
| Q1 | 99.2 G | 1.00 G | 0.108 s | 7.20 s |
| Q81 | 31.4 G | 0.99 G | 0.102 s | 2.29 s |
| Q30 | 7.25 G | 0.49 G | 0.054 s | 0.52 s |

Q81's SubPlan now shows `hits=6841 misses=52`, one miss per distinct state.

Values are identical:

- sorted full output of Q1, Q30 and Q81 equals the `:65438` reference (SELECT only);
- sf025 PASS=99 (61 ck), and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Still bailing** (ledger row). A sublink whose plan, or whose CTE body, holds another unmodelled kind still stays on
the stack path: WindowAgg, SetOp, RecursiveUnion and ProjectSet.

## After slice 91 — measured, not landed (2026-10-24)

**SubPlan cache census.** All 99 SF0.25 queries ran under EXPLAIN (ANALYZE) on the clone (`tmp/s92-subplan-census.sh`).
Every SubPlan now hits its result cache, and the misses equal the distinct parameter values:

| query | calls | hits | misses |
|---|---|---|---|
| Q1 | 12 881 | 12 874 | 7 |
| Q6 | 6 514 | 6 503 | 11 |
| Q10 | 2 278 | 2 277 | 1 |
| Q30 | 3 210 | 3 158 | 52 |
| Q35 | 6 427 | 6 426 | 1 |
| Q41 | 245 | 173 | 72 |
| Q45 | 1 616 | 1 615 | 1 |
| Q81 | 6 893 | 6 841 | 52 |
| Q92 | 38 | 34 | 4 |

No SubPlan with `hits=0` is left, so slice 91 closed this lever.

**A buffered stable merge sort for the sort operators** (sortChunk, sortPackedChunk, the window and incremental-sort
permutations), replacing `sort.SliceStable`'s in-place SymMerge, which was 19 % of Q67. The output is identical by
construction: a stable sort's result is unique, and a test against `sort.SliceStable` on lengths 0 to 4099 with ties
passed.

| measurement | change |
|---|---|
| Q67 instructions | −3.0 % |
| Q47 instructions | −3.1 % |
| Q51 instructions | −4.0 % |
| Q67 serial cycles | +2.6 % (12.48 against 12.16 G, three runs each) |

Dropped. Its full-array merge passes touch the scattered key values (48-byte Datums, string bytes behind them) more
than SymMerge's localised work. The comparator's cost is memory access, not instruction count.

**The cooperative parallel hash build's producer clone.** In Q95's `ws_wh` CTE, about 13 % of instructions is
`cloneRowOwnedPrefix`, plus `memclr` and `malloc`, inside the producers of `parallelBuildLazyHashTable`. Q95 allocates
1.5 GB and 4.6 M objects per run.

- Each producer's seq scan clones every row up to its deform bound (about 18 of `web_sales`'s 34 columns) to hand it
  across the channel.
- The consumer's build loop then copies each row again (`retainBuildRow`), so the producer's copy is transient.
- Recycling it needs the producer's scan to borrow, and a borrowed scan row keeps a stale, possibly arena-backed
  tail past the deform bound. The generic `transferRowForQueue` also returns a row with no arena cell as-is, which
  would hand the scan's reused row across the channel.

A safe version copies only the build side's live columns into recycled batch storage that the consumer returns. That
needs a design pass of its own.

**Remaining gaps against release PG**, by absolute excess:

- **Q14** (+3.6 s): 16 173 parameterised `store_sales_pkey` probes and 688 k heap fetches, and the `cross_items`
  intersect build. This is the per-probe constant factor.
- **Q23** (+1.4 s): the same index-probe class.
- **Q67** (+1.1 s): sort comparisons over eight `char(n)` keys.
- **Q95** (+1.0 s): the build clone above, plus 10 M CTE rows through two semi joins.

**A rendering difference** found along the way: goopg's EXPLAIN ANALYZE prints `actual time` totals over all loops
(Q14's `store_sales_pkey` scan shows `0.003..1857.725` with loops=16173), where PG prints per-loop averages. Plain
EXPLAIN parity is unaffected. It is recorded here and in the ledger, not fixed.

## Slice 92 — cooperative hash build producers borrow their scan and fill recycled batch blocks

**Target.** Q95 allocated 1534 MB in 4.63 M objects per run. About 13 % of its instructions
(`cloneRowOwnedPrefix` plus `memclr` and `malloc`) went to the cooperative parallel hash build's producers
(`parallelBuildLazyHashTable`, M0129-S4.1).

- Each producer's tree was built without the scan borrow, so its seq scan cloned every row, a full-width allocation
  of `web_sales`'s 34 columns for the `ws_wh` build.
- The rows then crossed the channel, and the consumer's build loop copied each one again (`retainBuildRow`).
- The producer's clone was therefore a transient full-width allocation per build row.

**Change** (`parallel_hash_build.go`).

- **Borrowing.** A producer whose tree is the bare scan, or the scan under a Project whose every target is a plain
  column (`coopBorrowScan`), marks the borrow chain. The scan then hands up its reused row instead of a clone.
- **The copy.** The producer copies each row into its batch before pulling the next:
  - for the bare scan, the scan's own deform prefix;
  - behind a column-only Project, every output column, since the Project passes the scan's cells through with their
    arena marks;
  - each cell through `MaterializeArena`.
- **Refused trees.** Any other tree keeps its scan's clone. A computed target over a borrowed scan can alias the
  scan's page arena without being marked arena-backed (substr over an arena cell, the slice-83 invariant), and the
  copy cannot detach that.
- **Recycled blocks.** Rows are carved from one block per batch (`coopBatch{rows, cells}`). `channelSource` hands a
  block back on a free channel when it moves past the batch, and a producer takes one from there before allocating
  (`coopBatchCells`). Tail cells, from the deform prefix to the row width, are set once at allocation: NULL, or the
  poison when armed, as `cloneRowOwnedPrefix` left them. Producers never write them again. Neither side blocks on the
  free list.
- **Why recycling is safe.** The build loop copies every row it keeps before it pulls the next one (`retainBuildRow`
  in `buildLoopLeft` / `buildLoopRight`: `retainBuildRowHeap`, `packDenseBuildRow`, `ownedBuildRow`). The serial
  build's scan borrow already rests on that invariant (`joinBorrowBuildChild`), so by the time a block goes back
  nothing reads its batch.

**The first cut was wrong twice** before the version above:

- **Wrong bound.** It bounded the copy by the build subtree's `buildBound`. That is the scan's input bound, not the
  tree's output: a Project emitting 3 columns over a scan bounded at 2. The poison test caught it: a consumer read an
  undeformed cell.
- **Too narrow.** Restricting the borrow to the bare scan was safe but missed Q95, whose producer is a column-only
  Project (−1.4 % bytes instead of −21 %).

**Pins** (`coop_build_recycle_test.go`).

- `TestCoopBuildRecycledBatchesMatchSerialBuild` runs three queries over a 3000-row build side with arena-backed text
  and columns past the deform bound: a column-only Project, `d.*` from the bare scan, and substr computed above the
  join. Each must equal the serial build's rows (cooperative build off), with poison off and armed, five times each.
  It also asserts that the recycling and borrowing paths ran.
- A consumer that hands a block back on receipt, before reading it, fails the test in 4 runs out of 4.
- `TestCoopBorrowScanShapes` checks which trees may borrow. Admitting a computed target fails it.
- `-race` passes on the cooperative tests.

**Measured** against `a5ba2cb39`:

| query | measure | before | after | change |
|---|---|---|---|---|
| Q95 | bytes per run | 1534 MB | 1205 MB | −21.5 % |
| Q95 | objects per run | 4.63 M | 4.26 M | −8 % |
| Q95 | instructions | — | — | −1.6 % |
| Q95 | `ws_wh` build time | 65.1 / 89.7 ms | 47.4 / 46.9 ms | −27 to −48 % (two runs) |
| Q4 | instructions | — | — | −2.0 % |
| Q59 | instructions | — | — | −1.2 % |
| Q2 | instructions | — | — | flat |

End-to-end time moved within noise on the final binary, in both directions:

- cycles 8.62 → 8.59 G and 9.13 → 7.88 G over two runs;
- EXPLAIN ANALYZE execution 2538 → 2603 ms on one pair and 3080 → 2636 ms on another;
- warm wall time in one long sweep under `GOGC=off` was noisy too.

The measured gains are allocation and the build itself.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH (TPC-H's cooperative builds);
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

## After slice 92 — probe: computed values over a borrowed scan (filed P0-E12)

Slice 92 refused to borrow behind a Project with computed targets. The probe asked whether the existing borrow gates
have the same hazard.

**Gather workers.** A hand-built Gather over Project(`substr(t, 2, 6)`) over a parallel scan of a multi-page text
table returns about 16 000 of 20 000 strings with another column's bytes. From SQL, the planner places such
projections above the Gather, so workers ship raw scan cells and the probed queries (projection, filter) match the
serial run.

**Hash aggregate over a borrowed scan, serial.** `SELECT substr(t, 2, 6), count(*) FROM gq GROUP BY 1` returns
60 000 rows with only 36 distinct keys.

- `aggregateBorrowsScanRows` lets the scan hand up its reused row because "group keys are MaterializeArena'd at group
  creation".
- But `evalSubstr` slices the arena-backed string, and `NewStringDatum` wraps the bytes uncopied. The key has
  `ArenaID` 0 and its `Buf` inside the page arena, so `MaterializeArena` keeps it as is, and a later page overwrites
  it.
- It reproduces at `a05a55628`, before this part's slices.

It is filed as **P0-E12** (S2, owner placement). The repro is `analysis/p0e12/`, with the expected output computed
from the generator. The probe tests were not kept; the P0-E12 task names the regression test its fix needs.

## Slice 93 — C16a: the join search memoises its narrowed-width inputs (Q64 planning −6.7 %)

Loop #187, 2026-10-27, the first return under the owner's return-first rule. The census
`analysis/m0122/m0122-0012/census-20261027/` (G1/G2 against release PG 18.3) puts TPC-DS Q64 in the combined top
10 with an excess that is mostly **planning**: 636 ms per plan against release PG's 51.9 ms, about 584 of Q64's
846 ms excess. All of it is GEQO, ~500 tours of `gimmeTree` → `makeJoinRel`. PG runs the same tour count, so the
gap is the cost of each joinrel goopg builds. Profile (25 s of cache-busted plan-only EXPLAINs):

| share | site |
|---|---|
| 59 % | `addPathsToJoinrel` (all join methods) |
| 26 % | `mallocgc` (inclusive, spread) |
| 12.7 % | pathkey usefulness / truncation |
| 12.5 % | `joinrelNarrowedWidths`: `relNarrowedColumns` 6.8 %, `attrNeededAbove` 3.7 % |
| 7.9 % | `innerRelProvenUnique` → `provableKeys` / `keysCovering` |

`joinrelNarrowedWidths` (M0146-0234, build_joinrel_tlist for the cost triple) re-derived two pure inputs for every
joinrel of every tour:

- **`relNarrowedColumns(member)`**: the member's kept columns. It depends only on the base rel's leaf, needed set
  and column statistics, all fixed before the search.
- **`attrNeededAbove(relids)`**: the above-tree NAME set. It walks every clause and builds a map, and it depends
  only on relids, the clause list and the statement's output set.

PG computes `attr_needed` once per base rel (`build_base_rel_tlists`), and `build_joinrel_tlist` (relnode.c) reads
it.

**Change.** `searchCtx` memoises both for the search:

- `narrowColsMemo` keyed by member rel;
- `attrNeededMemo` keyed by relids, valid for one clause list at one length (a different list or length resets
  it).

Declines are memoised too. `joinrelNarrowedWidths` sums the members' kept columns in place instead of
concatenating them. `baseRelNarrowedWidths` reads the same memos.

**Measured (Target written before coding: Q64, instructions:u per cache-busted plan, `tools/ds-planinstr.sh`).**

| run | before | after |
|---|---|---|
| 1 | 6.496 G | 6.061 G |
| 2 | 6.490 G | 6.052 G |

That is **−6.7 %**, which meets the 3 % bar. The SF0.25 sweep's Q64 reads 971 ms against the census median of
1019 ms.

Plans are identical: sf025 plans 99/99 same, fire-set no fires on either corpus, TPC-H census 22/22.

Gates: units; spotcheck; acceptance 24 MATCH; sf025 PASS=99; RegressSuite per-case counts equal to the baseline
except the plpgsql flapper; isolation PASS.

Test: `TestNarrowedWidthMemoMatchesAndResets`.

**Next (C16b).** `innerRelProvenUnique` re-proves inner uniqueness per call (`provableKeys` → `keysCovering`
builds maps on every call). PG caches `innerrel_is_unique` results per (outer relids, inner rel) in
`unique_for_rels` / `non_unique_for_rels` (analyzejoins.c). Re-profile after it, because addPaths and allocation
dominate what remains.
