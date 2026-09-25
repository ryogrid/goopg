# M0141-S2 — serial Hashed-vs-Sorted mechanism trace: two causes, neither a quick local fix

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md` M0141-S2 opened with: "per-query trace of the up-to-42
serial-shaped queries S1 named ... to determine, for each, whether goopg's
Sorted `PathAgg` candidate was priced-and-lost or never generated ... land the
fix that trace implies (expected: a cost formula or pathkey-availability gap in
already-shipped machinery, not a new subsystem)". This task traces TPC-H's 10
`aggregation-strategy`-tagged queries (S1's full-serial set) and answers S1's
open question, but the answer **refutes S2's own "not a new subsystem"
expectation** for the majority case — see Finding 2. Per the plan-parity
harness, a task whose trace shows the "fix" is not a bounded local patch stops
short of landing one rather than forcing a shape; this task **files the
correction rather than closing S2 as originally scoped**. No production code
was changed beyond the one-line `plan.go` comment fix S1 had already deferred
here.

## Method

Read the two S1-committed capture files
(`analysis/m0141/m0141-s1-tpch.plans.txt` = goopg,
`analysis/m0141/m0141-s1-tpch.pg.plans.txt` = PG 18.3 reference; **not
re-captured**, per the harness's "way of working" — no new `rNNN` round, no
live server touched) for all 10 TPC-H `aggregation-strategy` queries (Q2, Q3,
Q4, Q5, Q8, Q12, Q13, Q18, Q21, Q22), extracting which of
`HashAggregate`/`GroupAggregate` each engine chose, then reading
`groupingpaths.go`, `cost_funcs.go:costAgg`, and `upperordered.go` to explain
the direction of each mismatch.

## Finding 1 — both candidates are always generated; S1's open question resolves to "(a) priced-and-lost", uniformly

For every one of the 10 queries, the GROUP BY is over plain columns with no
`DISTINCT`/`ORDER BY`/`WITHIN GROUP` aggregate (`groupingHasSpecialAgg` false)
and at least one group column (`groupingHashable` true), so
`addGroupingPaths` always offers **both** a Hashed and a Sorted `PathAgg`
candidate to `addPath` — never "(b) never generated". S1's open question
("why doesn't the contest pick PG's shape") is answered: it always *does* run
a real contest, and PG's shape loses it. But the *reason* it loses splits
into two unrelated mechanisms, not one:

| query | goopg picks | PG picks | mechanism |
|---|---|---|---|
| Q2 | Hashed | (aggregate node outside captured window; not re-classified this task) | — |
| Q3 | **Sorted** | Hashed | (A) — HASHED overpriced |
| Q4 | Hashed | **Sorted** | (B) — SORTED underpriced-relative-to-benefit |
| Q5 | Hashed | Sorted | (B) |
| Q8 | Hashed | Sorted | (B) |
| Q12 | Hashed | Sorted | (B) |
| Q13 | Hashed + GroupAgg (mixed, 2 agg nodes) | Hashed + Hashed | mixed — needs per-node trace, not done this task |
| Q18 | GroupAgg + GroupAgg | Hashed + Hashed | mixed — needs per-node trace, not done this task |
| Q21 | Hashed | Sorted | (B) |
| Q22 | Hashed | Sorted | (B) |

Six of the eight cleanly-classified queries (Q4, Q5, Q8, Q12, Q21, Q22) are
mechanism (B); only Q3 is mechanism (A). **(B) is the dominant lever for this
corpus, not (A).**

## Finding 2 — mechanism (A): Q3, a known, already-decided tradeoff (M0139-gated, not a bug)

Q3's Hash Join child carries every raw column of `lineitem`/`orders`/`customer`
into the GROUP_AGG rel (no `Project`/`attr_needed` narrowing exists yet —
`goopg_optimizer_no_attr_needed_no_ios_path`), so `aggInputWidth`'s
`nodeAvgVarBytes` sums the variable-width footprint of columns the aggregate
never touches (comment fields, etc.), inflating `inAvgVarBytes` well past what
PG's own narrowed input carries (PG's own captured Hash Join for Q3 is
`width=29`; goopg's is `width=176`). `costAgg`'s HASHED arm
(`cost_funcs.go:516-540`, the R3 spill arm) prices a hash table against that
inflated width, and because `hashAggSetLimits`'s early-return-if-it-fits
threshold is width-dependent, the inflated width alone can push a grouping
that would fit in PG's currency into goopg's "does not fit" branch, adding
spill I/O cost to HASHED that SORTED never pays — flipping the contest.

This exact phenomenon, TPC-H flipping to Sorted under the spill arm, is
**already recorded** by `groupagg_hashagg_test.go`'s (actually
`groupingpaths_test.go`'s) `TestCostAggHashedNeverChargesSpill` doc comment:
*"measured, it flipped Q3/Q10/Q13/Q18 to sorted ... Resume WITH executor
spill support"*, and the R3 commit (`cost_funcs.go:463-515`) explicitly
**reinstated** the spill arm anyway, over that exact objection, because
without it TPC-DS emitted `HashAggregate` 133x / `GroupAggregate` 1x against
PG's 29x/100x — a much larger corpus win. **This is not an accidental bug for
S2 to silently patch**: reverting the spill arm to fix Q3 would regress the
TPC-DS win R3 measured as larger. The real fix is the currency gap R120/R124
already named and gated on the K65/K66 ncols-narrowing family
(`cost_funcs.go:509`) — i.e. **M0139** (executor-side narrowing). Re-measure
mechanism (A) once M0139 lands; do not touch the spill arm before then.

## Finding 3 — mechanism (B) is F15/K12(B)/K24's already-named root cause, not a new local gap

Q4 is the cleanest instance. PG's chosen plan is
`GroupAggregate -> Sort -> Nested Loop Semi Join` with **no enclosing top-level
Sort** at all, because `ORDER BY o_orderpriority` is exactly the `GROUP BY`
key — the Sorted aggregate's output pathkeys satisfy the query's required
order for free. goopg's chosen plan is
`Sort -> HashAggregate -> Nested Loop Semi Join`: HashAggregate's output is
unordered, so an enclosing Sort is added over the (tiny, 5-row) aggregated
output.

Read in isolation, goopg's `costAgg` correctly prices this: per
`TestCostAggSortedHashedShareTotalCpu`'s pinned invariant, Sorted and Hashed
share the same total CPU term-for-term, and Sorted wins **only** when its
input is already ordered (no fresh Sort needed) — `addGroupingPaths` always
builds a fresh `sortPathForBounded` when the input isn't already sorted, so
locally, Sorted's `Cost.Total` is HASHED's total **plus** the cost of sorting
the (thousands-of-rows) pre-aggregation input, which is real money. Hashed's
tiny post-aggregation Sort (5 rows) is nearly free. **By local cost alone,
goopg's choice is not wrong** — the saving PG's plan realizes (skipping the
top-level Sort entirely) is invisible at the point the choice is made.

That invisibility is structural, not a missing cost term: `addGroupingPaths`
runs its Hashed-vs-Sorted contest and calls `setCheapest` on the GROUP_AGG
rel, and the caller collapses the winner to a single finished `Node` before
the top-level `ORDER BY` step (`createOrderedPaths`/`addOrderedPaths`,
`upperordered.go:64-128`) ever runs. `addOrderedPaths` takes exactly **one**
`input *Path`, not the GROUP_AGG rel's `Pathlist` — so even though `addPath`'s
own dominance rule should keep both Hashed and Sorted paths when their
pathkeys are pairwise incomparable (`addGroupingPaths`'s own comment: *"addPath
keeps whichever is cheaper (or both, if neither dominates on cost+pathkeys)"*),
by the time `createOrderedPaths` runs, only the GROUP_AGG rel's single
pre-selected cheapest `Node` is available to it — the alternative (here,
Sorted, whose pathkeys would let `addOrderedPaths` skip the Sort entirely via
its `pathkeysContainedIn` check at line 123) was already discarded upstream.
**The joint comparison PG makes — "Sorted-agg with no top Sort" total vs
"Hashed-agg plus top Sort" total — never happens; only the pre-collapsed
winner's local cost is compared.**

This is **not a new discovery**: `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/01-what-we-learned.md`
F15 states it directly — *"PG picks `GroupAggregate` mostly because it
delivers an ordering something above needs, not because the hash spills ...
goopg's upper planner does not model ordering requirements"* — and ties it to
K12(B)/K23/K24: *"the upper planner receives a finished `Node`, not the join
rel's paths ... K12(B) needs `Pathkeys`"*, calling it **"the largest single
item in the workstream"**. `.ralph/fix_plan.md`'s own M0141 preamble already
names this exact constraint ("the upper planner receives a finished `Node`
rather than the join rel's paths and K23/K12(B) share that root cause (K24)")
— M0141 **is** K24's "ordering contest, slice 3". This task's contribution is
narrower than that prior evidence: it **confirms empirically, for the first
time, that this already-named root cause is the dominant mechanism (6 of 8
classified queries) behind TPC-H's `aggregation-strategy` mismatches
specifically** — not just a general join-pathkey/parallelism concern — and
pins the exact code location (`upperordered.go:64-128`'s single-`Path` seam)
where the aggregation case instantiates it.

## Why S2 does not land a fix this task

S2's fix_plan wording expected *"a cost formula or pathkey-availability gap
in already-shipped machinery, not a new subsystem"*. Finding 3 shows the
dominant mechanism is neither: it requires the GROUP_AGG rel (and, by the same
argument, every upper rel feeding `createOrderedPaths`) to publish its
**Pathlist**, not a pre-collapsed **Node**, so the final ORDER BY step can run
its own cost contest between "cheapest matching-pathkeys candidate, no Sort"
and "cheapest candidate + Sort" — exactly the plumbing change K24 already
scoped as the workstream's largest single item. That is real, multi-file
upper-planner surgery (every `createXPaths` caller in `planner.go` that
currently hands `createOrderedPaths` a `Node` would need to hand it a rel), not
a one-line gap. Landing it inside this task would violate the harness's "never
force a shape" and "one task per loop" rules simultaneously. Mechanism (A)
(Q3) is separately gated on M0139 per Finding 2. **Neither mechanism has a
task-sized local fix available today.**

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement**: none — no production behavior change (comment-only
  edit to `plan.go`).
- **shape-delta**: 0.
- **Stats epoch**: unchanged from S1's capture (`analysis/m0141/m0141-s1-tpch.txt`'s
  `# stats-epoch:` line); no new capture taken this task.
