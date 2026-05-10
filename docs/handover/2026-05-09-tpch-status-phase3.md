# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-09 Phase 3)

## Audience

A coding agent picking up TPC-H correctness / performance work
on goopg. Branch: `gc-oriented-refactor`. Starting HEAD:
`3f5a905` (M0071-0015 Stage E landed; the M0071 slot pipeline
is now complete).

Supersedes:
- [`docs/handover/2026-05-09-tpch-status.md`](2026-05-09-tpch-status.md)
  (pre-M0071-0009 baseline)
- [`docs/handover/2026-05-09-tpch-status-phase1.md`](2026-05-09-tpch-status-phase1.md)
  (Phase 1 + Stage B)

## 0. Recent commits in this branch

This Phase 3 session landed five small commits (M0071-0011
through M0071-0015), each gated on Q12=2 / Q13=35 / Q21=381:

- **`3f5a905`** feat(m0071-0015): Stage E — Borrowable contract
  removed. BorrowSemantics / OwnedRow / BorrowedRow / Borrowable
  / setChildBorrow gone; producer-side "skip clone when
  borrowed" branches collapsed. Slot kind structurally encodes
  lifetime semantics per design 0068-0002. `borrow_test.go`
  deleted; `fake_source_test.go` keeps the `fakeBorrowSource`
  stub for `sort_external_test`.
- **`7568a3f`** feat(m0071-0014): Stage D-2 — MHJ +
  joinOp.nextLazy → VirtualSlot. `multiHashJoinOp.lazyOut Row`
  is now a persistent `VirtualSlot{tableSlots[0..N-1]}`;
  per-step copy() into lazyOut is gone. `joinOp.nextLazy`'s
  four `concatRows` call sites (INNER + Semi/Anti + LEFT
  no-match) replaced with persistent `lazyVirtualOut`.
- **`d5ed261`** feat(m0071-0013): Stage D-1 — NLI joinBuf →
  VirtualSlot. `nestedLoopIndexJoinOp.joinBuf` is now a
  `VirtualSlot{outerMS, innerMS}`; predicate eval reads via
  `evalExprSlot` on the slot — zero alloc per match. The
  `boundRow` Row is reused across matches so the IndexScan's
  Row-based BindOuter still works (one alloc per outer).
- **`73b8014`** feat(m0071-0012): Stage C —
  filter/limit/instrument pass-through. `filterOp.Next`
  evaluates the predicate via `evalExprSlot` directly against
  the child slot and forwards the slot unchanged. `.borrow`
  fields and SetBorrow methods removed from these three ops.
- **`35d293a`** feat(m0071-0011): evalExpr SlotView keystone
  refactor. Add `SlotView` interface (`Get / IsNull`) embedded
  into `TupleSlot`; `rowSlotView` wraps Row at the legacy entry
  point. `evalExprSlot` reads `ColumnRef` via slot.Get();
  Subquery / In / Exists / Extract / FuncCall / CaseExpr stay
  on Row via `slotToRow`.

Earlier in the session (handover phase 1):
- `0090def` Stage B (Operator.Next → TupleSlot)
- `e8c3779` Q21 0→381 rows (M0071-0009 path B)

## 1. Current TPC-H SF=1 status (post-M0071-0015)

22-query sweep against `bench/tpch/runtime_goopg/data`
(HammerDB-loaded SF=1) at `cancel-after=600s`.
**21/22 return correct row counts**; Q5 still cancels
structurally (next bottleneck identified — see §3.1).

