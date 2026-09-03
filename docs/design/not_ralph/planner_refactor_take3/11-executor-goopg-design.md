# 11 — goopg executor design (counterpart to 10)

goopg's executor at HEAD `adf2d1e13` (Sep 2026), section-for-section against
the PG executor design in
[10-executor-pg-design.md](10-executor-pg-design.md): what each stage does,
where it lives, and where it diverges from PostgreSQL 18.3. Read doc 10
first; this document cites it and does not repeat it. Response context:
[07](07-gap-analysis.md) §6 (executor residual, out of scope for the planner
bundle), [08-target-design.md](08-target-design.md) (P4-04/05 executor
dependencies), [09-verification-and-acceptance.md](09-verification-and-acceptance.md)
(bars).

> Method note: Serena MCP timed out on the first symbol call
> (`serena_get_symbols_overview`, MCP error -32001), so every claim below is
> verified read-only via `Grep`/`Read` against the tree instead. Doc 10 landed
> concurrently while this file was drafted, so section order was aligned to
> its `##` headers post-hoc (each header carries the `↔ 10 §N` mapping);
> §§14, 17–18 have no 10 counterpart and exist because the brief requires
> them (codec/TOAST, storage/visibility, hot-spot table, verification log).
> Claims carried from 07 without re-verification are marked **[carried]**.

---

## 1. Dispatch: `Build` / `BuildWorker` / `BuildFast`, `Operator`, `opTreeSlab`, `OpIterator` (↔ 10 §1)

Every plan becomes a pull-based `Operator` tree
(`internal/executor/operator.go:34`): `Open(*Context)`, `Next() (TupleSlot,
error)`, `Close()`, plus `Schema()`. Three builders coexist:

| builder | site | role |
|---|---|---|
| `Build` | `executor.go:21` | legacy per-node `Operator` tree (interface dispatch) |
| `BuildWorker` | `executor.go:32` | per-worker subtree for parallel workers (`gatherOp.runWorker`, `operators_gather.go:334-336`) |
| `BuildFast` | `executor.go:633` (func `BuildFast`; returns `(*opTreeSlab, int32, error)`) | slab builder |

`opTreeSlab` (`opnode.go:239-257`) is the backing `[]OpNode` store; nodes are
appended via `add` (`opnode.go:257`) and addressed by `int32` index — no
GC-traced child pointers on the hot path (`opnode.go:292-303`, pooled schemas
via `schemaIdx`, `executor.go:534`). `buildRec` (`executor.go:486`) compiles
the plan recursively. `OpIterator` (`opnode.go:411-429`) wraps a slab as an
`Operator` for backward-compatible wiring (`BuildFastIterator`,
`opnode.go:429`); dispatch is `opOpen` / `opClose` / `opNext`
(`opnode.go:506,616,679`), with `filterOpNext` / `projectOpNext` /
`limitOpNext` as slab-native arms (`opnode.go:732,765,805`).

*Fidelity vs PG.* PG specialises per-node `PlanState` under `ExecInitNode`
and indirects through `ExecProcNode`; goopg's slab + `int32` indices +
`evalFastExpr` kind-switch is the structural analogue of `ExecReadyExpr`'s
linear `ExprEvalStep` array (take7 design §1), with no JIT. Live gap:
`buildRec` has **no `Gather` arm**, so any plan under a `Gather` falls back
to legacy `Build` and the compiled predicate is unreachable for every
parallel query (`design-take7.md` §1; `exprnode.go:305,526`).

---

## 2. Executor lifecycle: `Open` / `Next` / `Close`, `Context`, cleanup (↔ 10 §2)

PG's `ExecutorStart/Run/Finish/End` maps onto: builders (§1) → `Open`
(propagates `*Context`, `context.go:27`, allocating arenas, pins, hash tables)
→ `Next` pull loop (`RunFast`, `executor.go:650`, for slab trees) → `Close`
releasing pins, arenas and scan state (`releaseScanState`,
`operators_storage.go:1784-1815`) → per-query temp-file release
(`tempFileRegistry.release`, `tempfiles.go:91`; registry owned at
`context.go:380`).

*Fidelity vs PG.* No `ExecutorFinish`/`AfterTrigger` analogue on the read
path; resource discipline is per-operator `Close` plus the query-scoped
registry — leak-safe by construction, with none of PG's portal/memory-context
layering.

---

## 3. Slots and the Materialize boundary (↔ 10 §3)

`TupleSlot` (`slot.go:18-31`) is the inter-operator currency, with a
read-only column view `SlotView` (`slot.go:47-48`) that both slot kinds
satisfy. `MaterializedSlot` owns its `Row` (`slot.go:65-89`); `Materialize()`
is `cloneRowOwned` and therefore the ownership boundary (`slot.go:113-114`).
`VirtualSlot` references columns across source slots without copying
(`slot.go:130-165`); even its `Materialize()` now routes through the arena
step (`cloneRowOwned(s.Row())`, `slot.go:181-188`) after the fix that caught
it skipping the boundary. Row↔slot adapters are zero-copy where possible
(`asSlot`, `slot.go:202-248`; `SlotFromRow`, `:81`).

