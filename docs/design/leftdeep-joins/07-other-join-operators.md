# 07 — The Rest of the Join Inventory: Merge, Nested Loop, Outer Fill, NLI, Parallel

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| PG oracle | `postgres/src/backend/executor/nodeMergejoin.c` (EXEC_MJ state machine, mark/restore); `nodeNestloop.c`; `nodeMaterial.c`; `nodeHashjoin.c` HJ_FILL_INNER/HJ_FILL_OUTER handling |
| scope | every join-related operator not covered by [05](05-executor-pipeline-rework.md)/[06](06-hash-spill-and-memory.md); user directive explicitly includes join-operator rework beyond hash |

## 1. Current state (what "materialise everything" still means here)

- **Merge join** (`runMergeJoin`, `internal/executor/operators_join_agg.go:730`):
  both sides fully drained, keys evaluated via a fresh `concatRows` per row
  (`:892-897`), `sort.SliceStable` in memory, **entire join output**
  accumulated into `o.rows`. No streaming, no spill.
- **Nested loop** (`runNestedLoop`, `:364`): both sides drained; fresh
  `concatRows` per **candidate pair** (`:388` — historically 56 GB alloc on
  Q9, 7,980 GB on Q20); full output buffered.
- **Lateral** (`openLateral`, `:251`): per-outer-row re-execution into
  `o.rows`.
- **RIGHT/FULL** are forced onto merge join by planning rule
  ([0003-0001](../0003-0001-planner-overview.md)), inheriting merge's full
  materialisation.
- **NLI** (`operators_nljoin.go`) and **Memoize** are already streaming and
  zero-copy — the executor's good citizens.

## 2. Merge join → streaming with pathkey-fed inputs

Under the new DP, a merge path exists only when both inputs deliver the key
order (Sort paths or index order, via `pathkeys.go`). The operator then
becomes PG's streaming merge:

- pull one tuple per side, advance the lesser (EXEC_MJ_SKIP states);
- equal-key **groups** buffer only the inner group (mark/restore semantics —
  we buffer the group in memory rather than implementing tuplestore
  mark/restore in v1; groups above `work_mem` overflow to a `spillWriter`
  file, reusing the [06](06-hash-spill-and-memory.md) registry);
- multi-column keys come from `HashKeys`/pathkeys
  ([05](05-executor-pipeline-rework.md) §5) — the current "first equality
  conjunct is the merge key, rest is per-pair residual" (`:805-816`)
  becomes full-key comparison with residual only for non-equijoin conjuncts;
