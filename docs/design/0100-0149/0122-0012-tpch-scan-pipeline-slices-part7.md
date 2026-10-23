# M0122-0012 — scan pipeline slices, part 7 (TPC-DS executor gaps)

Continues [0122-0012-tpch-scan-pipeline-slices-part6.md](0122-0012-tpch-scan-pipeline-slices-part6.md) (slices
58–68 and M0122-0016). It was split out on 2026-10-23 under the 800-line design-doc limit; slice numbering is
unchanged.

**How this part measures.**

- **The bench clone holds SF0.25.** `tmp/q64-clone` (port 5592, 2 GB shared\_buffers) is the **SF0.25** corpus, not
  SF1: Q75's output on it is byte-identical to the `:65438` reference's `tpcds025` database and differs from
  `tpcds`. Earlier parts' "PG warm time" comparisons should be read against `tpcds025`.
- **Instruction counts are the A/B of record.** Per-query `instructions:u` (`tmp/ds-instr.sh`) is the measure. A
  fresh server spends its first ~7 heavy runs growing its heap toward the memory limit, page-faulting fresh pages
  (part 6, "M0122-0016"). Concurrent sessions on this host also run CPU-heavy fuzz tests, which swing user CPU per
  query by ±50 %. Wall and CPU numbers are reported only alongside a warm, quiet A/B.
- **Warm timings.** `tmp/ds-warm.sh <bin> <warm-q> <warm-runs> <q...>` warms one server past its heap plateau, then
  times each query (3 runs, wall plus user and sys CPU) on that same server.

## Slice 69 (2026-10-23): a hashed DISTINCT feeding an order-insensitive hashed aggregate stops sorting its output — TPC-DS Q75

**Target.** Ranked on warm timings, Q75 (0.56 s against PG's 0.24 s) is the largest untouched gap. Its CTE runs
`HashAggregate (sum) → HashAggregate (the UNION's de-duplication) → Gather → Parallel Append` over three sales
and returns join branches, about 485 K rows. A leader-split instruction profile put 90 % of the leader in
`distinctOp.Open`. A third of it was a `sort.Slice` over every de-duplicated row (`pdqsort`, `compareDatumPlain`).
`distinctOp`, the executor of the planner's hashed `Distinct` (EXPLAIN `HashAggregate`), de-duplicates through a
hash map and then sorts its output "for deterministic output". PG's hashed DISTINCT emits hash-table order and
never sorts. The sort makes an unordered DISTINCT's own output reproducible, but an aggregate whose result
cannot depend on its input order can never observe it.

**Change.**

- **`distinctOp.unordered`** skips the output sort. The rows come out in first-seen order, with the same rows
  and keys as before.
- **`markDistinctUnordered`**, called from `buildNode`'s Aggregate arm, sets it on a `distinctOp` that feeds the
  aggregate directly or through projections (and the EXPLAIN-ANALYZE instrumentation wrapper). It only does so
  when `aggregateIgnoresInputOrder` holds:
  - the aggregate is hashed (or ungrouped), not a Finalize, and has no grouping sets;
  - every aggregate is `count`, `min` or `max`, or `sum`/`avg` over a non-float input;
  - no aggregate has an ORDER BY or WITHIN GROUP.
  
  Float sums and averages round differently in a different order, and `array_agg`, `string_agg` and
  user-defined aggregates keep their input order, so any of those keeps the sort. A top-level DISTINCT, or any
  other consumer, sorts as before.

**Pins.**

- `TestAggregateIgnoresInputOrder` covers the allow list and every exclusion: float sum and avg, collecting and
  user aggregates, an ordered aggregate, the sorted strategy, Finalize, and grouping sets. Admitting floats
  fails it.
- `TestDistinctUnderOrderFreeAggregateSkipsSort`: a DISTINCT under `sum(int4)` through a Project is marked; one
  under `array_agg`, and a top-level one, are not. Stopping the walk at the Project fails it.
- `TestDistinctUnorderedEmitsFirstSeenOrder`: the sorted mode still emits ascending order with NULLs last, and
  the unordered mode emits first-seen order with the same rows. Ignoring the flag fails it.
- Q75's output is byte-identical before and after, and identical to the `:65438` reference's `tpcds025` output.

**Measured** against `32a6ac752`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q75 | 14.45–14.49 | 12.29–12.39 (−14.3 % to −15.2 %) |
| Q87, Q38, Q16, Q94 | — | within ±0.4 % |

Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH; RegressSuite PASS), and plan shapes are
identical.