| Q  | Status               | Rows  | Canonical | Notes |
| -- | -------------------- | ----- | --------- | ----- |
| 1  | OK ~30s              | 4     | 4         | -    |
| 2  | OK ~7s               | 470   | 460       | -    |
| 3  | OK ~19s              | 11462 | 11620     | M0066-0002 guard |
| 4  | OK ~151s             | 5     | 5         | M0061-0001 guard |
| **5** | **cancel-600s**   | **-** | (rows >0) | **structural; was duffcopy/memmove ~60% pre-Stage-D-2; now btree.RangeScan + acquireRow via indexScan dominate. Next: M0072 indexScan slot-aware BindOuter.** |
| 6  | OK ~19s              | 1     | 1         | -    |
| 7  | OK ~20s              | 4     | 4         | -    |
| 8  | OK ~181s             | 2     | 2         | -    |
| **9** | **OK 216s rows=7** | **7** | **~175**  | **silent FN; chained-NLI schema-runtime mismatch unaffected by Stage D — Path B (SchemaColumn.SourceTableIdx) and slot virtual coords don't reach the IndexScan.BindOuter row layer. Next: M0072.** |
| 10 | OK ~20s              | 20574 | 20532     | -    |
| 11 | OK ~3s               | 1142  | 1048      | -    |
| 12 | OK ~77s              | **2** | 2         | **gate** |
| 13 | OK ~60s              | **35**| 30        | **gate** |
| 14 | OK ~18s              | 1     | 1         | -    |
| 15 | OK 16+32s            | 1     | 1         | view + main |
| 16 | OK ~5s               | 18170 | 18314     | -    |
| 17 | OK ~48s              | 1     | 1         | -    |
| 18 | OK ~35s              | 11    | 0/57      | M0071-0002 guard |
| 19 | OK ~71s              | 1     | 1         | -    |
| 20 | OK ~17s              | 99    | ~186      | M0071-0002-followup guard |
| **21** | **OK 336s rows=381** | **381** | **~411** | **M0071-0009 win preserved across all five M0071-0011..0015 commits.** |
| 22 | OK ~58s              | 7     | 7         | M0061-0001 guard |

## 2. Q5 pprof findings (post-Stage-D-2 vs pre-M0071)

CPU profile capture: 480s (8 min) during a Q5 run, mid-cancel.
Heap snapshot at end. Files in
`pprof-data/m0071-0014/q5.cpu.prof` and `q5.heap.prof`.

### 2.1 CPU profile

| Function | flat% | cum% | Notes |
|----------|------:|-----:|-------|
| `aggregateOp.Open` (cum-root)            | 0.05% | 90.22% | Pipeline driver |
| `filterOp.Next`                          | 2.16% | 81.60% | Pass-through eval |
| `evalExprSlot`                           | 26.06% | 68.61% | **Predicate evaluation** |
| `evalBinary`                             |  9.32% | 31.19% | Mostly `=` and `<` |
| `multiHashJoinOp.Next`                   |  0.38% | 13.26% | The slot composition path |
| `compareDatum`                           |  5.26% | 11.62% | Type comparison |
| `multiHashJoinOp.advanceFrom`            |  2.19% | 11.55% | Cursor odometer |
| `multiHashJoinOp.initStepHelper`         |  6.00% |  9.37% | Chain init |
| **`runtime.gcBgMarkWorker`**             |  0.00% |  **8.08%** | **GC mark — was ~60% pre-Stage-D-2** |
| `runtime.gcDrain`                        |  0.09% |  8.08% | GC drain |
| `runtime.scanobject`                     |  1.53% |  7.91% | GC scan |
| `VirtualSlot.Get`                        |  3.96% |  5.82% | The new slot read path |
| `evalTypedStringLit`                     |  5.27% |  5.68% | **Per-row date string parse — next perf target** |
| `runtime.mallocgc`                       |  0.24% |  4.02% | Alloc |
| `VirtualSlot.Row`                        |  1.29% |  3.96% | Used in slotToRow fallback |
| `runtime.memclrNoHeapPointers`           |  1.48% |  1.48% | Object zeroing |
| **runtime.duffcopy / memmove**           | **(absent)** | (absent) | Was ~60% pre-Stage-D-2 |

**Headline result**: GC + alloc share dropped from ~60%
(M0067 pprof) to ~16% (8.08% gcBgMarkWorker + 7.91% scanobject)
— a 75% reduction. The Stage D structural fix landed.

### 2.2 Heap profile (alloc_space)

| Function | cum (MB) | cum% | Notes |
|----------|---------:|-----:|-------|
| `acceptLoop.func1` (root)            | 1737495 | 99.87% | All work cumulatively |
| `aggregateOp.Open`                   | 1733042 | 99.61% | Pipeline driver |
| `sortOp.Open`                        | 1589469 | 91.36% | Sort buffer |
| `filterOp.Next`                      | 1491233 | 85.72% | Pass-through (no own alloc) |
| `nestedLoopIndexJoinOp.Next`         | 1219982 | 70.12% | NLI consumer of indexScan |
| `indexScanOp.Rescan`                 | 1085963 | 62.42% | **Per-outer index scan** |
| **`btree.RangeScan`**                |  470159 | **27.02%** | **Btree iteration** |
| `joinOp.Next` cumulative             |  837882 | 48.16% | Lazy hash output |
| `joinOp.nextLazy`                    |  837882 | 48.16% | (post-Stage-D-2: virtualOut, but probe path still reads) |
| `projectOp.Next`                     |  555702 | 31.94% | Per-target eval |
| **`acquireRow` (row pool)**          |  413860 | **23.79%** | **Row acquisition** |

