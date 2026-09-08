# EX2-01 — Retention-boundary audit (document only, no behaviour change)

```
label: EX2-01-audit | date: 2026-09-04
scope: every `cloneRowOwned` / `MaterializeArena` / `acquireRow` site
       in internal/executor/ (*.go, tests excluded from the site count)
method: rg enumeration + per-site read of the enclosing operator/seam;
        serena unavailable (initial_instructions timed out), used rg + read
gates: audit doc reviewed, no code. (TODO_EXECUTOR.md EX2-01;
      design docs/design/not_ralph/planner_refactor_take3/13-executor-target-design.md §4)
behaviour change: none — no existing file touched, no commit
rework: 2026-09-04, all sites + line numbers re-verified against HEAD
  6fa76854c (read-only rg/read; findings 1–6 addressed inline, §5 added)
```

Line numbers are HEAD as of 2026-09-04. Test pins name the closest
existing unit test; "suite values gate" means the site is covered only
by the TPC-H/TPC-DS values diff + plan-gate, with no dedicated unit pin.

Background (read once, referenced per site):

- `cloneRowOwned` (`datum.go:493`) = `acquireRow` dst + per-Datum
  `MaterializeArena`. Deep-copies arena bytes (String/Bytes/big-numeric)
  into owned storage AND copies the Row slice. The retention primitive.
- `Datum.MaterializeArena` (`datum.go:433`) = per-Datum promotion;
  no-op for non-arena Datums. Used where a single Datum (not a row) is
  retained (agg state, group keys, memoize entries).
- `acquireRow` (`row_pool.go:42`) = pooled `Row` backing only — shallow,
  preserves `ArenaID`. NEVER a retention primitive on its own; every
  `acquireRow`-only buffer below is either never retained past the next
  `Next()` (batch reuse) or retained via a paired `cloneRowOwned` /
  `Materialize` at the same seam.
- `cloneRow` (`datum.go:883`, via `acquireRow` at `datum.go:887`) is
  deliberately shallow and is NOT transfer-safe across worker or
  arena-reset boundaries (`parallel_runtime.go:37-40`). Not counted below.
- `slot.Materialize()` (`slot.go:113-116`, `slot.go:181-189`,
  `opnode.go:115-124`) and `MaterializeForTransfer`
  (`parallel_runtime.go:42-44`, thin wrapper over `cloneRowOwned`) are
  the indirect retention paths for sort/window/lockRows/Run/gather —
  they resolve to counted sites, listed once in §5.

## §1 cloneRowOwned call sites (18)

