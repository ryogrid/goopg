# 07 — Cost-Driven Join Order

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [03](03-path-substrate-and-plan-creation.md), [05](05-statistics-and-estimation-inputs.md), [06](06-scan-and-join-path-costs.md) |
| premise | this is the milestone core — it is where the Round-4 regressions recover |

## 0. Why this chapter exists

The Round-4 regressions ([01](01-current-state-and-gap-analysis.md) §5) are join-
order failures: real statistics let the DP choose orders whose consequences its
integer, join-only cost could not price. This chapter switches the DP from that
integer cost to the real per-node PG-unit cost of [06](06-scan-and-join-path-costs.md),
running through pathlists and `set_cheapest`. It is the single phase where plans
change on purpose, and the phase the milestone is measured on.

## 1. Reusing the DP, changing its currency

goopg already has the right search: `enumerateBushyPlans`
(`internal/planner/bushy.go:500`) is a DPccp subset DP that, for each subset of
base relations, keeps the minimum-cost way to build it. The structure is correct
and stays. What changes is what fills each DP cell:

**Today** each `dpEntry` holds `{plan Node, rows int64, cost int64}` and the cost
is the integer `outputRows·1 + build·4 + probe·1` (`estimateJoinCost`,
`bushy.go:785`).

**Under this bundle** each subset's `RelOptInfo` holds a `Pathlist`, populated by
`add_path` over every join path generated for that subset
([06](06-scan-and-join-path-costs.md) §2), and `set_cheapest` selects its
`CheapestTotal` / `CheapestStartup`. The DP composes subsets by joining the
*cheapest paths* of the two halves, generating join paths, and adding them to the
combined subset's rel — reproducing how PG's `join_search_one_level` builds each
level's joinrels. The scalar `dpEntry.cost` becomes the `Path.Cost.Total` of the
subset's cheapest path.

The result the DP hands upward is the top rel's `CheapestTotal` path, which
`create_plan` ([03](03-path-substrate-and-plan-creation.md) §3) turns into the
executor Node tree.

### 1.1 The greedy comma reorder is subsumed

`reorderCommaFromByCardinality` (`internal/planner/joinorder.go:60`) is a greedy
pre-pass that today seeds the FROM order before the DP. With a real cost-driven DP
enumerating orders, its greedy nearest-neighbour heuristic is redundant for the
3–12-relation cases the DP covers, and it can be retired for those. It remains
relevant only outside the DP's range (it applies at `len(FromExprs) ≥ 3` with pure
comma-FROM, and the DP caps at 12 relations, `bushy.go:80`); above 12 relations the
greedy order stays as the DP's fallback seed. Stated so the two mechanisms' overlap
is explicit rather than accidental.

### 1.2 `LIMIT` selects on the startup axis

The DP hands up the top rel's `CheapestTotal` for an unbounded query. `set_cheapest`
also computes `CheapestStartup` ([03](03-path-substrate-and-plan-creation.md) §1),
and it is **not** decorative: a query with `LIMIT k` does not need the whole result,
so the cheapest way to produce the *first k rows* can be a higher-total, lower-
startup path — the motivation the two-number cost is introduced for
([02](02-pg-path-and-cost-oracle.md) §1.2, §4.3). At the top of the plan, when a
`LIMIT`/`FETCH` is present, selection uses the startup axis: goopg reproduces the
shape of PG's `get_cheapest_fractional_path` — interpolate each candidate top path's
cost at the fraction `k / rel.Rows` (`startup + fraction·(total − startup)`) and
pick the minimum, which reduces to `CheapestStartup` as the fraction → 0 and
`CheapestTotal` as it → 1. Several milestone queries are `ORDER BY … LIMIT` (Q2, Q3,
Q10, Q18, Q21, [04](04-pathkeys-and-ordering.md) §1), where a pre-sorted, higher-
total path can win because it needs no final sort before the first row. This is a
small addition at the top-level path selection, not a change to the DP; without it,
`CheapestStartup` would be computed and never read, and the LIMIT motivations
elsewhere in the bundle would be unfulfilled.

