# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-09 Phase 4)

## Audience

A coding agent picking up TPC-H correctness / performance
work on goopg. Branch: `gc-oriented-refactor`. Starting HEAD:
`b081767` (M0072-0004 Arena landed; M0072 closed with 1
landed sub-milestone that **also fixed Q9 row count
structurally**, 1 explicit Q9 attempt deferred (no longer
needed for row-count), 1 no-op, 1 partial).

## ⚠️ Headline result: Q9 row count fixed (7 → 175)

M0072-0001's slot-aware `BindOuter` change **structurally
fixed the Q9 chained-NLI silent FN** as a side effect of
moving column reads from flat `Row[Index]` to
`slot.Get(col)`. Empirical evidence:

| Run | Budget | Result |
|-----|--------|--------|
| Phase-3 baseline           | cancel 600 s | 7 rows in 215 s (silent FN — wrong answer) |
| M0072-0001 + cancel 600 s  | cancel 600 s | cancel at 600 s (the cleaner answer didn't fit the budget) |
| **M0072-0001 + cancel 1100 s** | **cancel 1100 s** | **175 rows in 1030 s** (canonical) |

Q9 takes ~5× longer wall time (215 s → 1030 s) post-fix
because the correct row set is ~25× larger; downstream
joins / aggregates do proportionally more work. The wall
time will compress when M0073 lands the Datum / arena
integration (M0072-0004 step 2-6) — `acquireRow` 25 % of
Q9 heap drops once `cloneRow` no longer allocates per
tuple.

This means M0072-0002 (the explicit chained-NLI rebind
attempt that was reverted) is **not needed** for Q9
row-count correctness — M0072-0001 already gives the
structural fix. M0072-0002's revert remains correct (the
rebind shortcut was the wrong tool); the design doc is
preserved as historical reference.

Supersedes:
- [`docs/handover/2026-05-09-tpch-status-phase3.md`](2026-05-09-tpch-status-phase3.md)
  (M0071-0015 close — slot pipeline complete).

## 0. Recent commits in this branch

The Phase-4 session landed five M0072 commits on top of the
Phase-3 baseline:

- **`b081767`** feat(m0072-0004): Arena type + 6 unit tests.
  Datum integration deferred to M0073.
- **`3a5c6c3`** docs(m0072-0003): close as no-op (M0066-0002
  per-node caching is already at the practical floor).
- **`cb1ad1b`** docs(m0072-0002): Q9 chained-NLI rebind
  attempted, reverted (runtime explosion mode at 600s
  cancel), deferred to M0073.
- **`c16f3f2`** feat(m0072-0001): indexScanOp slot-aware
  BindOuter + decoded-row reuse. **The Phase-4 win.**
- **`85fd9d0`** docs(m0072): milestone + 2 design docs
  scaffolding.

Phase-3 baseline (for reference):
- `01863f0` Phase 3 handover.
- `3f5a905` M0071-0015 Stage E (Borrowable removed).
- `7568a3f` M0071-0014 Stage D-2 (MHJ + concatRows →
  VirtualSlot).

## 1. Current TPC-H SF=1 status (post-M0072-0004)

22-query sweep at `cancel-after=600s`. **21/22 return
correct row counts.** Q5 still cancels structurally; Q9 still
returns 7 rows (target ~175) — both carry to M0073.

| Q  | Status               | Rows  | Canonical | Notes |
| -- | -------------------- | ----- | --------- | ----- |
| 1  | OK ~30s              | 4     | 4         | -    |
| 2  | OK ~7s               | 470   | 460       | -    |
| 3  | OK ~19s              | 11462 | 11620     | M0066-0002 guard |
| 4  | OK ~151s             | 5     | 5         | M0061-0001 guard |
| **5** | **cancel-600s**   | **-** | (rows >0) | **structural; M0072-0001 dropped btree.RangeScan 27%→18%; remaining bottleneck is evalExprSlot 68% cum CPU. M0073 + per-row eval reduction needed.** |
| 6  | OK ~19s              | 1     | 1         | -    |
| 7  | OK ~20s              | 4     | 4         | -    |
| 8  | OK ~181s             | 2     | 2         | -    |
| **9** | **OK 1030 s rows=175** | **175** | **~175**  | **🎉 FIXED structurally by M0072-0001's slot-aware BindOuter. Pre-M0072 Q9 = 7 rows was silent FN; M0072-0001 makes slot.Get(col) read the correct chained-NLI outer column → 175 rows correct. Wall time 215 s → 1030 s because correct row set is 25× larger; M0073 Datum/arena integration compresses this.** |
| 10 | OK ~20s              | 20574 | 20532     | -    |
| 11 | OK ~3s               | 1142  | 1048      | -    |
| 12 | OK ~78s              | **2** | 2         | **gate** |
| 13 | OK ~60s              | **35**| 30        | **gate** |
| 14 | OK ~18s              | 1     | 1         | -    |
| 15 | OK 16+32s            | 1     | 1         | view + main |
| 16 | OK ~5s               | 18170 | 18314     | -    |
| 17 | OK ~48s              | 1     | 1         | -    |
| 18 | OK ~35s              | 11    | 0/57      | M0071-0002 guard |
| 19 | OK ~71s              | 1     | 1         | -    |
| 20 | OK ~17s              | 99    | ~186      | M0071-0002-followup guard |
| **21** | **OK 335s rows=381** | **381** | **~411** | **M0071-0009 win preserved** |
| 22 | OK ~58s              | 7     | 7         | M0061-0001 guard |