**Also in this commit.** `internal/utils/mmgr/mctx.go`'s `permanentContext` comment no longer says big numerics
allocate there (M0122-0016's leftover). `mmgr.Perm()` now has no production caller in the executor.

**Left.** goopg's other hashed DISTINCT consumers (a hash join's build side, a hashed SetOp, a semi-join's
unique-ified inner) are also order-insensitive but keep the sort. Each needs its own proof that nothing above it
depends on the order, as the aggregate's does here.

## Slice 70 (2026-10-23): projections run compiled, COALESCE compiles, and dedup keys are built in a reused buffer — TPC-DS Q75

**Target.** After slice 69, Q75's whole-query instruction profile, workers included, showed two per-row costs:

- **Projections.** `projectOp.Next` ran `evalExprSlot` for every non-column target, about a fifth of the query,
  with per-row `strings.ToLower`, bpchar-typmod lookups and a numeric literal re-parsed each row. Q75's
  `cs_quantity - COALESCE(cr_return_quantity, 0)` targets went through the interpreter on every row. Filters
  and aggregate arguments already ran compiled; the legacy projection never did. The compiler also left
  `COALESCE` (a `FuncCall`) as an `ExprAdapter`.
- **Dedup keys.** The UNION's hashed DISTINCT built each row's key with `rowKeyTrimmed`: a `strings.Builder` over
  every column's `Format()` string (`strconv.FormatInt`, `formatNumeric`), plus a clone of every input row,
  duplicates included.

**Change.**

- **`projectOp` runs compiled.** It compiles its targets once, on the first `Next`, into `fast` / `fastIdx`
  (`buildExprCtx`, so literal-only subtrees fold) and keeps them across re-Opens. Non-column targets evaluate
  through `evalFastExpr`. Kinds the compiler does not know stay `ExprAdapter` nodes, which call `evalExprSlot`
  as before. Bare column targets keep slice 58's in-place copy.
- **`ExprCoalesce`.** `COALESCE(a, b, …)` compiles to a chain: the first argument and the COALESCE of the rest.
  `evalFastExpr` returns the first non-NULL argument as is, raises an argument's error, and leaves the remaining
  arguments unevaluated. That is evalFuncCall's `"coalesce"` arm, which stays the interpreted reference.
- **Keys in a reused buffer.** `appendRowKeyTrimmed` appends `rowKeyTrimmed`'s key: the same `|`-separated
  `Format()` text with trailing decimal zeros stripped, and `<NULL>`. An int, string, byte or int64-lane numeric
  column appends directly (`appendFormatNumeric` is `formatNumeric` without its intermediate strings).
  `rowKeyTrimmed` is now a wrapper over it, so its five callers cannot drift. `distinctOp` looks keys up as
  `seen[string(buf)]` without allocating, and clones and keeps a key string only for a row that starts a new key.

**Pins.**

- `TestCoalesceCompiledMatchesInterpreted`: one, two and three arguments, COALESCE inside arithmetic, and an
  argument that divides by zero ahead of or behind a non-NULL one. The per-row and batch evaluators match the
  interpreter on every row, and no adapter is left. Swallowing the argument's error, or dropping a middle
  argument, fails it.
- `TestProjectTargetsRunCompiled`: COALESCE arithmetic compiles, `upper()` stays an adapter, and values equal
  `evalExprSlot`'s across two Opens. Recompiling on every `Next` fails it. Evaluating through the interpreter
  instead gives the same values, which is the point: the change is speed-only.
