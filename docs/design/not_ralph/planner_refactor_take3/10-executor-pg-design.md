# 10 — PostgreSQL 18.3 executor design

Scope: how PG 18.3 executes a `PlannedStmt`: dispatch
(`ExecInitNode` / `ExecProcNode` / `ExecProcNodeFirst`), the
`execMain` lifecycle (`ExecutorStart` / `Run` / `Finish` / `End`),
the `TupleTableSlot` family with lazy deform, every scan and join
node (plus `Memoize`), aggregation strategies, `tuplesort`,
parallel execution, `tuplestore` spill, expression compilation, and
memory contexts. All paths are relative to `postgres/`; every
section cites the file and function that establishes it.

Verification: every `global -x` symbol cited below was re-checked
read-only against the oracle on 2026-09-03. Symbols that returned
no location are listed in §17, not cited as functions.

Notation: `estate` = `EState`, `econtext` = `ExprContext`,
`slot` = `TupleTableSlot`, `work_mem` = the `work_mem` GUC.

---

## 1. Dispatch: `ExecInitNode` / `ExecProcNode` / `ExecProcNodeFirst`

`ExecInitNode` (`postgres/src/backend/executor/execProcnode.c:ExecInitNode`,
line 142) recursively initializes the plan tree. It switches on
`nodeTag(node)` (`T_SeqScan`, `T_HashJoin`, …) and calls the
per-node `ExecInit<Node>` constructor, which allocates the
`<Node>State`, initializes subplans, builds expressions with
`ExecInitExpr` / `ExecInitQual`, creates result and scan slots,
and wires `ps_ExecProcNode` to the node's `Exec<Node>` function.
`NULL` input returns `NULL`, so leaves terminate the recursion.

First-call dispatch goes through `ExecProcNodeFirst`
(`postgres/src/backend/executor/execProcnode.c:ExecProcNodeFirst`,
static, line 448): stack-depth check, one call to the node's
real function to force lazy state, then later calls go straight
to it. The public entry `ExecProcNode`
(`postgres/src/include/executor/executor.h:ExecProcNode`,
line 310) is the inline dispatcher every parent calls to pull
one `TupleTableSlot *` (or `NULL` at EOF) from a child.
`ExecProcNodeInstr` (same `execProcnode.c`, line 479) wraps the
same call with timing/row counting for `EXPLAIN ANALYZE`.

Hot-path chain: `ExecProcNode` → `ExecProcNodeFirst` (once) →
`Exec<Node>` (e.g. `ExecSeqScan`) → child `ExecProcNode` →
`ExecProject` / qual check → return `slot`.

`MultiExecProcNode` (same file, line 507) is the second entry
point: `Hash` never returns tuples via `ExecProcNode`; the
parent `HashJoin` drives it to completion first. Teardown
mirrors init: `ExecEndNode` (line 562) dispatches per-node
`ExecEnd<Node>`, and `ExecShutdownNode` (line 772) runs
pre-`End` shutdown (parallel workers, tuplestores).

---

## 2. `execMain`: `ExecutorStart` / `Run` / `Finish` / `End`

`ExecutorStart` (`postgres/src/backend/executor/execMain.c:ExecutorStart`,
line 122) honors `ExecutorStart_hook` or falls through to
`standard_ExecutorStart` (same file, line 141), which builds the
`EState` with `CreateExecutorState`, switches into
`es_query_cxt`, copies external params, allocates
`es_param_exec_vals`, assigns the command id for writes / row
marks, then calls `InitPlan` → `ExecInitNode` (plus
`ExecInitSubPlan` subplans and initPlans).

Hot-path chain: `ExecutorStart` → `standard_ExecutorStart` →
`CreateExecutorState` → `ExecInitNode` → `ExecInitExpr` /
`ExecInitQual` per node.

`ExecutorRun` (same file, line 297) runs `ExecutePlan`: loop
`slot = ExecProcNode(top)` into the destination receiver until
`NULL` or `count` rows. Rescans go through `ExecReScan`
(`postgres/src/backend/executor/execAmi.c:ExecReScan`, line 77),
which dispatches per-node `ExecReScan<Node>`. `ExecutorFinish`
(`execMain.c`, line 406) fires deferred work (`AFTER ROW`
triggers, foreign-table post-modify). `ExecutorEnd` (same file,
line 466) calls `ExecEndNode`, frees the `EState`, and releases
locks and tuple queues. `EXPLAIN (ANALYZE)` runs the full
cycle with the `ExecProcNodeInstr` wrapper, then reports.

---

## 3. `TupleTableSlot` family, lazy deform, materialize

`TupleTableSlot` (`postgres/src/include/executor/tuptable.h:TupleTableSlot`,
line 114) is the universal row carrier: `tts_values` /
`tts_isnull` datum arrays, `tts_nvalid` (columns currently
deformed), `tts_ops` (implementation vtable),
`tts_tupleDescriptor`, `tts_mcxt`, plus `tts_tid` /
`tts_tableOid`. The vtable `TupleTableSlotOps` (same file,
line 134) defines `init`, `release`, `clear`, `getsomeattrs`,
`getsysattr`, `materialize`, `copyslot`, `get_heap_tuple` /
`get_minimal_tuple`, and `copy_heap_tuple` /
`copy_minimal_tuple`. Four implementations exist:
`TTSOpsVirtual` (expression results, projection),
`TTSOpsHeapTuple`, `TTSOpsMinimalTuple` (stored/passed tuples),
`TTSOpsBufferHeapTuple` (heap page pinned).

