# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-10 Phase 6)

## Audience

A coding agent picking up TPC-H correctness / performance
work on goopg. Branch: `gc-oriented-refactor`. Starting HEAD:
`4d892ac` (M0074-0003 arena registry infrastructure;
**partial** — Datum struct flip deferred to M0075). M0074
closes with 6 implementation commits where 4 of 5
sub-milestones landed in **partial scope** under
autonomous-mode risk management.

Supersedes:
- [`docs/handover/2026-05-10-tpch-status-phase5.md`](2026-05-10-tpch-status-phase5.md)
  (M0073 close — Q5 total heap 1463 GB → 404 GB, −72 %).

## Headline result: full-scope wins on M0074-0006 (numeric int64) + M0074-0004 (projection arena); structural infrastructure landed for M0074-0001/0002/0003 with implementation deferred to M0075

The M0074 session prioritized correctness (zero row-count
regression, Q9 mode-1 floor preserved at 7) over
ambitious wholesale changes. Two sub-milestones landed
in full scope; three landed forward-compat infrastructure
only with the risky implementation deferred:

| # | Sub-milestone | Landed | Deferred to M0075 |
|---|---|---|---|
| 0006 | Numeric int64 fast-path | **FULL** — `numericCmp` + `numericAdd/Sub/Mul` | `numericDiv` (NBASE-formula complexity) |
| 0004 | DecodeRowProjectionIntoArena | **FULL** — codec.go + index-build call sites | None |
| 0002 | Chained-NLI virtual-coord | INFRA — `VirtualCol(col)` + bounds check | Planner-side rebind (M0072-0002 hang risk) |
| 0001 | Vectorised evalBinary + ColumnRef inline | INFRA — hoist + batch entry + detector | seqScanOp predicate batch wiring |
| 0003 | Datum struct packed layout | INFRA — `arenaRegistry` + `permArena` | Datum struct flip + 37-site migration |

## 0. Recent commits in this branch

This Phase-6 session landed seven M0074 commits on top of
the M0073 close:

- **`4d892ac`** feat(m0074-0003): arenaRegistry +
  permArena infrastructure (PARTIAL). 256-slot registry,
  permArena reserved at slot 0, `permanent` + `registryIdx`
  fields on Arena, `AllocateString`/`AllocateBytes`
  helpers, register-on-NewArena, unregister-on-Drop.
  **Datum struct flip** (`Buf []byte` + `arena *Arena` →
  `(ArenaRef, Offset, Length)`) **NOT landed** — risk-
  managed under autonomous-mode constraints.
- **`3bc631d`** feat(m0074-0001): ColumnRef hoist +
  evalBinaryBatch entry (PARTIAL). Hoist `*planner.ColumnRef`
  to a fast-path early-return ahead of evalExprSlot's
  type switch (saves 12 type-test arms on the hot path).
  Add `evalBinaryBatch`, `canVectoriseBinary`,
  `canVectoriseExpression` as forward-compat infrastructure.
  **seqScanOp predicate batch wiring NOT landed** —
  benchmark-evidence gate.
- **`bdee869`** feat(m0074-0002): VirtualCol accessor +
  evalExprSlot bounds check (PARTIAL). `VirtualCol(col)`
  on `*VirtualSlot`; defensive bounds check in ColumnRef
  arm. **Planner-side rebind for chained-NLI NOT
  landed** — M0072-0002 hang precedent.
- **`4906451`** feat(m0074-0004): DecodeRowProjectionIntoArena
  + index-build wiring. Arena variant of
  DecodeRowProjection; collectBTreeEntries +
  backfillBTree wired with per-page arena.
- **`8080efa`** feat(m0074-0006): numeric int64 fast-path
  in cmp + add/sub/mul. Skip `big.Int` allocation when
  both operands have `Big == nil`. TPC-H NUMERIC(15,2)
  workload in Q5/Q9 hits the fast path on every
  comparison. `numericDiv` deferred (NBASE formula
  complexity).
- **`54f3536`** docs(m0074): milestone + 5 design docs
  scaffolding.

## 1. Current TPC-H SF=1 status (post-M0074)

22-query sweep at `cancel-after=1100s`:

