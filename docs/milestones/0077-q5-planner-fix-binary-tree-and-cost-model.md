# Milestone 0077 — Q5 planner fix: binary tree preservation + cost-model maturation

**Status:** planned
**Branch:** `gc-oriented-refactor` / `try-codex`
(continuation of M0076)
**Depends on:** M0076-final (Phase 8 handover at
`docs/handover/2026-05-10-tpch-status-phase8.md`);
M0076-0006 plan-snapshot harness (commit `b27869c`);
M0076-0004 cost-model preparation infrastructure
(commit `2511184`); M0075-0001 equiv-class union-find
module (commit `e89c98a`).
**Drives:** **Q5 plans into the expected binary
hash-join family** described in `tmp/q5-plan-analysis.md`
§2 — filtered region+orders inputs below the join tree,
customer joins to filtered nation through anchored
synthesis, NO 6-table `MultiHashJoin`. Q12=2 / Q13=35 /
Q21=381 / Q22=7 / Q9 ≥ 7 (mode-1 baseline) / Q3 = 11462
every commit.

## Context

The M0076 close handover (Phase 8 §3) identified the
keystone structural gap: `estimateJoinCost`'s
single-output-cardinality formula
`(L*R)/max(NDistinct)` cannot distinguish good small-
build plans from disastrous big-build plans. Re-enabling
the M0075-0001 transitivity hook with a flat
`inferredEdgePenalty=2.0` (M0076-0001 second attempt)
produced a worse Q5 plan with a 303M-row lineitem⋈orders
intermediate.

The user authored a 4-document design bundle at
`docs/design/fix-for-q5/` specifying exactly how to
land the fix. That bundle is the AUTHORITATIVE
specification for M0077's implementation; this
milestone doc is a contract pointing at the bundle and
tracking sub-milestone landing order.

**Why this differs from M0075-0001 + M0076-0001:**

The earlier attempts re-enabled a global transitivity
hook + tuned a flat penalty. Both reverted because the
cost model couldn't distinguish "synthesised edge into
filtered nation" from "synthesised edge into unfiltered
large fact". The design bundle's 4-slice order — local
filters first, then post-filter row estimates, then
build-side cost, THEN anchored synthesis — ensures each
slice has the prerequisite information the next needs:

1. **Slice A** makes filtered leaves visible at the
   binary-tree level (no MHJ collapse).
2. **Slice B** makes the planner aware that
   `Filter(SeqScan(region))` is 1 row, not 5.
3. **Slice C** makes the cost model penalise large hash
   table builds, so synthesised edges through
   unfiltered relations look expensive.
4. **Slice D** adds only synthesised edges anchored to
   small or filtered relations.

Each slice is independently revertible; failure at
slice N does not require reverting earlier slices.

## Sub-milestones

| # | Sub-milestone | Risk | Tier | Depends on |
| - | ------------- | ---- | ---- | ---------- |
| 0001 | Slice A — local predicate partition + attachment | MED | structural | M0076 close |
| 0002 | Slice B — filtered base-row estimates (`baseRelInfo`) | LOW | perf-infra | 0001 |
| 0003 | Slice C — build-side-aware 3-part hash-join cost | MED-HIGH | structural | 0002 |
| 0004 | Slice D — anchored equality synthesis (Q5 unlock) | HIGH | structural | 0001 + 0002 + 0003 |
| 0005 | Final 22-query SF=1 sweep + Phase 9 handover | — | — | 0001..0004 |

## Design references

The 4-document design bundle at `docs/design/fix-for-q5/`
is the authoritative spec:

- [`docs/design/fix-for-q5/README.md`](../design/fix-for-q5/README.md)
  — purpose, change-of-direction rationale vs prior
  M0075-0001 / M0076-0001 attempts, intended landing
  order.
- [`docs/design/fix-for-q5/01-target-shape-and-local-filtering.md`](../design/fix-for-q5/01-target-shape-and-local-filtering.md)
  — Slices A. Q5's expected post-planning leaf shape;
  relation-local predicate partition + attachment;
  deliberate `MultiHashJoin` skip-on-filtered-leaf
  contract; `shouldAttachBeforeMHJ` rollout gate.
- [`docs/design/fix-for-q5/02-cost-model-and-selective-equivalence.md`](../design/fix-for-q5/02-cost-model-and-selective-equivalence.md)
  — Slices B + C + D. `baseRelInfo` post-filter row
  estimates; `clauseSelectivityWithSource` reliable-
  vs-fallback signal; 3-part hash-join cost
  (output + build + probe); DP state extension to
  carry row counts; selective `inferAnchoredEqualities`
  (NOT global).
- [`docs/design/fix-for-q5/03-validation-and-rollout.md`](../design/fix-for-q5/03-validation-and-rollout.md)
  — 4-slice landing plan; plan-snapshot diff policy
  with 3 query categories (must change / may change
  with focused gate / should stay identical); focused
  execution gate (`--queries=3,5,8,9,12,13,21,22`);
  per-slice rollback.

Carrying-forward references:
- [`docs/handover/2026-05-10-tpch-status-phase8.md`](../handover/2026-05-10-tpch-status-phase8.md)
  — M0076 close + Q5 keystone finding.
- [`tmp/q5-plan-analysis.md`](../../tmp/q5-plan-analysis.md)
  — current vs expected Q5 plan + 5 distinct planner
  gaps (untracked working-doc; section §2 defines the
  M0077 acceptance shape).

## Definition of Done

**Mandatory (per design 03 §6):**

- [ ] **Q5 plans into the expected binary hash-join
      family** described in `tmp/q5-plan-analysis.md`
      §2. Filtered region input below the join tree;
      filtered orders input below the join tree;
      customer joined to filtered nation through the
      anchored synthesised edge; lineitem joined LAST
      as probe.
