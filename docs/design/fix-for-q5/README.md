# Q5 Planner Fix Design Bundle

| field | value |
| --- | --- |
| status | draft |
| date | 2026-05-10 |
| scope | planner-only design bundle |
| supersedes | none |

## 1. Purpose

This design bundle describes a new planner-first path for making
TPC-H Q5 plan into the expected binary hash-join shape documented in
`tmp/q5-plan-analysis.md`, without requiring the repository to follow
the exact M0075/M0076 sequence that was attempted before.

The target is not "make Q5 a little faster". The target is:

1. Push the Q5 region and orders filters below the join tree.
2. Cost joins using post-filter relation sizes, not raw table sizes.
3. Re-enable only the transitive equality edges that are justified by
   selective anchors, instead of globally re-enabling every inferred
   edge.
4. Preserve the binary join tree for Q5 instead of collapsing it back
   into a 6-table `MultiHashJoin`.

The baseline and the empirical failure mode that motivated this design
are documented in:

- `docs/handover/2026-05-10-tpch-status-phase8.md`
- `tmp/q5-plan-analysis.md`
- `plan_snapshots/m0076-baseline-ffc3429.txt`

## 2. Why this bundle does not follow the previous plan exactly

The previous direction centered on re-enabling global transitive
equality inference and then tuning the cost penalty for inferred
edges. Phase 8 showed that this is not enough:

- Q5 still cancelled.
- The new plan changed structurally, but it changed in the wrong
  direction.
- The main missing input to the planner was not "more edges"; it was
  "better information about filtered relation size and build-side
  cost".

This design therefore changes the order of work:

1. Make relation-local filters first-class planner inputs.
2. Make join cost sensitive to filtered row counts and hash-build size.
3. Re-enable only selective inferred equalities that are anchored by a
   small or strongly filtered relation.
4. Keep `MultiHashJoin` away from plans where preserving binary join
   staging is the whole point.

## 3. Design map

- [01-target-shape-and-local-filtering.md](01-target-shape-and-local-filtering.md)
  describes how Q5 should look after planning, how relation-local
  predicates are extracted, and how they are attached to scan inputs.
- [02-cost-model-and-selective-equivalence.md](02-cost-model-and-selective-equivalence.md)
  describes the row-count, cost-model, and selective equality
  inference changes needed to make the planner choose that shape.
- [03-validation-and-rollout.md](03-validation-and-rollout.md)
  describes the regression strategy, plan-diff rules, implementation
  slices, and acceptance gates.

## 4. Expected end state

After the full bundle lands, Q5 should no longer plan as:

```text
Filter
  -> Multi-Way Hash Join (6 tables)
```

It should instead plan as a binary hash-join tree whose leaves already
carry the region and orders filters, with the lineitem join happening
last. The exact textual shape can vary, but all of the following must
be true:

1. The region scan is filtered before joining to nation.
2. The orders scan is filtered before joining to customer/lineitem.
3. Customer can join through a synthesized equality only when the
  planner has evidence that the equivalence class is anchored safely,
  either by a reliably selective local filter or by existing
  `SmallDimension` metadata in the current codebase.
4. Q5 is not packed into a 6-table `MultiHashJoin`.

## 5. Intended implementation order

The intended landing order is:

1. Relation-local filter extraction and scan attachment.
2. Post-filter base-row estimation.
3. Build-side-aware hash join cost.
4. Selective transitive equality synthesis.
5. Full regression pass and handover update.

That order is deliberate. Steps 1-3 already improve Q5 planning and
are also the safety rails for step 4.