- output streams through the same composed-`VirtualSlot` seam as hash join
  (E1's machinery) — no `o.rows` accumulation;
- Sort inputs spill via the existing external `sortOp` — already correct.

RIGHT/FULL on merge keep working during migration; after §3 they compete on
cost instead of being forced.

## 3. Outer-join fill in hash join (removes the RIGHT/FULL→merge pin)

Add PG's matched-tuple tracking to `joinOp` hash mode:

- one bit per build tuple (per batch), set on every successful
  probe match — PG's `HeapTupleHeaderSetMatch` analogue, a `[]uint64`
  bitmap parallel to each bucket's row list;
- **RIGHT join** = INNER emit + post-probe sweep emitting unmatched build
  rows null-padded on the probe side (HJ_FILL_INNER_TUPLES);
- **FULL join** = LEFT fill (existing miss/residual-fail paths,
  `:1234-1241`, `:1360-1369`) + the RIGHT sweep;
- per-batch: the sweep runs after each batch's probe replay
  ([06](06-hash-spill-and-memory.md) §2.5);
- planner: `add_paths_to_joinrel` may now generate hash paths for
  RIGHT/FULL, and the `BuildLeft` pin for non-INNER relaxes to PG's actual
  legality matrix (`hash_inner_and_outer` generates JOIN_RIGHT etc. with
  fill on the appropriate side).

This closes a semantic *and* a performance hole: RIGHT/FULL currently
inherit merge's full materialisation unconditionally.

## 4. Nested loop → streaming outer + Materialize inner

Replace drain-both-and-buffer-output with PG's shape:

- outer streams one tuple at a time;
- inner is wrapped in a new **`Materialize` operator**
  (`nodeMaterial.c` analogue): first pass caches the inner's output
  (memory → spill file past `work_mem`, reusing `spillWriter`), rescans
  replay the cache — the planner adds it below the inner side of an NL path
  exactly when PG would (`cost_rescan` logic: inner without a cheap rescan);
- the join emits through the shared composed-slot seam; `concatRows`-per-
  candidate-pair dies;
- keyless Semi/Anti (`:156-165`) keep their early-out but stream the outer.

`Materialize` is a general operator (plan node + path), also usable under
merge join's inner in a later pass; v1 constructs it only for NL inners.

## 5. Semi / Anti / NULL-aware

Hash Semi/Anti stay probe-streaming (`:1335-1366`) and gain E-series
improvements automatically (multi-key, single map, spill). The NOT IN
NULL-aware short-circuit keeps its semantics with the batch-global
`antiBuildHasNull` rule from [06](06-hash-spill-and-memory.md) §2.5.
Unnest-produced semi/anti joins remain pinned opaque for the search
([03](03-join-search-pg-dp.md) §2, §4.4) in v1; folding them into the DP as
JOIN_SEMI/JOIN_ANTI jointypes (PG's SpecialJoinInfo machinery) is the
recorded follow-up — **bushy-phase-dependent** per
[03](03-join-search-pg-dp.md) §4.3 (ordering restrictions can make left-deep
levels unplannable), ledger row at implementation time.

## 6. NLI and Memoize become paths (behaviour unchanged)

`nestedLoopIndexJoinOp` and `memoizeOp` are untouched at runtime. What
changes is provenance: NLI/Memoize enter the DP as parameterised paths
([03](03-join-search-pg-dp.md) §5.2) instead of being bolted on by
`rewriteJoinsToNLI` after order selection — for searched joins the
constructor is invoked from `createPlan` with the DP's decision already
made, honouring the one-constructor rule. The NLI cost gate's legacy env
switches (`GOOPG_NLI_COSTGATE=legacy`) retire with the rewrite-as-authority
role; `enable_nestloop_index` (the real GUC) keeps working as a path
disable.

## 7. Parallel interplay

- `gatherOp`/`gatherMergeOp` and worker builds (`BuildWorker`) are
  unaffected structurally; workers run the same binary cascade and benefit
  from the same seam fixes (the E1 fix must be exercised under
  `inWorker=true` in tests — fusion's decline-in-worker precedent shows the
  worker path diverges easily).
- Shared hash build (`parallel_hash_build.go`): stays leader-serial;
  interacts with [06](06-hash-spill-and-memory.md) §6 (no shared spilling
  builds in v1 — a build that wants nbatch > 1 declines sharing).
- Partial paths: the DP integrates with the existing partial-path design
  from [cost-model/08](../cost-model/08-parallel-paths-and-degree.md) — each
  joinrel may carry a `partialPathlist`; a Gather path materialises at the
  top exactly as that chapter specifies. Nothing here changes worker-count
  policy.

## 8. EXPLAIN

- Hash join gains `Batches`/memory lines ([06](06-hash-spill-and-memory.md)
  §4); merge/NL lose nothing;
- the `MultiHashJoin` EXPLAIN arms (`operators_explain.go:1386`, `:1562`)
  are deleted in [08](08-migration-and-removal.md)'s final stage;
- goal state: for the TPC-H/DS suites, goopg's join-spine EXPLAIN output is
  node-for-node comparable with PG 18.3's (modulo cost literals), enabling
  the [09](09-verification-and-acceptance.md) §4 structural diff gate.
