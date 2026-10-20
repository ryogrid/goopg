# M0146-0240 — a partial aggregate reads a presorted partial path without a Sort

Status: slice 1 landed 2026-10-20. Open: the SF1 web-branch join method (§5).
Evidence: `analysis/m0146/m0146-0240/`.

## 1. Record

The S9 pass at cdb6a4513 ranked join-method first: SF1 Q13 and SF1 Q77 had
one record each. Q77 was chosen because its divergence is the shallower one,
and because its ws CTE showed the same defect as SF0.25 Q77's sr CTE.

| corpus | branch | goopg | PG |
|---|---|---|---|
| SF0.25 | Q77 c0 (sr) | Partial GroupAggregate → **Sort (s\_store\_sk)** → Merge Join | Partial GroupAggregate → Merge Join |
| SF1 | Q77 c2 (ws) | Partial GroupAggregate → **Sort (wp\_web\_page\_sk)** → Merge Join | Partial GroupAggregate → Merge Join |

In both rows the Merge Join's cost equals PG's; the extra Sort is the only
difference.

## 2. PG mechanism

create_partial_grouping_paths (planner.c:7523-7579) walks the input rel's
partial_pathlist. For each path it calls
`make_ordered_path(root, partially_grouped_rel, path, cheapest_partial_path,
group_pathkeys, -1)`, which:

- returns the path unchanged when `pathkeys_contained_in(group_pathkeys,
  path->pathkeys)`;
- adds an Incremental Sort for a presorted prefix when
  enable_incremental_sort is on;
- adds a full Sort only for the cheapest partial path.

The comparison is between canonical pathkeys. A merge join on
`sr_store_sk = s_store_sk` is ordered by the single pathkey of that
equivalence class, and that pathkey is also the group key `s_store_sk`'s.

## 3. goopg before

The upper split (addPartialAggSplitPath) works on one partial seed, `pseed`:
the searched rel's cheapest partial path, as a prebuilt node. The seed carried
no pathkeys, and every sorted partial arm put
`sortPathForBounded(pseed, …)` under it:

- the sorted-input split (sortedInputPartialAgg, which
  hashedPartialAggSurvives also prices);
- the aggregate-free partial Group arm (addPartialGroupArm);
- the R56 worker-sort Gather Merge (workerSortGatherMergePath).

## 4. Slice 1

- **Seed ordering.** `pseed.Pathkeys` takes the partial path's own pathkeys,
  validated against the unwrapped input's schema (validatedSearchPathkeys).
  canonicalPartialInputKeys then renames every key that is equivalent, within
  the searched rel (`usefulKeys.equivalentWithin`), to a group expression. The
  result is the grouping's name for PG's canonical pathkey.
- **orderedPartialInput** is make_ordered_path's first test. It returns pseed
  itself when its pathkeys contain the required keys, and a Sort over it
  otherwise. All three arms in §3 use it.
- **Plan builder.** createFinalizeAggSortedPlan accepts a sorted partial whose
  input is not a PathSort. splitAggregateTransportSortedInput then builds no
  worker Sort when it is given no input keys.

### Measurements

- **Repro** (SF0.25 clone, the sr CTE alone): the plan equals PG's, and
  Finalize costs 4517.99..4521.29 exactly as in PG. Results are byte-identical
  to PG's.
- **SF0.25:** PLAN-PARITY 98 → **99/99**, every category 0. Only Q77's plan
  changed.
- **SF1:** 96 → 96. ws now costs exactly PG's 24830.64..24859.35 and wr's
  GroupAggregate exactly PG's 4683.51..4688.26, but the web branch still
  elects a Hash Left Join (29549.08) over PG's Merge Left Join (29548.61).
- **TPC-H:** 22/22, plans unchanged.

### Pin

`internal/postmaster/partial_agg_presorted_input_test.go`
(TestPartialAggOverPresortedPartialJoin) compares plan shape, with costs
stripped, against PG 18.3's EXPLAIN on the same statements, and checks the
grouped values.

Two mutations each fail the pin:

- always sorting in orderedPartialInput;
- dropping the equivalence rename.

## 5. Open (slice 2)

SF1 Q77's web branch: PG's `Merge Left Join (ws, wr)` at 29514.15..29548.61
against goopg's `Hash Left Join` at 29519.60..29549.08. Both inputs now cost
exactly what PG's cost. First step: check whether goopg files the merge left
join over the two grouped CTE leaves at all (instrument addPath on the
joinrel), then compare its cost with PG's.

## 6. Not done (ledgered)

- make_ordered_path's Incremental Sort arm for a partially presorted cheapest
  partial path: goopg's partial arms still sort it fully.
- The other partial paths in the input rel's partial_pathlist: PG offers
  every presorted one, goopg only the cheapest.
- create_partial_distinct_paths (distinctpaths.go) still sorts its partial
  seed unconditionally.
