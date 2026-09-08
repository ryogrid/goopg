# C-15 (P4-06) — `create_grouping_paths`: the GROUP_AGG upper rel with sorted + hashed paths priced by `cost_agg`

Status: **design only**. Nothing under `internal/` was modified while it was
written. Every goopg claim was read out of the tree at `ab9588238` (C-13b
landed); every PostgreSQL claim is cited to `./postgres` (PG 18.3, read-only
oracle). Upstream design: take3 `08-target-design.md` §7 (P4-06); prerequisite
scoping C-10a/C-10c/C-10d landed; C-11 (registry) and C-12 (ORDERED producer)
landed — this cut is the second producer on the same scaffolding.

Item: build the `GROUP_AGG` upper rel with sorted, hashed (and plain)
`PathAgg` candidates priced by a `cost_agg` port **including the hash spill
arm**, select by cost, and **retire the three aggregate rules**
(`applyIndexOrderedGroupingRule`, `applyPresortedAggregateRule`,
`applyEnableHashAggRule`) whose GUC-gated outcome-reproduction the model
subsumes. Gate: take3 09 §5 P4 (PP + timing) — but unlike C-12/C-13b this cut
**may legitimately move plans** (a cost model choosing vs rules forcing), so
the gate is values + attribution + timing, and take3 08's P4 exit criterion
(`aggregation-strategy` diffs strictly decrease) is the direction check.

---

## 0. The four findings that shape everything below

**F1. The three rules reproduce cost-model OUTCOMES without a cost model.**
Each rule's doc comment says so explicitly: the hashagg bridge "reproduces
the *outcome* directly" (`groupagg_hashagg.go:5-11`), the presorted rule ports
only the *pathkey selection* half of `adjust_group_pathkeys_for_groupagg`
(the choice among sort orders, never a price), and the indexorder rule "cannot
tell whether an index-ordered sorted plan actually beats a hash aggregate the
way PG's `cost_agg` does" and fires only under `enable_hashagg = off` as a
proxy (`groupagg_indexorder.go:71-85`). C-15 does not improve the rules; it
deletes the need for them by pricing what they guess at.

**F2. `PathAgg` exists with no producer and no arm.** `path.go:61` declares
the kind; the only other mention is `narrowoutput.go:348`'s switch. There is
no `createAggPlan`, no `add_path` caller offering one, and `createPlanNode`'s
switch (`createplan.go:54-`) has no `PathAgg` arm — it would panic on one
today. The kind reservation is the whole of the existing scaffolding.