Lazy deform is the core OLAP saving. `slot_getsomeattrs`
(same `tuptable.h`, line 359) deforms only up to attribute
`attnum`: a no-op when `tts_nvalid >= attnum`, else
`tts_ops->getsomeattrs` (`slot_deform_heap_tuple` for heap
slots). Expression steps thus pay only for columns they touch
(`EEOP_*_FETCHSOME` passes the highest referenced attribute);
`slot_getallattrs` (line 372) is reserved for whole-row ops
(`WHOLEROW`, tuple comparison, hashing all keys). Scans over
wide TPC-H/TPC-DS rows never decode unreferenced columns.

Hot-path chain: `ExecSeqScan` → `heap_getnextslot` (buffer
slot, no deform) → `ExecQual` → `EEOP_SCAN_FETCHSOME` →
`slot_getsomeattrs` → `slot_deform_heap_tuple` (once per needed
prefix) → `ExecProject` into a virtual slot.

`ExecMaterializeSlot` (same file, line 476) detaches a slot
from pins/buffers/transient tuples via `tts_ops->materialize`;
`ExecCopySlot` (line 525) copies through the *destination*
slot's `copyslot` into its own context. Lifecycle helpers:
`MakeTupleTableSlot`
(`postgres/src/backend/executor/execTuples.c:MakeTupleTableSlot`,
line 1301), `ExecSetSlotDescriptor` (same file, line 1478),
`ExecStoreHeapTuple` (line 1541), `ExecStoreMinimalTuple`
(line 1635), `ExecClearTuple` (`tuptable.h`, line 458),
`ExecFetchSlotHeapTuple` (`execTuples.c`, line 1833).

---

## 4. Scan nodes: Seq, Index, Index-only, Bitmap, TID

`SeqScan` (`postgres/src/backend/executor/nodeSeqscan.c:ExecInitSeqScan`,
line 207; `ExecSeqScan`, line 110) opens the relation with
`table_beginscan`
(`postgres/src/include/access/tableam.h:table_beginscan`,
line 875), then per call runs `heap_getnextslot`
(`postgres/src/backend/access/heap/heapam.c:heap_getnextslot`,
line 1387) into a buffer slot, checks `ExecQual`, and projects.
Mechanics in one paragraph: the only scan touching every page,
so predicate pushdown and deform laziness (§3) matter most here;
cost is purely sequential I/O plus per-tuple qual evaluation.

`IndexScan` (`postgres/src/backend/executor/nodeIndexscan.c:ExecInitIndexScan`,
line 909) positions an index AM scan with runtime keys, fetches
the heap tuple per TID, rechecks non-index quals, and supports
ordered output plus mark/restore for merge joins. Mechanics in
one paragraph: one index descent per distinct key plus random
heap fetches after, with prefetch hiding some I/O latency;
killed tuples are marked dead to the index AM.

`IndexOnlyScan`
(`postgres/src/backend/executor/nodeIndexonlyscan.c:ExecInitIndexOnlyScan`,
line 528; `ExecIndexOnlyScan`, line 337) returns index tuples
directly, skipping the heap fetch when the visibility-map bit
is set. Mechanics in one paragraph: the fastest scan for
covered aggregates and counts, which is why covering indexes
dominate OLAP microbenchmarks; unset VM bits still force heap
fetches for MVCC correctness.

`BitmapHeapScan`
(`postgres/src/backend/executor/nodeBitmapHeapscan.c:ExecInitBitmapHeapScan`,
line 333; `ExecBitmapHeapScan`, line 212) splits index access
from heap access: index scans build a `TIDBitmap`, iteration
uses `tbm_begin_iterate`
(`postgres/src/backend/nodes/tidbitmap.c:tbm_begin_iterate`,
line 1574), and heap pages are visited in physical order.
Mechanics in one paragraph: bitmap build cost up front, then
each heap page streamed once — random I/O converted into
sequential sweeps; lossy pages (TID pressure overflow) force
per-tuple rechecks while exact pages do not.

`TidScan` (`postgres/src/backend/executor/nodeTidscan.c:ExecInitTidScan`,
line 499; `ExecTidScan`, line 444) serves `ctid =` / `ctid IN`
by direct TID fetch; `TidRangeScan`
(`postgres/src/backend/executor/nodeTidrangescan.c:ExecInitTidRangeScan`,
line 359) scans a TID range. Both are O(pages touched), used
for `TABLESAMPLE` and constraint-driven plans. Remaining scans
share the `ExecInit<Node>` shape, including: `ExecInitCteScan`
(`nodeCtescan.c`, line 175), `ExecInitValuesScan`
(`nodeValuesscan.c`, line 210), `ExecInitForeignScan`
(`nodeForeignscan.c`, line 142), `ExecInitCustomScan`
(`nodeCustom.c`, line 26), `ExecInitSubqueryScan`
(`nodeSubqueryscan.c`, line 97), plus
`ExecInitNamedTuplestoreScan` (`nodeNamedtuplestorescan.c`),
`ExecInitTableFuncScan` (`nodeTableFuncscan.c`), and the
`WorkTableScan`/`RecursiveUnion` worktable path.

---

## 5. Joins I: NestLoop, MergeJoin, HashJoin

`NestLoop` (`postgres/src/backend/executor/nodeNestloop.c:ExecInitNestLoop`,
line 262; `ExecNestLoop`, line 60) re-scans the inner side with
the outer's `ParamExec` values for each outer tuple. Mechanics
in one paragraph: no build cost and no extra memory, but cost
is `outer × inner` plus a full inner `ExecReScan` per outer
row — it wins only with a tiny outer or a parameterized inner
index scan (the standard OLTP shape).

