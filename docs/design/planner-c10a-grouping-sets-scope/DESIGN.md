# C-10a (P4-00a) — grouping-sets representation and strategy scope

Status: accepted (two decisions + one executor pin; deliberately **not** a
refactor).
Filed by `docs/design/not_ralph/minimize_datum/TODO_ALL.md` C-10a, scoped by
`analysis/planner-refactor-take3/c10-p400-scoping-20260905/README.md` §1.

C-10a's cardinality half landed 2026-09-05 (`estimateAggregate` now sums
`estimateNumGroups` over the grouping sets, `internal/optimizer/cardinality.go:1087-1137`).
This document is the SCOPE half: the two decisions the item also owns.

Reads: `docs/design/planner-c10c-upper-qual-placement/DESIGN.md` (the sibling
contract doc), take3 `08-target-design.md` §7.

Oracle: PG 18.3 under `./postgres/` (read-only). PG line numbers below are
`global -x` hits in that tree; goopg line numbers are HEAD of
`plan-narrowing-and-etc` on 2026-09-06.

---

## 1. What this item is for

Phase 4 turns the upper planner into paths. Two of its items have to make a
decision about grouping sets *before they can be written*, and neither has
anywhere to look it up:

- **C-11** (P4-02 upper `RelOptInfo`s) fixes the struct that `GROUP_AGG`
  paths hang off. Whether that struct carries PG's rollup list or goopg's
  flat `Aggregate.GroupingSets [][]int` is a struct decision, so it must be
  made before the struct exists, not after.
- **C-15** (P4-06 `create_grouping_paths`) states as its deliverable "retire
  the three aggregate rules" (`TODO_ALL.md:760-762`). Those rules are three of
  the four things currently keeping grouping sets on the only strategy the
  executor implements. Retiring them without deciding what replaces the guard
  is the item's whole risk.

This document answers both, with the evidence, and pins the load-bearing
executor property in `internal/executor/grouping_sets_strategy_gate_test.go`.

## 2. What goopg has today

### 2.1 The representation

`GROUPING SETS` / `ROLLUP` / `CUBE` is lowered to **one** `Aggregate` node
(`internal/optimizer/groupingsets.go:1-31` records why, and that the retired
alternative was an N-branch `UNION ALL`). The node carries:

| field | site | content |
|---|---|---|
| `GroupExprs` | `plan.go:1240` | the **deduplicated union** of every set's expressions, first-mention order — PG's `parse->groupClause`. Built by `prepareGroupingSets` (`groupingsets.go:48-73`). |
| `GroupingSets [][]int` | `plan.go:1256-1268` | one entry per set, each an **ascending** list of indices into `GroupExprs`. Built by `groupingSetIndices` (`groupingsets.go:92-122`). The empty set is an empty — not nil — slice. |
| `GroupingMasks [][]int64` | `plan.go:1269-1283` | one entry per distinct `GROUPING(...)` call, indexed by set, materialised as an output column. |
| `Strategy` | `plan.go:1284-1291` | `AggStrategyHashed` (zero value) or `AggStrategySorted`. |

`GroupingSets [][]int` is exactly PG's `RollupData.gsets` — a list of integer
index lists into a group clause — with the rollup grouping level removed. That
one missing level is the whole of Decision 1.

### 2.2 The four declines

| site | condition | effect |
|---|---|---|
| `internal/optimizer/groupagg_hashagg.go:60-67` | `aggNode.GroupingSets == nil` is one conjunct of the guard at `:64` | `enable_hashagg=off` bridge never forces `AggStrategySorted` |
| `internal/optimizer/groupagg_presorted.go:45-48` | `if !ps.EnablePresortedAggregate \|\| aggNode.GroupingSets != nil` (`:47`) | `adjust_group_pathkeys_for_groupagg` port never fires |
| `internal/optimizer/groupagg_indexorder.go:63-70` | `aggNode.GroupingSets != nil` (`:67`) | index-ordered grouping input never fires |
| `internal/optimizer/parallel_agg.go:113-124` | `a.GroupingSets != nil` | the node is never split across a parallel boundary |

