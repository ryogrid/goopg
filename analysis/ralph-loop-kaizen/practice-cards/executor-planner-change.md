# Practice card — executor / planner change

**Load when** the task touches `internal/planner/` or `internal/executor/`,
or mentions joins, predicates, row counts, or a TPC-H query shape.

**Why this card exists:** silent row-count regressions are the most expensive
failure mode in this project's history (608 regression anchors; multi-loop
bisects). A change here can pass its own check and silently break a *different*
query. See [02 Theme A](../02-pain-points.md).

## Must-run gate BEFORE committing

The gate is now ONE command:

```bash
scripts/tpch-spotcheck.sh
```

It does the fresh server restart + Q12/Q13 row-count spot-check for you
(memory-capped start, canonical counts from `bench/tpch/spotcheck_expected.env`,
exit 1 on mismatch). On machines without the TPC-H data dir it prints
SKIPPED and exits 0 — in that case fall back to the manual steps below
where data exists.

1. **Fresh server restart** — stale state hides regressions.
2. **Q12 / Q13 row-count spot-check** — these are the canonical silent-regression
   tripwires (canonical is `Q12=2/Q13=35`; `Q12=0/Q13=2` is the known failure
   signature). Confirm canonical row counts, not just "no error".
3. **Run the affected query set**, then broaden if row counts shifted anywhere.

(Encodes `feedback_tpch_pre_commit_gates`, `m0071_stage_b_silent_regression`.)

Never `-count=1` in a gate run (cache policy: ci/design/test-gate-speedups/05 §1).

## Sibling-path audit (do this in the SAME loop)

A planner/executor change usually has a twin that must change together. Before
finishing, check whether the change has a sibling and update both:

- fast-path evaluator ↔ interpreted evaluator
- column-lookup ↔ star-expansion
- encode ↔ decode
- Semi/Anti residual eval ↔ schema-column source-table mapping

A unit test on one path can pass while the other is silently wrong
(`pattern_sibling_paths_must_agree`).

## Known traps

- Synthesised join predicates can produce huge intermediates if the cost model
  ignores build-side memory (`m0076_q5_cost_model_root_cause`).
- A prior full attempt at the Q9 rebind caused a runtime **hang**
  (`M0072-0002`) — bound the change and verify incrementally; do not retry the
  whole rewrite blind (`m0074_partial_scope_lessons`).

## If you must defer

Record a deferral-ledger line (what landed / what deferred / resume point / why)
rather than closing silently with a forward reference.