Hot-path chain: `ExecNestLoop` → outer `ExecProcNode` → bind
params → `ExecReScan` inner → inner `ExecProcNode` loop →
`ExecQual` → `ExecProject`.

`MergeJoin` (`postgres/src/backend/executor/nodeMergejoin.c:ExecInitMergeJoin`,
line 1439; `ExecMergeJoin`, line 594) walks two sorted inputs
with a sliding inner window; equal-key inner groups re-scan via
mark/restore (`ExecMarkPos` / `ExecRestrPos`, backed by
`tuplesort_rescan` or materialize rewind). Mechanics in one
paragraph: each side advances monotonically, total `sort + N +
M`; skew only lengthens the inner window, never re-sorts.

Hot-path chain: `ExecMergeJoin` → compare merge comparators →
advance smaller side → on equality mark inner, drain matches,
restore mark for the next outer duplicate.

`HashJoin` (`postgres/src/backend/executor/nodeHashjoin.c:ExecInitHashJoin`,
line 716; `ExecHashJoin`, line 684) builds once on the inner
relation (inner `Hash` child driven via `MultiExecProcNode`),
then hashes each outer tuple's keys, walks the bucket chain,
and checks join quals. Mechanics in one paragraph: unmatched-
outer handling (left/full) drains the table's unmatched marks
after probing; build cost is paid once regardless of outer
size, so it dominates large equijoins.

Hot-path chain: `ExecInitHashJoin` → `ExecInitHash` →
`MultiExecProcNode` → `ExecHash` → `ExecHashTableInsert` per
inner tuple → `ExecHashJoin`: hash outer →
`ExecHashGetBucketAndBatch` → chain walk → `ExecQual`.

---

## 6. Joins II: `Hash` internals — `dense_alloc`, batching, skew

`ExecInitHash` (`postgres/src/backend/executor/nodeHash.c:ExecInitHash`,
line 370) and `ExecHash` (same file, line 91) own the build
side. `ExecHashTableCreate` (line 446) sizes the table with
`ExecChooseHashTableSize` (line 658), converting planner
`ntuples × tupwidth` into `nbuckets` and an initial `nbatch`
from `work_mem`. Tuples pack contiguously into
`HASH_CHUNK_SIZE` arenas via `dense_alloc` (line 2896):
bump-allocate in the current `HashMemoryChunk` of `batchCxt`,
oversized tuples getting their own chunk. One `palloc` per
chunk instead of per tuple, with sequential bucket-chain
memory — the OLAP-critical allocator.

`ExecHashTableInsert` (line 1749) routes through
`ExecHashGetBucketAndBatch` (line 1960): tuples for inactive
batches go to spill files, not memory. Under pressure,
`ExecHashIncreaseNumBatches` (line 1030) doubles the batch
count and repartitions stored tuples; probe-side matches for
later batches spill symmetrically, so peak memory stays near
`work_mem` while each batch pair re-reads both sides. Skew is
pinned resident: the planner supplies MCV keys via the
`skewTable`, sized by `ExecChooseHashTableSize`
(`nodeHash.c:452-480`: `OidIsValid(node->skewTable)` into
`num_skew_mcvs`) and built by `ExecHashBuildSkewHash`
(`:633-638`, reading MCV statistics for the planner-named
relation/column); `ExecHashSkewTableInsert` (line 2601) pins
those planner-supplied keys resident in an in-memory skew
table that bypasses batching (probe-side skew check
`:181-187`), so the most common value never spills — the
executor pins, it does not detect. Parallel hash exchanges batch tuples through
`SharedTuplestore` (struct in
`postgres/src/backend/utils/sort/sharedtuplestore.c:SharedTuplestore`,
line 59; header `postgres/src/include/utils/sharedtuplestore.h`),
written via `sts_puttuple` (`nodeHash.c:1474,1889`) and read
via `sts_begin_parallel_scan` (same file, line 253) — one
writer per worker, coordinated readers. Every
`sts_begin_parallel_scan` caller outside `sharedtuplestore.c`
is parallel hash (`nodeHash.c:1526-1527` repartition scan,
`nodeHashjoin.c:1328,1357` batch inner/outer scans): this is
parallel-hash infrastructure, not GatherMerge exchange. The
parallel-hash build/probe phases otherwise reuse the
structures in shared memory with barrier synchronization.

---

## 7. `Memoize`: caching a parameterized inner side

`ExecInitMemoize`
(`postgres/src/backend/executor/nodeMemoize.c:ExecInitMemoize`,
line 952) sits above a parameterized inner plan (typically the
inner index scan of a nestloop) and caches result sets keyed by
outer parameters. `ExecMemoize` (same file, line 697) runs a
state machine: `MEMO_CACHE_LOOKUP` hashes current parameters
(building the table on first call, sized by `est_entries`); a
complete hit returns
`ExecStoreMinimalTuple(entry->tuplehead->mintuple, slot)` with
no inner-plan access; a miss creates the entry, fetches from
the inner plan, and appends each minimal tuple until EOF marks
it complete.

Mechanics in one paragraph: repeated outer keys (skewed FK
joins, correlated subplans) pay the inner scan once per
distinct key instead of once per row; hits cost one hash lookup
plus a slot store. Eviction is LRU over entries with
whole-scan bypass: entries that cannot be created or stored
fall into `MEMO_CACHE_BYPASS_MODE` (entry==NULL or
`cache_store_tuple` failure at `nodeMemoize.c:806-823`, same
on the fill path at `:896-907`) until the end of the scan,
streaming the inner plan directly (`:914+`), rather than
thrashing the cache. Rescans on parameter change
re-enter lookup; `cache_hits` is exposed in `EXPLAIN ANALYZE`.