The first three run in a fixed order from `planner.go:1521 / :1537 / :1546`,
around the landed compute-only `stampAggregateInputTarget(agg.node, nil)` at
`planner.go:1527`. The fourth is the parallel gate.

The `parallel_agg.go` decline is the only one of the four that is **also**
PG's behaviour: `consider_groupingsets_paths` is reached only from the
non-partial grouping path (`postgres/src/backend/optimizer/plan/planner.c:7167`,
`:7280`), so PG likewise never makes a grouping-sets `Agg` parallel-aware.
C-15 should not retire it; the other three are what this document is about.

### 2.3 The execution

`aggregateOp.Open` (`internal/executor/operators_join_agg.go:2091`) routes to
`openSorted` only when

```
Strategy == AggStrategySorted && GroupingSets == nil &&
len(GroupExprs) > 0 && Mode == AggModeSimple
```

Everything else takes the hash path, which:

1. unifies the plain and grouping-sets cases by giving a plain aggregate one
   implicit set holding every column (`:2100-2112`) — the same unification
   `nodeAgg.c` makes with `numsets == 1`;
2. evaluates every grouping column **once per input row** and cuts each set's
   key out of that one vector (`:2151-2156`) — one pass over the child;
3. routes each row into every set's group. The plan.go comment says "one hash
   table per set"; the implementation is **one** Go map whose key is prefixed
   with the set index when `len(sets) > 1` (`setGroupKey`, `:2376-2392`).
   Same memory characteristic — every level's groups co-resident — but worth
   stating precisely, because the difference matters to anyone costing a
   spill;
4. **sorts the output** per set index, then by group key (`:2298-2338`), which
   the comment records was chosen to reproduce the retired UNION-ALL row
   order. This is the fact that reverses Decision 2's naive time argument
   (§4.5).

`openSorted`'s own invariant comment (`:2538-2540`) states that Open only
routes there with `GroupingSets == nil`, "so there is exactly one implicit
grouping set holding every group column and setIdx is always 0" — and the body
keeps exactly one `cur *groupRuntime` with `setIdx: 0` (`:2547-2548`, `:2606`).
**Verified**: the scoping doc's claim about this comment is accurate.

### 2.4 The costing

`aggCost` (`internal/optimizer/cost_funcs.go:594`) implements only the
AGG_HASHED arm of `cost_agg`, has no sorted arm and no spill arm, and has
**no production caller** — its only reference is
`internal/optimizer/cost_funcs_test.go:205`. `PathAgg`
(`internal/optimizer/path.go:58`) is a declared-but-unproduced `PathKind`: its
only other mention is a decline arm in `narrowoutput.go:346-348`, and
`createplan.go` has no `case PathAgg`. Verified by grep; this confirms the
scoping doc.

## 3. Decision 1 — the flat list stays; `GROUP_AGG` carries no rollup list

### 3.1 What PG's rollup list is for

PG models grouping sets as a **list on one path**, not as N paths:
`GroupingSetsPath.rollups` is a `List *` of `RollupData`
(`postgres/src/include/nodes/pathnodes.h:2436-2445`), and a `RollupData`
(`:2419-2430`) is

```c
List       *groupClause;   /* applicable subset of parse->groupClause */
List       *gsets;         /* lists of integer indexes into groupClause */
List       *gsets_data;    /* list of GroupingSetData */
Cardinality numGroups;
bool        hashable;
bool        is_hashed;     /* to be implemented as a hashagg */
```