- `TestAppendRowKeyTrimmedMatchesOracle`: 20 000 random rows over every kind, including `-0.`-style and dotted
  strings, bpchar trims and skipped columns, give exactly the old string builder's key, also after a prefix.
  Removing the `-` edge case, the dot pass, or the prefix-aware separator fails it.
- `TestAppendFormatNumericMatchesFormatNumeric`: 50 000 mantissas, including `MinInt64`, at scales 0..25.

**Measured** against `606717ffc`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q75 | 12.27–12.33 | 11.71 (−4.6 %; keys −2.0 %, compiled projections −2.6 %) |
| Q11, Q4, Q39 | 20.77, 45.73, 21.66 | 20.48 (−1.4 %), 45.30 (−0.9 %), 21.49 (−0.8 %) |
| Q7, Q22, Q35, Q59, Q87, Q97, Q2 | — | within ±0.3 % |

Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH; RegressSuite PASS), and plan shapes are
identical. The executor package passes with the deform poison armed.

**Left.** The scan deform (`decodeRowRangeInfo`, 15 % of Q75) walks every column up to the last one read. PG's
`attcacheoff` jumps straight to fixed-offset attributes in tuples without NULLs.

## Slice 71 (2026-10-23): a sort read only by consumer-complete readers stores just their columns, a Gather Merge's partial plan is built for its readers, and a VirtualSlot materialises in one pass — TPC-DS Q97, Q78

**Target.** Q97 is the largest warm gap on the SF0.25 clone: 0.36 s against PG's 0.11 s. Each side runs
`Group → Gather Merge → Sort → Parallel Hash Join (sales × date_dim)`. A split profile put 20 % of the worker
samples in `transferRowForQueueLive`, sitting on the load of a sorted row's cell. Those are cache misses: each
worker Sort stored whole join rows (store\_sales × date\_dim, about 51 cells, roughly 2.4 KB each), and the
Gather Merge ships 3 of them. Each row was also built twice on the way in: `slot.Materialize()` on the join's
VirtualSlot was `cloneRowOwned(s.Row())`, so it allocated and copied a full row, then copied it again.

**Change.**

- **One-pass `Materialize`.** `VirtualSlot.Materialize` detaches each cell once into the owned row
  (`MaterializeArena` of `Get(i)`). That is `cloneRowOwned(Row())` without the intermediate row and copy, and it
  benefits every caller.
- **`sortOp.live`.** It is set by `buildCompleteChild`'s Sort arm to the columns the consumers and the sort keys
  read (`finishSortNodeLive`). Ingestion then reads and detaches only those cells (`materializeLiveRow`: a fresh
  full-width row, other cells NULL, or the deform poison when armed). Rows keep the schema width, so no reader
  sees a short row. It is skipped when the sort tracks ctids, or uses the packed representation (off by default).
- **`newGatherMergeNodeFor`.** A Gather Merge built under a consumer-complete parent passes its readers (the
  consumers plus its merge keys, the same set as its `liveCols`) to each participant's partial plan. That plan is
  then built through `buildCompleteChild`, so the worker Sort learns its live columns. A Gather Merge built
  anywhere else keeps the plain build.

**Pins.**

- `TestVirtualSlotMaterializeOwnsEveryCell`: one-pass `Materialize` equals `cloneRowOwned(Row())` and survives
  the producer arena's reset. Dropping the detach fails it.
- `TestSortForConsumerCompleteReadersStoresLiveColumns`: under a sum aggregate, the Gather Merge's worker Sort
  stores columns [3 5 11] (group column, merge key, aggregate argument). A top-level Gather Merge's sort stores
  every column. Not passing the readers fails it.
- `TestSortWithLiveIngestionSortsLiveCells`: sorting with a live mask orders by the key, keeps the live cells,
  and leaves the dead one NULL. Ignoring the mask fails it.
- The executor package passes with the deform poison armed. So does an SF0.25 sweep on a poison-armed binary
  (PASS=99, 61 ck), so no reader touches a cell a sort leaves dead.

**Measured** against `cabdb6039`:

| query | HEAD Ginstr | candidate Ginstr | warm wall / user CPU (s), HEAD → candidate |
|---|---|---|---|
| Q97 | 7.65 | 7.42 (−3.0 %) | 0.350–0.357 / 0.70 → 0.341–0.344 / 0.66 |
| Q78 | 27.57 | 24.50 (−11.1 %) | |
| Q35, Q51 | 10.45, 12.12 | 10.25 (−1.9 %), 11.90 (−1.8 %) | |
| Q87, Q38, Q75 | — | within ±0.1 % | |

The warm timings come from `tmp/ds-warm.sh` after 8 Q39 runs, so both servers are past the heap plateau.
Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH; RegressSuite PASS), and plan shapes are
identical.

**Left.** The stored rows still have the schema's full width (about 2.4 KB for Q97), so a sorted scan still
misses cache once per row. PG's tuplesort stores MinimalTuples of the sort's own narrow targetlist. Narrow
storage would need the sort to emit full-width rows from compact ones without breaking readers that keep a row
across `Next`.

## Slice 72 (2026-10-23): a participant probing a leader-prebuilt shared hash table takes the empty-inner exit — TPC-DS Q61

**Target.** Ranked by warm time against the `:65438` `tpcds025` reference, the largest ratios were small
queries. Q61 took 91 ms against PG's 16 ms, and burned about 0.37 CPU-s per run. Its profile was scan deform
(`decodeRowRangeInfo` 29 %, `seqScanOp.Next` 14 %) over both store\_sales branches.

Two deform micro-changes measured flat on Q61 and were dropped:

- leaving an already-NULL skipped cell unwritten;
- PG's `attcacheoff` cached offsets for NULL-free tuples. At SF0.25 only about 40 % of store\_sales tuples are
  NULL-free, so the extra branch cost what it saved.

PG's own EXPLAIN ANALYZE showed the real difference. Its `Parallel Seq Scan on store_sales` returns **1 row per
process**. The filtered `store` (`s_gmt_offset = -7`) is empty at SF0.25, and ExecHashJoin's HJ\_BUILD\_HASHTABLE
state ends the join without scanning the outer relation (`hashtable->totalTuples == 0 && !HJ_FILL_OUTER`,
`nodeHashjoin.c`). The one row is the outer-first prefetch. goopg already had that exit (`emptyBuildEndsJoin`,
M0146-0049d1) for private builds. But under a Gather, goopg's leader pre-builds one shared table
(`prebuildSharedHashJoins` / `captureSharedBuild`), and the participant branch of `openLazyHashJoin`
(`applySharedBuild` → `openProbeSide`) never checked it. Every worker scanned its whole store\_sales share
against an empty table. PG's workers each build the table privately and quit on it.

**Change.**

- `sharedHashBuild` carries the builder's `buildRows` / `buildRowsCounted`, set by `captureSharedBuild` and
  copied by `applySharedBuild`.
- The shared branch of `openLazyHashJoin` takes `emptyBuildEndsJoin()`. When the table is empty and the join
  does not emit unmatched probe rows (`probeFillsUnmatched`: not LEFT, ANTI or FULL), it returns without opening
  the probe side, and `Next` answers EOF.
- A Parallel Hash participant's share (`openParallelHashJoin`) does not set the count, since an empty share says
  nothing of the whole table, so that path is unchanged. PG takes the exit there too after its build barrier;
  that remains a ledger row.

**Pins.**

- `TestSharedHashBuildEmptyBuildSkipsProbe` builds a real shared table with the builder's own
  `buildLazyHashTable` / `captureSharedBuild`, registers it in `Context.SharedHashBuilds`, and opens a
  participant. With an empty build, INNER and SEMI never open the probe side and return nothing, while LEFT and
  ANTI still emit all 3 probe rows. A non-empty INNER probes as before. Removing the exit, or not carrying the
  count, fails it.
- The serial path's `TestHashJoinEmptyBuildSkipsProbe` is unchanged and passes.

**Measured** against `2efa8fe12`. Q61 went from 5.645 to 0.012 Ginstr per run, and from 87–92 ms warm wall to
3–7 ms, below PG's 16 ms. In the sf025 sweep it went from 288 to 13 ms. Its output (a single NULL row at SF0.25)
is byte-identical. Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH; RegressSuite PASS;
TestPort\_Isolation PASS), and plan shapes are identical.