Hot-path chain: `ExecMemoize` → `ResetExprContext` → hash
params → `cache_lookup` → hit: `ExecStoreMinimalTuple` →
miss: inner `ExecProcNode` → append minimal tuple → EOF marks
complete.

---

## 8. Aggregation: PLAIN / SORTED / HASHED / MIXED + `aggsplit`

`ExecInitAgg` (`postgres/src/backend/executor/nodeAgg.c:ExecInitAgg`,
line 3279) selects one of four strategies
(`postgres/src/include/nodes/nodes.h`, lines 362–363):
`AGG_PLAIN` (no grouping, single running state), `AGG_SORTED`
(group boundaries on sorted input, one transition state per
group, nothing spilled), `AGG_HASHED` (key → hash entry with
per-group transition states), `AGG_MIXED` (grouping-sets mixing:
hashed grouping sets accumulated in phase 0 while sorted/plain
phases 1..n stream — `nodeAgg.c:134` "an `AGG_MIXED` strategy
that populates the hashtables during the first sorted phase",
`:159-168` "the real node is marked `AGG_MIXED` if there are
both types present", i.e. hashed sets chained with
sorted/plain phases in the `chain` list).
`ExecAgg` (same `nodeAgg.c`, line 2244) drives the strategy:
plain/sorted feed each row to `advance_transition_function`
(line 708); hashed builds the table, then runs final functions
per entry.

Partial aggregation (also the parallel shape) is encoded per
aggregate by `aggsplit` (`nodes.h`, line 385, e.g.
`AGGSPLIT_INITIAL_SERIAL = SKIPFINAL | SERIALIZE`): the lower
node runs `transfn` with serializable partial states, the upper
runs `combinefn` + `finalfn`, so states cross worker/batch
boundaries as bytea. Ordered-set aggregates force sorted-mode
handling regardless of strategy.

Hot-path chain: `ExecAgg` → `advance_transition_function` per
row (`transfn` datum call) → group boundary / table-full →
`finalfn` per group → `ExecProject`. For OLAP the per-row
`transfn` call and per-group state size dominate; `MIXED`
exists so grouping sets mixing hashed and sorted groupings run
in one pass (hashed sets accumulate while sorted phases
stream), not as a memory-overflow mode. The hash-aggregation
spill escape is separate: past the memory limit PG enters
"spill mode" via `hash_agg_check_limits` — new-group tuples
spill to partitioned logical tapes for later batches, recursing
if a batch still overflows (`nodeAgg.c:200-211`).

---

## 9. Sort: `tuplesort` variants, datum fast path, top-N, logtape

`ExecInitSort` (`postgres/src/backend/executor/nodeSort.c:ExecInitSort`,
line 221) consumes the whole outer plan into a `Tuplesortstate`
before the first return; `ExecSort` (same file, line 50) emits
via `tuplesort_gettupleslot`
(`postgres/src/backend/utils/sort/tuplesortvariants.c:tuplesort_gettupleslot`,
line 1003). Constructors per input shape funnel into
`tuplesort_begin_common`
(`postgres/src/backend/utils/sort/tuplesort.c:tuplesort_begin_common`,
line 642): `tuplesort_begin_heap` (variants file, line 180),
`tuplesort_begin_cluster` (line 253),
`tuplesort_begin_index_btree` (line 359) /
`tuplesort_begin_index_hash` (line 441) for index builds,
`tuplesort_begin_datum` (line 668) — the single-datum fast path
that skips tuple formation for one pass-through sort column.

Mechanics in one paragraph: in-memory quicksort under
`work_mem`; beyond that, runs spill to temp files and a
polyphase merge over `LogicalTapeSetCreate` tapes
(`postgres/src/backend/utils/sort/logtape.c:LogicalTapeSetCreate`,
line 556) merges them, with `tuplesort_performsort`
(`tuplesort.c`, line 1363) choosing quicksort vs merge.
Abbreviated keys cut memcmp cost for text/numeric sorts.
`ORDER BY … LIMIT n` avoids the full sort:
`tuplesort_set_bound` (`tuplesort.c`, line 838), called before
loading via the `ExecInitLimit` → `ExecSetTupleBound`
(`execProcnode.c:848`, setting `SortState->bounded/bound` at
`:866-877`) → `tuplesort_set_bound` (`nodeSort.c:123-124`)
chain: `ExecInitLimit` (`nodeLimit.c:447`; `ExecLimit`,
line 40) calls `ExecSetTupleBound(compute_tuples_needed(node),
…)` at `nodeLimit.c:419-423`, which marks the child sort
bounded, and `ExecSort` applies the bound before loading —
keeping a top-N heap.
`IncrementalSort`
(`postgres/src/backend/executor/nodeIncrementalSort.c:ExecInitIncrementalSort`,
line 976) sorts only each group of a presorted prefix
(e.g. index order on `(a)` for `ORDER BY a, b`), so memory
scales with the largest group. `tuplesort_rescan`
(`tuplesort.c`, line 2402) rewinds for mark/restore and
nestloop rescans without rebuilding; `Unique`
(`postgres/src/backend/executor/nodeUnique.c:ExecInitUnique`,
line 114; `ExecUnique`, line 46) is adjacent-dedup on sorted
input, not a hash.

---

## 10. Parallel execution: `Gather` / `GatherMerge`, shared tuplestore