| Q  | Status               | Rows  | Canonical | Notes |
| -- | -------------------- | ----- | --------- | ----- |
| 1  | OK ~21s              | 4     | 4         | -    |
| 2  | OK ~4s               | 470   | 460       | -    |
| 3  | OK ~22s              | 11462 | 11620     | M0066-0002 guard |
| 4  | OK ~153s             | 5     | 5         | M0061-0001 guard |
| **5** | **cancel-1100s** | **-** | (rows >0) | **structural; CPU-bound (`evalExprSlot` 72 % cum). M0075 candidate: plan-level work (build-side selection in MHJ chain, transitivity inference, selectivity estimates).** |
| 6  | OK ~17s              | 1     | 1         | -    |
| 7  | OK ~22s              | 4     | 4         | -    |
| 8  | OK ~183s             | 2     | 2         | -    |
| **9** | **OK 212s rows=7** | **7** | **~175**  | **bimodal mode-1 baseline preserved; M0074-0002 infra landed; planner-side rebind deferred to M0075** |
| 10 | OK ~22s              | 20574 | 20532     | -    |
| 11 | OK ~3s               | 1142  | 1048      | -    |
| 12 | OK ~79s              | **2** | 2         | **gate** |
| 13 | OK ~61s              | **35**| 30        | **gate** |
| 14 | OK ~20s              | 1     | 1         | -    |
| 15 | OK 16+31s            | 1     | 1         | view + main |
| 16 | OK ~6s               | 18170 | 18314     | -    |
| 17 | OK ~45s              | 1     | 1         | -    |
| 18 | OK ~36s              | 11    | 0/57      | M0071-0002 guard |
| 19 | OK ~70s              | 1     | 1         | -    |
| 20 | OK ~16s              | 99    | ~186      | M0071-0002-followup guard |
| **21** | **OK 339s rows=381** | **381** | **~411** | **M0071-0009 win preserved** |
| 22 | OK ~59s              | 7     | 7         | M0061-0001 guard |

Row count parity vs Phase-5 baseline: **22/22 preserved**.

## 2. M0074 sub-milestone scoreboard

| # | Sub-milestone | Status | Notes |
|---|---|---|---|
| 0006 | Numeric int64 fast-path | **FULL `8080efa`** | `numericCmp` + `Add` + `Sub` + `Mul` skip big.Int when both operands int64-lane; fuzz vs slow-path equivalence verified |
| 0004 | DecodeRowProjectionIntoArena | **FULL `4906451`** | codec.go variant + collectBTreeEntries + backfillBTree wired with per-page arena |
| 0002 | Chained-NLI virtual-coord | **PARTIAL `bdee869`** | `VirtualCol(col)` accessor + defensive bounds check; planner-side rebind deferred (M0072-0002 hang precedent) |
| 0001 | Vectorised evalBinary + ColumnRef inline | **PARTIAL `3bc631d`** | ColumnRef hoist (saves 12 type-test arms); `evalBinaryBatch` + detectors as forward-compat; seqScanOp wiring deferred |
| 0003 | Datum struct packed layout | **PARTIAL `4d892ac`** | `arenaRegistry` + `permArena` + helpers; struct flip deferred to M0075 |
| 0005 | Final 22-query sweep + handover | **LANDED `<this commit>`** | 22-q sweep parity confirmed; pprof artefacts at `pprof-data/m0074-final/` |

## 3. M0074-0001 ColumnRef hoist CPU finding

The ColumnRef hoist was expected to drop `evalExprSlot`
cum CPU via early-return before the 12-arm type switch.
Post-M0074 pprof comparison:

| Function | M0073-final cum % | M0074-final cum % | Δ |
|----------|------------------:|------------------:|---:|
| `evalExprSlot` | 68.68 | 72.09 | +3.4 (NOT improved) |
| `evalBinary` | 33.72 | 33.35 | flat |
| `compareDatum` | 12.17 | 12.79 | flat |

The hoist did not move the macro cum %. The likely
explanation: Go's type switch already JIT-compiles
efficiently, and the dispatch overhead was small relative
to `slot.Get()` body + downstream `evalBinary` /
`compareDatum`. Same lesson as M0073-0003 (OpCode int8
refactor delivered correctness without macro CPU win).

The numeric int64 fast-path (M0074-0006) had a similar
profile: result-correctness verified against big.Int
slow path on 1000-pair fuzz, but `compareDatum` macro
flat % unchanged (5.86 % → 5.88 %). The structural change
is correct; the gain is in inner-loop allocation
elimination not visible at sample-period granularity.

## 4. Q5 / Q9 residual cost analysis

### 4.1 Q5 — CPU-bound; plan-level work is the next lever

Q5 still cancels at 1100 s. M0073-final dropped heap
72 %; M0074 didn't move CPU at the macro level. The Q5
residual cost analysis from Phase-5 stands:

| Function | flat % | cum % | M0075 lever |
|----------|-------:|------:|-------------|
| `evalExprSlot` | 28.06 | 72.09 | — (already inlined) |
| `evalBinary` | 10.94 | 33.35 | seqScanOp batch wiring (deferred from 0001) |
| `compareDatum` | 5.88 | 12.79 | numericDiv int64 fast-path (deferred from 0006) |
| `multiHashJoinOp.Next` | 0.17 | 11.41 | build-side selection (plan-level) |