A rollup is **the set of levels computable from one sort order**. That is the
unit `nodeAgg.c` calls a *phase* (`nodeAgg.c:124-131`: "we break them down into
phases, where each phase has a different sort order… during each phase but the
last, the input tuples are additionally stored in a tuplesort which is keyed to
the next phase's sort order"). `extract_rollup_sets`
(`plan/planner.c:2924`, called from `preprocess_grouping_sets`, `:2182`) is
what partitions a flat list of sets into rollups.

So PG's rollup list exists to answer exactly two questions: *how many sorts do
I need, and which levels ride on each*, and *which of them is hashed rather
than sorted* (`is_hashed`).

### 3.2 What is derivable from goopg's flat form

Everything in `RollupData` except `is_hashed`:

| `RollupData` field | derivable from goopg's flat form? |
|---|---|
| `gsets` | **is** goopg's `GroupingSets [][]int` (same shape, same ascending-index convention — `groupingsets.go:92-122`) |
| `groupClause` | the projection of `GroupExprs` through one set's index list |
| `gsets_data` | annotation only |
| `numGroups` | already computed per set by the landed `estimateAggregate` (`cardinality.go:1109-1124`), which is `get_number_of_groups`' `dNumGroups += rollup->numGroups` accumulation (`planner.c:3704`) |
| `hashable` / the rollup **partition** itself | `extract_rollup_sets` is a self-contained algorithm over the set list. It needs no planner state the flat form has discarded. |
| `is_hashed` | **not derivable** — it is a chosen strategy, not a property of the sets |

### 3.3 The one thing the flat form cannot express

A **mixed** strategy. `is_hashed` per rollup is what `AGG_MIXED` is: PG builds
the mixed path by marking some rollups hashed and leaving one sorted
(`planner.c:4320` and `:4333` — `strat = AGG_MIXED` — and the knapsack at
`:4371-4466` that chooses which rollups to hash within `hash_mem`).

goopg's flat `[][]int` has no per-set strategy slot at all. But — and this is
the finding that decides it — **a per-set strategy slot is not a rollup list.**
A parallel `[]AggStrategy` (or a hashed-set bitmask) beside `GroupingSets`
expresses `is_hashed` at the granularity goopg's executor actually consumes:
the executor's unit is the *set*, not the rollup (`setGroupKey`,
`operators_join_agg.go:2376-2392`, keys on the set index). goopg has no phases
and no intra-operator tuplesort, so the rollup — whose entire purpose is to be
the unit of one sort order — currently has **no consumer** on either side.

### 3.4 What each choice costs C-11

- **Flat.** C-11 adds nothing. The `GROUP_AGG` rel's grouping path carries the
  `[][]int` it already has, exactly as `Aggregate` does. Zero bytes on `Path`.
- **Rollup list.** `Path` is one flat struct, deliberately: "kept deliberately
  small — thousands are allocated per join search — with kind-specific data in
  a narrow payload rather than a fat struct" (`internal/optimizer/path.go:82-85`).
  PG can afford `GroupingSetsPath` as a *separate node type* beside `AggPath`
  (`pathnodes.h:2394` / `:2436`); goopg has one `Path` for every kind. A
  `Rollups []*RollupData` field costs a slice header on every path in every
  join search, for a payload used by at most one path per statement — and it
  introduces a **second representation of the grouping sets** that
  `Aggregate.GroupingSets` must be kept in agreement with across
  `createPlanNode`. That is the repo's recurring silent-bug shape (encode/decode,
  fast-path/interpreted evaluator, column-lookup/star-expansion).

### 3.5 Recommendation

**Keep the flat `GroupingSets [][]int`.** C-11 gives `GROUP_AGG` no rollup
list. If and when a second strategy ships, add a per-set strategy tag beside
the existing list; derive the rollup partition on demand with a port of
`extract_rollup_sets` at the point of use.

- **Cost of being wrong** (C-15 later ships a genuinely mixed arm): one
  derivation function (`extract_rollup_sets`, ~80 lines, no planner state
  needed) plus a per-set strategy slice. Additive, localised to the grouping
  path producer, and it cannot silently mis-answer — a wrong rollup partition
  produces a wrong *sort requirement*, which fails loudly in the executor
  rather than returning wrong rows.
- **Cost of the other error** (rollup list adopted, C-15 pins to hashed per
  Decision 2): a permanently-unused struct on the hot `Path` type, plus a
  sibling-representation invariant with no consumer to keep it honest.
- **Reversibility: high, and asymmetric in favour of flat.** The flat form
  discards nothing — the rollup partition is a pure function of the set list.
  The reverse is not true in the same way: once a rollup list is the carrier,
  every producer and consumer is written against it.