**Left.** Q61's 23× per-row CPU gap would also show where the join is not empty. goopg deforms every scanned
row up to its last referenced column before the first hash probe, while PG's `slot_getsomeattrs` deforms
lazily, only as far as each consumer reads.

## Slice 73 (2026-10-23): the Parallel Hash build-side claim walk passes a partial Unique — TPC-DS Q87, Q38

**Instrument: the per-node actual-rows census.** Slice 72 showed that a timing gap can hide an algorithmic
difference that only PG's actual row counts reveal. `tmp/rows-census.sh <port>` captures
`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF)` of every TPC-DS query's first statement on the SF0.25 clone and on
the `:65438` `tpcds025` reference. `tmp/rows-census.py` aligns the two plans node by node (the shapes are
identical, 99/99) and lists every node whose total goopg rows exceed 3 × PG's.

- **Totals.** goopg prints per-worker lines for parallel nodes; they are summed. PG prints the average per loop;
  it is multiplied by the loop count.
- **Leads it surfaced:**
  - Q87 and Q38's catalog branch: about 4 × PG on the Parallel Hash Join and everything above it.
  - Q72: the `inventory_pkey` index scan at 20 ×.
  - Q67 and Q51: their Sorts at 10 × and 330 ×.
  - Q17, Q25 and Q29: a `catalog_sales_pkey` index scan that PG runs for 0 rows.

**Finding (Q87, Q38).** In the catalog branch every goopg participant's `Parallel Seq Scan on date_dim` returned
all 365 rows, where PG's split them (91.25 × 4). Each participant therefore inserted the whole build relation
into the one shared Parallel Hash table, and every probe row matched four times. The branch's plan is
`Gather Merge → Unique (partial, per worker) → Sort → Nested Loop → Parallel Hash Join`. The Parallel Hash
build-side walk (`attachParallelHashBuildSides`) had no `distinctOnOp` arm, so it stopped at the per-worker Unique
and the build scan never received its claim state (`hashBuildBranch`). The three scan attachers
(`attachParallelScan`, `attachParallelIndexScan`, `attachParallelBitmapScan`) all had that arm: a sibling-walk
drift. The results stayed correct only because the per-worker Unique and the leader's Unique removed the
identical copies. The store branch, whose Unique sits above the Gather Merge, was unaffected.

**Change.**

- `attachParallelHashBuildSides` gains the `distinctOnOp` arm.
- So does `unwrapToSetOp`, the SetOp claim walk, which lacked it too.
- Both descend only a producer-marked partial Unique (`DistinctOn.PartialUnique`), exactly as
  `attachParallelScan` does: an unmarked Unique under a Gather is not a per-worker dedup.

**Pins.**

- `TestAttachAllWiresHashBuildUnderPartialUnique`: Unique → Sort → Parallel Hash. The probe scan takes the flat
  claim set, the build scan takes the join's `hashBuildBranch` set, and an unmarked Unique wires nothing. Dropping
  the arm, or its gate, fails it.
- `TestUnwrapToSetOpThroughPartialUnique`: a marked Unique is passed and an unmarked one is not.

**Measured** against `d92a36e6f`:

| query | HEAD Ginstr | candidate Ginstr | warm wall (s), HEAD → candidate (PG) |
|---|---|---|---|
| Q87 | 23.73 | 13.11 (−44.8 %) | 0.633 → 0.371 (0.30) |
| Q38 | 23.57 | 12.86 (−45.4 %) | 0.620 → 0.357 |

Each participant's catalog-branch join now returns about 17.5 K rows, 71 906 in total, which is PG's figure.
Q87's and Q38's outputs are byte-identical to the `tpcds025` reference's. Values are identical (sf025 PASS=99,
61 ck; acceptance 24 MATCH; RegressSuite PASS; TestPort\_Isolation PASS), and plan shapes are identical.