`ExecInitGather` (`postgres/src/backend/executor/nodeGather.c:ExecInitGather`,
line 53) launches up to `num_workers` backends on the same
subplan and funnels tuples through per-worker queues;
`ExecGather` (same file, line 137) reads whichever queues are
ready. Queue setup is `ExecParallelSetupTupleQueues`
(`postgres/src/backend/executor/execParallel.c:ExecParallelSetupTupleQueues`,
line 547). Mechanics in one paragraph: partial paths (`Partial
Aggregate`, `Partial Sort`) run below the gather while the
leader finalizes; serialization cost of partial states and
queue batching set the speedup ceiling, so narrow partial
targets parallelize better than wide ones.

`ExecInitGatherMerge`
(`postgres/src/backend/executor/nodeGatherMerge.c:ExecInitGatherMerge`,
line 67) adds ordering: each worker sorts its slice and the
leader merges heads with a binary heap (`lib/binaryheap.h`,
`nodeGatherMerge.c:21`; `gm_heap = binaryheap_allocate(…,
heap_compare_slots, …)` at `:430-431`;
`binaryheap_first/replace_first/remove_first` at `:567-578`,
comparator `heap_compare_slots` at `:752`) over per-worker
tuple queues fed via `execParallel.c` tuple-queue setup.
`ExecGatherMerge`
(same file, line 183) always returns the minimum head.
`Material`
(`postgres/src/backend/executor/nodeMaterial.c:ExecInitMaterial`,
line 164; `ExecMaterial`, line 39) buffers one side above an
unparameterized node for repeated rescans without re-execution.

---

## 11. `tuplestore` spill and who uses it

`tuplestore_begin_heap`
(`postgres/src/backend/utils/sort/tuplestore.c:tuplestore_begin_heap`,
line 330) creates a buffer with `randomAccess` (forward-only vs
rewindable/backward) and `maxKBytes` — "when in doubt, use
`work_mem`" per the header comment. Writes go through
`tuplestore_puttupleslot` (line 742), reads through
`tuplestore_gettupleslot` (line 1130). Under `maxKBytes`,
tuples live in an in-memory array; past it, chunks page out to
`BufFile` temp files transparently to readers.
`tuplestore_trim` (line 1412) discards consumed prefixes.

Consumers: `Material` (inner-side rescan buffer), `WindowAgg`
(`postgres/src/backend/executor/nodeWindowAgg.c:ExecInitWindowAgg`,
line 2431) peer buffering, `SetOp`/`RecursiveUnion`
worktables, CTE scans, and holdable-portal storage. Control
nodes share the `ExecInit<Node>` / `Exec<Node>` pattern:
`Append` (`nodeAppend.c:ExecInitAppend`, line 109;
`ExecAppend`, line 303), `MergeAppend`
(`nodeMergeAppend.c:ExecInitMergeAppend`, line 65;
`ExecMergeAppend`, line 215), `Result`
(`nodeResult.c:ExecInitResult`, line 180), `ProjectSet`
(`nodeProjectSet.c:ExecInitProjectSet`, line 227),
`ModifyTable` (`nodeModifyTable.c:ExecInitModifyTable`,
line 4632, the write path with `AFTER ROW` queueing).

---

## 12. Expression compilation: `ExecInitExpr` → `ExprEvalStep` → `ExecInterpExpr`

`ExecInitExpr` (`postgres/src/backend/executor/execExpr.c:ExecInitExpr`,
line 143) compiles an expression tree into a flat
`ExprEvalStep` array
(`postgres/src/include/executor/execExpr.h:ExprEvalStep`,
line 300); `ExecInitQual` (same `execExpr.c`, line 229) does
the same for a qual list with `AND` shortcutting, and
`ExecInitExprRec` (line 919) recurses per node type (`Var`,
`FuncExpr` via `ExecInitFunc`, line 2704, `OpExpr`,
`BoolExpr`, …). Each step is an `EEOP_*` opcode (e.g.
`EEOP_FUNCEXPR`, `execExpr.h` line 122): fetch-some-attrs
(`EEOP_*_FETCHSOME`), var load (`EEOP_*_VAR` with inline
`slot_getsomeattrs`), evaluation (`EEOP_FUNCEXPR`,
`EEOP_OPEXPR_*`, `EEOP_BOOL_*`, `EEOP_JUMP_*`), assignment
(`EEOP_ASSIGN_*_VAR`, `EEOP_ASSIGN_TMP`).

`ExecInterpExpr`
(`postgres/src/backend/executor/execExprInterp.c:ExecInterpExpr`,
line 460) is the interpreter loop: computed-goto dispatch
(`EEO_USE_COMPUTED_GOTO`, table at line 474) over the steps
with inner/outer/scan slots bound to locals, returning a
`Datum`. Projection compiles to the same steps and runs under
`ExecProject`
(`postgres/src/include/executor/executor.h:ExecProject`,
line 479); subplans compile via `ExecInitSubPlan`
(`postgres/src/backend/executor/nodeSubplan.c:ExecInitSubPlan`,
line 810) with `ParamExec` dataflow into `estate`. JIT reuses
the step list: `JitProviderCallbacks`
(`postgres/src/include/jit/jit.h`, lines 65–74) lets an LLVM
provider replace `evalfunc_private` (default `ExecInterpExpr`)
above the `PGJIT_*` cost thresholds. Deform laziness (§3) and
JIT attack the same `ExecInterpExpr` loop from opposite ends.

---

## 13. Memory contexts: AllocSet / generation / slab / bump, per-tuple reset