**Stated uncertainty.** This rests on the claim that goopg will never want
PG's *phase* mechanism (multi-rollup with inter-phase re-sorts). §4.3 shows
the measured corpus never needs it — every grouping-sets query in TPC-DS is a
single `ROLLUP`, and TPC-H has none — but that is a corpus argument, not a
proof about SQL. A hand-written `GROUPING SETS ((a,b),(c,d))` needs two sort
orders and would want phases. The flat form does not *prevent* phases; it just
does not pre-build for them.

## 4. Decision 2 — C-15 pins grouping sets to AGG_HASHED

### 4.1 What retiring the three declines actually does — verified

The scoping doc frames the declines as "the guard". They are **a** guard.
There is a second, independent one, and it is the one that matters.

If C-15 retires the three planner declines with no grouping-sets arm, a
grouping-sets `Aggregate` can be stamped `AggStrategySorted` and given a child
`Sort`. The executor then re-tests `GroupingSets == nil` at
`operators_join_agg.go:2091` and **still takes the hash path**. The result is:

- correct rows (the hash path computes every level regardless of the stamp);
- a redundant `Sort` under a node that does not consume sortedness;
- an EXPLAIN label unchanged, because `operators_explain.go:2321-2331` prints
  `HashAggregate (N keys, M grouping sets)` from `GroupingSets` **before** it
  ever looks at `Strategy`.

So the failure mode of a careless C-15 is a **silent pessimisation**, not a
wrong answer. The wrong answer only arrives if the executor gate at `:2091` is
also removed, because `openSorted` keeps one current group at `setIdx 0`
(`:2538-2540`, `:2547-2548`) and would emit one level's rows instead of N.

This is pinned by `internal/executor/grouping_sets_strategy_gate_test.go` (§6).

**This changed the shape of the decision.** "Retire the declines" is not a
correctness cliff; it is a requirement to *move* the guard rather than delete
it. C-15's obligation is therefore narrow and statable: its grouping-path
producer must emit only a hashed path when `GroupingSets != nil`, and the
executor gate at `:2091` must stay until an arm exists behind it.

### 4.2 What a grouping-sets arm would require in the executor

`nodeAgg.c:113-137` names three separable mechanisms:

1. **Concurrent per-level transition state, single sort order** (`:115-122`):
   "a list of grouping sets which is structurally equivalent to a ROLLUP
   clause… can be processed in a single pass over ordered data… keeping a
   separate set of transition values for each grouping set… on group
   boundaries we reset those states (starting at the front of the list) whose
   grouping values have changed."
   goopg's `openSorted` has this structure for **one** level. Generalising it
   to a prefix chain is a bounded change: a `[]*groupRuntime` instead of one
   `cur`, and a boundary test that finds the longest matching prefix. It reuses
   `finalizeGroup` verbatim, which is already shared by both strategies
   (`operators_join_agg.go:2394-2399`).
2. **Phases with an inter-phase tuplesort** (`:124-131`), for more than one
   rollup. goopg has **no counterpart at all** — the `Aggregate` node has no
   phase concept and the operator contains no sort.
3. **AGG_MIXED** (`:133-137`): "populates the hashtables during the first
   sorted phase, and switches to reading them out after completing all sort
   phases." Depends on (1) and the hash path coexisting in one operator.

The honest ordering: **(1) alone is much cheaper than "a grouping-sets arm"**,
and (1) alone covers 8/8 of the measured corpus (§4.3). (2) and (3) are the
expensive part and nothing in the corpus needs (2).

### 4.3 The corpus

**Correction to the scoping doc.** It says "12 of 100 TPC-DS queries use
grouping sets". The shipped corpus is
`bench/tpcds/runtime_goopg/tpcds-data/queries/`, which holds `query1.sql` …
`query99.sql` **plus a junk `query_0.sql`** (4 866 lines of concatenated
fragments; it contains 11 `rollup` occurrences, which is where the twelfth
came from). Grepping `query1..99` for `rollup` / `cube(` / `grouping sets` /
`grouping(`:

**11 of 99: Q5, Q14, Q18, Q22, Q27, Q36, Q67, Q70, Q77, Q80, Q86.**