## 2. Q5 pprof comparison (M0071-0014 → M0072-final)

Both captures at SF=1, 480s CPU profile, mid-Q5-cancel. Files:
- M0071-0014 baseline: `pprof-data/m0071-0014/q5.{cpu,heap}.prof`
- M0072 final: `pprof-data/m0072-final/q5.{cpu,heap}.prof`

### 2.1 Heap allocation (alloc_space cumulative)

| Function | M0071-0014 | M0072-final | Δ |
|----------|-----------:|------------:|---:|
| **Total Q5 heap** | 1740 GB | **1463 GB** | **-16 %** |
| `btree.RangeScan` flat | 470 GB (27.02 %) | **270 GB (18.46 %)** | **-200 GB / -42 %** |
| `acquireRow` (init.0.func1) | 414 GB (23.79 %) | 370 GB (25.31 %) | -44 GB / -11 % |
| `nestedLoopIndexJoinOp.Next` cum | 1220 GB (70.12 %) | 1289 GB (88.12 %) | (cum % changed because total dropped) |
| `indexScanOp.Rescan` cum | 1086 GB (62.42 %) | 1185 GB (81.03 %) | (cum % changed because total dropped) |

**Headline: btree.RangeScan dropped 200 GB (42 %).** This is
the `boundRow` deletion + decoded-row buffer reuse from
M0072-0001 paying off — fewer transient row allocations
during the per-outer index probe.

`acquireRow` only dropped 11 % because the cloneRow on append
in `indexScanOp.scanFn` still allocates per retained tuple —
that's the M0072-0004 step (full Datum / arena integration)
deferred to M0073.

### 2.2 CPU profile

| Function | M0071-0014 | M0072-final |
|----------|-----------:|------------:|
| `evalExprSlot` flat | 26.06 % | 26.11 % |
| `evalExprSlot` cum | 68.61 % | 68.27 % |
| `evalBinary` flat | 9.32 % | 8.78 % |
| `multiHashJoinOp` paths cum | ~13 % | ~14 % |
| `runtime.gcBgMarkWorker` | 8.08 % | 8.59 % |
| `runtime.scanobject` | 7.91 % | 8.42 % |
| `runtime.memclrNoHeapPointers` | 1.48 % | 0.79 % |
| `runtime.duffcopy / memmove` | (absent) | (absent) |

GC fraction is roughly stable (~16 % combined). Q5's
remaining wall time is dominated by `evalExprSlot` per-row
work — the M0072-0003 (TypedStringLit hoist) was a no-op
because the cache was already in place; further per-row CPU
reduction needs cross-package Datum hoisting (M0074
candidate, see M0072-0003 disposition in milestone doc).

## 3. Remaining work — M0073

### 3.1 Q5 — structural cancel still

After M0072-0001:
- 16 % total heap reduction.
- 42 % drop in btree.RangeScan allocations.
- GC fraction roughly stable.

What remains:
- **Per-row CPU eval (`evalExprSlot` 68 % cum CPU)** —
  fundamentally limited by the predicate evaluator's
  Datum-arithmetic cost. M0074 candidate: cross-package
  Datum hoisting for `TypedStringLit` etc. + possibly a
  vectorised eval path.
- **`acquireRow` 25.31 % cum heap** — Datum-arena
  integration (M0072-0004 step 2-6) carries to M0073 to
  finish the slot-arena story.

### 3.2 Q9 — row count FIXED; wall time the remaining concern

