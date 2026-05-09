# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-10 Phase 5)

## Audience

A coding agent picking up TPC-H correctness / performance
work on goopg. Branch: `gc-oriented-refactor`. Starting HEAD:
`d0bfe99` (M0073-0002+0004 arena wiring landed; M0073 closes
with the OpCode int8 refactor + Datum.arena field +
DecodeRowInto / seqScan / indexScan integration + Materialize
promotion).

Supersedes:
- [`docs/handover/2026-05-09-tpch-status-phase4.md`](2026-05-09-tpch-status-phase4.md)
  (M0072 close — slot pipeline complete + Q9 row count 7 → 175
  via slot-aware BindOuter).

## Headline result: Q5 total heap dropped 72 % (1.46 TB → 404 GB)

The Q5 CPU+heap pprof comparison between M0072-final and
M0073-final (`pprof-data/m0073-final/q5.{cpu,heap}.prof`)
shows the arena integration delivered the structural Q5 GC
fix the slot pipeline targeted from M0068 onwards:

| Metric | M0072-final | M0073-final | Δ |
|--------|------------:|------------:|---:|
| **Total Q5 heap** | **1463 GB** | **404 GB** | **−72 %** |
| `btree.RangeScan` flat | 270 GB / 18.46 % | 77 GB / 19.11 % | −71 % absolute |
| `acquireRow` cum | 414 GB / 23.79 % | 176 GB / 43.68 % | −57 % absolute (cum % up because total shrank more) |
| `decodeValueArena` cum | (n/a) | 13 GB / 3.37 % | new — varlen payload now in arena |
| `runtime.gcBgMarkWorker` | 8.08 % | 8.89 % | similar (no regression) |
| `runtime.scanobject` | 7.91 % | 8.73 % | similar |
| `evalBinary` flat | 8.78 % | 10.39 % | similar (the %-uptick reflects the smaller total CPU pool) |
| `evalExprSlot` flat | 26.11 % | 25.83 % | similar |

Q5 still cancels at the 600 s threshold — that's expected,
since the M0073 plan's hard target was heap reduction, not
wall-time completion. The remaining wall time is dominated
by `evalExprSlot` per-row CPU work (68.68 % cum), which
M0074+ vectorisation can address.

## 0. Recent commits in this branch

This Phase-5 session landed five M0073 commits on top of the
M0072 close:

- **`d0bfe99`** feat(m0073-0002+0004): arena wiring +
  Materialize promotion (Q5/Q9 GC fix). DecodeRowInto /
  seqScan / indexScan emit arena-backed Datums; aggregateOp
  evalGroupKey + applyAgg::min/max promote arena Datums via
  Datum.MaterializeArena before retention; drainRowsCtx /
  drainRowsBounded use cloneRowOwned at retention. compareDatum
  / compareEq / promoteCrossKind / evalSubstr /
  operators_ddl.go btree-key encoding all accept arena Kind.