- **Seam-decline census**: not applicable — no seam-declining code path is
  exercised or changed.
- **Planning route**: not re-verified per query this task (S1's caveat on the
  TPC-DS marker-search classification still stands, untouched).

## Verification

- `go build ./...` clean (comment-only diff to `plan.go`; no other file
  touched).
- No values/plan-gate run: no behavior changed, so a values sweep would be a
  no-op measurement of an unmodified binary.

## Resume points (filed as separate fix_plan slices, see below)

- **Mechanism (A), Q3** (and likely Q10/Q13/Q18 per the pre-existing test
  comment, not reconfirmed this task): re-measure once M0139 (executor-side
  narrowing) lands and `inAvgVarBytes` reflects only the columns the aggregate
  actually needs; do not touch the R3 spill arm before then (would regress the
  larger TPC-DS win).
- **Mechanism (B), the majority** (Q4, Q5, Q8, Q12, Q21, Q22 confirmed; Q13/Q18
  need a per-node trace): this is K24's "ordering contest, slice 3" — the
  GROUP_AGG rel (and siblings) must publish `Pathlist` through to
  `createOrderedPaths` instead of a collapsed `Node`, so the top-level ORDER BY
  step can jointly cost "matching-pathkeys candidate, no Sort" against
  "cheapest candidate + Sort". Scope this as its own task before attempting
  it — it touches every `createXPaths` -> `createOrderedPaths` call site in
  `planner.go`, not just the aggregate path.