## 2. Why real cost recovers the Round-4 regressions

Each regression is a specific order the integer cost could not see was bad. The
PG-unit cost makes each visible:

- **Q4 (79×): NL-Semi → Hash-Semi building 6 M-row `lineitem`.** The integer cost
  charges the hash build `buildRows·4`, but has no absolute scale to weigh that
  against the nested-loop-semi alternative, which it prices in unrelated units
  (`nliCostGateAccepts`, a bare `outerRows ≤ 100000` threshold). Under the cost
  model both plans are costed in PG units in one pathlist: the Hash-Semi's
  `initial_cost_hashjoin` build term over 6 M rows is `~(cpu_tuple_cost +
  cpu_operator_cost·k)·6·10⁶` of **startup** cost, and the NL-Semi's
  `final_cost_nestloop` with a selective (~3.7 %) `orders` outer and a `match_frac`
  early-out is far cheaper. `add_path` keeps the NL-Semi. The regression was the
  planner unable to compare the two; the fix is a common unit.
- **Q8 (53×): 8-way tree rearranged, `lineitem` buried in an MHJ.** The integer
  cost has no term for the intermediate materialisation the rearrangement forces.
  The MHJ comparability invariant ([06](06-scan-and-join-path-costs.md) §4.1) costs
  the MHJ path against the cascade in the same units, so an order that buries
  `lineitem` badly is priced with its true probe cost and loses to the order that
  joins the filtered dimensions first.
- **Q22 (128×), Q2 (26×), Q12 (4.4×): orders the DP cannot cost.** Same mechanism:
  a join order that produces a large intermediate is cheap under an integer cost
  that only counts `output + 4·build + probe` of the *final* join, but expensive
  under a cost that sums every node's `(startup, total)` up the tree. The absolute,
  per-node model prices the intermediate the integer model was blind to.

The claim this chapter must *prove*, not assert, is that this is not merely
plausible but measured — [09](09-verification-and-acceptance.md) §2 makes "the five
regressions recover without losing Q5" the acceptance gate, on real SF1 data.

## 3. Not losing Q5

Q5 is the query statistics **fixed** (415 s → 18 s), by finding a good order via
`baseRelInfo.filteredRows` and the anchored `c_nationkey = n_nationkey` edge
([fix-for-q5/02](../fix-for-q5/02-cost-model-and-selective-equivalence.md) §5). The
cost model must preserve that win: the filtered-region and filtered-orders inputs
must still be costed as small (via `Rel.Rows` scaling, [05](05-statistics-and-estimation-inputs.md) §1),
and the anchored equality must still be available to the DP so the customer-to-
nation path exists. The cost model *consumes* fix-for-q5's `baseRelInfo` and
anchored-equality machinery; it does not replace it. Acceptance
([09](09-verification-and-acceptance.md) §2) checks Q5 explicitly, because a cost
model that recovers the five regressions by reverting to the pre-statistics
heuristic would also revert Q5 — that is a regression wearing a fix's clothes.

## 4. Determinism: the integer→float tie-break

Switching `dpEntry.cost` from `int64` to `float64` introduces a real hazard:
two orders whose real costs are within floating-point noise can flip between runs,
and plan-gate reproducibility depends on a stable choice. The solution is not new —
it is `add_path`'s fuzzy comparison ([03](03-path-substrate-and-plan-creation.md) §2):

- Two paths within `STD_FUZZ_FACTOR = 1.01` are declared **cost-equal**, and the
  tie is broken deterministically on the other dominance dimensions (pathkeys,
  `parallel_safe`) and then on a stable secondary key (e.g. the relids bitmask
  order) — never on the raw float difference.
- Because the fuzz band is multiplicative and wide (1 %), genuinely close orders do
  not flicker; only orders that differ by more than 1 % are ranked by cost, and
  those are stable.

This must be a stated test target: run the DP twice on the same input and assert an
identical chosen path ([09](09-verification-and-acceptance.md) §1). Without it, the
plan-gate becomes non-deterministic the moment the cost is a float.