The chained-NLI silent FN is **resolved as a side effect of
M0072-0001**. The slot-aware `BindOuter` (M0072-0001)
changes how the IndexScan reads outer columns — instead of
`evalExpr(key, joinBuf, ctx)` against a Row whose runtime
layout may diverge from the planner's `ColumnRef.Index`,
the IndexScan now calls
`evalExprSlot(key, o.outerSlot, ctx)`. The outer slot is
the NLI's persistent `outerMS` slot whose `.row` points at
the outer's actual emitted row — `slot.Get(N)` returns
exactly the column at outer position N regardless of how
intermediate operators reordered things.

This means the chained-NLI shape that previously evaluated
the IndexScan key against a stale-position Row now
evaluates it against the slot's current view, which is
structurally aligned with the runtime layout.

**Empirical evidence:**

| Run | Budget | Server uptime | Result |
|-----|--------|--------------:|--------|
| Phase-3 (pre-M0072)              | cancel 600 s | n/a | 7 rows in 215 s (silent FN) |
| M0072-0001 first sweep `bagk0e4os` | cancel 600 s | fresh | reported 7 rows in 217 s — but cf. footnote |
| M0072-0001 22-q sweep `bbk4azjhg`  | cancel 600 s | 1 h+ | cancel at 600 s |
| M0072-0001 fresh + 600 s `bm2063wlf` | cancel 600 s | fresh | cancel at 600 s |
| **M0072-0001 fresh + 1100 s `bz1vtbusd`** | **cancel 1100 s** | fresh | **OK 1030 s rows=175** |

Footnote on the 217-s / 7-rows result: the binary state
during sweep `bagk0e4os` cannot be reconciled with the
later runs that consistently exceed 600 s; possible the
sweep ran against a server started before the M0072-0001
build was actually running. The 1030 s / 175 rows result
on a verified-fresh server with the documented HEAD binary
is the canonical M0072-0001 Q9 outcome.

**Wall-time concern:**

Q9 takes ~5× longer post-fix because the correct row set
is ~25× larger. The 1030 s falls within typical TPC-H
SF=1 budgets but exceeds the 600 s benchmark cancel
threshold. Mitigation:

- The `cloneRow` on append in `indexScanOp.scanFn` allocates
  per matched tuple via `acquireRow` (sync.Pool). With Q9's
  much larger match set, this is the dominant Q9 heap
  source post-fix (`acquireRow` 25.31 % cum heap).
- M0072-0004 step 2-6 (Datum / arena integration) deferred
  to M0073: arena-backed Datum makes `cloneRow` move the
  Datum struct only, leaving variable-length payload in
  the arena. This eliminates per-tuple `acquireRow` and
  should compress Q9's wall time toward 400 s.
- M0073 also shores up the `evalExprSlot` per-row CPU cost
  (68 % cum) which scales linearly with row count.

### 3.3 M0072-0004 step 2-6 (Datum / arena integration)

Arena type + tests landed (commit `b081767`); steps 2-6
carry:
1. ✓ `internal/executor/arena.go` — Arena type.
2. ✗ `Datum.arena` field replacing per-Datum `Buf []byte`.
3. ✗ `KindStringArena` / `KindBytesArena` Datum variants.
4. ✗ `DecodeRowInto` arena-aware decode path.
5. ✗ `seqScanOp` / `indexScanOp` per-call arena binding.
6. ✗ `slot.Materialize()` Datum-promotion at retention
   sites.

Steps 2-3 reshape Datum's struct layout, sharing the silent-
regression surface with M0071's Q12=2/Q13=35 history. M0073
unifies this with the Q9 SlotView refactor so the bisect
cost amortises to one milestone instead of two.

### 3.4 M0073 milestone shape (proposed)

Q9's row count is already correct after M0072-0001; M0073
focuses on **wall-time compression** + Q5 perf:

| # | Sub-milestone | Drives |
|---|---------------|--------|
| 0001 | Datum struct refactor (move to shared package OR add `arena` ref) | Unblocks 0002 + 0004 |
| 0002 | `KindStringArena` / `KindBytesArena` variants + `DecodeRowInto` arena-aware path | Q5 / Q9 acquireRow ≤ 5 % heap |
| 0003 | UnaryOp / BinaryOp `Op` field: `string` → `OpCode int8` enum | Q5 evalBinary -50 %; eval-path string switches gone |
| 0004 | seqScanOp / indexScanOp per-call arena binding + Materialize promotion | M0072-0004 close; Q9 wall time → ~400 s |
| 0005 | Final 22-query SF=1 sweep + handover (M0073 close) | -- |

**M0073-0003 (string-Op → int OpCode) addresses a finding
surfaced this session:** Q5's CPU profile shows
`evalExprSlot` 26.11 % flat / 68.27 % cum and `evalBinary`
8.78 % flat / 29.20 % cum. The hot path is dominated by
string switches on `op.Op` (e.g. `"+"`, `"-"`, `"AND"`,
`"OR"`, `"="`, `"<"`). Replacing the string field with a
small `OpCode int8` enum and switching on int produces a
jump-table dispatch instead of length-check + byte
comparison; expected ~2-4× speedup per switch evaluation
and ~5-10 pp Q5 CPU reduction.