| # | site | enclosing operator / seam | what is retained and why | sole-owner bounded-lifetime candidate (EX2-02a/b/c feed) | test pin |
|---|---|---|---|---|---|
| C1 | `executor.go:761` | `RunFast` drain loop (slab path, public boundary) | Full row, deep copy: `dst.Cells` is reused every iteration; callers own the returned `[]Row` with unbounded lifetime (ownership boundary to the caller) | No — rows escape with unbounded lifetime; the copy IS the boundary | none (suite values gate) |
| C2 | `join_lateral_stream.go:193` | `lateralJoinStream.stepOuter`, outer-row hold | Outer tuple across the whole right-side re-execution (inner `Open`/`Next` drain overwrites left-child buffers); arena-gated, else slice copy | No — lifetime spans unbounded inner iterations; producer advances underneath | `from_srf_lateral_correlation_test.go` (shape); no unit pin on the clone |
| C3 | `join_nl_stream.go:257` | `nlJoinStream.stepOuter`, outer-row hold | Same as C2 for the nested-loop streaming join (outer revisited per inner tuple + `Rescan`) | No — same reason as C2 | `nl_semi_anti_join_test.go`, `right_join_spine_rows_test.go` (shape) |
| C4 | `operators_bitmap.go:734` | `bitmapHeapScanOp.fetchOneTuple`, page-tuple emit | Decoded heap tuple past page-buffer reuse / pin release; `o.scanRow` is overwritten per tuple | No — consumer lifetime unbounded, scan buffer must survive for the next tuple | none (suite values gate) |
| C5 | `operators_bitmap.go:793` | `bitmapHeapScanOp.fetchExact`, exact-TID emit | Same as C4 on the `fetchExact` (NLI-inner / EPQ refetch) path | No — same reason as C4 | `nli_left_residual_exec_test.go` (shape) |
| C6 | `operators_ddl.go:26293` | `ddlOp.execAlterDropColumn`, rewrite accumulation | Rewritten row in `allRows` past page unpin + pooled-scratch `releaseRow`; source is `newRow := make(Row, len-1)` (fresh, function-local), shallow-filled from the scratch, then unconditionally `cloneRowOwned` | **Yes (gated; cold path)** — `newRow` is sole-owned, so it could transfer directly when `!rowHasArena(newRow)` with `cloneRowOwned` only on the arena path; DDL rewrite is one-shot utility, NOT an EX2-02 priority | none (DDL rewrite values gate) |
| C7 | `operators_ddl.go:26619` | `ddlOp.execAlterColumnType`, rewrite accumulation | Converted row in `allRows` past page unpin + pooled-scratch `releaseRow`; unlike C6 the clone source IS the pooled scratch (`row := acquireRow`, mutated in place, `:26572-26615`) | No — the scratch must return to the pool for the next tuple; transfer would need a pool-discipline change. Same cold-path caveat as C6; both sites could still take a `rowHasArena` gate (elimination, not passing) | none (DDL rewrite values gate) |
| C8 | `operators_join_agg.go:933` | `ownedBuildRow`, hash-join build side (folded drain, ex-`drainRowsBounded` copy) | Build row for the life of the join (probe phase outlives every producer page arena); arena-gated, else O(width) struct copy | **Yes, scoped** — the COPY is sole-owned by the hash table (join-bounded lifetime), but the SOURCE (`r := slotRow(rSlot)`, `:871`) aliases the producer slot, so transfer needs a producer-side ownership change, not a local fold; EX2-02a seam target, probe-side transfer pairs here | `owned_build_poison_test.go` (5 tests, EX1-04 Cut 1) |
| C9 | `operators_join_agg.go:4150` | `drainRowsCtx`, generic drain (hash/merge/CTE builds) | Each drained row past producer slot reuse + arena reset; unconditional slice copy THEN conditional `cloneRowOwned` (double copy on the arena path) | **Yes** — fold to a single `cloneRowOwned`; drained rows are sole-owned with consumer-bounded lifetime | `derived_table_join_rows_test.go` (shape) |
| C10 | `operators_join_agg.go:4182` | `drainRowsCtxCTID`, CTID-preserving drain (build side under `FOR UPDATE`) | Same as C9 + captured TID sidecar | **Yes** — same fold as C9 | none (FOR UPDATE e2e gate) |
| C11 | `operators_lockrows.go:1691` | `lockRowsOp.refetchRow`, EPQ/lock refetch decode | Freshly decoded heap tuple returned past buffer unpin; UNCONDITIONAL clone (no `rowHasArena` gate unlike C2/C8/C9) | **Yes** — buffer is function-local and sole-owned; gate on `rowHasArena` per the sibling pattern | none (locking e2e gate) |
| C12 | `operators_material.go:274` | `materializeOp.Next`, rescan buffer append | Buffered row across child's next step + across `Rescan`; arena-gated, else make+copy (double copy on the arena path) | **Yes** — sole owner, rescan-bounded lifetime; same fold as C9 (feeds EX3 materialize work, not EX2-02) | none (suite values gate) |
| C13 | `operators_storage.go:2223` | `seqScanOp.Next`, page-tuple emit (M0100-0005) | Decoded + detoasted row BEFORE page `RUnlock`: concurrent UPDATE could otherwise tear bytes the parent is decoding; also detaches the per-page arena (reset at `curBlock++`, `:2328`) | No — `o.scanRow` is reused every tuple; consumer lifetime unbounded; the copy IS the boundary | `chain_tail_ctid_test.go` (tear shape); else suite gate |
| C14 | `opnode.go:118` | `Slot.Materialize`, slab concrete-slot boundary | Deep copy of `s.Cells` for consumers holding past the next `opNext` (slab `dst` slot reused per iteration, cf. C1) | No — producer `Cells` buffer reused; same aliasing contract as `projectOp.o.out` | none (slab parity gate) |
| C15 | `parallel_runtime.go:43` | `MaterializeForTransfer`, worker-queue crossing primitive | Full row made goroutine-safe: defeats BOTH slot aliasing and arena lifetime (doc `:21-44`); the ONLY sanctioned cross-worker move | N/A — it IS the transfer mechanism EX2-02c builds on | `parallel_substrate_test.go: TestAssertTransferableRejectsArenaBackedRow`, `TestAssertTransferableAllowsPermanentArena` |
| C16 | `slot.go:114` | `MaterializedSlot.Materialize`, in-place pipeline boundary | Promotes arena Datums + deep-copies the slice: producers (`projectOp.o.out`, scan `scanRow`) alias reused buffers overwritten on next `Next()` (M0092-0002 contract) | No — producer buffer reused, consumer (sort/window/lockRows/Run) lifetime unbounded | `datum_arena_test.go: TestM0073CloneRowOwnedPromotesArenaDatums`, `TestM0092MaterializeAlwaysDeepCopies`, `TestBigNumericArenaSurvivesReset` |
| C17 | `slot.go:188` | `VirtualSlot.Materialize`, virtual→materialized boundary (M0126-0001) | `cloneRowOwned(s.Row())`: virtual row past source slots' next step + arena detach | **Yes** — the `Row()` buffer (`slot.go:174`) is fresh per call and dead after; the pool churn (acquire, never released) is the EX2-02a/EX2-03 target | `join_slot_chain_test.go: TestProbeSeamZeroAllocs` (cost quantified) |
| C18 | `spill.go:614` | `drainRowsBounded`, pre-spill accumulate | Row held in memory across producer pages and, after spill, re-read from file; arena-gated, else struct copy | No — conditional pattern already optimal; spill/file lifetime unbounded | `spill_datum_contract_test.go`, `spill_test.go`, `sort_external_test.go` |