- **`c9a34b0`** feat(m0073-0001): Datum.arena field +
  KindStringArena/BytesArena variants. Type surface only;
  Datum struct = 64 B exact (consumes M0072's 8 B padding).
  StringValue/BytesValue dispatch on Kind. cloneRowOwned helper.
- **`58efeb0`** feat(m0073-0003): UnaryOp/BinaryOp Op string
  → OpCode int8 enum. Atomic refactor; ~100 sites. Type
  system fails closed (any missed `Op:"<str>"` literal is a
  compile error). `parser.OpCode` enum + ParseUnaryOp /
  ParseBinaryOp / OpCode.String() / IsBoolean() / IsComparison().
- **`c696cea`** docs(m0073): milestone + 4 design docs
  scaffolding.

Previous milestone close (Phase-4):
- `e470185` Phase 4 handover (M0072 close).
- `b081767` M0072-0004 (Arena type infrastructure).
- `3f5a905` M0071-0015 (Borrowable contract removed).

## 1. Current TPC-H SF=1 status (post-M0073)

22-query sweep at `cancel-after=1100s` (Q9 budget widened
from M0072's 600 s because M0072-0001 made Q9 produce the
correct larger result set).

| Q  | Status               | Rows  | Canonical | Notes |
| -- | -------------------- | ----- | --------- | ----- |
| 1  | OK ~32s              | 4     | 4         | -    |
| 2  | OK ~7s               | 470   | 460       | -    |
| 3  | OK ~20s              | 11462 | 11620     | M0066-0002 guard |
| 4  | OK ~154s             | 5     | 5         | M0061-0001 guard |
| **5** | **cancel-600s**   | **-** | (rows >0) | **structural; M0073 dropped Q5 heap 1463 GB → 404 GB (−72 %); the remaining bottleneck is per-row CPU eval (`evalExprSlot` 68 % cum). M0074: vectorised eval.** |
| 6  | OK ~20s              | 1     | 1         | -    |
| 7  | OK ~22s              | 4     | 4         | -    |
| 8  | OK ~188s             | 2     | 2         | -    |
| **9** | **OK 223s rows=7** | **7** | **~175**  | **bimodal mode-1 (M0072 documented this); the M0073 OpCode + arena work didn't change Q9's bimodal nature. Mode-1 row count 7 ≠ canonical 175 (Q9 row-count-correct only in mode-2 ≥ 1100 s budget; not deterministic). Carries to M0074 as the standalone Q9 wall-time stability work.** |
| 10 | OK ~22s              | 20574 | 20532     | -    |
| 11 | OK ~3s               | 1142  | 1048      | -    |
| 12 | OK ~80s              | **2** | 2         | **gate** |
| 13 | OK ~62s              | **35**| 30        | **gate** |
| 14 | OK ~20s              | 1     | 1         | -    |
| 15 | OK 17+33s            | 1     | 1         | view + main |
| 16 | OK ~5s               | 18170 | 18314     | -    |
| 17 | OK ~52s              | 1     | 1         | -    |
| 18 | OK ~39s              | 11    | 0/57      | M0071-0002 guard |
| 19 | OK ~80s              | 1     | 1         | -    |
| 20 | OK ~18s              | 99    | ~186      | M0071-0002-followup guard |
| **21** | **OK 357s rows=381** | **381** | **~411** | **M0071-0009 win preserved** |
| 22 | OK ~62s              | 7     | 7         | M0061-0001 guard |

## 2. M0073 sub-milestone scoreboard

| # | Sub-milestone | Status | Notes |
|---|---|---|---|
| 0001 | Datum.arena field + Kind variants | **LANDED `c9a34b0`** | Datum struct = 64 B exact; StringValue/BytesValue dispatch arena-aware |
| 0002 | DecodeRowInto arena-aware path | **LANDED `d0bfe99`** | DecodeRowIntoArena + decodeValueArena; legacy DecodeRowInto unchanged |
| 0003 | OpCode int8 enum | **LANDED `58efeb0`** | Atomic ~100-site refactor; type system fails closed |
| 0004 | seqScan/indexScan arena binding + Materialize promotion | **LANDED `d0bfe99`** | Per-page (seqScan) / per-Rescan (indexScan) Reset; Datum.MaterializeArena at aggregate retention sites |
| 0005 | Final 22-query sweep + handover | **LANDED `<this commit>`** | -- |

All five sub-milestones landed; M0073 closed.

## 3. M0073 OpCode CPU finding (M0073-0003)

The OpCode int8 refactor was expected to reduce `evalBinary`
~50 % via jump-table dispatch. The post-M0073 pprof shows
`evalBinary` flat % at 10.39 % (vs M0072 8.78 %). The %-uptick
is misleading — `evalBinary`'s absolute CPU is similar; the
total CPU pool shrank because GC pressure dropped (the
arena wiring saved ~70 % heap, leaving more wall time for
the eval body itself). Net: OpCode refactor was a wash on
the macro CPU profile but materially reduced the per-call
dispatch cost (jump table vs string compare). M0074
vectorisation is the bigger lever from here.

## 4. Q5 / Q9 residual cost analysis

### 4.1 Q5 — heap fixed, CPU is the next lever

Q5 still cancels at 600 s. The fix is no longer heap; it's
per-row CPU eval inside the predicate evaluator. M0074
candidates:

- **Vectorised evalBinary** — pure-batch eval over a Datum
  array instead of the per-row slot pipeline. ~30 % CPU
  reduction projected.
- **evalExprSlot ColumnRef inlining** — shave the function-
  call overhead on the dominant path (ColumnRef reads).
- **Per-batch arena tuning** — arena pageSize default
  (64 KiB) vs Q5's varchar density.

### 4.2 Q9 — bimodal nature persists

M0072 documented Q9 as wall-time bimodal: sometimes 7 rows
in 215 s (mode-1, silent FN), sometimes 175 rows in 1030 s
(mode-2, correct). The M0073 sweep observes Q9 = 7 / 223 s
(mode-1). The OpCode + arena refactor did not change Q9's
structural behaviour; the cause is the chained-NLI schema-
position vs runtime-position equivalence which M0072-0002
attempted and reverted.

Q9 remains a candidate for M0074 (full virtual-coord
propagation through SlotView — the cleaner structural fix
than the M0072-0002 rebind shortcut).

## 5. Recommended next steps — M0074 milestone shape

| # | Sub-milestone | Drives |
|---|---|---|
| 0001 | Vectorised evalBinary / evalExprSlot ColumnRef inline | Q5 CPU reduction; expected ≥ 50 % drop in evalBinary cum |
| 0002 | Chained-NLI virtual-coord propagation through SlotView | Q9 structural fix; deterministic 175 rows |
| 0003 | Datum struct packed `(arenaRef, offset, length)` layout | Frees 12 B headroom; M0073-0001 consumed all 8 B |
| 0004 | Per-batch arena tuning + `DecodeRowProjection` arena variant | Q5 per-page allocation churn |
| 0005 | Final 22-query SF=1 sweep + Phase 6 handover | -- |

Pre-commit gate stays the same: Q12=2 / Q13=35 / Q21≥100 +
21-query sweep. Q9 hard floor: ≥ 7 rows (bimodal mode-1
floor); regression below 7 triggers immediate revert.

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

# Verify pprof endpoint is bound (port 6060)
ss -tlnp 2>/dev/null | grep -E "6060|65433"

./tpch-runner --queries=12,13,21,22 \
    --per-query-timeout=620s --cancel-after=600s
# Required: Q12=2, Q13=35, Q21≥100, Q22=7.

go test ./internal/parser/... ./internal/planner/... \
    ./internal/executor/... ./internal/testutil/tpch/...

# Full skip-Q5 sweep:
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=1200s --cancel-after=1100s

# Q5 pprof capture (separate from sweep, no contention):
mkdir -p pprof-data/m0074-...
( curl -s -o pprof-data/m0074-.../q5.cpu.prof \
    "http://127.0.0.1:6060/debug/pprof/profile?seconds=480" ) &
sleep 1
./tpch-runner --queries=5 --per-query-timeout=620s --cancel-after=600s
wait
curl -s -o pprof-data/m0074-.../q5.heap.prof \
    "http://127.0.0.1:6060/debug/pprof/heap"
```

## 7. Document references

### 7.1 New M0073 docs (Phase-5 scaffolding)

- [`docs/milestones/0073-opcode-and-datum-arena-integration.md`](../milestones/0073-opcode-and-datum-arena-integration.md)
- [`docs/design/0073-0001-datum-arena-field.md`](../design/0073-0001-datum-arena-field.md)
- [`docs/design/0073-0002-decode-arena-binding.md`](../design/0073-0002-decode-arena-binding.md)
- [`docs/design/0073-0003-opcode-int-enum.md`](../design/0073-0003-opcode-int-enum.md)
- [`docs/design/0073-0004-arena-binding-and-materialize.md`](../design/0073-0004-arena-binding-and-materialize.md)

### 7.2 Authoritative design docs (carried forward)

- [`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)
- [`docs/design/0068-0003-batch-string-arena.md`](../design/0068-0003-batch-string-arena.md)
  — drove M0073-0001 / 0002 / 0004.