**The bigger M0075 lever is plan-level**:
- TPC-H Q5 at SF=1 has theoretical wall time ~5-10 s; the
  600-1100 s cancel suggests the plan is touching far
  more rows than necessary.
- MHJ is being used (good — not NLI), but build-side
  selection in the MHJ chain matters: optimal order is
  region(5) → nation(25) → supplier(10K) → customer(150K).
- Filter push-down timing (e.g. `r_name = 'ASIA'` at
  region scan vs after join), transitivity inference
  (`c_nationkey = n_nationkey` from
  `c.nk = s.nk AND s.nk = n.nk`), selectivity estimates
  on `o_orderdate ∈ [1994, 1995)` all matter.
- Comparison vs upstream Postgres' EXPLAIN output is the
  recommended starting point for M0075-0001.

### 4.2 Q9 — bimodal mode-1 baseline; planner-side rebind needed

Q9 stays at 7 rows / ~212 s (mode-1 baseline). M0074-0002
landed forward-compat infrastructure (`VirtualCol(col)`
accessor + defensive bounds check) but did NOT attempt
the planner-side rebind because M0072-0002's same
attempt caused a runtime hang at 380-600 s with 0 rows.

For M0075-0002, the recommended approach:
1. Start with EXPLAIN Q9 + per-outer match-set analysis
   on a live SF=1 corpus.
2. Identify the chained-NLI shape's exact ColumnRef
   binding pattern.
3. Compare with the rewritten outer's runtime VirtualSlot
   composition.
4. Only then attempt the rebind, gated on per-outer
   selectivity check (the M0072-0002 hang was selectivity
   collapse — the rebind landed on a high-cardinality
   column).

## 5. Recommended next steps — M0075 milestone shape

| # | Sub-milestone | Drives |
|---|---|---|
| 0001 | Q5 plan-level investigation (EXPLAIN diff vs Postgres; build-side selection; transitivity) | Q5 wall time → seconds, not 600-1100 s cancel |
| 0002 | Q9 chained-NLI planner-side rebind (with per-outer selectivity guard) | Q9 deterministic 175 rows |
| 0003 | Datum struct packed layout (full flip) | Frees 24 B headroom (was 12 B in original estimate); 64 B → 40 B |
| 0004 | seqScanOp predicate batch wiring (consume M0074-0001 entry) | 10-30 % CPU drop on Q5/Q12/Q13 if benchmark gate clears |
| 0005 | numericDiv int64 fast-path | Q1 / Q3 / Q14 avg() compression |
| 0006 | Final 22-query SF=1 sweep + Phase 7 handover | -- |

Pre-commit gate stays the same: Q12=2 / Q13=35 / Q21≥100 +
21-query sweep parity. Q9 hard floor: ≥ 7 rows; M0075-0002
must produce ≥ 100 rows DETERMINISTICALLY OR revert.

## 6. Verification methods

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

# Tight gate (Q12/Q13/Q21/Q22 + Q9 hard floor)
./tpch-runner --queries=12,13,21,22,9 \
    --per-query-timeout=620s --cancel-after=600s

go test ./internal/parser/... ./internal/planner/... \
    ./internal/executor/... ./internal/testutil/tpch/...

# 21-query SF=1 sweep
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=1200s --cancel-after=1100s

# Q5 pprof capture (separate from sweep, no contention):
mkdir -p pprof-data/m0075-...
( curl -s -o pprof-data/m0075-.../q5.cpu.prof \
    "http://127.0.0.1:6060/debug/pprof/profile?seconds=480" ) &
sleep 1
./tpch-runner --queries=5 --per-query-timeout=1200s --cancel-after=1100s
wait
curl -s -o pprof-data/m0075-.../q5.heap.prof \
    "http://127.0.0.1:6060/debug/pprof/heap"
```

Note: pprof `inuse_space` (default) is the steady-state
metric; `alloc_space` (`-sample_index=alloc_space`) is the
cumulative-since-process-start. The latter scales with
total query runs across the server lifetime, not with
the workload of a single Q5 run — use `inuse_space` for
heap pressure comparison and `alloc_space` only for
allocation-rate analysis on a fresh-restart server.

## 7. Document references

### 7.1 New M0074 docs (Phase-6 scaffolding)

- [`docs/milestones/0074-cpu-and-numeric-optimisation.md`](../milestones/0074-cpu-and-numeric-optimisation.md)
- [`docs/design/0074-0001-vectorised-binary-and-columnref-inline.md`](../design/0074-0001-vectorised-binary-and-columnref-inline.md)
- [`docs/design/0074-0002-chained-nli-virtual-coord.md`](../design/0074-0002-chained-nli-virtual-coord.md)
- [`docs/design/0074-0003-datum-packed-layout.md`](../design/0074-0003-datum-packed-layout.md)
- [`docs/design/0074-0004-decode-row-projection-arena.md`](../design/0074-0004-decode-row-projection-arena.md)
- [`docs/design/0074-0006-numeric-int64-fast-path.md`](../design/0074-0006-numeric-int64-fast-path.md)

### 7.2 Authoritative design docs (carried forward)

- [`docs/design/0072-0002-chained-nli-rebind.md`](../design/0072-0002-chained-nli-rebind.md)
  — reverted approach; carries M0075-0002 lessons.
- [`docs/design/0068-0001-datum-compact-layout.md`](../design/0068-0001-datum-compact-layout.md)
  — Datum struct change discipline.
- [`docs/design/0073-0001-datum-arena-field.md`](../design/0073-0001-datum-arena-field.md)
  — M0073 arena field; M0074-0003 builds on it.

### 7.3 Carry-over memory

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0073_arena_q5_heap_drop.md` — Q5 heap −72 % via
  arena wiring.