## §2 MaterializeArena call sites (14: 13 seams + M0 infra)

Per-Datum retention: the source is arena-owned (producer page reset
invalidates it) while the carrier (agg state, key vec, cache entry) is
freshly allocated, so only the Datum bytes — not the row slice — need
promotion. All are no-ops for non-arena Datums.

| # | site | enclosing operator / seam | what is retained and why | sole-owner candidate | test pin |
|---|---|---|---|---|---|
| M0 | `datum.go:500` | inside `cloneRowOwned` itself (infra, not a seam) | — | N/A | `datum_arena_test.go` (all M0073 tests) |
| M1 | `operators_join_agg.go:2280` | `aggregateOp.evalGroupExprs`, group-key build | Group key Datums in `groupRuntime.groupValues` past next input page `Reset` | No — group table lifetime = whole aggregation; source arena-owned, must copy | `grouping_sets_single_pass_test.go`, `groupagg_indexorder_data_test.go` |
| M2 | `operators_join_agg.go:2772` | `applyAgg` `min`, first-seen `st.value` | Running min/max state across input pages | No — state lifetime unbounded within the agg; arg aliases producer arena | `hypothetical_set_agg_errpos_test.go` (shape); agg values gate |
| M3 | `operators_join_agg.go:2781` | `applyAgg` `min`, new-minimum replace | Same as M2 (old `st.value` dead, new arg must be detached) | No — same reason; old-value death does not detach the new arg | same as M2 |
| M4 | `operators_join_agg.go:2785` | `applyAgg` `max`, first-seen | Same as M2 | No | same as M2 |
| M5 | `operators_join_agg.go:2794` | `applyAgg` `max`, new-maximum replace | Same as M3 | No | same as M2 |
| M6 | `operators_join_agg.go:2981` | `applyAgg` `array_agg` ORDER BY key capture | Per-row sort keys held to `finishAgg` sort | No — accumulation to end of group; multi-consumer (sort + render) | `agg_order_by_test.go` |
| M7 | `operators_join_agg.go:2989` | `applyAgg` `any_value`, first value | First non-null arg held to end of group | No — same class as M2 | none (agg values gate) |
| M8 | `operators_join_agg.go:3007` | `applyAgg` user-agg DISTINCT/ORDER BY row, sort keys | Accumulated rows held to `finishAgg` sort + sfunc replay | No — unbounded accumulation, multi-consumer | `operators_function_test.go` (UDA shape) |
| M9 | `operators_join_agg.go:3010` | same row, `arg` element | Same as M8 | No | same as M8 |
| M10 | `operators_join_agg.go:3015` | same row, `Arg2` element | Same as M8 | No | same as M8 |
| M11 | `operators_join_agg.go:3022` | same row, `ExtraArgs` elements | Same as M8 | No | same as M8 |
| M12 | `operators_join_agg.go:3208` | `evalAggOrderByKeys`, shared ORDER BY key eval | Per-row key vecs for `array_agg`/`string_agg` WITH ORDER BY, held to the `finishAgg` sort | No — accumulation to end of group; multi-consumer (sort + render) | `agg_order_by_test.go` |
| M13 | `operators_memoize.go:224` | `memoizeOp.Next`, fill-path entry build | Per-Datum promotion into the fresh `cp` entry row (`cp := make(Row, len(src))` then `cp[i] = d.MaterializeArena()`); the entry is retained in the per-key cache past the child's next page `Reset` — same M0073-0004 contract as hash-join builds and group keys (comment `:216-219`) | No — entry lifetime extends to eviction/budget-overflow and serves multiple future probes; source arena-owned, must copy | `memoize_exec_test.go` |