`materializeOp` (`operators_material.go:190-308`: `Open` `:216`, `Next`
`:249`, `Rescan` `:295`, `setUnbounded` `:308`) buffers the child for
re-scans (NL inner input, CTE scans) — P2-06 prices the NL inner as
materialised precisely because the executor materialises unconditionally
(take2 TODO P2-06).

*Fidelity vs PG.* PG deforms lazily (`slot_getsomeattrs` stops at the highest
referenced attnum) and offers minimal-tuple narrow rows; goopg deforms whole
projected rows up front (§4.1) and every owned row is a full `[]Datum` at 48
B/column (§13) — the deform gap is hot-spot row 10 (§17).

---

## 4. Scan nodes: seq, index, index-only, bitmap, TID (↔ 10 §4)

### 4.1 `seqScanOp` — decode and prefetch

`seqScanOp` (`operators_storage.go:979`) owns a borrowed scan row, a ring (or
bare page pin), and a per-operator mctx arena (`releaseScanState`,
`operators_storage.go:1784-1815`). Page bytes become `Row` via
`decodeScanRow` (`operators_storage.go:1378`) and the ranged variant
`decodeScanRowRange` (`operators_storage.go:1390`), with per-column type info
resolved once in `Open` (`colInfo`, take6 RESULTS §2). Toasted values resolve
inline (`DetoastRow`, `operators_storage.go:2143-2150`). Read-ahead is
`refillPrefetchWindow` (`operators_storage.go:1764-1782`): a lookahead window
of `Pool.Prefetch` calls (`operators_storage.go:1779`), **disabled for
parallel scans** — a worker's next block comes from the shared allocator, not
`curBlock+1` (`operators_storage.go:1765-1773`).

### 4.2 `indexScanOp` — btree read path and HOT

`indexScanOp` (`operators_index.go:185`) opens one `nbtree.BTree` handle per
`Open` (`operators_index.go:263-289`) and drives matches out of
`btree.RangeScan` (`operators_index.go:190`), keeping physical positions
(`poss []nbtree.ScanPos`, `operators_index.go:211`) and a kill list
(`killList []nbtree.KillItem`, `operators_index.go:212,577`) parallel to the
TIDs. Each heap fetch pins the page (`Pool.Pin`,
`operators_index.go:556`), then resolves HOT with the **no-copy** variant so
`tuple.Data` aliases the page bytes (`operators_index.go:561-567`,
`followHOTChainNoCopy`, `operators_index.go:75-80`; copying variant
`followHOTChain`, `operators_index.go:16-27`). The scan predicate evaluates
after fetch + detoast (`operators_index.go:636-653`).

### 4.3 `indexOnlyScanOp` — covering probe with heap fallback

`indexOnlyScanOp` (`operators_indexonly.go:24`, M0046-0004) serves rows from
the key when the Visibility Map says the page is all-visible
(`decodeRowFromKey`, `operators_indexonly.go:600`; key projection, `:652`).
Otherwise it falls back to heap fetch + HOT + MVCC (`:332-385`,
`followHOTChain` at `:362`). Multi-column equality probes ride
`compositeUpperBound` (`:232-290`); NLI parents drive it via `BindOuter` /
`Rescan` without re-running `Open` prep (`:206-217`).

### 4.4 Bitmap ops and `TIDBitmap`

`bitmapIndexScanOp` (`operators_bitmap.go:38`) feeds `TIDBitmap`
(`tidbitmap.go:16`; lossy/index-condition semantics, `tidbitmap.go:48`);
`bitmapHeapScanOp` (`operators_bitmap.go:266`) rechecks heap tuples with the
no-copy HOT walk (`operators_bitmap.go:680,738`); `bitmapAndOp` /
`bitmapOrOp` (`operators_bitmap.go:869,950`) combine streams.

### 4.5 `scanPrefilter`

`scanPrefilter` (`scan_prefilter.go:7-64`) pushes the `Filter`'s WHERE
predicate into the sequential scan so rejected rows die before the copy
(`evalPrefilter`, `scan_prefilter.go:142-163`): cached `SlotView`
(`:148`), compiled slab first (`evalFastExpr`, `:161`), interpreter fallback
(`evalExprSlot`, `:163`) — the take6 fix for `evalExpr`'s per-row interface
boxing (`:143-147`). Surviving rows are re-checked by `filterOp` above, which
still interprets (take7 results §7, ~0.33 pp).