**Left.** The census's other leads (Q72, Q67, Q51, Q17/Q25/Q29) are the next slices' targets.

## Slice 74 (2026-10-23): a presorted, partitioned window consumes its input one partition at a time — TPC-DS Q51, Q67

**Target (from the rows census).** Q51's WindowAgg sits under `Limit 100` / `Subquery Scan … Filter`. PG's Sort
under it emitted 506 rows; goopg's emitted all 169 297. `windowOp.Open` drained its whole input, then evaluated every
window function for every row before returning one. PG's WindowAgg spools one partition at a time (`spool_tuples` /
`begin_partition`, `nodeWindowAgg.c`), so a LIMIT above stops it after the partitions it needed. Q67 (255 K
against 25 K) has the same shape.

**Change.**

- **When it streams.** `streamsPartitions` holds when the plan delivers the input already sorted (`Presorted`; the
  in-operator sort needs every row first) and there is a PARTITION BY.
- **How.** `Open` spools only the first partition. `Next` spools the next one when the buffer runs out
  (`spoolPartition`), keeping the row that ended a partition as the next one's first (`pending`). `evalWindowFuncs`
  is unchanged: given one partition's rows it evaluates exactly that partition, and it skips re-detecting partition
  boundaries on a streaming buffer.
- **Run Condition.** A failed Run Condition in a partitioned window now skips the rest of the buffer, which is the
  rest of the partition (WINDOWAGG\_PASSTHROUGH\_STRICT).
- **Frame offsets.** An empty input still evaluates the frame offsets once, as `ExecWindowAgg` does on its first call
  (`calculate_frame_offsets` under `all_first`). They and the frame-aggregate helper are resolved once per scan.

**Pins.**

- `TestWindowStreamingMatchesWholeInput`: seven window queries over seven random partitions, run streaming and
  with `windowStreamPartitions` off, return identical rows. They cover row\_number/rank/dense\_rank;
  sum/count/avg; lag/lead/first\_value/last\_value; ntile/percent\_rank/cume\_dist; a whole-partition aggregate;
  a rank filter; and a LIMIT.
- `TestWindowStreamingStopsAtThePartitionItNeeds`: emitting the first partition reads exactly its rows plus one,
  and the whole scan reads every row once.
- Losing the lookahead row fails both tests.

**Measured** against `cb3e7e6b6`:

| query | HEAD Ginstr | candidate Ginstr |
|---|---|---|
| Q51 | 11.91 | 9.94 (−16.5 %); its Sort now emits 514 rows |
| Q67 | 28.26 | 26.95 (−4.6 %) |
| Q47, Q57 | 18.88, 9.22 | 19.03 (+0.8 %), 9.28 (+0.7 %): per-partition spool overhead over many small partitions |
| Q44, Q89, Q70, Q86, Q36, Q12 | — | within ±0.3 % |

The outputs of Q51, Q67, Q44, Q47, Q57, Q89, Q70, Q86 and Q36 are byte-identical to the `:65438` `tpcds025`
reference's. Values are identical (sf025 PASS=99, 61 ck; acceptance 24 MATCH; RegressSuite PASS;
TestPort\_Isolation PASS), and plan shapes are identical.

**Census triage and discoveries.**

- **Q17/Q25/Q29** were not executor waste. goopg's EXPLAIN ANALYZE counts an index scan's rows before its own
  Filter (`rows=25.63` beside `Rows Removed by Filter: 26`), where PG counts the rows the node returns. Filed as
  M0122-0017.
- **Q12/Q20/Q98** outputs differ from PG, as they already did at HEAD. A zero dividend's numeric division prints
  scale max(dscale) (`0`, `0.00`) where PG's `select_div_scale` derives it from the divisor
  (`0.00000000000000000000`). Filed as P0-E11, an S2 escalation awaiting owner placement.
- **Left.** Within a partition goopg still evaluates every window function for every row at spool time; PG
  evaluates them as rows are returned (`eval_windowfunction` per output row, with pass-through after a failed Run
  Condition). Q44's top-N window therefore still reads its sorted input to the end. A non-presorted window still
  drains its input.
