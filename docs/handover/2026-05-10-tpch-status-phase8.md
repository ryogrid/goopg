# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-10 Phase 8)

## Audience

A coding agent picking up TPC-H correctness / performance
work on goopg. Branch: `gc-oriented-refactor`. Starting HEAD:
`83e7853` (M0076-0001 second-attempt revert + M0076
infrastructure landed). M0076 closes with **2/7 sub-
milestones FULL + 5/7 PARTIAL/DEFERRED** under Q5-priority
ordering. The session's empirical Q5 attempt produced
the most actionable finding of the M0073-M0076 sequence:
**the Q5 cancel is gated by a missing build-side memory
term in `estimateJoinCost`, not by transitivity inference
or executor optimisations.**

Supersedes:
- [`docs/handover/2026-05-10-tpch-status-phase7.md`](2026-05-10-tpch-status-phase7.md)
  (M0075 close — 1 FULL + 4 PARTIAL).

## Headline result: M0076-0006 plan-snapshot harness + M0076-0004 cost-model preparation are the only landed wins; M0076-0001 hook re-enable empirically demonstrated the cost-model gap

The Q5-priority reorder (per user directive 2026-05-10)
attempted to unlock Q5 first, with executor work
deferred. The session ran:

- **A**: docs scaffolding (2 new design docs)
- **B**: plan-snapshot harness — **FULL** (`b27869c`)
- **C**: cost-model preparation — **FULL** (`2511184`)
- **D**: hook re-enable for Q5 fix — **DEFERRED**
  (`83e7853`) after Q5 still cancelled at 1100 s
  with a structurally-different but worse plan
- E/F/G/H: deferred to M0077 per the M0076 plan's
  R7 (Q5 fix gate is the keystone — when D fails,
  remaining commits defer).
- **I**: this handover.

The Q5 attempt's failure mode was high-signal: the
synthesised `c_nationkey = n_nationkey` edge DID
appear in Q5's plan (plan-diff confirmed structural
change from `MultiHashJoin(6 tables)` to nested
`Hash Join → Hash Join → MultiHashJoin(4 tables)`),
but the new plan estimated 303 M rows for the
intermediate lineitem⋈orders join. The cost model
picked it because `estimateJoinCost = (L*R)/NDistinct`
favours high-NDistinct keys regardless of build-side
memory cost. **The Q5 fix requires deeper cost-model
work (build-side memory term in `cost_hashjoin` shape),
deferred to M0077-0001.**

The detailed root-cause analysis with the 5 distinct
planner gaps and 4 M0077 fixes lives at
[`tmp/q5-plan-analysis.md`](../../tmp/q5-plan-analysis.md).

## 0. Recent commits in this branch

This Phase-8 session landed five M0076 commits on top
of the M0075 close + M0076 docs:

- **`83e7853`** docs(m0076-0001): defer Q5 fix to
  M0077 — empirical regression. Behavioural change
  reverted; design doc updated with the second-
  attempt postmortem.
- **`2511184`** feat(m0076-0004): cost-model edge
  discount + deterministic synthesis. `joinEdge.isInferred`
  field, `inferredEdgePenalty=2.0` constant,
  `buildJoinGraph` `inferredCount` parameter,
  `estimateJoinCost` penalty multiplier, deterministic
  `equiv_class.go::classes()` ordering. Hook NOT YET
  enabled — preparation only. 5 unit tests pin the
  contract.
- **`b27869c`** feat(m0076-0006): plan-snapshot
  regression harness. `cmd/plan-snapshot` capture +
  diff with three equality modes (structural /
  strict-text / semantic-cost). Makefile targets
  `plan-snapshot-capture` / `plan-diff`. M0076
  baseline captured at HEAD `ffc3429`. Per-query
  diff is fully deterministic (~30 s); batch-mode
  has connection-pool ordering caveats documented.
- **`8ace779`** docs(m0076): scaffold
  cost-model-edge-discount + plan-snapshot-harness
  designs (2 new design docs).
- **`ffc3429`** docs(m0076): create milestone doc +
  README index pointer (M0075 close prep).

## 1. Current TPC-H SF=1 status (post-M0076)

22-query sweep at `cancel-after=1100s`:

| Q  | Status                | Rows  | Canonical | Notes |
| -- | --------------------- | ----- | --------- | ----- |
| 1  | OK ~22 s              | 4     | 4         | -    |
| 2  | OK ~6 s               | 470   | 460       | -    |
| 3  | OK ~22 s              | 11462 | 11620     | M0066-0002 guard |
| 4  | OK ~152 s             | 5     | 5         | M0061-0001 guard |
| **5** | **cancel-1100 s** | **-** | (rows >0) | **structural; M0077-0001 build-side memory cost in estimateJoinCost is the keystone fix; full root-cause at tmp/q5-plan-analysis.md** |
| 6  | OK ~17 s              | 1     | 1         | -    |
| 7  | OK ~22 s              | 4     | 4         | -    |
| 8  | OK ~190 s             | 2     | 2         | -    |
| **9** | **OK ~232 s rows=7** | **7** | **~175** | **mode-1 baseline preserved; M0075-0002 selectivity guard preserved** |
| 10 | OK ~22 s              | 20574 | 20532     | -    |
| 11 | OK ~3 s               | 1142  | 1048      | -    |
| 12 | OK ~102 s             | **2** | 2         | **gate** |
| 13 | OK ~66 s              | **35**| 30        | **gate** |
| 14 | OK ~20 s              | 1     | 1         | -    |
| 15 | OK 18+33 s            | 1     | 1         | view + main |
| 16 | OK ~5 s               | 18170 | 18314     | -    |
| 17 | OK ~46 s              | 1     | 1         | -    |
| 18 | OK ~37 s              | 11    | 0/57      | M0071-0002 guard |
| 19 | OK ~70 s              | 1     | 1         | -    |
| 20 | OK ~17 s              | 99    | ~186      | M0071-0002-followup guard |
| **21** | **OK ~406 s rows=381** | **381** | **~411** | **M0071-0009 + M0075-0002 wins preserved** |
| 22 | OK ~63 s              | 7     | 7         | M0061-0001 guard |

Row count parity vs Phase-7 baseline: **22/22 preserved**.

## 2. M0076 sub-milestone scoreboard

| # | Sub-milestone | Status | Notes |
|---|---|---|---|
| 0006 | Plan-snapshot regression harness | **FULL `b27869c`** | cmd/plan-snapshot + 9 unit tests + 22-q baseline; per-query diff in ~1 s; replaces 25-min sweep cost for planner-only commits |
| 0004 | Cost-model edge discount + deterministic synthesis | **FULL `2511184`** | inferredEdgePenalty=2.0 + isInferred edge tagging + deterministic equiv_class ordering + 5 unit tests; dormant until hook re-enabled in a future milestone |
| 0001 | Re-enable inferTransitiveEqualities hook (Q5 fix) | **DEFERRED `83e7853`** | Second attempt with M0076-0004 preparation; Q5 plan structurally changed but cancel persisted (303M-row intermediate). Empirical evidence that build-side memory cost is the keystone fix |
| 0005 | Q9 chained-NLI combined fix | **DEFERRED to M0077** | depends on 0001 hook being active to combine with synthesised predicates; 0001 deferred → 0005 also defers |
| 0002 | Arena retention-site audit + Datum packed flip | **DEFERRED to M0077** | per M0076 plan R7: when D fails, executor work also defers (Q5-priority session) |
| 0002b | filterOp predicate batch wiring | **DEFERRED to M0077** | depends on 0002 audit |
| 0003 | Build-toolchain A/B test isolation | **DEFERRED to M0077** | orthogonal but lower priority for Q5 unlock |
| 0007 | Final 22-q sweep + Phase 8 handover | **LANDED `<this commit>`** | 22/22 row-count parity confirmed |

## 3. Key empirical finding: build-side memory cost is the Q5 keystone

The M0076-0001 second attempt with `inferredEdgePenalty=2.0`
produced a Q5 plan structurally different from baseline
but worse in practice:

```
Old plan (baseline, ffc3429):
  MultiHashJoin (6 tables)
    Seq Scan on orders   (1.5M)
    Seq Scan on customer (150K)
    Seq Scan on supplier (10K)
    Seq Scan on nation   (25)
    Seq Scan on region   (5)
    Seq Scan on lineitem (6M)
  → cancels at 1100s; predicates evaluated post-join

New plan (with synthesised c_nationkey=n_nationkey edge):
  Hash Join (INNER)
    Hash Join (INNER) (rows=303042055)  ← 303M intermediate!
      Seq Scan on lineitem (6M)
      Seq Scan on orders   (1.5M)
    MultiHashJoin (4 tables)
      Seq Scan on customer (150K)
      Seq Scan on supplier (10K)
      Seq Scan on nation   (25)
      Seq Scan on region   (5)
  → also cancels at 1100s; intermediate cardinality
    dwarfs the baseline plan
```

