# Q5 Planner Fix 03 - Validation and Rollout

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-10 |
| scope | planner validation |
| supersedes | none |

## 1. Validation strategy

This work is planner-heavy and touches historically sensitive queries.
Validation therefore must combine:

1. unit tests for the new helpers,
2. plan-snapshot diffs for all 22 TPC-H queries,
3. focused execution gates for the historically fragile queries,
4. one full TPC-H sweep after the final planner slice lands.

The plan-snapshot harness from M0076 is the primary regression tool.

## 2. Expected plan-diff policy

The default expectation is no broad planner churn, but a Q5-only diff is
not a credible assumption once relation-local predicates start affecting
MHJ eligibility. The regression policy therefore distinguishes between
queries that must change, queries that are allowed to change under a
focused gate, and queries that should remain stable.

Allowed categories:

1. `must change`
   - Q5
2. `may change, but only with a focused execution gate`
   - Q2, Q3, Q7, Q8, Q9, Q10, Q11, Q12, Q13, Q18, Q21
3. `should stay identical`
   - Q1, Q4, Q6, Q14, Q15, Q16, Q17, Q19, Q20, Q22

Why those queries are special:

- Q2, Q7, Q10, Q11, Q18, and Q21 already use bushy-DP/MHJ-heavy shapes
  and can plausibly react when relation-local filters affect binary-vs-MHJ
  eligibility.
- Q3 has a historical regression guard in the fix plan.
- Q8 and Q9 are planner-sensitive and have prior NLI/MHJ history.
- Q12 and Q13 are the canonical silent-regression sentinels from slot and
  arena work; they are cheap to run and should remain in every tight gate.

## 3. Required tests

### 3.1 Unit tests

Add or extend tests under `internal/planner/`:

1. local filter partitioning
2. local filter attachment
3. MHJ skip-on-filtered-leaf contract
4. base relation post-filter row estimation
5. build-side-aware join cost
6. anchored equality synthesis
7. a Q5-specific plan-shape test that asserts:
   - no 6-table MHJ,
   - region filter below join tree,
   - orders filter below join tree,
   - customer joins the filtered nation side through the anchored edge

### 3.2 Plan-snapshot diffs

Run structural diff against `plan_snapshots/m0076-baseline-ffc3429.txt`.

Required interpretation:

1. Q5 diff is mandatory.
2. Any diff in a `should stay identical` query is a stop-and-explain
   event.
3. A diff in a `may change` query requires the focused execution gate
   before more code lands.

### 3.3 Focused execution gate

The minimum focused gate after each substantive slice is:

```sh
./tpch-runner --queries=3,5,8,9,12,13,21,22 \
    --per-query-timeout=620s --cancel-after=600s
```

Required checks:

1. Q5 plan shape improves and runtime does not worsen.
2. Q3 row count stays at the current guarded value.
3. Q9 row count does not drop below the current mode-1 baseline.
4. Q12 and Q13 preserve their current gate row counts.
5. Q21 and Q22 keep their existing planner behavior.

## 4. Implementation slices

The work should land in four planner slices, each independently
verifiable.

### Slice A - local predicate partition and attachment

Contents:

1. `partitionConjunctsForJoinPlanning`
2. `attachRelationLocalFilters`
3. `TestRewriteMultiWayChainSkipsFilteredLeaves`

Expected external effect:

1. Q5 structural plan diff
2. no equality inference yet
3. early pre-MHJ attachment remains limited to `Filter(leaf)` wrappers,
   not pre-MHJ `IndexScan` promotion
4. planner-sensitive MHJ queries may diff and therefore stay inside the
   focused gate

### Slice B - filtered base-row estimates

Contents:

1. `baseRelInfo`
2. row estimation from local filters
3. DP uses filtered leaf rows

Expected external effect:

1. Q5 join order may improve even before new inferred edges
2. no new equality edges yet

### Slice C - build-side-aware join cost

Contents:

1. replace output-only cost with build/probe/output cost
2. align build-side choice with the same filtered-row inputs

Expected external effect:

1. Q5 should stop preferring large-build alternatives
2. Q9 should remain unchanged because no new edge set is introduced

### Slice D - anchored equality synthesis

Contents:

1. `inferAnchoredEqualities`
2. Q5-specific anchor path through filtered nation
3. no global re-enable of all synthesized edges

Expected external effect:

1. Q5 reaches the expected binary hash-join family
2. Q9 remains within baseline because the anchored rule does not fan out
   into unfiltered large classes

## 5. Rollback rules

Rollback is mandatory if any of the following occurs:

1. Q5 still changes into a plan with a lineitem-orders intermediate that
   is larger than the current baseline.
2. Q9 row count or runtime regresses materially.
3. Q12 or Q13 change row count.
4. A `should stay identical` query shows a structural diff without a
   convincing execution win.

The rollback unit is the last landed slice, not ad hoc line edits.

## 6. Final acceptance

The full bundle is accepted only when all of the following hold:

1. Q5 plans into the expected binary hash-join family described in
   `tmp/q5-plan-analysis.md`.
2. Q5 no longer plans as a 6-table MHJ with top-level region/orders
   filters.
3. The focused gate passes.
4. The full TPC-H sweep preserves existing row-count gates.
5. Plan-diff noise is limited to Q5 and any query with an explicitly
   justified improvement.