## §3 acquireRow call sites (13: 11 seams + A0/A1 infra)

`acquireRow` alone retains nothing (shallow, preserves `ArenaID`); each
site below is either batch reuse (buffer overwritten on the next `Next()`,
released at `Close`) or a scratch whose retained copy is made by a paired
§1/§2 site named in the row.

| # | site | enclosing operator / seam | role and paired retention | sole-owner candidate | test pin |
|---|---|---|---|---|---|
| A0 | `datum.go:494` | inside `cloneRowOwned` itself (infra, not a seam) | dst allocation for the deep copy | N/A | `datum_arena_test.go` (all M0073 tests) |
| A1 | `datum.go:887` | inside `cloneRow` (infra) | shallow dst; preserves `ArenaID`, explicitly NOT transfer-safe (`parallel_runtime.go:37-40`) | N/A (must never cross a boundary as-is) | `datum_arena_test.go: TestM0073RowHasArena` (detection side) |
| A2 | `operators.go:341` | `projectOp.Open`, reused output buffer `o.out` | Batch reuse: returned slot aliases `o.out` (M0092-0002); consumers retain via C16 | No — buffer reused every row; released at `Close:349` (EX2-03 sizes it) | none (aliasing contract doc `docs/design/0092-0002-projectop-slot-aliasing.md`) |
| A3 | `operators.go:461` | `resultOp.Open`, childless single-emit buffer | Same aliasing contract as A2 (first `Next` sets `emitted`, second is EOF) | No — same reason as A2 | none (suite values gate) |
| A4 | `operators.go:497` | `resultOp.Next`, with-child per-row buffer | Same aliasing contract as A2 under a `LIMIT 1` parent | No — same reason as A2 | none (suite values gate) |
| A5 | `operators_bitmap.go:198` | `bitmapIndexScanOp.lookupBounds`, key-eval scratch | Eval scratch only, never retained past the lookup call | No — never retained | none (suite values gate) |
| A6 | `operators_bitmap.go:700` | `bitmapHeapScanOp.fetchOneTuple`, decode buffer | Reused per tuple; retained copies leave via C4 | No — same pattern as A11/C13 | none (suite values gate) |
| A7 | `operators_bitmap.go:758` | `bitmapHeapScanOp.fetchExact`, decode buffer | Reused per call; retained copies leave via C5 | No — same reason as A6 | none (suite values gate) |
| A8 | `operators_ddl.go:26283` | `ddlOp.execAlterDropColumn`, decode scratch | Pooled scratch, `releaseRow` after C6 takes the retained copy | No — must be re-acquired per tuple; pairs with C6 | none (DDL rewrite values gate) |
| A9 | `operators_ddl.go:26572` | `ddlOp.execAlterColumnType`, decode scratch | Pooled scratch mutated in place, `releaseRow` after C7 takes the retained copy | No — pairs with C7 | none (DDL rewrite values gate) |
| A10 | `operators_index.go:621` | `indexScanOp.Next`, heap-fetch decode buffer | Reused per TID; no `cloneRowOwned` in this file — retention delegated downstream to consumer `Materialize` (C16) | No — batch reuse; consumer-side boundary | `index_scan_test.go`, `indexonly_rescan_test.go` (shape) |
| A11 | `operators_storage.go:2096` | `seqScanOp.Next`, decode buffer | Reused per tuple across pages; retained copies leave via C13 (or downstream C16) | No — the copy at C13 IS the boundary | none (suite values gate) |
| A12 | `slot.go:174` | `VirtualSlot.Row()`, fresh pooled row per call | Fresh buffer per call, handed out with NO `releaseRow` (pool churn); consumed immediately by C17's `cloneRowOwned` | **Yes** — dead right after `Materialize`; release-discipline or direct-fill target (EX2-02a/EX2-03, pairs with C17) | `slot_test.go: TestM0069VirtualSlot`; cost in `join_slot_chain_test.go: TestProbeSeamZeroAllocs` |