Of those, **Q36, Q70 and Q86 are `SKIP (oracle: SKIP_QUERYGEN)`** — dsqgen
artefacts that fail on PG too (`bench/tpcds/plans-pg/Q36.txt` and the sweep,
`scripts/tpcds-sf05-regression.sh:28`). So the **measurable corpus is 8**:
Q5, Q14, Q18, Q22, Q27, Q67, Q77, Q80.

Two facts about their shape:

- **Every one is a single `ROLLUP(...)`.** No `CUBE`, no multi-unit
  `GROUPING SETS` list. Verified by reading each `group by` clause. A single
  rollup is exactly PG's one-`RollupData` case — mechanism (1) above, no
  phases.
- **TPC-H uses grouping sets in zero of 22 queries** (no `rollup`/`cube`/
  `grouping sets` in `bench/tpch/plans-pg/`).

Rollup widths: Q5/Q77/Q80 are `rollup(channel, id)` (3 levels); Q27
`rollup(i_item_id, s_state)`; Q36/Q86 `rollup(i_category,i_class)`; Q70
`rollup(s_state,s_county)`; Q14 `rollup(channel, i_brand_id, i_class_id,
i_category_id)` (5 levels); Q18 `rollup(i_item_id, ca_country, ca_state,
ca_county)`; Q22 `rollup(i_product_name, i_brand, i_class, i_category)`;
Q67 `rollup(i_category, i_class, i_brand, i_product_name, d_year, d_qoy,
d_moy, s_store_id)` — **9 levels**.

### 4.4 What PG actually picks — 8/8 not hashed

From the committed SF0.5 fixtures (`bench/tpcds/plans-pg/`, captured against
`127.0.0.1:65438` db `tpcds05`):

| query | PG's grouping-sets node | goopg's (`plans-20260906-021252.txt`) |
|---|---|---|
| Q5 | `MixedAggregate` (`Q5.txt:4-7`: 2 `Hash Key:` + `Group Key: ()`) | `HashAggregate (2 keys, 3 grouping sets)` |
| Q14 | `GroupAggregate` (`Q14.txt:69-74`, 5 `Group Key:` lines) | `HashAggregate (4 keys, 5 grouping sets)` |
| Q18 | `GroupAggregate` (`Q18.txt:4-9`) | `HashAggregate (4 keys, 5 grouping sets)` |
| Q22 | `GroupAggregate` (`Q22.txt:4-9`) | `HashAggregate (4 keys, 5 grouping sets)` |
| Q27 | `GroupAggregate` (`Q27.txt:4-7`) | `HashAggregate (2 keys, 3 grouping sets)` |
| Q67 | `GroupAggregate` (`Q67.txt:11-20`, 9 `Group Key:` lines) | `HashAggregate (8 keys, 9 grouping sets)` |
| Q77 | `MixedAggregate` (`Q77.txt:4-7`) | `HashAggregate (2 keys, 3 grouping sets)` |
| Q80 | `MixedAggregate` (`Q80.txt:4-7`) | `HashAggregate (2 keys, 3 grouping sets)` |

**PG never picks a pure `HashAggregate` for a grouping-sets node on this
corpus.** 5 sorted, 3 mixed, 0 hashed.

The three `MixedAggregate`s are all the same shape and it is worth naming why,
because it is not the knapsack: a `ROLLUP` always contains the empty set, and
"empty grouping sets can't be hashed" (`planner.c:4282-4284`), so when PG takes
the not-sorted branch it bursts the rollup into individually-hashed sets and
puts the empty set into a non-hashed rollup, which sets `strat = AGG_MIXED` at
`:4333`. **Every hashed grouping-sets plan PG can produce for a `ROLLUP` is a
`MixedAggregate`, never a `HashAggregate`.** goopg's label can therefore never
match PG on any `ROLLUP` query, whatever strategy it picks — see §5.

The 5 `GroupAggregate`s are the sorted branch (`planner.c:4506-4515`): one
sort, all levels in one pass.

### 4.5 Why 8/8 is not the time argument it looks like

The naive reading is "goopg does the wrong thing on 8/8, therefore it is
slower on 8/8". That is wrong, and the reason is §2.3 item 4.

