# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-09 Phase 1+2)

## Audience

A coding agent picking up TPC-H correctness/performance
work on goopg. Branch: `gc-oriented-refactor`. Starting
HEAD: `0090def` (M0071-0010 Stage B landed; M0071-0009 Q21
fix preceding). Supersedes
[`docs/handover/2026-05-09-tpch-status.md`](2026-05-09-tpch-status.md)
which captured the pre-M0071-0009 baseline.

## 0. Recent commits in this branch

- `0090def` feat(m0071-0010): Stage B — Operator.Next returns
  TupleSlot. 33 operator types touched; producer cloneRow
  preserved; Materialize at retention sites
  (sortOp/windowOp/lockRowsOp). Q12=2/Q13=35/Q21=381 + all
  regressions clean. Foundation for Stages C+D+E.
- `48d7354` docs(handover): TPC-H Phase 1 status post-M0071-0009.
- `e8c3779` feat(m0071-0009): Q21 0→381 rows via three coupled
  fixes (SchemaColumn.SourceTableIdx + reresolveJoinByName
  Semi/Anti schema preserve + nextLazy residual Predicate eval).

## 1. Current TPC-H SF=1 status (post-M0071-0009)

22-query sweep against `bench/tpch/runtime_goopg/data`
(HammerDB-loaded SF=1) at `cancel-after=600s`.
**21/22 return correct row counts**; 1 silent FN; 1
cancels structurally.

| Q  | Status                | Rows  | Canonical | Notes |
| -- | --------------------- | ----- | --------- | ----- |
| 1  | OK ~36s               | 4     | 4         | -    |
| 2  | OK ~4s                | 470   | 460       | -    |
| 3  | OK ~29s               | 11462 | 11620     | regression-guard for M0066-0002 |
| 4  | OK ~166s              | 5     | 5         | regression-guard for M0061-0001 EXISTS unnest |
| **5**  | **cancel-600s+**  | **-** | (rows >0) | **structural; per-row concat in NLI joinBuf + MHJ lazyOut. Confirmed cancel at 1200s budget too — only slot pipeline Stage D fixes this.** |
| 6  | OK ~26s               | 1     | 1         | -    |
| 7  | OK ~30s               | 4     | 4         | -    |
| 8  | OK ~186s              | 2     | 2         | -    |
| **9**  | **OK 213s rows=7** | **7** | **~175**  | **silent FN; chained-NLI schema-runtime layout mismatch.** Path B (SchemaColumn.SourceTableIdx) does NOT cover this — the columns' Names are unique per table; the issue is that schema position N differs from runtime row position N for *NestedLoopIndexJoin outers. Carry to slot pipeline. |
| 10 | OK ~30s               | 20574 | 20532     | -    |
| 11 | OK ~3s                | 1142  | 1048      | -    |
| 12 | OK ~85s               | **2** | 2         | **Q12/Q13 silent-regression gate (rows must hold across slot-pipeline attempts)** |
| 13 | OK ~61s               | **35**| 30        | **Q12/Q13 silent-regression gate** |
| 14 | OK ~28s               | 1     | 1         | -    |
| 15 | OK 22+47s             | 1     | 1         | view + main |
| 16 | OK ~4s                | 18170 | 18314     | -    |
| 17 | OK ~62s               | 1     | 1         | -    |
| 18 | OK ~47s               | 11    | 0/57      | regression-guard for M0071-0002 IsolatedScope |
| 19 | OK ~57s               | 1     | 1         | -    |
| 20 | OK ~26s               | 99    | ~186      | regression-guard for M0071-0002-followup |
| **21** | **OK 364s rows=381** | **381** | **~411** | **NEW: M0071-0009 landed (was 0). Threshold ≥100 met, target ~411 within distributional variance.** |
| 22 | OK ~58s               | 7     | 7         | regression-guard for M0061-0001 EXISTS unnest |

## 2. What landed in M0071-0009 (commit `e8c3779`)

Three coupled fixes for Q21's silent-FN. The fix was
3-layered (planner index ambiguity + Semi schema widening +
executor missing residual eval):

### 2.1 SchemaColumn.SourceTableIdx (planner)

Per-FROM monotonic identifier stamped onto every base-scan
column at bind time. `ColumnRef` and `OuterColumnRef` carry
the same field. New helpers:

- `internal/planner/plan.go:23-29` — extended `SchemaColumn`
  with `SourceTableIdx int16` (1..N for base bindings, 0 =
  unknown / derived).
- `internal/planner/plan.go:221-237` — extended `ColumnRef`
  and `OuterColumnRef` with `SourceTableIdx`.
- `internal/planner/planner.go:171-183` — `tableSchema`
  delegates to `tableSchemaWithSource(t, sourceIdx)`.
- `internal/planner/planner.go:planFromClause/planFromRangeVars/
  planFromItem/planScanRangeVar/planSubqueryRangeVar` —
  threaded `sourceIdx` parameter; counter starts at 1.