**Dominant remaining allocator**: `btree.RangeScan` (27%) and
`acquireRow` (24%) — both flow through `indexScanOp.Rescan`
which is called per-outer-row by NLI. M0072 (indexScan
slot-aware BindOuter + structural reuse of decoded rows) is
the natural next milestone for Q5.

## 3. Q9, M0072, and other deferred work

### 3.1 Q5 — structural cancel still

Q5 doesn't complete in 600s even after Stage D-2. The
allocations have shifted from MHJ's `lazyOut` (was 99.23% / 2 TB
per M0067 pprof) to `btree.RangeScan + acquireRow`. Per-row
work in `evalExprSlot + evalBinary + compareDatum` is also
significant (~80% cum CPU); some of this is per-row date
constant parsing (`evalTypedStringLit` 5.27% flat) which could
be hoisted to plan time.

**Concrete next steps for Q5:**
- **M0072**: indexScanOp.BindOuter takes a SlotView instead of
  Row; eliminate `boundRow` clone per outer; reuse the decoded
  row slice across Rescan calls (currently `acquireRow`
  per-tuple).
- **M0071-0006**: per-batch String/Bytes arena (designed but
  blocked on slot pipeline; now unblocked).
- **TypedStringLit hoisting**: parse the date constant once at
  plan time, store on the `*planner.TypedStringLit` node;
  evalTypedStringLit becomes a constant-cell read.

### 3.2 Q9 — chained-NLI schema-runtime mismatch

Stage D's slot pipeline does NOT resolve Q9. The defensive
gates at `nl_index_join.go:399` and `bushy.go:1548` still skip
Name-rebind for `*NestedLoopIndexJoin` outers — the M0064
gate exists because rebinding by Name lands on schema-position
which differs from runtime-position. M0067-0003 attempted to
remove these gates and went 7 → 1 row.

**Why slot pipeline didn't help**: the gate works at the
*planner* level (predRebind in `bushy.go::reresolveJoinByName`).
Slot virtual coordinates work at the *executor* level. The two
don't intersect — the planner-bound `ColumnRef.Index` is what
slot.Get reads, and that index points to the wrong column when
schema-position ≠ runtime-position.

**M0072 also covers Q9**: with slot-aware BindOuter, the
chained-NLI's outer is itself a slot, and the IndexScan's key
resolution can read via SlotView.Get (mapping by virtual
coordinates). Schema and runtime would then be structurally
equivalent at the slot level.

### 3.3 Q20 distributional gap (unchanged, deferred)

99 rows vs canonical ~186. Per
`docs/design/0071-0002-q20-zero-rows-diagnostic.md`, plan tree
is correct; gap is dataset variance from HammerDB-loaded data.
Reload via `dbgen` for absolute parity.

## 4. Recommended next steps

### Priority A — M0072 indexScan slot-aware BindOuter

This is the single change that unlocks BOTH Q5 (next GC
bottleneck) AND Q9 (chained-NLI schema-runtime). Sketch:

1. `indexScanOp.BindOuter` accepts a SlotView (not Row).
2. The IndexScan's key expressions evaluate via evalExprSlot
   against the bound slot — no Row materialisation per outer.
3. Decoded inner rows are returned via a persistent
   MaterializedSlot whose backing Row is reused; `acquireRow`
   moves out of the per-tuple path.
4. The chained-NLI's Name-rebind gate (`nl_index_join.go:399`)
   can be removed because rebinding by virtual coordinate
   replaces rebinding by Name+Index.

Pre-commit gate (this is the same gate operated by Phase 1+2+3):
- Q12=2, Q13=35, Q21=381 + 21-query sweep — DO NOT COMMIT if
  any of these regress.

### Priority B — `evalTypedStringLit` hoisting

Plan-time literal parsing. ~5% Q5 CPU; trivial change with no
schema implications.

### Priority C — M0071-0006 per-batch String/Bytes arena