goopg's hashed path is **not** PG's `AGG_HASHED`. It hashes and then **sorts
its own output**, per set then by key (`operators_join_agg.go:2298-2338`). PG's
`AGG_SORTED` sorts the **input** — N rows. goopg sorts the **output** — G rows,
the total group count across all levels.

G is *not* bounded by N: for k sets it is bounded by k·N, and the pin's own
fixture is a counterexample to the tighter bound (4 input rows, 6 output rows —
§7). But the levels of a `ROLLUP` are prefixes, so each coarser level has no
more groups than the finest one, and G is in practice a small multiple of the
finest level's group count. On every corpus query that count is far below the
input: PG's own per-level estimates for Q22 sum to 71 857 against a 22 503-row
input only because goopg and PG disagree wildly about that input (below), and
Q67's nine levels estimate 34 106 against a much larger scan.

So for a rollup where the finest level's groups ≪ input — which is every
grouping-sets query in the corpus — goopg's shape does *less* sorting than the
plan PG chose, and produces the same row order. That is a claim about this
corpus, not a theorem: a rollup whose finest level is nearly one-group-per-row
inverts it.

What the pin actually costs is therefore **memory, not time**: all levels'
groups are co-resident in one un-spillable Go map, and goopg has no analogue of
`estimate_hashagg_tablesize` / `hash_mem_limit`, which is precisely the guard
PG consults before offering a hashed grouping-sets path at all
(`planner.c:4237-4247`: `if (hashsize > hash_mem_limit && gd->rollups) return;`).
Q22 (`ROLLUP` led by `i_product_name`) and Q67 (9 levels, led by a 4-column
prefix ending in `i_product_name`) are the two candidates for that to bite.

**Measured, at SF0.5** (`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260906-021252.txt`):
all 8 PASS — Q5 17 s, Q14 100 s, Q18 27 s, Q22 10 s, Q27 9 s, Q67 10 s,
Q77 14 s, Q80 15 s. No timeout, no mismatch. The pin costs nothing measurable
at SF0.5 today.

**Not measured**: SF=1 memory behaviour of Q22 and Q67. This is the honest gap
in the recommendation, and §4.6 makes it a condition rather than hiding it.

One estimate observation, stated so it is not mistaken for a grouping-sets
problem later: goopg prices Q22's grouping node at 4 710 000 rows against PG's
71 857. That is **not** the C-10a sum-over-sets fix over-firing — it is the
clamp to goopg's own input estimate, and goopg's input estimate for that join
is 4 710 000 against PG's 22 503 (`plans-20260906-021252.txt` Q22 vs
`bench/tpcds/plans-pg/Q22.txt:10-18`). The join estimate is 200× off; the
aggregate then clamps to it. Fixing the aggregate would not move it.

### 4.6 Recommendation

**C-15 ships no grouping-sets arm. Grouping sets pin to AGG_HASHED as a
measured permitted divergence** — subject to three conditions that are part of
the decision, not caveats on it:

1. **C-15 moves the guard, it does not delete it.** The three declines
   (`groupagg_hashagg.go:64`, `groupagg_presorted.go:47`,
   `groupagg_indexorder.go:67`) are retired *into* C-15's grouping-path
   producer: when `GroupingSets != nil`, produce a hashed path only. The
   fourth decline (`parallel_agg.go:117`) is PG's own behaviour and stays.
   The executor gate at `operators_join_agg.go:2091` stays until an arm exists
   behind it. Deleting the planner declines while leaving the executor gate is
   what buys a redundant `Sort` for nothing (§4.1).
2. **The pin is conditional on a memory measurement that has never been run**:
   Q22 and Q67 at SF=1 under the cgroup cap, watching RSS across the aggregate.
   If either blows up, the fix is mechanism (1) of §4.2 — a single-rollup
   `AGG_SORTED` arm — and it becomes an item. Nothing in this document argues
   that arm is not worth building; it argues it is not C-15's job to build it
   blind.
3. **The divergence is recorded as `aggregation-strategy` at 8/8**, and the
   parity tool must first be taught the two labels, because today it cannot
   see this category at all (§5.2). Without that, the take3 P4 exit criterion
   ("`aggregation-strategy` / `sort-strategy` diffs strictly decrease",
   `08-target-design.md:435-436`) is being evaluated on a bucket these 8
   queries never enter.