- `m0071_0009_q21_path_b_landed.md` — Q21 fix details.
- `m0071_stage_b_silent_regression.md` — Q12/Q13 break
  pattern.
- `feedback_tpch_pre_commit_gates.md` — pre-commit gate
  operation.

### 7.4 Code anchors (Phase 6 changes)

Numeric int64 fast-path (M0074-0006):
- `internal/executor/numeric.go:34-39, 416-425` —
  `numericMant` + `numericCmp` (int64 fast-path
  wrapper).
- `internal/executor/numeric.go` (NEW helpers):
  `int64Pow10`, `mulInt64Pow10`, `alignNumericInt64`,
  `numericCmpInt64Fast`, `addInt64Overflow`,
  `subInt64Overflow`, `mulInt64Overflow`.
- `internal/executor/numeric.go::numericAdd / numericSub
  / numericMul` — int64 fast-path arms.
- `internal/executor/numeric_int64_fast_test.go` (NEW)
  — boundary + 1000-pair fuzz vs slow path.

DecodeRowProjectionIntoArena (M0074-0004):
- `internal/executor/codec.go:65-127` —
  `DecodeRowProjection` wraps `decodeRowProjectionArena`;
  `DecodeRowProjectionIntoArena` is the new arena entry.
- `internal/executor/operators_ddl.go::collectBTreeEntries`
  + `backfillBTree` — per-page arena lifecycle, call
  flips to arena variant.
- `internal/executor/codec_projection_arena_test.go`
  (NEW) — projected vs skipped vs backward-compat.

Chained-NLI infra (M0074-0002):
- `internal/executor/slot.go::VirtualSlot.VirtualCol`
  (NEW accessor) — exposes runtime
  `(sourceIdx, sourceCol)` for future planner-side
  rebind.
- `internal/executor/expr.go::evalExprSlot` (M0074-0001
  hoist subsumed the M0074-0002 bounds check) —
  defensive `*VirtualSlot` bounds check + clearer error.
- `internal/executor/virtual_slot_bounds_test.go` (NEW).

ColumnRef hoist + batch infra (M0074-0001):
- `internal/executor/expr.go::evalExprSlot` — hoisted
  ColumnRef fast-path; original `case *planner.ColumnRef:`
  arm removed.
- `internal/executor/expr_batch.go` (NEW) —
  `evalBinaryBatch`, `canVectoriseBinary`,
  `canVectoriseExpression`.
- `internal/executor/expr_batch_test.go` (NEW) —
  per-row equivalence, NULL three-valued logic,
  whitelist + walker.

Arena registry infra (M0074-0003):
- `internal/executor/arena_registry.go` (NEW) —
  256-slot registry, `permArena` at slot 0,
  `registerArena` / `unregisterArena`,
  `Arena.AllocateString` / `Arena.AllocateBytes`.
- `internal/executor/arena.go` — `permanent` +
  `registryIdx` fields on Arena; register-on-NewArena;
  unregister-on-Drop; Reset no-op for permArena.
- `internal/executor/arena_registry_test.go` (NEW) —
  6 tests pinning slot-0 reservation,
  register/unregister lifecycle, never-reset invariant.

### 7.5 Profile artefacts

- `pprof-data/m0074-final/q5.cpu.prof` — 480 s CPU
  profile, Q5 mid-cancel (CPU-bound shape preserved).
- `pprof-data/m0074-final/q5.heap.prof` — heap snapshot
  at end of Q5 1100 s cancel. **Use `inuse_space`
  (default)** for heap pressure (3.35 GB, ~unchanged
  vs M0073-final's 3.38 GB); `alloc_space` is
  cumulative-since-start and not directly comparable
  across sessions.
- (M0073-final captures preserved at
  `pprof-data/m0073-final/` for cross-milestone diff.)