*Fidelity vs PG.* PG places quals via `distribute_qual_to_rels` (P3-02, open);
goopg's prefilter is an executor-side copy of the same idea.

---

## 5. Joins I: hash / nested-loop / merge dispatch and streams (↔ 10 §5)

`joinOp` (`operators_join_agg.go:39`) implements all three algorithms in one
node, selected by `plan.Algo` at `Open` (`operators_join_agg.go:308-355`:
`JoinAlgoHash` → `:321`, `JoinAlgoNestedLoop` → `:323`, merge → `:355`).
`Next` (`operators_join_agg.go:1381`) is a three-arm dispatch (the merge arm
exists because merge state lives for the whole `Open`,
`operators_join_agg.go:128`).

Nested loop runs `nlJoinStream` — "the state machine. One instance per Open"
(`join_nl_stream.go:58-73`, phases `nlPhaseSweep/Inner/Outer/Done`,
`:200-343`; entry `openNestedLoop` `:100`, `nextNL` `:162`). The lateral
variant mirrors its shape (`join_lateral_stream.go:47-75`, `openLateral`
`:98`, `nextLateral` `:135`). Merge join runs `mergeSortedSource`
(`join_merge_stream.go:83`), an explicit phase machine (`phase`, `:456`;
`mjPhaseGroup/GroupFill/Merge/Tail*`, `:607-883`) on PG's pull-one-tuple-
per-side discipline (`join_merge_stream.go:21`). Keys compile at `Open`
(`join_compiled_key_test.go:134`; `initMergeKeys`, `join_merge_key.go:48`);
merge residuals evaluate per row (`mergeResidualMatch`,
`join_merge_key.go:106`). Single keys encode through `buildKeyOfRow`
(`join_batch.go:725`) with `Datum`- and string-lane evaluators
(`:1015-1072`); composite keys are covered by dedicated tests
(`join_composite_key_test.go:37`, `join_batch_test.go:330`). Outer-join NULL
extension is `join_outer_fill.go` (side predicates `:44-58`, null-key
recording `:116`, sweep emitters `:126-201`), cross-checked hash-vs-NL in
tests (`join_outer_fill_test.go:102-107`).

*Fidelity vs PG.* Same three algorithms, but goopg multiplexes them in one
node where PG has three node types — EXPLAIN must special-case the label
(`operators_explain.go:2091-2096,2541-2559`).

---

## 6. Joins II: hash internals — lazy build, geometry, batching, skew (↔ 10 §6)

The build is lazy (`buildLazyHashTable`, `operators_join_agg.go:535`;
`openLazyHashJoin`, `:477`) with shared presize (`presizeLazyHash`, `:743`)
driven by `hashsize` geometry (`buildGeometry`, `:699`). Build loops are
side-specialised (`buildLoopLeft`, `:781`; `buildLoopRight`, `:854`; CTID
variant `:948`), inserting via `insertBuildRow` (`:843`) /
`lazyHashInsertDatum` (`:1152`) with int→string demotion on overflow
(`demoteIntHash`, `:1184`). Multi-batch spill state is `hashBatchState`
(`join_batch.go:79`; ctor with sizing, `:293`; eligibility + fill predicates,
`:245-286`). Geometry comes from `hashsize.Choose` over
`EntryBytes(ncols, avgVarBytes)` with `DatumBytes = 48`, `RowSliceBytes = 24`
(`hashsize.go:42-50,90-188`); the session-less budget is `DefaultMemLimitBytes
= 512<<20` (`:78-83,172`) — the executor half of the still-512 MB default
(04 §12.4; TODO P2-02b orders the BootVal correction after P4-01).

*Fidelity vs PG.* PG hangs `Buckets/Batches/Memory Usage` off a standalone
`Hash` node; goopg reports off the Hash Join because the build lives
**inside** `joinOp` (0127 ledger note). Algorithm-identical sizing (take2
05:1610) with goopg constants — 48 B slot / 456 B entry vs 8 B / 136 B, **no
skew (MCV) buckets**, no parallel combined budget. Skew pricing is TODO
P2-11b; runtime skew handling is hot-spot row 13 (§17).

---

## 7. NLI and `Memoize`: caching a parameterised inner side (↔ 10 §7)

The planner rewrites eligible equi-joins in `rewriteJoinsToNLI`
(`internal/optimizer/nl_index_join.go:83-89`) behind the `GOOPG_NLI_COSTGATE`
escape hatch (`nl_index_join.go:52-55`). The inner is an index (only) scan
driven by `BindOuter` + `Rescan`, never re-`Open`ed (§4.3). Take2 measured NLI
offered 694× / accepted 23×, losing by 0.05%–12% — narrowly, not
systematically (07 §2.2 **[carried]**); retirement waits on the remaining
`btcostestimate` + hash terms (TODO P6-04).