Note that the exit criterion says *strictly decrease*, not *reach zero*, so
the pin does not by itself make Phase 4 unexitable — but it does permanently
fix 8 queries' worth of diff, and that should be a stated floor rather than a
surprise at the exit gate.

**Cost of being wrong.** If the pin is wrong, it is wrong about memory at
scale, and the symptom is an OOM or a GC collapse on Q22/Q67 at SF=1 — loud,
attributable, and already the shape the repo's benchmark hygiene notes warn
about. It is not a wrong-answer risk: §4.1's second gate holds, and §6 pins it.

## 5. The EXPLAIN divergence

### 5.1 Already ledgered — cited, not duplicated

Two rows in `.ralph/deferral_ledger.md` already own this:

- **`.ralph/deferral_ledger.md:912` (M0125-0048)** — goopg labels the node
  `HashAggregate (N keys, M grouping sets)`; PG prints one
  `Hash Key:` / `Group Key:` detail line **per set** including the bare
  `Group Key: ()`, and labels the sorted+hashed mix `MixedAggregate`
  (`explain.c show_grouping_sets`). Recorded as a facet of the pre-existing
  detail-line gap: goopg emitted no `Group Key` line for **any** aggregate at
  the time.
- **`.ralph/deferral_ledger.md:1447` (M0134-0001 grouping-sets keys)** — S18
  deliberately left the grouping-sets branch byte-identical; when per-set lines
  are added they must adopt S18's reference-vs-compute `forceParen` rule from
  the outset.

The half of row 912's premise that has since changed: goopg **does** now emit a
`Group Key:` line for ordinary aggregates (`operators_explain.go:867-905`), and
that site explicitly excludes grouping sets — `if len(p.GroupExprs) > 0 &&
p.GroupingSets == nil` at `:874`, with the reason stated at `:867-873`. So the
grouping-sets branch is now the *only* aggregate shape with no key line, which
makes it a smaller, self-contained job than row 912 assumed.

### 5.2 New, and it is the part C-15 will trip over

`scripts/pg-plan-parity-diff.py` — the tool the take3 09 gate uses — **cannot
see this divergence at all.** Verified by driving its own parser:

```
goopg kind: 'HashAggregate (2 keys, 3 grouping sets)'
pg  kind:   'MixedAggregate'
warnings:   ["unknown node kind: 'HashAggregate (2 keys, 3 grouping sets)'",
             "unknown node kind: 'MixedAggregate'"]
mismatch_category(goopg, MixedAggregate)  -> 'join-order'
mismatch_category(goopg, GroupAggregate)  -> 'join-order'
presence_category(either)                 -> None
```

Three separate causes:

- `AGG_KINDS = ("Aggregate", "HashAggregate", "GroupAggregate")`
  (`pg-plan-parity-diff.py:151`) has no `MixedAggregate`, and neither does
  `KNOWN_KINDS` (`:154-183`).
- goopg's label is not a bare kind, so the parser's exact-match list
  (`:340-345`) falls through to `kind = text` (`:348`) and the whole suffixed
  string becomes the kind.
- With neither side in `AGG_KINDS`, `mismatch_category` (`:908-936`) falls all
  the way through to its `return "join-order"` default (`:936`).

So today every grouping-sets strategy divergence is **mis-filed as
`join-order`**, inflating that bucket by 8 and emptying the
`aggregation-strategy` bucket of the only 8 queries that are guaranteed to be
in it. Both directions are wrong, and both are invisible except as an
unknown-kind warning.

**Consequence to record for C-15**: the parity tool needs `MixedAggregate` in
`AGG_KINDS`/`KNOWN_KINDS` and a normalisation of goopg's suffixed label
*before* the P4 exit criterion is read. That is a ~5-line change to the tool,
and it must land before, not with, C-15 — otherwise C-15's own gate cannot
distinguish "I improved aggregation strategy" from "I moved a join".

This is a new finding, not covered by rows 912 or 1447 (both are about the
rendered text; this is about the *consumer* of the rendered text).