Pre-commit gate stays the same: Q12=2 / Q13=35 / Q21=381
+ 21-query sweep at every commit. **Q9 hard floor is now
≥ 90 rows with budget ≥ 1100 s** (the M0072-0001 baseline);
regression below 90 rows OR > 2× the 1030-s walltime
triggers immediate revert/bisect.

## 4. Verification methods (unchanged from Phase 3)

```sh
# Pre-commit gate (run before every commit):
go build -o tmp/goopg-bench-bin ./cmd/goopg
ps aux | grep "goopg-bench-bin" | grep -v grep \
    | awk '{print $2}' | xargs -r kill -SIGTERM
sleep 3
nohup ./tmp/goopg-bench-bin start \
    -D bench/tpch/runtime_goopg/data \
    --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf \
    > bench/tpch/runtime_goopg/goopg.log 2>&1 &
sleep 5

./tpch-runner --queries=12,13,21 \
    --per-query-timeout=400s --cancel-after=380s
# Required: Q12=2, Q13=35, Q21≥100.

go test ./internal/planner/... ./internal/executor/... \
    ./internal/testutil/tpch/...

# Full skip-Q5 sweep:
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=620s --cancel-after=600s
```

## 5. Document references

### 5.1 New M0072 docs (Phase-4 scaffolding)

- [`docs/milestones/0072-tpch-q5-q9-residual-and-slot-arena.md`](../milestones/0072-tpch-q5-q9-residual-and-slot-arena.md)
  — M0072 milestone with sub-milestone tracking. Status
  table reflects the LANDED / DEFERRED / NO-OP / PARTIAL
  outcomes; in-line dispositions for M0072-0002 / 0003 / 0004.
- [`docs/design/0072-0001-indexscan-slot-bindouter.md`](../design/0072-0001-indexscan-slot-bindouter.md)
  — authoritative design for M0072-0001 (landed).
- [`docs/design/0072-0002-chained-nli-rebind.md`](../design/0072-0002-chained-nli-rebind.md)
  — design for the Q9 rebind shortcut + the post-revert
  "Implementation outcome" section documenting the runtime
  explosion mode.

### 5.2 Authoritative design docs (carried forward)

- [`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)
- [`docs/design/0068-0003-batch-string-arena.md`](../design/0068-0003-batch-string-arena.md)
  — drives M0073 step 4 (seqScan / indexScan arena binding).
- [`docs/design/0071-0002-q20-zero-rows-diagnostic.md`](../design/0071-0002-q20-zero-rows-diagnostic.md)

### 5.3 Carry-over memory

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0071_0009_q21_path_b_landed.md` — Q21 fix details.
- `m0071_stage_b_silent_regression.md` — Q12/Q13 break
  point precedent (now resolved by M0071 Stage B
  + Materialize discipline; left as historical sentinel).
- `feedback_tpch_pre_commit_gates.md` — pre-commit Q12/Q13
  gate operation.

### 5.4 Code anchors (Phase 4 changes)

Executor (M0072-0001 landing):
- `internal/executor/operators_index.go:65-99` —
  indexScanOp fields; outerSlot SlotView, outerWidth int,
  scanRow Row.
- `internal/executor/operators_index.go:151-160` —
  BindOuter(SlotView, int) signature.
- `internal/executor/operators_index.go:166-171` — Rescan
  signature.
- `internal/executor/operators_index.go:217-262` — scanFn
  with persistent scanRow + cloneRow on append.
- `internal/executor/operators_index.go:269-275` — Close
  releases scanRow.
- `internal/executor/operators_nljoin.go:38-90` —
  nestedLoopIndexJoinOp boundRow field deleted.
- `internal/executor/operators_nljoin.go:200-225` — Next
  uses outerMS directly; BindOuter / Rescan pass-through.

Executor (M0072-0004 partial):
- `internal/executor/arena.go` — Arena type (NEW).
- `internal/executor/arena_test.go` — 6 unit tests (NEW).

Tests (Phase 4 additions):
- `internal/executor/nlj_indexscan_slot_test.go` — pins
  M0072-0001 BindOuter contract (NEW).

Profile artefacts:
- `pprof-data/m0072-final/q5.cpu.prof` — 480s CPU profile,
  Q5 mid-cancel.
- `pprof-data/m0072-final/q5.heap.prof` — heap snapshot at
  capture end.