## §4 Count reconciliation (45 sites, checkable)

| family | cloneRowOwned (§1) | MaterializeArena (§2) | acquireRow (§3) | subtotal |
|---|---|---|---|---|
| join | C2, C3, C8, C9, C10 (5) | — | — | 5 |
| agg | — | M1–M12 (12) | — | 12 |
| gather | — (via C15; G1–G4 in §5) | — | — | 0 direct |
| sort | — (via C16; see §5) | — | — | 0 direct |
| spill | C18 (1) | — | — | 1 |
| scan | C4, C5, C13 (3) | — | A5, A6, A7, A10, A11 (5) | 8 |
| other | C1, C6, C7, C11, C12, C14, C15, C16, C17 (9) | M0, M13 (2) | A0, A1, A2, A3, A4, A8, A9, A12 (8) | 19 |
| total | 18 | 14 | 13 | **45** |

Infra rows (not seams, counted for checkability): M0, A0, A1.
Seams: 17 + 13 + 11 = 41.

## §5 Indirect retention paths (resolve to counted sites)

G — worker-queue crossings via `MaterializeForTransfer` (→ C15).
All four batch rows for a channel send; the copy is the queue-crossing
ownership boundary (arena rule `parallel_runtime.go:31-44`):

| # | site | seam | what crosses and why |
|---|---|---|---|
| G1 | `operators_gather.go:433` | `gatherOp.runWorker`, batch send | Worker-computed rows to the leader across the channel; EX2-02c (= 13 EX2-04) target seam | 
| G2 | `operators_gather_merge.go:283` | `gatherMergeOp.runWorker`, batch send | Same as G1 on the order-preserving path |
| G3 | `operators_gather_merge.go:330` | `gatherMergeOp.advanceRow`, leader-local `src.cur` | Does NOT cross a goroutine, but must survive until the heap pops it several `Next()` calls later — retained like any queued row |
| G4 | `parallel_hash_build.go:480` | parallel hash-build worker, batch send | Build-side rows to the shared build across the channel |

`slot.Materialize()` call sites (→ C16, C14, or C17): `sortOp.Open:898`
(sort/spill buffer), `windowOp.Open:62` (partition buffer),
`lockRowsOp.drainAndStamp:1004` (stamp-phase hold), `executor.Run:523`
(public boundary), `join_merge_stream.go:168,190` (merge-join primed
rows), `join_outer_fill.go:217` (FULL JOIN USING coalesce row),
`operators_join_agg.go:1512` (lazy probe-out row).

## Top 5 ownership-passing candidates for EX2-02 (revised)

1. **A12/C17** `VirtualSlot.Row()` + `VirtualSlot.Materialize` — pooled
   row never released, per-probe-row churn on the join seam (EX2-02a).
2. **C9** `drainRowsCtx:4150` — fold make+copy / conditional-clone double
   copy into one (EX2-02a).
3. **C10** `drainRowsCtxCTID:4182` — same fold (EX2-02a).
4. **C11** `lockRowsOp.refetchRow:1691` — gate the unconditional clone on
   `rowHasArena` (sibling pattern).
5. **C6** drop-column `newRow` — gated transfer when
   `!rowHasArena(newRow)`; cold-path DDL, so lowest EX2-02 priority.
   (C7 explicitly NOT a candidate: pooled scratch must be released.)

## Deviations / notes

- D1 (line drift, carried): TODO EX2-02c cites the gather
  worker-streaming site as `operators_gather.go:334-336`; those lines are
  the channel-closer invariant comment (M0127-P5.9). Actual sends are
  G1–G4 above.
- D2 (stale comment, no action): `parallel_runtime.go:51-53` says
  big-mantissa `KindNumeric` "falls through cloneRowOwned's else-branch
  with ArenaID intact". At HEAD `MaterializeArena` (`datum.go:433-452`)
  detaches `flagBigNumeric` via `mmgr.Perm()`; the audit's
  characterization (deep-copies big-numeric) matches HEAD behaviour —
  only the comment is stale.
- Otherwise none: all 45 line numbers verified at HEAD 6fa76854c;
  no other drift found.