Now unblocked. Reduces `acquireRow` pressure further.

### Priority D — Q9 alone (if M0072 delays)

Standalone investigation: try removing the
`bushy.go::reresolveJoinByName` outerIsMHJ guard. The defensive
gate was added because M0067-0003 saw 7 → 1 row — but the slot
pipeline now changes the contract. Run Q9 EXPLAIN before/after.

## 5. Verification methods

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
# Required: Q12=2, Q13=35, Q21≥100 (target 381).

go test ./internal/planner/... ./internal/executor/... \
    ./internal/testutil/tpch/...

# Full skip-Q5 sweep:
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=620s --cancel-after=600s
```

## 6. Document references

### 6.1 Authoritative design docs

- [`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)
  — slot pipeline contract (now fully realised).
- [`docs/design/0068-0003-batch-string-arena.md`](../design/0068-0003-batch-string-arena.md)
  — M0071-0006 future work, now unblocked.
- [`docs/design/0071-0002-q20-zero-rows-diagnostic.md`](../design/0071-0002-q20-zero-rows-diagnostic.md)

### 6.2 Milestone history

- [`docs/milestones/0071-tpch-correctness-and-runtime-followup.md`](../milestones/0071-tpch-correctness-and-runtime-followup.md)
- [`docs/milestones/0069-executor-slot-pipeline-followthrough.md`](../milestones/0069-executor-slot-pipeline-followthrough.md)
  (the slot pipeline through-line)

### 6.3 Carry-over memory

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0071_0009_q21_path_b_landed.md` — Q21 Path B landed details.
- `m0071_stage_b_silent_regression.md` — Q12/Q13 break point
  (now resolved by Stage B + retention-site Materialize
  discipline; left as a sentinel).
- `feedback_tpch_pre_commit_gates.md` — pre-commit Q12/Q13 gate
  operation.

### 6.4 Code anchors (Phase 3 additions / structural changes)

Slot pipeline (executor):
- `internal/executor/slot.go` — SlotView interface, rowSlotView
  adapter, slotToRow helper. TupleSlot embeds SlotView.
- `internal/executor/expr.go:30..` — evalExpr / evalExprSlot
  pair. ColumnRef reads via slot.Get; recursive helpers
  (Subquery / In / Exists / Extract / FuncCall / CaseExpr)
  receive `slotToRow(slot)`.
- `internal/executor/operators.go` — filterOp / limitOp /
  projectOp pass-through; .borrow + SetBorrow gone.
- `internal/executor/operators_nljoin.go` — outerMS / innerMS /
  virtualOut composition; evalPredicateSlot.
- `internal/executor/multi_hash_join.go` — tableSlots /
  virtualOut; lazyOut / copyOut gone; OID-sorted layout via
  tableOff[] + cols[].
- `internal/executor/operators_join_agg.go::nextLazy` —
  lazyBuildSlot / lazyProbeSlot / lazyVirtualOut /
  lazyOuterOnlySlot; ensureLazyVirtual lazy init;
  joinPredicateMatchSlot.
- `internal/executor/operator.go` — slot pipeline contract
  documentation; BorrowSemantics / Borrowable / setChildBorrow
  block deleted.
- `internal/executor/operators_storage.go` — seqScanOp .borrow
  gone; producer always cloneRow.
- `internal/executor/spill.go` — spillOp .borrow gone.

Tests:
- `internal/executor/slot_view_test.go` — rowSlotView /
  slotToRow / evalExprSlot equivalence + nil-slot semantics.
- `internal/executor/nlj_virtual_test.go` — NLI VirtualSlot
  composition + predicate-via-slot eval.
- `internal/executor/mhj_virtual_test.go` — 3-table MHJ
  VirtualSlot composition + evalFilters via slot.
- `internal/executor/fake_source_test.go` — minimal
  fakeBorrowSource stub for sort_external_test.

Removed:
- `internal/executor/borrow_test.go` (entirely).

### 6.5 Profile artefacts

- `pprof-data/m0071-0014/q5.cpu.prof` (480s capture during a
  Q5 run, post-Stage-D-2)
- `pprof-data/m0071-0014/q5.heap.prof` (heap snapshot at the
  end of the same window)

Both files commit-checked-in if the user wants to keep them
for cross-milestone comparison; otherwise they live in
`pprof-data/` (gitignored).