`memoizeOp` (`operators_memoize.go:71`, S7, `:3`) caches inner results keyed
by outer parameters (`probeKey`, `:143`; `keyExprs`, `:129`). Interface
divergence: it has **no `Open` method** — entry is `openPrep` (`:96`) via the
NLI parent, with `BindOuter` (`:115`) and `Rescan` (`:156`) per outer row;
`Next` at `:194`, `Close` at `:251`. Cached rows are owned copies
(`MaterializeArena` per element, `:224`).

*Fidelity vs PG.* PG's parameterised rescan is `ExecReScan`; goopg's `Rescan`
protocol is the analogue, but `memoizeOp` cannot stand alone in a tree — a
real divergence, not naming (§18 row 4).

---

## 8. Aggregation: hashed / sorted / grouping sets, parallel split (↔ 10 §8)

`aggregateOp` (`operators_join_agg.go:1823-4101`, `Next` at `:4082`) runs
hashed aggregation from `Open` (`:1966`) and sorted aggregation from
`openSorted` (`:2455`); group expressions evaluate per slot (`evalGroupExprs`,
`:2262`), keys assemble per grouping set (`setGroupKey`, `:2290`), groups
finalise through `finalizeGroup` (`:2314`) and `finishAgg` /
`finishBuiltinAgg` (`:3566,3742`). Grouping-sets shape is pinned to one
aggregate over one scan (`grouping_sets_single_pass_test.go:170-174`).
`windowOp` borrows a bare `aggregateOp` as a frame helper
(`operators_window.go:194,562-630`). Parallel split is planner-gated
(`splitAggregateIsProfitable`,
`internal/optimizer/parallel_agg.go:269-282`); `parallel_agg_combine.go`
merges partial states (name normalisation `:30-32`); partial roots descend
past `aggregateOp`/`sortOp` (`parallel_scan.go:135,181,266`).

*Fidelity vs PG.* PG offers sorted + hashed grouping paths priced by
`cost_agg` with a hash-spill arm (P4-06, open); goopg picks strategy by rule
(TODO P4-06). Distinct sizing already matches PG exactly (TODO P1-25,
landed). No `MIXED`/partial-grouping cost competition yet.

---

## 9. Sort: chunk sort, spill, merge (↔ 10 §9)

`sortOp` (`operators.go:760-1258`) sorts memory-sized chunks (`chunkLimit`,
`:826`; `sortChunk`, `:957`) keyed by decoded vectors (`sortKeyVals`, `:905`;
`lessKeyVals`, `:923`; row fallback `lessRows`, `:1074`, CTID tiebreak tail
`sortTailWithCTIDs`, `:1003`). Overflow chunks spill (`flushChunk`, `:1119`)
to a file-backed `spillReader` or in-memory slice (`:1255-1258`) and merge
back (`initMerge`, `:1203`; `popMerge`, `:1236`; `Next`, `:1171`).

*Fidelity vs PG.* PG's `tuplesort` datum fast path, bounded (top-N) sort and
incremental sort have no counterpart: bounded sort is P4-04, Incremental Sort
is blocked on executor support (TODO P4-05, ledger `take2-P2-08` class), and
executor sort speed (`lessRows`, q16 34%) is a §17 residual.

---

## 10. Parallel execution: Gather / Gather Merge, shared builds (↔ 10 §10)

`gatherOp` (`operators_gather.go:49`, `Open` `:210`) fans out to workers that
each `BuildWorker` their own tree and stream materialised rows back
(`runWorker`, `:334-336`; `join_worker_path_test.go:27,66`). Gather Merge
(`gatherMergeOp`, `operators_gather_merge.go:46`, `Open` `:81`, worker `:193`)
preserves order (`parallel_merge_test.go:133-158`). Partial roots are placed
by `attachParallelScan` over `parallelScanState` (`parallel_scan.go:34`),
covering seq, bitmap heap (`:166`), index-only (`:251`), aggregates and
sorts — but **not plain IndexScan** (TODO P5-03, a `MISSING-NODE` entry).
Parallel hash build shares one build: `sharedHashBuild`
(`parallel_hash_build.go:42`; capture/apply `:67-87`), gated by
`parallelBuildEligible` (`:328-339`), driven lazily
(`parallelBuildLazyHashTable`, `:402`); only non-lateral `JoinAlgoHash`
participates (`:225`). Cross-goroutine Datum safety is explicit — workers
transfer only `ArenaID == 0` or permanent-arena Datums
(`parallel_runtime.go:31-71`; `parallel_substrate_test.go:26-80`).

*Fidelity vs PG.* PG merges worker counts through `SharedHashInfo` and prices
paths via `cost_gather[_merge]` (P5-04, open). goopg worker `Context`s are
built field-by-field, so per-worker hash counters die with the worker (0127
ledger note) — parallel hash stats cannot surface in EXPLAIN ANALYZE as PG's
do.