Every allocation belongs to a `MemoryContext` created by
`MemoryContextCreate`
(`postgres/src/backend/utils/mmgr/mcxt.c:MemoryContextCreate`,
line 1103) and freed in bulk by `MemoryContextReset` (same
file, line 386) / `MemoryContextDelete`. Four implementations
cover four lifetimes: `AllocSetContextCreate`
(`postgres/src/include/utils/memutils.h`, line 124) — chunked
freelists, the default; `GenerationContextCreate`
(`postgres/src/backend/utils/mmgr/generation.c:GenerationContextCreate`,
line 160) — append-mostly generations; `SlabContextCreate`
(`postgres/src/backend/utils/mmgr/slab.c:SlabContextCreate`,
line 322) — fixed-size blocks; `BumpContextCreate`
(`postgres/src/backend/utils/mmgr/bump.c:BumpContextCreate`,
line 131) — pure bump-pointer arenas with bulk reset.

Per-tuple discipline: each node owns an `ExprContext`
(`CreateExprContext`,
`postgres/src/backend/executor/execUtils.c:CreateExprContext`,
line 307; `ExecAssignExprContext`, line 485; `FreeExprContext`,
line 416) with `ecxt_per_tuple_memory`. Every `Exec<Node>`
starts with `ResetExprContext(econtext)`
(`postgres/src/include/executor/executor.h:ResetExprContext`,
line 646) — a `MemoryContextReset` of the per-tuple context —
so deform scratch, detoasted copies, and temporaries for row N
die before row N+1 untracked. Anything surviving the tuple
(projected output kept upstairs, hash entries, memoize tuples)
must first be copied into `es_query_cxt`, the node's own
longer-lived context, or a slot-owning context — the classic
use-after-reset bug class.

---

## 14. PG executor files vs role

| file (`postgres/…`) | role |
|---|---|
| `src/backend/executor/execMain.c` | `ExecutorStart/Run/Finish/End`, `ExecutePlan` loop |
| `src/backend/executor/execProcnode.c` | `ExecInitNode` switch, `ExecProcNodeFirst/Instr`, `MultiExecProcNode`, `ExecEnd/ShutdownNode` |
| `src/backend/executor/execAmi.c` | `ExecReScan`, mark/restore dispatch |
| `src/backend/executor/execTuples.c` | slot constructors, `ExecStoreHeap/MinimalTuple`, `ExecFetchSlotHeapTuple` |
| `src/include/executor/tuptable.h` | `TupleTableSlot`, four `TTSOps*`, `slot_getsomeattrs`, `ExecMaterializeSlot`, `ExecCopySlot` |
| `src/include/executor/executor.h` | `ExecProcNode`, `ExecProject`, `ResetExprContext`, `EState`/`ExprContext` |
| `src/backend/executor/execExpr.c` | `ExecInitExpr/Qual/ExprRec/Func` — step-list compilation |
| `src/backend/executor/execExprInterp.c` | `ExecInterpExpr` computed-goto interpreter, `EEOP_*` cases |
| `src/include/executor/execExpr.h` | `ExprEvalStep`, `ExprEvalOp` (`EEOP_*`) enum |
| `src/backend/executor/nodeSeqscan.c` | `ExecInit/ExecSeqScan`, `heap_getnextslot` driving |
| `src/backend/executor/nodeIndexscan.c` | keyed AM scan + heap fetch, ordered output |
| `src/backend/executor/nodeIndexonlyscan.c` | `ExecInit/ExecIndexOnlyScan`, VM-gated heap skip |
| `src/backend/executor/nodeBitmapHeapscan.c` | `ExecInit/ExecBitmapHeapScan`, bitmap → ordered heap sweep |
| `src/backend/executor/nodeTidscan.c`, `nodeTidrangescan.c` | `ExecInit/ExecTidScan`, `ExecInitTidRangeScan` |
| `src/backend/executor/nodeNestloop.c` | `ExecInit/ExecNestLoop`, parameterized inner rescans |
| `src/backend/executor/nodeMergejoin.c` | `ExecInit/ExecMergeJoin`, mark/restore window |
| `src/backend/executor/nodeHashjoin.c` | `ExecInit/ExecHashJoin`, probe loop |
| `src/backend/executor/nodeHash.c` | `ExecInit/ExecHash`, table create/insert, `ExecChooseHashTableSize`, batch growth, skew table, `dense_alloc` |
| `src/backend/executor/nodeMemoize.c` | `ExecInit/ExecMemoize`, param-keyed cache, LRU + bypass |
| `src/backend/executor/nodeAgg.c` | `ExecInit/ExecAgg`, four strategies, `advance_transition_function`, `aggsplit` |
| `src/backend/executor/nodeSort.c`, `nodeIncrementalSort.c` | `ExecInit/ExecSort`, presorted-prefix `ExecInitIncrementalSort` |
| `src/backend/executor/nodeLimit.c` | `ExecInit/ExecLimit`, bound pushdown for top-N |
| `src/backend/executor/nodeGather.c`, `nodeGatherMerge.c` | `ExecInit/ExecGather`, queues; ordered heap merge |
| `src/backend/executor/execParallel.c` | `ExecParallelSetupTupleQueues`, worker launch/sync |
| `src/backend/utils/sort/tuplesort.c` | `begin_common`, `set_bound`, `performsort`, `rescan` |
| `src/backend/utils/sort/tuplesortvariants.c` | heap/cluster/datum/index constructors, `gettupleslot` |
| `src/backend/utils/sort/logtape.c` | `LogicalTapeSetCreate`, merge-run tapes |
| `src/backend/utils/sort/tuplestore.c` | `begin_heap`, `put/gettupleslot`, `trim`, BufFile spill |
| `src/backend/utils/sort/sharedtuplestore.c` | `SharedTuplestore`, `sts_begin_parallel_scan` |
| `src/backend/utils/mmgr/mcxt.c` | `MemoryContextCreate/Reset/Delete`, context tree |
| `src/backend/utils/mmgr/aset.c`, `generation.c`, `slab.c`, `bump.c` | the four context implementations |
| `src/backend/executor/execUtils.c` | `Create/FreeExprContext`, `ExecAssignExprContext` |
| `src/backend/access/heap/heapam.c` | `heap_getnextslot`, heap AM scan |
| `src/backend/nodes/tidbitmap.c` | `TIDBitmap`, `tbm_begin_iterate`, lossy/exact pages |
| `src/include/jit/jit.h` | `JitProviderCallbacks`, `PGJIT_*` flags |
| `src/backend/executor/nodeAppend.c`, `nodeMergeAppend.c`, `nodeMaterial.c`, `nodeUnique.c` | `Append` / `MergeAppend`, rescan buffer, adjacent dedup |
| `src/backend/executor/nodeWindowAgg.c`, `nodeModifyTable.c`, `nodeSubplan.c` | window peers, write path, `ParamExec` dataflow |