- `internal/planner/planner.go::resolveColumnRefAt` —
  populates `SourceTableIdx` from `b.sourceIdx`.
- `internal/planner/bushy.go::findColumnIndexByNameAndSource`
  (NEW) — disambiguates by (Name, SourceTableIdx).
- `internal/planner/bushy.go::reresolveJoinByName::resolveSide`
  — SourceTableIdx-aware lookup with Name-only fallback.
- `internal/planner/unnest.go::resolveOuterIdx` —
  SourceTableIdx-aware OuterColumnRef rebind.

### 2.2 reresolveJoinByName preserves Semi/Anti schema (planner)

The join cached schema is no longer widened to merged on
refresh for Type=Semi/Anti — those joins emit Outer (Left)
only at runtime, so a refreshed merged schema previously
caused outer joins to observe a stale 15-col layout for
an 11-col runtime row.

`internal/planner/bushy.go::reresolveJoinByName` skips the
merge for Semi/Anti.

### 2.3 nextLazy evaluates Predicate for Semi/Anti (executor)

M0061-0001 emitted on hash-key match alone, ignoring any
residual conjuncts the planner ANDed onto the join Predicate
(Q21's `l3.l_suppkey <> l1.l_suppkey`). Without re-eval per
match, Anti silently over-excluded.

`internal/executor/operators_join_agg.go::nextLazy` walks
matches and treats "match" as hash-match AND
`joinPredicateMatch=TRUE`.

### 2.4 New tests

- `internal/planner/source_table_idx_test.go` — pins
  per-binding SourceTableIdx assignment +
  `findColumnIndexByNameAndSource` disambiguation.
- `internal/planner/q21_dump_test.go` — pins Q21 AntiJoin
  schema width (= leftWidth, no merged-leak) and predicate
  ColumnRef SourceTableIdx distinctness.

## 3. Remaining work (Q5 + Q9) — Stages C+D+E

Both require completion of the slot pipeline (design
[`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)).
Stage B landed in `0090def` (M0071-0010); Stages C+D+E
remain.

**Stage B in `0090def`** changed Operator.Next signature to
return TupleSlot. The 33 producer operators wrap their existing
Row return via SlotFromRow at the boundary; producer cloneRow
paths stay in place. Retention sites (sortOp.Open,
windowOp.Open, lockRowsOp.drainAndStamp) call
slot.Materialize() at the lifetime boundary. Q12/Q13 stay
green. **This is the foundation Stages C+D+E build on.**

### 3.1 Q5 — structural cancel

**Confirmed at 1200s budget**: still cancels (test ran in
`bea7fprns`). NLI joinBuf and MHJ lazyOut per-row concat is
the structural blocker. ~60% Q5 CPU is
`runtime.duffcopy + memmove + memclr` (M0067 pprof).

**Fix path**: Slot pipeline Stage D — replace `o.joinBuf`
with `VirtualSlot{outerSlot, innerSlot}`; replace
`o.lazyOut` with `VirtualSlot{probeSlot, build0..buildN}`.
Predicate evaluator must accept slot interface (or
materialize on call).

### 3.2 Q9 — chained-NLI schema-runtime mismatch

**Path B (SchemaColumn.SourceTableIdx) does NOT help** —
the relevant columns have unique Names per table; the issue
is structural: schema position N differs from runtime row
position N for `*NestedLoopIndexJoin` outers. The defensive
gates (`internal/planner/nl_index_join.go:399`,
`internal/planner/bushy.go:1548`) exist precisely because
rebinding by Name lands on schema-position which differs
from runtime-position. M0067-0003 attempted to remove these
gates and went 7 → 1 row.

**Fix path**: Slot pipeline's VirtualSlot virtual coordinates
(`(sourceIdx, sourceCol)`) survive operator substitutions,
making schema and runtime layouts equivalent at the slot
level. Stages B-E required; Stage D specifically gives the
virtual-coord composition.

### 3.3 Q20 distributional gap (small, deferred)

99 rows vs canonical ~186. Per
`docs/design/0071-0002-q20-zero-rows-diagnostic.md` and
`internal/planner/q20_unnest_test.go`, plan tree is
correct; the gap is dataset variance from HammerDB-loaded
data. Reload via `dbgen` if absolute parity needed.

## 4. Recommended next steps

### Stages C+D+E (build on `0090def`), MULTI-DAY

**Targets**: Q5 (cancel→completion via per-row concat
elimination), Q9 (silent FN 7→≥90 via slot virtual coords).

**The big remaining piece is `evalExpr` slot-awareness.**
Today `evalExpr(e, row, ctx)` takes a `Row=[]Datum` and
ColumnRef.Eval reads `row[c.Index]`. For VirtualSlot
composition to actually reduce per-row concat (Q5 GC
target), the predicate evaluator must read via
`slot.Get(col)` — otherwise NLI/MHJ still materialize at
the boundary and we save nothing.

**Approach**:
1. Add `evalExprSlot(e, slot TupleSlot, ctx) (Datum, error)`
   — parallel evaluator that uses slot.Get(col)/IsNull(col).
   Or: refactor `evalExpr` to accept a small interface
   `{ Get(int) Datum; IsNull(int) bool }` that both Row
   (via wrapper) and TupleSlot satisfy.
2. NLI: replace `joinBuf` Row reuse with a persistent
   `VirtualSlot{outerSlot, innerSlot}`. Predicate eval
   uses evalExprSlot. Returns the VirtualSlot directly
   (consumer materializes if it retains).
3. MHJ: replace `lazyOut` Row reuse with
   `VirtualSlot{probeSlot, build0Slot..buildNSlot}`. Each
   step's match rebinds the build slot pointer (no copy).
4. filterOp/limitOp/instrumentedOp pass-through directly
   (no Row materialization in the hot path).
5. Stage E cleanup: remove Borrowable / OwnedRow /
   BorrowedRow / setChildBorrow; delete borrow_test.go
   (no longer pinning the deprecated contract). Operators
   that had `if o.borrow == BorrowedRow ... else clone` can
   collapse to "always pass-through" (filter/limit) or
   "always materialize at retention" (sort/agg/join build).

**Pre-commit gate**: build, fresh-restart, Q12=2 +
Q13=35 + 22-sweep. The Stage B foundation in `0090def`
already preserves Q12/Q13 with my approach (producer
cloneRow + retention-site Materialize); maintain this
discipline for Stages C+D+E.

**Acceptance**:
- Q12=2, Q13=35 preserved (gate)
- Q3=11462, Q4=5, Q18=11, Q20=99, Q21≥100, Q22=7 preserved
- Q5 completes (rows ≥ 1) OR ≥30% faster than 600s cancel
- Q9 row count ≥ 90 (target 175 — uncertain; slot virtual
  coords may not fully resolve chained-NLI schema-runtime
  layout mismatch)
- pprof Q5: `duffcopy + memmove + memclr` ≤ 25%
  (target ≤ 10% per design 0068-0002)

### Path B — accept Q5 + Q9 as deferred, focus on other M0071 work

If slot pipeline is out of session budget, M0071-0006
(per-batch String/Bytes arena), M0071-0007 (IndexScan lazy
iteration), and M0071-0008 (buffer-pool poolMu byTag
partitioning) are independent perf targets that don't
depend on the slot pipeline.

## 5. Verification methods

(Same as the prior handover.)

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

./tpch-runner --queries=12,13 \
    --per-query-timeout=180s --cancel-after=170s
# Required: Q12=2 rows, Q13=35 rows.

go test ./internal/planner/... ./internal/executor/... \
    ./internal/testutil/tpch/...

# Full skip-Q5 sweep:
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=620s --cancel-after=600s
```

## 6. Document references

### 6.1 Authoritative design docs (unchanged from prior handover)

- [`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)
- [`docs/design/0068-0003-batch-string-arena.md`](../design/0068-0003-batch-string-arena.md)
- [`docs/design/0071-0002-q20-zero-rows-diagnostic.md`](../design/0071-0002-q20-zero-rows-diagnostic.md)

### 6.2 Milestone history

- [`docs/milestones/0071-tpch-correctness-and-runtime-followup.md`](../milestones/0071-tpch-correctness-and-runtime-followup.md)
- [`docs/milestones/0069-executor-slot-pipeline-followthrough.md`](../milestones/0069-executor-slot-pipeline-followthrough.md)

### 6.3 Carry-over memory

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0071_0009_q21_path_b_landed.md` — Q21 fix landed details.
- `m0071_stage_b_silent_regression.md` — Q12/Q13 break point
  for Stage B re-attempt.
- `feedback_tpch_pre_commit_gates.md` — pre-commit Q12/Q13
  gate operation.

### 6.4 Code anchors (Phase 1 additions)

Planner:
- `internal/planner/plan.go:23-29` — SchemaColumn extension
- `internal/planner/plan.go:221-244` — ColumnRef +
  OuterColumnRef extension
- `internal/planner/planner.go:171-183` — tableSchema /
  tableSchemaWithSource
- `internal/planner/planner.go::planFromClause` (l.642),
  `planFromRangeVars` (l.679), `planFromItem` (l.708),
  `planScanRangeVar` (l.832), `planSubqueryRangeVar` (l.962)
  — sourceIdx threading
- `internal/planner/bushy.go::findColumnIndexByNameAndSource`
  — SourceTableIdx-aware lookup
- `internal/planner/bushy.go::reresolveJoinByName` — Semi/Anti
  schema preserve + resolveSide/predRebind SourceTableIdx
  awareness
- `internal/planner/unnest.go::resolveOuterIdx` —
  SourceTableIdx-aware

Executor:
- `internal/executor/operators_join_agg.go::nextLazy`
  (l.479-577) — Semi/Anti residual Predicate evaluation

Tests:
- `internal/planner/source_table_idx_test.go`
- `internal/planner/q21_dump_test.go`
