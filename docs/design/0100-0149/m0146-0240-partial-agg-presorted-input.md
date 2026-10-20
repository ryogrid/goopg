# M0146-0240 — a partial aggregate reads a presorted partial path without a Sort

Status: slices 1–2 landed 2026-10-20 — CLOSED (SF0.25 99/99, SF1 97/99; Q77 matches PG at both scales).
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

## 5. Slice 2: a sorted aggregate over an EC-ordered input keeps its order

### Diagnosis (SF1 clone, the ws/wr pair alone)

- goopg filed the Merge Left Join (29519.98..29549.91), with an explicit Sort
  on both inputs. PG's starts at 29514.15, the sum of the two inputs'
  startups: neither side is sorted.
- ws's leaf carried its pathkeys, but wr's did not. wr's body is
  `GroupAggregate (wp_web_page_sk)` over a padding Project over a Gather
  Merge. That Gather Merge is ordered by `wr_web_page_sk`, which the
  subquery's join puts in the group key's equivalence class.
- The grouping step accepted that input as presorted through the EC
  (M0146-0134). aggregateEmissionPathkeys and its path twin
  groupingEmissionPathkeys then required the input key to *be* the group
  key, so the aggregate claimed no output order, and
  convert_subquery_pathkeys had nothing to convert.
- PG's AggPath carries its subpath's canonical pathkeys (create_agg_path):
  the group key's class, either way.

### Change

- `Aggregate.GroupInputKeys` records, per processed group-clause position,
  the EC member the grouping step proved the input ordered by. Two arms
  stamp it, via equivalentGroupInputKeys: the presorted-seed arm and the
  searched-candidate arm (contained case) in groupingpaths.go.
- `groupKeyOrderedBy` is the one test both emission twins now ask. It
  accepts the group key itself or the recorded member.
- upper_narrow_apply.go remaps the field with the input row. A key the cut
  drops removes the stamp: the aggregate then claims less order, and the
  site is never refused. The field-inventory pin was extended accordingly.

### Measurements

- **Repro:** Merge Left Join 29514.15..29549.06 (PG 29514.15..29548.61).
  goopg's hash alternative is 29549.08.
- **SF1:** PLAN-PARITY 96 → **97/99**. Q77 MATCHes and is the only changed
  plan.
- **SF0.25:** 99/99, no plan changed.
- **TPC-H:** 22/22, plans byte-identical.

### The 0.45 left in the merge join's total: trivial_pathtarget

PG's cost_subqueryscan adds nothing when the subquery rel's reltarget is
trivial (all subquery outputs, as Vars in attno order, no restriction quals),
because setrefs will remove the node. allpaths.c (set_subquery_pathlist)
reads the reltarget in the order build_base_rel_tlists discovered the Vars:
the target list first, then the join clauses.

Measured on :65438 by reordering the outer select list of the same
statement:

| variant | ws trivial | wr trivial | PG total |
|---|---|---|---|
| neither leaf in attno order | no | no | 29549.06 (goopg's number) |
| both leaves in attno order | yes | yes | 29548.15 |
| Q77's own order | yes | no | 29548.61 |

goopg's costSubplanLeaf always charges `cpu_tuple_cost × rows`. The charge
lands equally in every join method's cost, so it does not move this
election. It is ledgered; goopg's needed-column sets are name-only and
unordered, so a faithful port needs PG's discovery order.

### Pin

TestGroupAggOverECOrderedInputKeepsItsOrder: PG-anchored shape plus values.
It fails when groupKeyOrderedBy ignores the stamp.

## 6. Not done (ledgered)

- make_ordered_path's Incremental Sort arm for a partially presorted cheapest
  partial path: goopg's partial arms still sort it fully.
- The other partial paths in the input rel's partial_pathlist: PG offers
  every presorted one, goopg only the cheapest.
- create_partial_distinct_paths (distinctpaths.go) still sorts its partial
  seed unconditionally.
- cost_subqueryscan's trivial_pathtarget exemption (§5).