## 4.5 MEASURED at implementation (C4): the order-then-rewrite mismatch

An implementation attempt that costed **only binary hash-join order** in the DP —
leaving goopg's `MultiHashJoin` packing (`rewriteMultiWayChain`) and NL-index
conversion (`rewriteJoinsToNLI`) as the **post-DP passes they are today** — was
measured on SF1 (serial, ANALYZE'd) and **must not be shipped**. It recovered Q8
(200 s → 21 s) and improved Q5 (18 s → 5 s), changed 16 of 22 plans, but
**regressed Q9 from 27 s to > 250 s**. Every other changed query was
same-or-better; Q9 was the lone regression, and it is decisive.

The cause is structural, not a mis-tuned constant. goopg decides join **order** in
the bushy DP and then **rewrites method afterward** (binary hash-join chains →
`MultiHashJoin`; hash joins with an indexed inner → NL-index join). A cost model
that ranks *binary hash-join* orders is therefore optimising a cost that **does
not match how the plan will execute**. Q9's diagnosis makes it concrete: the
integer heuristic put `orders` in the 4-way MHJ and NL-index-probed `partsupp`
*selectively* through its composite PK; the binary-hash cost model instead put
`partsupp` in the MHJ and left `orders` to be NL-index-probed **~6 M times**. The
cost-"optimal" binary order rewrote into a catastrophic real plan.

**The correction — and it cannot be deferred.** Cost-driven join order in goopg is
unsafe until the DP costs the **actual execution shape**. The MHJ comparability
invariant ([06](06-scan-and-join-path-costs.md) §4) and the NL-index path
([06](06-scan-and-join-path-costs.md) §2.3) are therefore **not** later
refinements layered onto a working binary-order DP — they are *prerequisites*. The
DP must generate MHJ paths and NL-index paths as first-class alternatives **during**
enumeration (competing in `add_path` against the binary-hash path over the same
joinrel), so that `set_cheapest` ranks orders by what actually runs. This is the
"full-fidelity DP", and it is the real content of this phase; the binary-only
version above is recorded as a rejected intermediate so a future implementer does
not repeat it. Concretely, at each DP composition the generator must add: (a) the
binary hash path (both build orientations, §1); (b) an **NL-index path** when the
inner joinrel is a base table with an index covering the join key, costed by
`nestloopCost` with one index probe per outer row — this is what makes the
`partsupp`-vs-`orders` probe-selectivity difference visible to the cost; and (c),
for a subset whose binary shape would pack into an MHJ, the **MHJ path** costed per
[06](06-scan-and-join-path-costs.md) §4.1. Only then is the argmin switch safe.

## 5. Enumeration cost

The DP now generates several join-*method* paths per subset (hash build-left, hash
build-right, merge, nest, NLI, MHJ variants) where it kept one integer cost before.
That is a constant-factor increase in paths per subset, not a change in the
exponential structure — the subset count is unchanged, and `add_path`'s dominance
pruning discards dominated paths immediately, so the surviving pathlist per subset
stays small. The 12-relation cap (`bushy.go:80`) bounds the worst case as it does
today. If path generation ever dominates planning time, the mitigation is PG's own
— generate the cheapest-startup and cheapest-total paths eagerly and defer the rest
— but the milestone does not need it.

## 6. Divergence from PostgreSQL

- **goopg reuses its DPccp enumerator** rather than PG's `join_search_one_level`
  driver; the *search* is goopg's, the *costing and path bookkeeping* are PG's.
  Same result shape (a cheapest path per joinrel), different enumeration plumbing.
- **The greedy comma reorder survives only above 12 relations** (§1.1) — a goopg-
  specific fallback PG does not have (PG uses GEQO past `geqo_threshold`); the
  milestone's joins are all within the DP's range.
- **Plan choice is stabilised by the fuzz factor, not by integer exactness** (§4).
  This is PG-faithful, but it means goopg's plans can differ from a hypothetical
  infinite-precision choice within 1 % — deliberately, to stay reproducible.