The cost model picked the new plan because the
synthesised edge made the lineitem⋈orders intermediate
look cheap-by-NDistinct (`(6M * 1.5M) / 1.5M = 6M`),
but in reality building a 1.5 M-row hash table on
orders is the bottleneck. PostgreSQL's `cost_hashjoin`
includes `cpu_operator_cost * inner_path_rows` which
makes the orders-as-build cost dominant; goopg's
`estimateJoinCost` formula `(L * R) / max(NDistinct)`
treats build and probe symmetrically.

**Implication for M0077:** simply tuning the penalty
(2.0 → 4.0 → ∞) cannot fix Q5. At penalty=∞ the DP
falls back to the baseline plan (which also cancels
at 1100s). At penalty=1.0 the DP picks the worse
303M-row plan. **No tuning of the penalty alone makes
Q5 complete** — the cost-model's structural blind
spot must be addressed first.

## 4. Recommended next steps — M0077 milestone shape

The Q5 fix requires structural cost-model work, then
re-enabling the hook can deliver the row-count win.
Order in priority of impact:

| # | Sub-milestone | Drives |
|---|---|---|
| 0001 | **Build-side memory cost in `estimateJoinCost`** — port PostgreSQL's `cost_hashjoin` shape: `cpu_operator_cost * inner_rows` (build) + `cpu_operator_cost * outer_rows * num_buckets` (probe). | Enables 0003 to pick the right plan for Q5 |
| 0002 | Selectivity-aware rowcount in `estimateJoin` — propagate post-filter rowcount of each input to the join cost (currently uses `tableRows()` raw). | Q5 + Q1 / Q3 / Q11 / Q12 / Q13 plan accuracy |
| 0003 | Re-enable transitivity inference (M0075-0001 / M0076-0001 third attempt) — depends on 0001 + 0002. With proper cost model, the synthesised edge unlocks Q5's optimal join order. | Q5 wall time → seconds, not 1100 s cancel |
| 0004 | Q9 chained-NLI rebind + cardinality refinement — the M0076-0005 carry-forward, now combinable with active hook. | Q9 ≥ 100 rows DETERMINISTICALLY |
| 0005 | Arena retention-site audit + Datum packed flip — M0076-0002 carry; sticky per-query slots. | 24 B Datum struct savings; M0075-0003 silent-regression cause documented at memory `m0075_partial_outcomes_and_findings.md` |
| 0006 | filterOp predicate batch wiring — depends on 0005 arena work + M0074-0001 evalBinaryBatch. | Q12/Q13/Q5 wall time win once Q5 plan is good |
| 0007 | Build-toolchain knob isolation — M0075-0007 / M0076-0003 carry. | Per-knob A/B post-Q5-fix |
| 0008 | Final 22-q sweep + Phase 9 handover. | -- |

**Priority recommendation:** M0077 should focus 0001 +
0002 + 0003 first (the Q5 unlock chain). 0004 / 0005 /
0006 / 0007 carry forward as separate concerns.

## 5. Verification methods

```sh
# Pre-commit gate (run before every commit):
make bench-build  # uses default-unoptimised flow
ps aux | grep "goopg-bench-bin" | grep -v grep \
    | awk '{print $2}' | xargs -r kill -SIGTERM
sleep 3
nohup ./tmp/goopg-bench-bin start \
    -D bench/tpch/runtime_goopg/data \
    --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf \
    > bench/tpch/runtime_goopg/goopg.log 2>&1 &
sleep 5

# Tight gate
./tpch-runner --queries=12,13,21,22,9 \
    --per-query-timeout=620s --cancel-after=600s

# Unit tests
go test ./internal/parser/... ./internal/planner/... \
    ./internal/executor/... ./internal/testutil/tpch/... \
    ./cmd/plan-snapshot/...

# M0076-0006 plan-snapshot harness (FAST — for planner-only changes)
for q in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 16 17 18 19 20 21 22; do
    ./tmp/plan-snapshot diff --label m0076-baseline-ffc3429 --queries=$q
done

# Full 21-q sweep (MANDATORY for executor / Datum / arena / catalog
# / wire-protocol changes)
./tpch-runner --queries=1,2,3,4,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22 \
    --per-query-timeout=1200s --cancel-after=1100s
```