---

## 11. Spill files: `spillWriter` / `spillReader`, `tempFileRegistry` (↔ 10 §11)

`spillWriter` (`spill.go:20`) / `spillReader` (`spill.go:155`) stream rows to
temp files owned per-query by `tempFileRegistry` (`tempfiles.go:10-91`:
`dirFor` `:52`, `register` `:75`, `forget` `:82`, `release` `:91`). Sort
spill reuses the same substrate (§9). The round5 Stack-tax
elimination has **LANDED** (commit `1d6b1e396`, 2026-07-24 —
before this doc's `adf2d1e13` pin, verified by ancestry check):
`newSpillWriterInDir` caches the registry handle once at
construction (`spill.go:78-85`) instead of calling
`activity.LookupCurrentGoroutine()` (→ `runtime.Stack`) per
row, and the per-row path (`writeFrame`, `spill.go:121-135`)
uses the cached handle (`WaitEventStart/End`, no Stack walk);
the reader mirrors it (`newSpillReader`, `:181`). The only
`LookupCurrentGoroutine` sites left in the package are the two
constructors (once per file). The 69–86% / 3.3–7.3× round5
projections (`01-spill-writer-stack-elimination.md:12-23`)
are therefore superseded-with-evidence as a Stack-walk claim
— the mechanism they priced no longer exists. Remaining spill
cost (OPEN, residual TBD, for EX3-01 to re-measure on
Q4/Q7/Q13-class shapes): per-row `WaitEventStart/End` via the
cached handle, per-column `encodeDatum` (`appendRowPayload`,
`:111-118`), bufio buffered writes plus file I/O — and still
no run-formation or tape-merge discipline (§17 row 8).

*Fidelity vs PG.* PG spills through `tuplestore` logical tapes with
`work_mem`-sized runs; goopg spills whole encoded rows per writer — safe
(`release`) but with no run-formation or tape-merge discipline.

---

## 12. Expression compilation: interpreter, slot entry, slab fast path (↔ 10 §12)

The interpreter is `evalExpr(e, row, ctx)` (`expr.go:352`) over owned rows;
the slot entry is `evalExprSlot(e, slot, ctx)` (`expr.go:413`) over any
`SlotView`. The compiled fast path flattens expressions into an `exprTreeSlab`
beside the `opTreeSlab` (`exprnode.go:4`); `evalFastExpr` dispatches by
integer kind-switch (`exprnode.go:288`, func `evalFastExpr`, switch through
`:456`), with `ExprAdapter` delegating
unsupported nodes back to `evalExprSlot` (take7 design §1). Bounds-check
parity between the twins is load-bearing
(`exprnode.go:28,88-90,154,231`; `join_compiled_key_test.go:6-20`).

*Fidelity vs PG.* This is goopg's `ExecReadyExpr` interpreter half, no JIT.
Reachability gap, still live: only the scan prefilter is compiled on the
parallel path; `filterOp` above re-interprets the same predicate
(`operators.go:565`, take7 design §1) — compiling it needs a `Gather` arm in
`buildRec`, a planner-shape change (take7 results §7).

---

## 13. `Datum` and memory contexts: 48 B, arenas, `mmgr`, pools (↔ 10 §13)

`Datum` is exactly 48 B, pinned by `const _ uintptr = 48 -
unsafe.Sizeof(Datum{})` (`datum.go:187`) and `datum_arena_test.go:17-19`
(M0107-0002: 64 B → 48 B via `ArenaID uint16` replacing `*mctx.Context`,
`Big *big.Int` removal, KindArena merge). Hot-path `KindString`/`KindBytes`
carry `ArenaID≠0` + packed `(offset<<32|length)` into an mctx arena
(`datum.go:158-174`); `ArenaID=0` is `Buf`-backed (cold path). Payload access
looks the arena up per call (`mmgr.Lookup(d.ArenaID)`,
`datum.go:210,234,354,453,661,669`); big numerics promote into the permanent
arena (`parallel_substrate_test.go:62-80`). Row ownership transfer is
`cloneRowOwned` (`datum.go:493`) via `MaterializeArena` (`datum.go:434-478`;
`slot.go:95,185-187`); scratch rows come from `acquireRow(width)`
(`row_pool.go:42`, pooled widths ≤ 64) and return via `releaseRow`/`Release`
(`slot.go:118-122`; `operators_storage.go:1807-1815`).

*Fidelity vs PG.* PG's `Datum` is 8 B pass-by-value-or-pointer with per-tuple
context resets. goopg pays 48 B per materialised column — the co-dominant
half of the P2-02b residual with width (TODO P4-01b;
`FINDING-p401-alone-is-not-enough.md`). Take2 ruled out the two cheap
explanations (pointer-chasing, GC-tracing): the tax is the footprint itself.

---

## 14. Codec, TOAST, storage, visibility, I/O (no 10 counterpart; brief-required)

Heap bytes decode through `codec.go` (`decodePhysicalPGValueLowered`,
`decodeRowRangeInfo`; take6 RESULTS §2) with per-column type info resolved
once (`resolveColTypeInfo`, `coltypeinfo.go`). Compressed/out-of-line values
surface as `KindToastPointer` and resolve via `DetoastRow`
(`toast.go:514-522`; contract `:213`), called on scan paths
(`operators_storage.go:2143-2150`, `operators_index.go:636-641`) and before
expression use (`datum.go:55-57`). PG detoasts lazily per attribute; goopg
detoasts whole rows at scan time — correct, but wide columns are priced into
every consumer. Wide-text histograms have a catalog-writer gap (TODO P1-11).

Page reads pin through the pool (`Pool.Pin`, `bufpool.go:1871`; `TryPin`,
`:2038`), faulting via `Manager.ReadBlock` on miss (`bufpool.go:2001`;
`smgr.go:337-340`). Full scans may bypass the pool through `ScanRing`
(`scan_ring.go:17-75`). Index descent rides per-scan `nbtree.BTree` handles
(`operators_index.go:218,263-289`) with `RangeScan` positioning (`:190,479`).
Visibility is `transam.TupleVisible` (`visibility.go:36`) after HOT
resolution (§4.2), with the take6 atomics (`oldestClogXid` → `atomic.Uint32`,
subxact fast path; take6 RESULTS §3) and the later hint-bit follow-up that
removed the CLOG consult from the per-tuple path (take6 RESULTS §8).

Prefetch note: lookahead issues `Pool.Prefetch` hints (`bufpool.go:1346`),
not async reads. The AIO `ReadStream`
(`internal/storage/aio/read_stream.go:59-106`, lookahead default 4 / max 256)
**exists but is unwired** — `NewReadStream` has zero production callers
(test-only otherwise: 7 sites in `aio/read_stream_test.go`) (§18 row 2). PG 18.3 drives sequential prefetch through its `ReadStream`
API; goopg's analogue is pool hints, with parallelism from N workers, not
depth (§4.1).

---

## 15. Executor files vs role (↔ 10 §14)

| file(s) | role |
|---|---|
| `executor.go`, `operator.go`, `opnode.go`, `context.go` | builders, `Operator` interface, slab, per-query `Context` |
| `operators_storage.go`, `scan_prefilter.go` | seq scan, decode, prefetch, prefilter |
| `operators_index.go`, `operators_indexonly.go`, `operators_bitmap.go`, `tidbitmap.go` | index / index-only / bitmap scans, TID bitmap |
| `operators_join_agg.go`, `join_batch.go`, `join_nl_stream.go`, `join_merge_stream.go`, `join_merge_key.go`, `join_outer_fill.go`, `join_lateral_stream.go` | `joinOp`, batches, NL/merge machines, keys, outer fill, lateral |
| `operators_join_agg.go` (agg half), `parallel_agg_combine.go` | `aggregateOp`, partial-state combine |
| `operators.go` (sort half) | `sortOp` |
| `operators_gather.go`, `operators_gather_merge.go`, `parallel_scan.go`, `parallel_hash_build.go`, `parallel_runtime.go` | Gather, partial roots, shared builds, arena rules |
| `hashsize/hashsize.go` | build geometry |
| `spill.go`, `tempfiles.go` | spill streams, per-query file registry |
| `expr.go`, `exprnode.go` | interpreter, slot entry, compiled slab |
| `datum.go`, `row_pool.go`, `slot.go` | 48 B Datum, row pool, slots |
| `operators_material.go`, `operators_memoize.go` | Materialize, Memoize |
| `codec.go`, `coltypeinfo.go`, `toast.go` | decode, once-resolved types, detoast |
| `internal/optimizer/nl_index_join.go`, `internal/optimizer/parallel_agg.go` | NLI rewrite + cost gate, agg-split gate (planner side) |
| `internal/storage/bufpool.go`, `smgr.go`, `scan_ring.go`, `aio/read_stream.go` | pool, smgr, scan ring, (unwired) async stream |
| `internal/access/transam/visibility.go` | tuple visibility |

---

## 16. The tuple journey: heap page to client (↔ 10 §15)

One surviving row, hot non-parallel non-spilling path:

1. **Pin.** `seqScanOp.Next` (`operators_storage.go:1877`) holds (or
   re-acquires) the page pin; parallel scans read through the shared
   allocator (`:1765-1773`). Misses fault via `Pool.Pin` → `Manager.ReadBlock`
   (`bufpool.go:1871,2001`; `smgr.go:337-340`).
2. **Decode.** `PageGetHeapTupleInto` parses into the scan-owned scratch
   buffer (take6 RESULTS §2: one copy, not three); `decodeScanRow`
   (`operators_storage.go:1378`) deforms projected columns into the borrowed
   `scanRow` with once-resolved `colInfo`.
3. **Prefilter.** `evalPrefilter` (`scan_prefilter.go:142`) runs the compiled
   slab (`evalFastExpr`, `:161`) over the cached `SlotView` (`:148`); ~98% of
   Q6 rows die here, never reaching step 4 (take5 §2.1).
4. **`cloneRowOwned`.** Survivors detach via `cloneRowOwned` (`datum.go:493`)
   + `MaterializeArena` — moved to *after* the filter by take5 (take5 §2.1:
   old profile `acquireRow` 39.2% / `MaterializeArena` 28.5%).
5. **Slot.** Owned rows cross operators as `MaterializedSlot` (`SlotFromRow`,
   `slot.go:81`); joins compose inputs as `VirtualSlot` (`slot.go:146`) and
   re-materialise at each cascade seam (§17 row 11).
6. **Operators.** Each `Next()` pulls through `joinOp` (`:1381`),
   `aggregateOp` (`:4082`), `sortOp` (`operators.go:1171`) or `materializeOp`
   (`operators_material.go:249`); expressions run on compiled slabs with
   `ExprAdapter` fallback to `evalExprSlot` (§12).
7. **Final clone.** Results materialise once more at the boundary (gather
   workers stream materialised rows, `operators_gather.go:334-336`; slot
   copies preserve `ArenaID`, `:378`).

---

## 17. Where `work_mem` bites + measured hot spots: LANDED vs OPEN (↔ 10 §§16–17)

"LANDED" = shipped with alternating-A/B measurement and a byte-identical
result gate; "OPEN" = measured but unfixed, or fixed only on paper. Q6 chain
for orientation (serial): 23.40 → 11.51 (numeric) → 6.63 (take5) → 4.49
(take6) → 3.79 (take7); parallel 1.009 → 0.838, still 4.1× PG's 0.203 s
(take7 results §§1–2, :155). `work_mem` mechanics: geometry via
`hashsize.Choose` (§6); BootVal still 512 MB vs PG's 4 MB with the correction
ordered after P4-01 (04 §12.4; TODO P2-02b).

| # | hot spot | measurement | status | evidence pointer |
|---|---|---|---|---|
| 1 | Identical-plan Q6 gap | 23.404 s vs 0.9905 s, **23.6×** (parallel scales alike) | OPEN (residual) | `tpch-q6-numeric-decode/DESIGN.md:207-209`; README Scope; 07 §6 |
| 2 | Numeric text-decode per value | serial 23.40 → **11.51 s**, **2.0×** | LANDED | `DESIGN.md:41,747`; `benchmark-results-take5.md:32` |
| 3 | Copy-before-filter + 16-of-16 deform | 11.51 → **6.63 s**; `cloneRowOwned` 26.6% → 1.16% CPU | LANDED (narrow: prefilter + 6-of-16 on 98% rows) | `benchmark-results-take5.md:20-32,276-277` |
| 4 | Triple-copy `PageGetHeapTuple` + `evalExpr` boxing | alloc −95.8% (18.8M → 0.80M); serial 6.55 → **4.49 s** (1.46×) | LANDED | `benchmark-results-take6.md:16-44,68-72` |
| 5 | Per-value `ToLower` + per-tuple `RWMutex` (CLOG horizon, subxact) | `ToLower` 4.64% → absent; atomics 10.86% → 0.54%; Q14 **1.26×**, Q3 1.15×, Q10 1.17×, byte-identical | LANDED | `perf-optimize-take6/RESULTS.md:17-34,63-83` |
| 6 | Interpreted expressions on scan path | 4.563 → **3.792 s** serial, 1.009 → **0.838 s** parallel (**1.20×**) | LANDED (prefilter only; `filterOp` still interprets) | `benchmark-results-take7.md:15-60,146` |
| 7 | `Datum` 48 B/column (was 64 B) | reduction shipped (M0107-0002); remainder co-dominant with width (`orders` 128→64 batches, not 1) | LANDED as reduction; OPEN as residual | `0107-0002-datum-48b-arena-id-merge.md`; `datum_arena_test.go:17-19`; take2 `FINDING-p401-alone-is-not-enough.md`; TODO P4-01b |
| 8 | Spill `runtime.Stack` per row | 69–86% CPU on Q4/Q7/Q13 (4.7×/3.3×/7.3× projected) — SUPERSEDED as a Stack-walk claim: construction-time caching landed in `1d6b1e396` (2026-07-24, ancestor of this doc's pin); the per-row path uses the cached handle, no Stack walk remains | LANDED (elimination; residual spill cost TBD — per-row WaitEvent instrumentation + encode + file I/O with no run discipline, for EX3-01) | elimination: `spill.go:78-85,121-135,181` + ancestry (`1d6b1e396` ancestor of `adf2d1e13`); projections (history): `tpch-round5-fixes/01-spill-writer-stack-elimination.md:12-23` |
| 9 | `cloneRowOwned` / `MaterializeArena` allocation | 36–41% of objects; top allocator post-take6 | OPEN | `perf-optimize-take6/RESULTS.md:94-96` |
| 10 | Whole-row deform (16 cols; PG stops at attnum 6) | largest structural item per take4; 6-of-16 on 98% rows since take5 | PARTIAL (general fix = P4-01 PathTarget) | `DESIGN.md:668-669`; take5 `:21`; TODO P4-01 |
| 11 | Probe re-materialisation at cascade seams | ~18M pool round-trips, ~2×2.3 GB on Q9-class (6M rows × 3 levels) | OPEN | `analysis/cost-driven-second-try-200731/02-premise-audit.md:87-155`; 07 §6 |
| 12 | Sort speed (`lessRows`, `sortTailWithCTIDs`, q16 34%) | planner bounded/incremental sort in scope (P4-04/05); executor speed out of scope | OPEN | 07 §6; TODO P4-04/05 |
| 13 | Hash skew (flat table vs PG MCV partitioning) | needs Phase-1 MCV input first | OPEN | 07 §6; TODO P2-11b |
| 14 | Build-side 2× memory (two passes + two maps) | peak build ~2× PG | OPEN | 07 §6 |
| 15 | CLOG consult on hinted visibility path | removed from per-tuple path; differential test green | LANDED | `perf-optimize-take6/RESULTS.md:131-172` |
| 16 | Q14 24× / Q3 11× / Q10 4× "spill" ratios | re-attributed: plan choice (merge over full 6M-row index scan), **not** spill; neither engine batches | STALE as spill claim (numbers hold, attribution corrected) | take2 `FINDING-workmem-advantage.md:45-77` (§2b CORRECTION) vs 07 §6 |

---

## 18. Verification log

Checked read-only (Grep/Read; Serena timed out, see §0): `Build`,
`BuildWorker`, `BuildFast`, `opTreeSlab`, `OpIterator`, `Operator`
Open/Next/Close, `decodeScanRow`, prefetch window, HOT-follow variants,
`indexOnlyScanOp`, bitmap ops, `TIDBitmap`, `scanPrefilter`, `joinOp` dispatch
+ `Next`, `hashBatchState`, `nlJoinStream`, `mergeSortedSource`, composite-key
tests, outer-fill emitters, `rewriteJoinsToNLI` + cost gate, `memoizeOp`,
`aggregateOp` + `openSorted` + `setGroupKey` + `splitAggregateIsProfitable` +
combine, `sortOp` chunk/spill/merge, Gather/GatherMerge + workers,
`parallelBuildLazyHashTable` + `sharedHashBuild`, `hashsize` sizing,
`spillWriter`/`spillReader` + `tempFileRegistry`, `evalExpr`/`evalExprSlot` +
`evalFastExpr`, 48 B `Datum` + `ArenaID`/`mmgr` + `cloneRowOwned` +
`acquireRow`, slots + `materializeOp`, codec/`DetoastRow`/toast, bufpool/smgr
+ btree read path + `TupleVisible` + AIO `ReadStream`, Q6 series numbers,
take5/6/7 outcomes, round5 spill design status, cost-driven re-materialisation
figure, take2 48 B findings, 10's `##` headers (order alignment only).

Claims that **failed** verification (kept out of the sections above, or
flagged inline):

1. **Doc 10 was absent when drafted** (highest file was 09); order was aligned
   to its headers after it landed concurrently (§0).
2. **AIO `ReadStream` on the scan path** — false at this HEAD. The type exists
   (`aio/read_stream.go:59-106`, func `NewReadStream` at `:107`) but has
   zero production callers (7 test-only sites in
   `aio/read_stream_test.go`); scans
   prefetch via `Pool.Prefetch` (`bufpool.go:1346`) and read via
   `ScanRing`/`Manager.ReadBlock` (§14).
3. **07 §6's spill attribution** for Q14/Q3/Q10 — superseded by the finding it
   cites: `FINDING-workmem-advantage.md` §2b corrects "spill efficiency" to
   "plan choice" with plans reproduced (§17 row 16).
4. **`memoizeOp.Open` does not exist** — entry is `openPrep` via the NLI
   parent (§7). The uniform Open/Next/Close picture does not hold for this
   operator.
5. **`filterOp` compilation** — the take7 win covers the scan prefilter only;
   the `filterOp` above still calls `evalExprSlot` directly (`operators.go:565`
   via take7 design §1), so "compiled expressions" as a blanket claim would be
   false (§12).

(End of file)