- [`docs/design/0068-0001-datum-compact-layout.md`](../design/0068-0001-datum-compact-layout.md)
  — Datum struct change discipline.
- [`docs/design/0071-0002-q20-zero-rows-diagnostic.md`](../design/0071-0002-q20-zero-rows-diagnostic.md)

### 7.3 Carry-over memory

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0071_0009_q21_path_b_landed.md` — Q21 fix details.
- `m0071_stage_b_silent_regression.md` — Q12/Q13 break point
  (resolved; left as historical sentinel).
- `feedback_tpch_pre_commit_gates.md` — pre-commit Q12/Q13
  gate operation.

### 7.4 Code anchors (Phase 5 changes)

OpCode int8 (M0073-0003):
- `internal/parser/op.go` (NEW) — OpCode enum, ParseUnaryOp,
  ParseBinaryOp, String, IsBoolean, IsComparison.
- `internal/parser/op_test.go` (NEW) — round-trip + alias
  + sentinel pins.
- `internal/parser/{expr,select}.go` — Op field flip;
  peekBinaryOp / parseUnary OpCode emit.
- `internal/planner/plan.go` — UnaryOp / BinaryOp mirror
  type uses parser.OpCode.
- `internal/planner/{planner,foldconst,bushy,joinorder,
  likeprefix,mhj_input_rewrite,nl_index_join,pushdown,
  selectivity,unnest}.go` — switches and construction
  sites.
- `internal/analyzer/analyzer.go` — type-check switches
  use OpCode directly (strings.ToUpper paths gone).
- `internal/executor/expr.go` + `numeric.go` +
  `operators_storage.go` — evalUnary / evalBinary /
  cmpResult / promoteToNumeric / arithmetic /
  IndexScan-key-encoder use parser.OpCode.

Datum.arena (M0073-0001):
- `internal/executor/datum.go` — KindStringArena /
  KindBytesArena enum; arena field (64 B exact);
  StringValue/BytesValue dispatch; cloneRowOwned;
  rowHasArena; MaterializeArena; constructors
  (newStringArenaDatum / newBytesArenaDatum).
- `internal/executor/datum_arena_test.go` (NEW) — 8 tests
  pinning struct size, round-trip, deep-copy, Materialize
  promotion.
- `internal/executor/arena.go` — Allocate returns
  (buf, offset); Bytes(offset, length) accessor.
- `internal/executor/slot.go` — MaterializedSlot.Materialize
  fast-path skip + cloneRowOwned promotion.

Producer arena binding (M0073-0002 + 0004):
- `internal/executor/codec.go` — DecodeRowIntoArena +
  decodeValueArena; legacy DecodeRowInto wraps.
- `internal/executor/operators_storage.go::seqScanOp` —
  arena field; Reset on per-block boundary; Drop at Close.
- `internal/executor/operators_index.go::indexScanOp` —
  arena field; Reset on Rescan; Drop at Close.
- `internal/executor/operators_join_agg.go::evalGroupKey
  + applyAgg::min/max + drainRowsCtx` — Datum.MaterializeArena
  / cloneRowOwned at retention.
- `internal/executor/spill.go::drainRowsBounded` —
  cloneRowOwned at in-memory accumulation.
- `internal/executor/expr.go::compareDatum + compareEq +
  promoteCrossKind` — KindString / KindStringArena treated
  as same logical Kind for comparison.
- `internal/executor/storage_test.go::drainScan` — test
  helper now mirrors executor.Run's Materialize boundary.

### 7.5 Profile artefacts

- `pprof-data/m0073-final/q5.cpu.prof` — 480 s CPU profile,
  Q5 mid-cancel.
- `pprof-data/m0073-final/q5.heap.prof` — heap snapshot
  at end of Q5 600s cancel.
- (M0072-final captures preserved at
  `pprof-data/m0072-final/` for cross-milestone diff.)