**F3. There is no `AggClauseCosts`, and the honest port starts flat.**
PG's `get_agg_clause_costs` prices each aggregate's trans/final functions
from `pg_proc.procost` plus input-expression costs. goopg's catalog *has*
`procost` (`internal/catalog/routines.go:81`) but the planner never reads
it, and the legacy display price charges a flat `cpu_operator_cost` per
aggregate per input row (`plancost.go:146-151`, "Charge cpu_operator_cost
per aggregate per input row, as cost_agg does"). The port therefore sets
`transCost.per_tuple = cpu_operator_cost` per aggregate and
`finalCost.per_tuple = cpu_operator_cost` per group with zero startup terms —
the same STRUCTURE as upstream (trans vs final, per-input vs per-group),
which is what lets real `procost` plug in later without re-plumbing, but the
numbers move from day one (the port adds grouping comparisons and output
terms the legacy charge lacks). Gate step 4 re-pins accordingly; "flat"
is the interim, labeled as such in code.

**F4. The GROUP_AGG rel sizes itself from `estimateNumGroups`, which is
already PG-faithful.** `estimateNumGroups` (`cardinality.go:1202`) is the
`estimate_num_groups` port the rowest census explicitly REFUTED as a defect
site ("computes the right answer whenever it is given real ndistinct
values"). `Rows ← max(1, estimateNumGroups(...))`, `Width ←
nodeTupleWidth(aggOutput)`, `NCols ← len(output)` — the same three reads
`sizeUpperRelFromNode` performs, with Rows from the estimator rather than the
legacy stamp.

---

## 1. PostgreSQL oracle

`create_grouping_paths` (`planner.c:3780`) builds the `(GROUP_AGG, NULL)`
upper rel via `make_grouping_rel`, decides three flags, and delegates to
`create_ordinary_grouping_paths` (`:4031`) except for degenerate grouping:

- `GROUPING_CAN_USE_SORT` when rollups exist or
  `grouping_is_sortable(processed_groupClause)`;
- `GROUPING_CAN_USE_HASH` when grouping, no ordered aggs
  (`numOrderedAggs == 0`), and the grouping is hashable;
- `GROUPING_CAN_PARTIAL_AGG` when `can_partial_agg`.

`add_paths_to_grouping_rel` (`:230`) then builds, per input path: sorted
`Agg` over the input when its pathkeys deliver the group keys (else over a
`Sort`), hashed `Agg` when allowed, each priced by `cost_agg`, plus Gather
over partial paths. No implementation at all ⇒ `ereport(ERROR,
"could not implement GROUP BY")`.

`cost_agg` (`costsize.c:2682`) has three arms. PLAIN: input total + trans
once per input tuple + final once + one output tuple. SORTED/MIXED: startup
= input startup (streams), total = input total + trans/input-tuple +
`cpu_operator × numGroupCols`/input-tuple + final on `numGroups` +
`cpu_tuple × numGroups`. HASHED: startup = input total (blocking) + trans +
hash-computation per input tuple + final startup; total adds final/output and
retrieval. Then the spill arm for HASHED/MIXED: batches from the hash entry
size vs the memory limit; writes accrue to startup AND total, reads to total
only (`:2741-2810`).

Two load-bearing details: (i) SORTED and HASHED have **exactly the same total
CPU cost** — sorted wins on startup alone, i.e. iff the input is already
ordered (`:2720-2732`, with the roundoff warning); (ii) grouping sets use
HASHED, MIXED (`consider_groupingsets_paths`, `planner.c:4354+` — which also
builds a simple `AGG_SORTED` arm when no set is unsortable), and sorted-input
sharing across sets, all keyed off per-set sortable/hashable flags. goopg's
executor has no mixed strategy and no sorted-groupingsets execution
(`openSorted` requires `GroupingSets == nil`,
`operators_join_agg.go:2222`; `groupingsets.go` runs one hash table per
set), so §3.1 stays hashed-only for grouping sets — a values-safe,
criterion-visible residual (§5, Q5/Q14 class), NOT a claim that PG lacks the
arm. PG has no `GroupPath`-less `Group` analogue goopg needs either: goopg
lowers group-only queries to `*Aggregate`, so every grouping shape below is
an `*Aggregate` shape.

---

## 2. What goopg does today

`buildAggregateStage` (`planner.go:7417`) builds ONE `*Aggregate` (strategy
default `AggStrategyHashed`, the zero value — `plan.go:1356`) over the
finished child, then three mutation rules fire in fixed order:

1. `applyIndexOrderedGroupingRule` — FIRST. enable_hashagg=off + every group
   key a plain column + exact leading btree-prefix match ⇒ swap child for an
   index scan, `Strategy = Sorted`, no Sort. Records `GroupKeyOrder` for the
   EXPLAIN renderer only.
2. `applyPresortedAggregateRule` — internal ORDER BY/DISTINCT aggs ⇒ wrap
   child in Sort on winning pathkeys, grouped ⇒ `Strategy = Sorted`.
3. `applyEnableHashAggRule` — enable_hashagg=off + still hashed + grouped,
   no grouping sets, simple mode ⇒ wrap child in Sort on group keys,
   `Strategy = Sorted`.

Pricing is `DeriveLegacyDisplayCost`'s `*Aggregate` arm: blocking reading
(child total + `cpu_operator × len(Aggs)` per input row) regardless of
strategy. The executor routes on `Strategy` (`openSorted` gate,
`operators_join_agg.go:2222`); parallel `splitAggregate`
(`parallel.go:868`) splits the emitted `*Aggregate` afterwards — its
recognizer reads node type + mode + aggregate-call whitelist
(`parallel_agg.go:113-139`), never cost, so producer-stamped costs cannot
perturb the split decision. Grouping sets fall through all three rules
(each bails) and stay hashed.

---

## 3. The cut: GROUP_AGG producer + `cost_agg` port + `PathAgg` arm, rules retired

### 3.1 Shape (option (b), as C-12)

At the site the three rules run today (child still fresh — a `*SeqScan` /
`*Filter` or a searched subtree), build the aggregate spec ONCE (the same
`*Aggregate` `buildAggregateStage` builds now, strategy left at its default),
then:

```
grouped := fetchUpperRel(reg, UpperGroupAgg, 0, tupleFraction)   // C-11
sizeGroupingRelFromAgg(grouped, aggNode)                          // §3.4
seed    := newPrebuiltPath(grouped, aggNode)                      // C-12's door:
                                                                  // seed.Cost = legacyDisplayCostOf(aggNode);
                                                                  // seed.Rows = Rows sized above;
                                                                  // input rows/costs below read the CHILD estimate,
                                                                  // not seed.Rows (= numGroups)
addGroupingPaths(grouped, seed, aggSpec, cp, flags)               // hashed / sorted / plain
setCheapest(grouped)
best := getCheapestFractionalPath(grouped, tupleFraction)
node, _ = createPlanNode(best)                                    // PathAgg arm → *Aggregate
```

`Path` gains an `AggStrategy AggStrategy` payload field (the `Jointype`
precedent, `path.go:88+` — a path-level attribute set by legality, invisible
to `comparePaths`). No new enum value: ungrouped candidates carry
`AggStrategyHashed`, today's zero value, which is exactly what ungrouped
nodes carry now and what the `"Aggregate"` EXPLAIN label reads
(`operators_explain.go:2340`).

`addGroupingPaths` is the per-input body of `add_paths_to_grouping_rel`
(`planner.c:7114`) for goopg's one input:

- **PLAIN** (no GROUP BY, no grouping sets): `PathAgg{Hashed}` over the
  seed — EXCEPT with usable presorted keys (internal ORDER BY/DISTINCT
  aggs), where it is `PathAgg{Hashed}` over the presorted `Sort`, matching
  both PG (`AGG_PLAIN` over sorted input for ordered aggs) and today's rule
  (`groupagg_presorted.go:164`, pinned by
  `TestPresortedAggregateNonGroupedTiebreak`). Price = `cost_agg` PLAIN arm.
- **HASHED** (grouped, hashable, no ordered aggs): `PathAgg{Hashed}` over
  the seed. `enable_hashagg = off` ⇒ `DisabledNodes++` (B-17a preference
  semantics, as the Sort producer does) — never skipped.
- **SORTED**: the input informed by the two input-shaping rules' surviving
  halves —
  - presorted keys win when ordered aggs are present AND usable (reuse the
    presorted key-selection + FILTER-safety + volatility logic as a pure
    function returning keys-or-absent; hashed arm declined in that case,
    exactly PG's `numOrderedAggs == 0` gate). When ordered aggs are present
    but unusable, SORTED falls back to group-keys-over-`Sort` — ordered
    aggs disable only HASH upstream, never the sort;
  - else group keys, over a `Sort` stacked via `sortPathForBounded` — UNLESS
    the indexorder matcher fires on the fresh child, in which case over the
    rebuilt index scan with NO Sort (reuse `buildIndexOrderedScan` verbatim;
    it already returns the replacement child + ok).
  `PathAgg{Sorted}` over that input path.
- **Grouping sets**: hashed-only. (Corrected §1: PG builds sorted/mixed
  grouping-sets paths too, but goopg's executor cannot run them — values-
  safe residual, ledgered at §5.)
- **Empty pathlist ⇒ explicit `PlanError` "could not implement GROUP BY"**
  (PG's `create_ordinary_grouping_paths:4144` ereport, not a panic): ordered
  aggs with unusable presorted keys fall back to group-keys Sort per the
  rule above, so GUC-off alone can never empty the list. The genuinely
  empty shapes are unhashable input with no sorted fallback.
  `setCheapest` leaves nils and `createPlanNode(nil)` returns
  `(nil, nil)` today — the producer must refuse BEFORE that, with the error
  naming the GROUP BY.

`createPlanNode`'s `PathAgg` arm builds the `*Aggregate` from the seed
node's spec — GroupExprs, Aggs, GroupingSets, Mode, Passthrough,
GroupingMasks, GroupKeyOrder, `pos`, schema — over `createPlanNode(input)`
with the path's Strategy. One construction site stays
(`buildAggregateStage`); the arm copies. The B-01c
`stampAggregateInputTarget` call stays where it is (it reads the emitted
node, strategy-independent; the construction stamp is recomputed after the
arm returns, ordered after any index-narrowing remap exactly as
`planner.go:1559` orders it today).

### 3.2 What "retire" means per rule (no silent absorption)

- **hashagg bridge**: DELETED. Its outcome (`enable_hashagg = off` ⇒ sorted
  wins) is `DisabledNodes` on the hashed path. Its unit tests migrate to
  producer tests asserting the hashed path carries `disabled=1` and the
  winner is the sorted Sort+GroupAggregate with a real `cost_agg` price.
- **presorted rule**: DELETED as a mutator; its key-selection half
  (`aggregateSortlist`, `aggArgsAllVarConst`/`stripPureRelabel`,
  `pathkeysContainVolatile`, WithinGroup skip, greedy covering with
  target-list-position tiebreak) survives VERBATIM as one pure
  keys-or-absent function, the sorted candidate's key source. Tests
  migrate to producer tests with the same EXPLAIN shapes (including the
  non-grouped Sort-preserving case of §3.1 PLAIN).
- **indexorder rule**: DELETED as a mutator INCLUDING its `enable_hashagg`
  proxy gate (`groupagg_indexorder.go:83-85` — the gate lives in the rule,
  not in `buildIndexOrderedScan`, which is GUC-free and survives verbatim
  as the sorted candidate's no-Sort input builder). Deleting the proxy is
  the point (price competition replaces it), and it exposes the one shape
  the proxy was papering over: GUC-ON queries whose group keys are a PK
  leading prefix, where PG picks hash but an uncalibrated index-scan price
  could win the sorted-index candidate. Gate item (not a design branch):
  the regress `aggregates.sql:1275-1370` block plus a GUC-on PK-FD pin
  asserting hash wins — if it fails, the matcher stays GUC-gated for the
  no-Sort variant (Sort-based sorted still competes freely) and the
  failure is a B-15 (index costing) witness, not a C-15 defect.

The `ps` GUC flags (`EnableHashAgg`, `EnablePresortedAggregate`) stay: they
are read by the producer's flag computation, not deleted with the rules.
The `enable_*` B-17 treatment (preference, not skip) is what makes that
safe. (`presortedAggEnabled`/`hashAggEnabled` process seeds remain as the
no-session defaults per take2 P2-02c.)

### 3.3 `cost_agg` port (`costAgg`, `cost_funcs.go` beside `costSortRun`)

Signature: `costAgg(cp, strategy, inputRows, inputStartup, inputTotal,
numGroupCols, numGroups, nAggs, entryBytes, memLimit) Cost` where the last
two serve the spill arm. Arms transcribed from §1 with goopg's existing
currency:

- trans/final terms per F3 (flat `cpu_operator`, zero startup).
- Grouping comparisons `cpu_operator × numGroupCols` per input tuple (both
  SORTED and HASHED, as upstream).
- Output tuples: 1 (PLAIN) else `numGroups`; `cpu_tuple` per output tuple.
- SORTED startup = input startup (streams); HASHED startup = input total
  (blocking). The note in `costsize.c:2720-2732` becomes a unit test: with
  identical CPU terms, sorted-wins-iff-input-ordered falls out of startup
  alone — pin it rather than trust it.
- **Spill arm**: OMITTED — deliberately, not deferred (gate-measured,
  2026-09-06). PG's arm charges batches the executor actually writes;
  goopg's `aggregateOp` "performs grouped aggregation in memory"
  (`operators_join_agg.go:1973`) with no spill path at all, so every spill
  page charged is I/O that never happens. With the arm in, the gate drove
  Q3/Q10/Q13/Q18 to sorted (Q13 5.67 s → 8.71 s), all four away from PG's
  hash — the §5 negative firing as written. The signature keeps input-side
  width parameters so the resume does not re-plumb callers. Resume WITH
  executor spill support, pricing the batches the executor really writes
  (the JOIN spill currency in `spillPages`, not PG's depth loop — goopg
  has no recursive hash partitioning); per-transition-state space stays
  ignored (no trans-state width model). A pinned test asserts the omission
  (`TestCostAggHashedNeverChargesSpill`), so the resume cannot land
  silently — it must delete that test loudly.

`numGroups` ← `estimateNumGroups(GroupExprs, child, inputRows)`; input
cost/rows ← the seed's legacy stamp (C-12's door, same monotonicity
argument); `numGroupCols ← len(GroupExprs)`; `nAggs ← len(Aggs)`.
The kept input-side width parameters are the spill arm's future inputs.
`PlanRows float64 → int64` at the boundary; pinned-regime staleness applies
as elsewhere.

### 3.4 Sizing the GROUP_AGG rel (`sizeGroupingRelFromAgg`)

`Rows ← max(1, estimateNumGroups(...))`, `Width ←
nodeTupleWidth(aggNode.Output())`, `NCols ← len(aggNode.Output())`,
`AvgVarBytes ← nodeAvgVarBytes(...)` — the §4.3 duty, stated here so the
review checks it. (No spill tripwire here: with no spill arm, NCols sizes
only the hypothetical resume, and the omission test pins that.) (The
volatile-`GROUP BY`-expression gap ledgered at `cardinality.go:1236-1239`
rides along unchanged — `estimateNumGroups` is read, not fixed, here.)
PG's `GroupPath` arm (GROUP BY without aggregates) needs no analogue:
goopg lowers group-only queries to `*Aggregate`, and HAVING needs none
either — goopg's HAVING is a separate `Filter` above the node
(`planner.go:1530-1532`), so no HAVING-qual costing belongs in `costAgg`.
Degenerate grouping in PG's sense (HAVING and/or grouping sets present,
no aggs, no GROUP BY): probe at implementation whether
`buildAggregateStage` ever builds such a node; if it cannot, no arm.

### 3.5 Explicitly out of scope (filed, not absorbed)

- **Partial aggregation paths** (C-19g/P5-07): the producer builds the FINAL
  grouped rel only. `splitAggregate` keeps working on the emitted
  `*Aggregate` (same shape, same fields — its recognizer switches on node
  type and mode, never on cost). take3 08 makes P5-07 depend on P4-06; this
  cut is the API it builds against, not its implementation.
- **Partitionwise aggregation**: goopg has no partitioned rels; the flag
  computation is omitted, not stubbed.
- **Degenerate grouping** (PG's `is_degenerate_grouping`: HAVING and/or
  grouping sets present, no aggregates, no GROUP BY): probe during
  implementation whether `buildAggregateStage` ever builds such a node; if
  it cannot, no arm is needed. Gate item, not a design branch.
- **Ordered-set aggregates** (`WITHIN GROUP`): presorted selection already
  skips them; hashed declined with ordered aggs present (PG's
  `numOrderedAggs` gate) — unchanged semantics.
- **Incremental Sort input** (C-14): the sorted candidate stacks a full
  Sort; no partial-prefix matching (the indexorder rule's own out-of-scope
  line, kept).

---

## 4. What is provably unchanged vs what may move

Unchanged by construction: the emitted `*Aggregate` spec (same GroupExprs/
Aggs/GroupingSets/Mode/Passthrough — the arm copies, never re-derives);
`MaybeAddGather` neutrality (same argument as C-12 §5.3 — Aggregate node
shape untouched; re-verify the three functions at implementation time);
`splitAggregate` recognition (node type + mode); C-10c placement (the
producer's Sort sits below the Aggregate exactly where the rules' Sorts
sat — the C-12 re-assert test's one-node-up analogue is a gate item);
values (tuple production untouched).

May move, legitimately: **strategy picks**. Anywhere the rules forced an
outcome that `cost_agg` prices differently, the winner changes — expected
direction is TOWARD the PG oracle (take3 08's P4 exit: aggregation-strategy
diffs strictly decrease), measured against the A-04 plan-parity baseline
(`analysis/planner-refactor-take3/a04-baseline-20260905/README.md`,
per-query category counts before/after — step 8 names the exact command).
Ledgered exception: grouping-sets queries (TPC-DS Q5/Q14 class — PG answers
`MixedAggregate`/`GroupAggregate`, goopg stays hashed for lack of an
executor strategy) keep their residual diffs; they must not increase.
Concretely at risk: queries where `enable_hashagg` defaults picked hash but
sort is cheaper (small groups over ordered input), and the indexorder-proxy
queries (which required `enable_hashagg = off` to fire at all). The gate
reads every such move line by line; a move AWAY from PG is a defect in the
port, not "the model disagrees".

What moves, precisely — and what does not. The `*Aggregate` node carries
no PlanCost (no `planCostSetter`: EXPLAIN recomputes the legacy display
number from the child), so `cost_agg` SELECTS but never displays: an
Aggregate EXPLAIN line moves only as a rollup of a moved child. With no
spill arm, hashed winners keep byte-identical subtrees (same child, same
legacy recompute — no move at all); sorted winners show the new Sort input
line (legacy bare-`&Sort` rewrite → `cost_sort`-priced, the same move C-12
made for ORDER BY sorts) plus rollups. `make plan-gate` needs an in-commit
re-pin with the diff attributed Sort-style: every changed line must be a
Sort-input line or its rollup, never a strategy flip away from PG.

---

## 5. Gate (take3 09 §5 P4) and negative results in advance

| step | instrument | pass condition |
|---|---|---|
| 1 | optimizer + executor suites | green (migrated rule tests assert same shapes WITH prices) |
| 2 | `RALPH_PRECOMMIT_SCOPE=units` | exit 0 |
| 3 | plan-gate structural | diff vs pre-cut capture read line by line; every shape move a strategy pick, every one toward PG |
| 4 | plan-gate costs | re-pinned in-commit; every changed line Aggregate or rollup |
| 5 | `tpch-runner -digest` + `-diff` vs pre-cut binary | VERDICT: PASS |
| 6 | TPC-DS SF0.5 sweep | PASS=95 MISMATCH=0 CKMISMATCH=0; TOTAL within ±17% (the suite-timing band, 09 §6: suite claims on totals, per-query only above band or on repeats) |
| 7 | timing | T on every strategy-moved query (bar B2, 1.2×); ten longest A/A both suites (hold server age constant — sweep-tail collapse mimics regression, 09 §6) |
| 8 | PP both suites | `scripts/pg-plan-parity-diff.py` vs the A-04 baseline: aggregation-strategy diffs do not increase (grouping-sets residuals ledgered, §4); other categories unchanged |

Negative results, stated in advance (one fired during the gate — recorded,
not removed):

- *A strategy move AWAY from PG.* The port misprices; fix the arm, do not
  re-add a rule to force it back. FIRED 2026-09-06: the spill arm as first
  written drove Q3/Q10/Q13/Q18 to sorted (Q13 5.67 s → 8.71 s), all four
  away from PG's hash — because the executor has no hash-agg spill path,
  so every charged spill page was fiction. Resolution: the arm was DELETED
  (see §3.3), not tuned; the omission is pinned by test.
- *`set_cheapest` keeps two paths and the rescan-visible EXPLAIN flaps.*
  Single candidate per input shape by construction (one hashed, one sorted,
  one plain max) — if the pathlist holds more, the producer over-generates.
- *Timing regresses with no shape change.* Not C-15 (same executor work,
  modulo strategy picks which ARE shape changes). Re-run the baseline
  (late-session drift ~1.7% documented).

---

## 6. Scope estimate

**~350–500 LOC production + ~300 test LOC.** Producer + arm (~150), `costAgg`
(no spill — the arm was deleted after the gate fired the §5 negative),
rule deletions (~-800 gross, the actual retirement), migrated tests.
Estimate, per the grep-oversizing lesson: make the change and run
`go build`.

## 7. C-10c re-assert table (per-item duty, C-10c)

| PG equivalent | goopg site | assertion |
|---|---|---|
| `create_grouping_paths` sorted input Sort | producer's `sortPathForBounded` below `*Aggregate` | `pushSingleSideQualsIntoInnerJoinInputs` `*Sort` arm still descends (mirror of C-12's `TestC10cPreservedSideQualMovesThroughOrderedSortArm`, one node up) |
| `adjust_group_pathkeys_for_groupagg` sort | presorted key function reused, not re-derived | FILTER-safety + volatility guards move WITH the keys (same function, new caller) |
| PlaceHolderVar target guard | none exists (C-10c finding) | producer never evaluates below the link: Sort child is the link's own preserved input; no new evaluation site introduced |

---

*End of C-15 design. Implementation starts after agent review (APPROVE),
committed (`-n`) and pushed before code.*