---

## 15. The tuple journey: heap page to client

1. **Pin.** `table_beginscan`
   (`postgres/src/include/access/tableam.h:table_beginscan`,
   line 875) opens the AM scan; the buffer manager pins the
   next page. No tuple exists yet.
2. **Fill, no deform.** `heap_getnextslot`
   (`postgres/src/backend/access/heap/heapam.c:heap_getnextslot`,
   line 1387) points a `TTSOpsBufferHeapTuple` slot at the heap
   tuple with `tts_nvalid = 0`: visibility check plus slot
   store, zero column decoding.
3. **Qual gate.** `ExecQual` runs compiled `ExprEvalStep`s; the
   first `EEOP_SCAN_FETCHSOME` calls `slot_getsomeattrs`
   (`postgres/src/include/executor/tuptable.h:slot_getsomeattrs`,
   line 359), deforming only the prefix up to the highest
   referenced attribute. Rejected tuples die with untouched
   columns never decoded — the largest OLAP filter saving.
4. **Project.** `ExecProject`
   (`postgres/src/include/executor/executor.h:ExecProject`,
   line 479) evaluates the targetlist into a virtual slot; only
   projected columns become datums, and by-reference values
   still point into the buffer or per-tuple detoasted copies.
5. **Own at barriers.** Crossing a lifetime barrier (sort
   input, hash insert, memoize append, tuplestore write, gather
   queue) must survive unpin and the next `ResetExprContext`:
   `ExecMaterializeSlot` (`tuptable.h`, line 476),
   `ExecCopySlot` (line 525), or minimal-tuple copy /
   `tuplestore_puttupleslot`
   (`postgres/src/backend/utils/sort/tuplestore.c:tuplestore_puttupleslot`,
   line 742). A missed copy is use-after-reset corruption.
6. **Block.** Sort accumulates in `tuplesort` (§9), hash in
   `dense_alloc` arenas
   (`postgres/src/backend/executor/nodeHash.c:dense_alloc`,
   line 2896), aggregation in hash entries or runs — each with
   its own `work_mem` tripwire (§16).
7. **Reset.** `ResetExprContext`
   (`postgres/src/include/executor/executor.h:ResetExprContext`,
   line 646) → `MemoryContextReset`
   (`postgres/src/backend/utils/mmgr/mcxt.c:MemoryContextReset`,
   line 386) frees `ecxt_per_tuple_memory` before the next row;
   uncopied detoasted values die here by design.
8. **Receive.** `ExecutePlan`
   (`postgres/src/backend/executor/execMain.c:ExecutorRun`,
   line 297) hands the final slot to the destination receiver
   (client, cursor tuplestore, `EXPLAIN` counter).
9. **Output.** Only here is every selected column necessarily
   materialized (type output to the network buffer) — narrow
   `SELECT` lists save deform (§3) and conversion together.
10. **Tear down.** `ExecutorFinish` (`execMain.c`, line 406)
    drains triggers; `ExecutorEnd` (line 466) → `ExecEndNode`
    (`execProcnode.c`, line 562) releases slots, stores, tapes,
    and contexts.

Copy-point summary: deform lazily at first touch, own at every
barrier, free per-tuple scratch every row, convert to output
bytes only at the receiver.

---

## 16. Where `work_mem` bites

| site | structure / function | over-budget behaviour |
|---|---|---|
| `tuplesort` quicksort | memtuples cap from `tuplesort_begin_common` (`postgres/src/backend/utils/sort/tuplesort.c:tuplesort_begin_common`, line 642) | runs spill to `LogicalTapeSetCreate` tapes (`logtape.c`, line 556); `tuplesort_performsort` (line 1363) becomes polyphase merge — temp I/O + ~2× comparisons |
| bounded top-N | `tuplesort_set_bound` (same `tuplesort.c`, line 838) | top-N heap stays resident even when the full sort would spill; no bite unless unset (parallel leader ignores the bound) |
| hash build | `ExecChooseHashTableSize` (`postgres/src/backend/executor/nodeHash.c:ExecChooseHashTableSize`, line 658) → `nbatch`; `ExecHashIncreaseNumBatches` (line 1030) | growth repartitions stored tuples; each batch pair re-reads both sides; skew keys pinned by `ExecHashSkewTableInsert` (line 2601) stay resident |
| hash arenas | `dense_alloc` (`nodeHash.c`, line 2896) in `batchCxt` | one `palloc` per chunk; oversized tuples take dedicated chunks (`HASH_CHUNK_THRESHOLD`), so wide rows cannot fragment the arena |
| `tuplestore` | `maxKBytes` of `tuplestore_begin_heap` (`postgres/src/backend/utils/sort/tuplestore.c:tuplestore_begin_heap`, line 330) | past the cap, chunks page to `BufFile`s at disk latency; `tuplestore_trim` (line 1412) reclaims consumed prefixes |
| hashed agg | `AGG_HASHED` entries (`postgres/src/backend/executor/nodeAgg.c:ExecAgg`, line 2244) | pure `HASHED` with more groups than memory enters `hash_agg_check_limits` spill mode: new-group tuples spill to partitioned logical tapes (`nodeAgg.c:200-211`), later batches reprocessed recursively |
| bitmap | `TIDBitmap` (`postgres/src/backend/nodes/tidbitmap.c:tbm_begin_iterate`, line 1574) | TID pressure flips exact pages to lossy → per-tuple recheck (CPU) instead of precise fetch |
| memoize | entries (`nodeMemoize.c:ExecMemoize`, line 697) | LRU eviction + oversized-key bypass: no disk spill, miss cost is inner-plan re-execution |