## 6. The pin

`internal/executor/grouping_sets_strategy_gate_test.go`, three tests over one
`ROLLUP(k1, k2)` shape (levels `(k1,k2)`, `(k1)`, `()`) over four `Values`
rows:

1. `TestC10aGroupingSetsHashedBaseline` — the control: today's production
   stamp produces all three levels, with the rolled-away dimensions NULL and
   the rows ordered per set then by key.
2. `TestC10aGroupingSetsIgnoreSortedStrategy` — **the pin.** The same node
   stamped `AggStrategySorted` — the state retiring the four declines would
   leave behind — still produces all three levels. Had `Open` routed it to
   `openSorted`, which keeps one current group at `setIdx 0`, this would
   return one level's rows instead of six.
3. `TestC10aGroupingSetsStrategyStampIsInert` — states the consequence
   positively: for a grouping-sets node the `Strategy` field is inert, the two
   stamps produce identical rows. That is what makes "pin grouping sets to
   AGG_HASHED" a plan-label decision rather than an execution one.

**What the pin cannot do.** It exercises the executor's gate at
`operators_join_agg.go:2091` only. If a future change teaches `openSorted`
about grouping sets, test 2 keeps passing (correctly) while telling you
nothing about whether the new arm is right — that arm needs its own tests.
And the pin says nothing about the redundant-`Sort` pessimisation of §4.1,
which is a plan-shape property and belongs to C-15's own gate.

## 7. Out of scope, and what is uncertain

- **The single-rollup `AGG_SORTED` arm is not filed here.** §4.2 sizes it
  (mechanism 1 only, reusing `finalizeGroup`) and §4.6 condition 2 names the
  measurement that would justify filing it. Filing it now would be filing work
  with no measured need.
- **Phases and `AGG_MIXED` (mechanisms 2 and 3) are out of scope and should
  stay out** until a query needs two sort orders. Nothing in TPC-H or TPC-DS
  does.
- **Hash spill.** goopg has no `hash_mem_limit`, no
  `estimate_hashagg_tablesize`, and no spill for any aggregate — grouping sets
  merely make the absence more consequential. C-15's own scope statement
  includes "incl. hash spill arm" (`TODO_ALL.md:760-761`); if that lands, the
  grouping-sets case is where it should be measured first, because it is the
  one shape holding N levels' groups at once.
- **A correction to C-10a's own landed half, found while writing this and
  deliberately not fixed here** (design doc; no production change). The
  cardinality half clamps the summed per-set estimate to the input row count
  (`internal/optimizer/cardinality.go:1129-1136`) and attributes the clamp to
  upstream: "Upstream clamps the accumulated total to the input row count in
  `create_grouping_paths`". **It does not.** `get_number_of_groups`
  (`postgres/src/backend/optimizer/plan/planner.c:3658-3705`) accumulates
  `dNumGroups += rollup->numGroups` with no clamp on the total; the clamp to
  `path_rows` happens inside each per-set `estimate_num_groups` call, not on
  the sum. The sound bound is `k · inputRows` for k sets, not `inputRows`, and
  §6's fixture is a live counterexample to the tighter one: 4 input rows in,
  6 output rows out. In practice the clamp binds on exactly one measured
  corpus query — Q22, where goopg's own input estimate is 200× too high
  anyway — so this is a latent under-statement, not a live one, and the
  landed gate (values 24/24, TPC-DS CKMISMATCH=0) is unaffected. It should be
  re-decided when C-15 first *reads* `estimateAggregate` for a cost, because
  that is when a clamped `dNumGroups` starts choosing a plan.
- **Uncertainty, named.** Decision 1 assumes goopg never wants phases; that is
  a corpus argument (§3.5). Decision 2's condition 2 is an *unrun*
  measurement, and the pin is only justified while it stays unrun-and-unneeded
  at SF0.5 — if someone runs SF=1 and Q22/Q67 are fine, the decision
  strengthens; if they are not, condition 2 fires and the arm becomes an item.
  Neither outcome invalidates Decision 1.
- **No production code changes under this item.** It is two decisions, a
  correction to the corpus count, one new finding about the parity tool, and a
  test file.