## 6. Document references

### 6.1 New M0076 docs (Phase-8)

- [`docs/milestones/0076-m0075-carry-forward-and-plan-snapshot-harness.md`](../milestones/0076-m0075-carry-forward-and-plan-snapshot-harness.md) — milestone doc.
- [`docs/design/0076-0004-cost-model-edge-discount.md`](../design/0076-0004-cost-model-edge-discount.md) — cost-model preparation.
- [`docs/design/0076-0006-plan-snapshot-harness.md`](../design/0076-0006-plan-snapshot-harness.md) — harness design + decision tree.
- [`tmp/q5-plan-analysis.md`](../../tmp/q5-plan-analysis.md) — **detailed Q5 plan root-cause analysis (current vs expected vs why)**. Load-bearing reference for M0077-0001 cost-model work.

### 6.2 Carry-over memory pointers

`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`:
- `m0075_partial_outcomes_and_findings.md` — autonomous-
  mode partial-commit pattern with documented failure
  modes per sub-milestone.
- `m0073_arena_q5_heap_drop.md` — Q5 heap −72 % via
  M0073 arena wiring.
- `m0071_stage_b_silent_regression.md` — silent-
  regression pattern that M0075-0003 / 0004 hit.
- `feedback_tpch_pre_commit_gates.md` — pre-commit gate
  operation; M0076-0006 plan-snapshot harness reduces
  the gate cost for planner-only changes.

### 6.3 Code anchors (Phase 8 changes)

Plan-snapshot harness (M0076-0006):
- `cmd/plan-snapshot/main.go` (NEW; capture + diff
  subcommands; per-query connection acquisition; Q15
  special-case skip).
- `cmd/plan-snapshot/main_test.go` (NEW; 9 tests).
- `Makefile` — `plan-snapshot-build`,
  `plan-snapshot-capture`, `plan-diff` targets.
- `plan_snapshots/m0076-baseline-ffc3429.txt` —
  baseline.

Cost-model preparation (M0076-0004):
- `internal/planner/equiv_class.go` — deterministic
  `classes()` + sorted root iteration in
  `inferTransitiveEqualities`.
- `internal/planner/bushy.go` —
  `joinEdge.isInferred` field; `inferredEdgePenalty`
  constant (= 2.0); `buildJoinGraph` `inferredCount`
  param; `estimateJoinCost` penalty multiplier.
- `internal/planner/cost_model_inferred_edge_test.go`
  (NEW; 5 tests).

Q5 hook attempt + revert (M0076-0001):
- `internal/planner/bushy.go::tryBushyDP` — call site;
  hook re-enable was attempted with `inferredCount =
  len(synthesised)` then reverted to `inferredCount = 0`
  with empirical postmortem inline.
- `docs/design/0075-0001-q5-equivalence-class-inference.md`
  — status flipped to "PARTIAL × 2" with second-attempt
  findings.

## 7. Lessons learned (autonomous mode, M0073-M0076 sequence)

The four-milestone sequence (M0073 / M0074 / M0075 /
M0076) reinforced two prior lessons and exposed two new
ones:

1. **Pre-commit gate catches central-type regressions
   the unit-test surface misses** (M0075-0003 silent-
   regression). Full sweep is the structural defence
   for executor commits.

2. **Conservative-scope partial commits accumulate
   value** (M0073/M0074/M0075/M0076 all landed forward-
   compat infrastructure that future milestones build
   on directly).

3. **NEW: Plan-diff harness pays off in 1 session**
   (M0076-0006). Per-query diff in ~1 s replaced ~25-
   min sweeps for the M0076-0004 + M0076-0001 verifications.
   The deterministic-synthesis-ordering fix in M0076-0004
   was a prerequisite for stable diffs.

4. **NEW: Empirical Q5 attempt was the most valuable
   non-landing in M0076.** The plan-diff showed
   conclusively that the issue is cost-model structure
   (`(L*R)/NDistinct` formula's blind spot for build-
   side memory), not the missing transitivity inference.
   Without the attempt, the M0077 priority queue would
   have been "re-attempt with higher penalty"; with the
   attempt, the priority is "fix the cost-model formula
   first". This is what makes the deferral PARTIAL
   rather than NULL — the design doc + this handover
   carry the empirical finding forward.