- [ ] **Q5 no longer plans as a 6-table `Multi-Way
      Hash Join` with top-level region + orders
      filters.**
- [ ] **Focused execution gate** (`--queries=3,5,8,9,12,13,21,22`)
      passes with row-count parity and runtime ≤ 110 %
      of M0076-final on stable queries.
- [ ] **Full TPC-H sweep** preserves existing row-count
      gates (Q3=11462, Q12=2, Q13=35, Q21=381, Q22=7,
      Q9 ≥ 7 mode-1 baseline).
- [ ] **Plan-diff** noise limited to Q5 and the
      "may change" set (Q2, Q3, Q7, Q8, Q9, Q10, Q11,
      Q12, Q13, Q18, Q21) with documented justification
      per diverging query. "Should stay identical"
      queries (Q1, Q4, Q6, Q14, Q15, Q16, Q17, Q19,
      Q20, Q22) MUST plan-diff MATCH.

**Per-slice (per design 03 §4):**

- [ ] **Slice A (M0077-0001)**: Q5 structural plan
      diff (no 6-MHJ); region + orders filters
      attached to leaves; no equality inference yet;
      pre-MHJ attachment limited to `Filter(leaf)`
      wrappers (no `IndexScan` promotion at this
      stage).
- [ ] **Slice B (M0077-0002)**: Q5 join order may
      improve; no new edges; row-count plumbing only.
      `selectivityEstimate.reliable` signal correctly
      classifies fallback vs stat-driven.
- [ ] **Slice C (M0077-0003)**: 3-part cost formula
      lands; Q5 stops preferring large-build
      alternatives (M0076-0001's 303M-row plan no
      longer rank-best). Q9 unchanged.
- [ ] **Slice D (M0077-0004)**: Q5 reaches the binary
      hash-join family. Q9 stays at mode-1 baseline.
      `inferAnchoredEqualities` synthesises only
      anchor → non-anchor edges.

**Best-effort:**

- [ ] Q5 wall time < 60 s (would be a 18× win over
      1100 s cancel baseline; tmp/q5-plan-analysis.md
      §2 estimates a ~150 s ceiling on goopg's
      row-at-a-time executor with the right plan).
- [ ] Q3 / Q11 / Q14 wall time delta within ±10 % of
      M0076-final.

**Final:**

- [ ] M0077-0005 sweep + handover doc (Phase 9)
      committed; profiles archived under
      `pprof-data/m0077-final/`.
- [ ] Plan-snapshot final capture
      (`plan_snapshots/m0077-final.txt`).
- [ ] `MEMORY.md` updated with M0077 outcomes.
- [ ] `go test ./...` PASS at every commit.

## Verification

Pre-commit gate (per design 03 §3.3 + this milestone):

```sh
make bench-build
ps aux | grep "goopg-bench-bin" | grep -v grep \
    | awk '{print $2}' | xargs -r kill -SIGTERM
sleep 3
nohup ./tmp/goopg-bench-bin start \
    -D bench/tpch/runtime_goopg/data \
    --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf \
    > bench/tpch/runtime_goopg/goopg.log 2>&1 &
sleep 5

# Unit tests
go test ./internal/parser/... ./internal/planner/... \
    ./internal/executor/... ./internal/testutil/tpch/... \
    ./cmd/plan-snapshot/...

# Focused execution gate
./tpch-runner --queries=3,5,8,9,12,13,21,22 \
    --per-query-timeout=620s --cancel-after=600s

# Per-query plan-diff against m0076 baseline
for q in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 16 17 18 19 20 21 22; do
    ./tmp/plan-snapshot diff --label m0076-baseline-ffc3429 --queries=$q
done
```

## Out of scope (carry to M0078+)

- **Q5 wall-time floor < 10 s** — depends on
  executor-side optimisation (filterOp batch wiring;
  M0076-0002b carry).
- **Per-connection permArena scoping** for
  multi-tenant production.
- **Datum struct packed flip** (M0075-0003 / M0076-0001
  carry) — prerequisite is arena retention audit.
- **filterOp predicate batch wiring** (M0076-0002b
  carry).
- **Build-toolchain knob isolation** (M0076-0003 carry).
- **MCV histogram refresh / re-ANALYZE flow**.
- **SIMD intrinsics** for `evalBinaryBatch`.
- **Q20 distributional gap** (99 vs canonical ~186).

## References

- `docs/handover/2026-05-10-tpch-status-phase8.md` —
  M0076 close + Q5 keystone finding.
- `pprof-data/m0075-final/q5.{cpu,heap}.prof` —
  M0075-final captures (cross-milestone diff baseline).
- `plan_snapshots/m0076-baseline-ffc3429.txt` —
  M0076 plan-snapshot baseline.
- `internal/planner/equiv_class.go` (M0075-0001 +
  M0076-0004) — union-find module + deterministic
  classes(); reused by `inferAnchoredEqualities`.
- `internal/planner/bushy.go` (M0076-0004) —
  `joinEdge.isInferred` field +
  `inferredEdgePenalty` constant; consumed by
  Slice D.
- `cmd/plan-snapshot/main.go` (M0076-0006) — primary
  regression mechanism for every M0077 commit.

## Memory carry-forward

- `m0076_q5_cost_model_root_cause.md` — load-bearing
  root-cause analysis pointing at this milestone.
- `m0075_partial_outcomes_and_findings.md` —
  autonomous-mode partial-commit pattern; M0077 slice
  ordering is designed to avoid the regression
  patterns documented there.
- `m0073_arena_q5_heap_drop.md` — Q5 heap −72 %
  baseline.
- `feedback_tpch_pre_commit_gates.md` — pre-commit
  gate operation.