One `work_mem` budget applies per sort/hash node instance (×
workers for parallel fragments), so concurrent spills multiply
temp-file I/O — multi-hash TPC-H plans degrade super-linearly
past the spill point.

---

## 17. Checklist: executor mechanisms a PG-compatible engine must reproduce for OLAP speed

- [ ] Volcano pull with per-node `ExecInit<Node>` /
      `Exec<Node>` plus first-call specialization
      (`execProcnode.c:ExecProcNodeFirst`) — one indirect call
      per tuple in hot loops, not a tag switch.
- [ ] Lazy deform (`tuptable.h:slot_getsomeattrs`): never
      decode untouched columns; whole-row deform only via
      `slot_getallattrs` paths.
- [ ] Four slot kinds with explicit ownership transfer
      (`ExecMaterializeSlot`, `ExecCopySlot`) and minimal
      buffer pins.
- [ ] Per-tuple reset (`ResetExprContext` → `MemoryContextReset`):
      everything surviving the row is copied out first (§15.5).
- [ ] Step-compiled expressions (`ExecInitExpr` →
      `ExprEvalStep` → `ExecInterpExpr`) with `FETCHSOME`-fused
      deform, `AND`-shortcut quals, JIT substitution point
      (`jit.h:JitProviderCallbacks`).
- [ ] All six scan shapes with their cost identities:
      streaming `SeqScan`, ordered `IndexScan`, heap-skipping
      `IndexOnlyScan`, I/O-reordering `BitmapHeapScan`, direct
      `TidScan` / `TidRangeScan` (one line item covering both TID
      variants, as §4 does).
- [ ] Three joins: rescan-driven `NestLoop`, mark/restore
      `MergeJoin`, build-once `HashJoin` with a
      `MultiExecProcNode`-driven `Hash` build.
- [ ] Hash internals (`nodeHash.c`): `ExecChooseHashTableSize`
      sizing, `dense_alloc` arenas, dynamic
      `ExecHashIncreaseNumBatches`, `ExecHashGetBucketAndBatch`
      routing, skew pinning.
- [ ] `Memoize` (`nodeMemoize.c:ExecMemoize`): param-keyed
      complete-flag entries, LRU eviction, oversized-key bypass.
- [ ] Four agg strategies (`AGG_PLAIN/SORTED/HASHED/MIXED`) +
      `aggsplit` two-phase partial/final with serializable
      states, per-row `advance_transition_function`.
- [ ] `tuplesort`: heap/datum/index constructors, abbreviated
      keys, quicksort→merge decision, logtape runs, `set_bound`
      top-N heap, `rescan` rewind, presorted-prefix
      `IncrementalSort`.
- [ ] `Gather`/`GatherMerge` with tuple queues
      (`ExecParallelSetupTupleQueues`) and binary-heap ordered
      merge (`binaryheap_allocate` + `heap_compare_slots`,
      `nodeGatherMerge.c:430-431,752); parallel-hash batch
      exchange via `SharedTuplestore` (`sts_puttuple` /
      `sts_begin_parallel_scan`).
- [ ] `tuplestore` with `maxKBytes`-gated memory→`BufFile`
      spill backing `Material`, window peers, CTEs, cursors.
- [ ] Four memory-context kinds with bulk reset and
      `ExprContext`-scoped lifetimes (`execUtils.c`).
- [ ] Lifecycle symmetry (`execMain.c`): `Start` builds
      `EState` + params, `Run` pumps `ExecProcNode` into a
      receiver, `Finish` drains triggers, `End` runs
      `ExecEndNode` — with `EXPLAIN ANALYZE` observing
      identical row flow via `ExecProcNodeInstr`.

Verification failures (queried, no location — not cited as
functions above): `ExecStoreTuple` (real names
`ExecStoreHeapTuple` / `ExecStoreMinimalTuple`, `execTuples.c`),
`ExecFreeExprContext` (real name `FreeExprContext`,
`execUtils.c`), `ExecProcNodeReal` (real wrappers
`ExecProcNodeFirst` / `ExecProcNodeInstr`, `execProcnode.c`),
`ExecInitTIDBitmapScan`, `ExecInitHashJoinOuter`,
`ExecAggTransReparent`, bare `tuplesort_begin_index` (real
constructors `tuplesort_begin_index_btree` /
`tuplesort_begin_index_hash`, `tuplesortvariants.c`),
`tuplesort_begin_index_unique`, `ExecInitTbmScan`.

(End of file)
