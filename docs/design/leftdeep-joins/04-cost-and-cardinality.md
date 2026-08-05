# 04 — Cost and Cardinality: One Currency, Rows Once, FK-Aware

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| PG oracle | `postgres/src/backend/optimizer/path/costsize.c` (`initial_cost_hashjoin` :4160, `final_cost_hashjoin` :4275, `cost_nestloop`, `cost_mergejoin`); `postgres/src/backend/optimizer/path/equivclass.c`; `postgres/src/backend/utils/adt/selfuncs.c` (`eqjoinsel`) |
| adopts | [cost-model/02](../cost-model/02-pg-path-and-cost-oracle.md), [05](../cost-model/05-statistics-and-estimation-inputs.md), [06](../cost-model/06-scan-and-join-path-costs.md), [14](../cost-model/14-fk-aware-and-mcv-join-selectivity.md) — this chapter binds them to the new DP, it does not re-derive them |
| retires | the integer relative cost (`estimateJoinCost`, `internal/planner/bushy.go:1258`; weights `bushy.go:1013-1017`); the ad-hoc quadratic build penalty (`costJoinCandidate`, `bushy.go:644-649`, commit `c63f8023`); `chooseInnerJoinAlgo` (`internal/planner/joincost.go:19`) for searched joins |

## 1. One currency

The DP of [03](03-join-search-pg-dp.md) costs everything in
`Cost{Startup, Total}` (`internal/planner/path.go:35`), PG units, computed by
the `cost_funcs.go` family. The parallel integer heuristic dies with the
bushy DP — there is no "cost-driven mode" flag anymore
(`costDrivenJoinOrder`, `bushy.go:569`, is retired; the *new* staging flag in
[08](08-migration-and-removal.md) §2 selects the enumerator, not the
currency). Two M0126-0013 artifacts get opposite treatment:

- **Keep `e13d6c6f` only as an interim, then replace it**: `hashJoinCost`'s
  unconditional `inner_pages × seq_page_cost` build term
  (`cost_funcs.go:104-117`). Its commit message calls it PG-faithful, but PG
  charges that I/O **only when `numbatches > 1`** (inside
  `initial_cost_hashjoin`'s batching branch, `costsize.c:~4239-4248`) — an
  in-memory build pays no page I/O in PG. The unconditional form is a
  goopg-only deterrent heuristic; it stays until §4's nbatch-aware costing
  lands (which prices exactly PG's conditional form), then it is deleted in
  the same commit.
- **Delete** `c63f8023`: the quadratic `largeBuildThreshold = 2M` penalty in
  `costJoinCandidate` — it is exactly the unfalsifiable shape-nudging the
  second-try bundle prohibited, and it becomes redundant once nbatch-aware
  costing (§4) prices big builds honestly. *(DONE — M0127-P5.6-d, 2026-08-05,
  the commit after §4's term landed. See §4.2.)*

`defaultCostParams` (`cost_funcs.go:45`) stays at PG 18 boot values; session
GUC threading (`seq_page_cost` etc.) remains the C3.2/C4 TODO it already is
— out of scope here, unchanged.

### 1.1 The index-scan currency: `cost_index` and one calibration knob

*(Added by P5.4c-ii-b, `internal/planner/costindex.go`.)*

goopg prices index access in **two** places, and the rule that keeps them one
currency is that only one of them is calibrated:

| | what it prices | PG oracle |
|---|---|---|
| `paramIndexScanCost` (`pathparamindex.go`) | ONE bound probe of a parameterised path; the join above multiplies it | `cost_index` at `loop_count > 1`, by convention "cost one execution" |
| `costIndexScan` (`costindex.go`) | a whole scan of the relation through the index | `cost_index` (costsize.c:520) at `loop_count == 1` |

The first is built on `indexProbeCost` × `indexProbeCostMultiplier`
(`cost_funcs.go`), a knob measured because goopg materialises the entire TID
list per probe and so runs slower than `random_page_cost` predicts. The second
is a faithful transcription of `cost_index` — Mackert-Lohman page estimation
(`index_pages_fetched`, costsize.c:906), `genericcostestimate`'s index-side
charge, `btcostestimate`'s 50 × `cpu_operator_cost` per descended page, and the
csquared interpolation between the all-random and mostly-sequential I/O cases
— **with the same multiplier applied to every random-page term**. Sequential
fetches and per-tuple CPU stay PG-native, because that is not what the
multiplier was measured against.

That is the whole discipline: one knob, reaching both models. A second
independently-derived index cost model beside the first is the failure §1
forbids — raising the knob would then make a probe expensive while leaving a
full index scan untouched, and the two would be compared inside one `addPath`.

**Correlation.** The interpolation reads `indexCorrelation`, which PG takes
from `STATISTIC_KIND_CORRELATION` on the index's leading column (negated for a
DESC key, × 0.75 for a multi-column index — `btcost_correlation`,
selfuncs.c:7305). goopg's ANALYZE collects no correlation slot, so
`indexCorrelationFor` returns 0 — which is not a stub but exactly what PG
charges for an index with no correlation statistic (`genericcostestimate`:
"generic assumption about index correlation: there isn't any"). The practical
consequence is that a full ordered index scan always prices at
`max_IO_cost` and normally loses to a sort over a sequential scan; it survives
in the pathlist only on the strength of its pathkeys. Collecting the statistic
is a separate slice (ledgered), not a tuning constant to invent here.

**Index geometry.** `cost_index` reads `index->pages`, `index->tuples` and
`index->tree_height` off the index relation. goopg's `catalog.Index` has none
of them — ANALYZE does not visit indexes — so `estimateIndexGeometry` derives
them from the heap's row count and the declared key width at the B-tree default
fillfactor. Ledgered; the resume point is index-level catalog statistics, not a
better formula.

## 2. Rows once: `RelOptInfo.rows` is the single source

Each `RelOptInfo` carries `rows` set exactly once:

- initial rels: today's `estimateBaseRelInfo` post-filter estimate
  (`internal/planner/cardinality.go:285`);
- join rels: `calcJoinrelSize(outer, inner, clauses)` at find-or-create time
  in `makeJoinRel` — **before** any path is generated, so every method's
  paths for one relset share one output-row figure, PG's
  `set_joinrel_size_estimates` discipline.
  *(Landed as P5.6-b, `internal/planner/joinrelsize.go`, together with the
  concrete `joinRelBuilder` — `searchJoinRelBuilder` — that binds this sizer
  to `addPathsToJoinrel` and closes the last seam `makeJoinRel` left open.
  The joinrel's WIDTH is the sum of the input widths: PG's
  `build_joinrel_tlist` instead sums only the columns needed above the join,
  which the search has no projection information to reproduce — 03 §10's
  boundary map is built over the full concatenation. Ledgered.)*

Costing never calls the tree-walking `EstimateRows`
(`cardinality.go:41`) — that walker remains only for EXPLAIN of non-searched
subtrees. This is cost-model invariant #2, now structurally enforced: paths
don't have a plan tree to walk.

One structured exception: **parameterised paths carry their own row
estimate** (`ppiRows` — per-outer-row output of an NLI inner; PG's
`get_parameterized_baserel_size`). The RelOptInfo `rows` stays canonical for
the rel; each parameterisation's rows live beside the path
([03](03-join-search-pg-dp.md) §9 rule 3).

## 3. Join selectivity: fixing class (a)

The Q9 blow-up (estimate chain 1,250 → 37M → 1.5e11 → 1.1e15 → 5.9e15 vs
actual 175, `evidence/order-attribution-summary.md`) is compounding
ndistinct error. The remedy set, in mandatory order:

1. **FK-superkey detection generalised** — today's
   `uniqueNoFanoutRawCount` (`bushy.go:1201`) already divides by the unique
   side's raw count when a UNIQUE/FK superkey covers the equated columns;
   it moves into `calcJoinrelSize` and must fire on **clause subsets** too
   (composite FKs matched partially), per
   [cost-model/14](../cost-model/14-fk-aware-and-mcv-join-selectivity.md) §2.
   *(Landed as P5.6-b, `superkeyJoinSelectivity`. It is
   `get_foreign_key_join_selectivity`'s structure — REMOVE the covered clauses
   from the restriction list and substitute one `1/raw-tuples` — rather than a
   divisor bolted onto the per-clause estimate, because that structure is what
   stops the covered clauses being charged twice. Three properties are
   upstream's and load-bearing: the divisor is the RAW count (a filtered key
   side then yields a real match fraction); the WHOLE key must be covered
   (`⊆` is the test on the key's columns, not on the clause list, so extra
   equated columns stay residual and are charged by eqjoinsel on top); and a
   clause is consumed once, so two overlapping keys cannot both charge for it.
   The one asymmetry: a UNIQUE index makes ITS OWN relation the key side, but a
   declared FK makes its relation the CHILD, so the divisor is the PARENT's raw
   count — `1.0/ref_tuples`, costsize.c:5847. The legacy
   `uniqueNoFanoutRawCount` reads that backwards, dividing by whichever table
   carried the constraint; ledgered, and it dies with P6.3.)*
2. **eqjoinsel semantics** for the residual clauses: selectivity
   `1/max(nd_left, nd_right)` with NULL-fraction correction
   (`selfuncs.c eqjoinsel_inner`), replacing per-edge products of
   `1/nd_side` (`bushy.go:1266-1301`) which double-counts correlated edges.
   *(Landed as P5.6-a, `internal/planner/joinselectivity.go`, together with
   `examine_variable` / `get_variable_numdistinct`. The MAX is not pessimism:
   each side gives an upper bound on the join's selectivity, so the estimate
   is the MINIMUM of the two bounds, which is the one with the larger nd in
   the denominator. Dispatch is on the clause's OPERATOR, not on
   `restrictInfo.isEquijoin` — `a.x = b.y + c.z` has no two-sided split and so
   keys no hash join, but PG prices it with eqjoinsel all the same, and the
   0.5 unhandled-clause fall-through would be 100× upstream's answer.)*
3. **Clamp discipline**: joinrel rows clamp to the FK-implied bound when a
   validated FK covers the join (rows ≤ referencing side's rows), the
   structural analogue of M0126-0010's `max(l,r)` fallback cap
   (`cardinality.go:400-406`) — keep that cap too for the non-FK fallback.
   *(Landed as P5.6-c, `keyImpliedRowsBound` + `calcJoinrelSize`'s two clamps.
   The two are deliberately not one mechanism. The key-implied bound is a
   COUNTING argument and is always sound: a proven key means each row of the
   other side matches at most one row of the key side, so the output cannot
   exceed the other side's rows whatever the selectivities multiplied out to —
   which is why it is generalised beyond declared FKs to every key the P5.6-b
   superkey pass proves, and why it is taken only when the key relation is the
   WHOLE of its side (inside a multi-rel input a lower join may already have
   duplicated its rows; ledgered). With consistent inputs the product lands
   exactly ON the bound, so what the clamp actually catches is the two
   disagreeing: a key side whose row ESTIMATE has outgrown its ANALYZE-time raw
   count divides by a tenth of what it will read and claims a 10× fan-out from
   a join that cannot fan out at all. The `max(l,r)` cap is the opposite kind of
   thing — a heuristic backstop with no upstream counterpart — so it fires only
   where M0126-0010's does: nothing proven AND every residual clause priced by
   a selfuncs.h constant, which is PG's `*isdefault` carried out of
   `get_variable_numdistinct` to the caller that finally uses it. Capping a
   MEASURED estimate would truncate genuine many-to-many joins, whose blow-up
   the planner must see; capping a clause-less join would truncate a cross
   product, where |L|·|R| is the answer. Both are ledgered as divergences: the
   cap dies when eqjoinsel's MCV arm makes the residual estimator trustworthy
   on skewed keys.)*
4. **MCV join selectivity** (doc 14 §3) — staged after 1–3; ledger row if it
   slips past this bundle's implementation window.

Estimate-quality bar: with 1–3, Q9's final joinrel estimate must land within
2 orders of magnitude of actual (175), against 13 orders today — checked by
the [09](09-verification-and-acceptance.md) §5 estimate audit, not by eye.

## 4. Hash-join cost with the spill model

**Status: LANDED (M0127-P5.7-a, 2026-08-05)** — `internal/planner/cost_funcs.go`
`hashJoinCost`, now taking a `hashJoinInputs` struct and calling
`hashsize.Choose`. What actually shipped, and the two things that did not, are
at the end of this section; the plan below is what it was built from.

Once [06](06-hash-spill-and-memory.md) gives the executor real
`(nbuckets, nbatch)` sizing, `hashJoinCost` prices what will actually run,
PG-style (`initial_cost_hashjoin`/`final_cost_hashjoin`):

- compute `nbatch` from inner size vs `work_mem` (the executor's own
  `chooseHashTableSize`, shared planner↔executor so they cannot diverge —
  sibling-path rule);
- `nbatch > 1` adds the batch I/O term: write+read of (inner + outer)
  spilled fractions at `seq_page_cost` — this is what *honestly* prices
  Q9's "three consecutive 6M-row builds" order into oblivion, replacing the
  deleted quadratic penalty;
- startup cost = inner scan + build (probe is per-tuple run cost), so
  Startup/Total split finally matters for LIMIT queries over joins.

The 48-byte-Datum row-width reality stays priced via `estimatedRowBytes`
semantics (`internal/executor/spill.go:324`) feeding `inner_pages`
([cost-model/06] owns the width model; note goopg hash entries cost ≈
`48·r·c` bytes vs PG's packed MinimalTuple — the planner's page math must use
goopg's widths, not PG's, or nbatch predictions will be systematically low).

### 4.1 What landed (M0127-P5.7-a)

`hashJoinCost` calls `hashsize.Choose(innerRows, innerCols, 0, cp.workMem)` —
the same function, with the same argument shape, that `joinOp.buildGeometry`
(`internal/executor/operators_join_agg.go:624`) calls at run time. When it
answers `NBatch > 1`, PG's charge is applied verbatim
(costsize.c:4239-4248): `seq_page_cost · innerPages` at startup (the inner is
written during the build) and `seq_page_cost · (innerPages + 2·outerPages)` at
run (the inner read back, the outer written and read). `spillPages` is
`page_size` with `relation_byte_size` replaced by `hashsize.EntryBytes`, so
the pages charged and the bytes the geometry solved for come from one model.

The pivotal detail is **which width crosses the boundary**. PG hands
`ExecChooseHashTableSize` a byte width because its entry is a packed
MinimalTuple; goopg's entry is a `[]Datum` of 48-byte structs, so its size
follows the COLUMN COUNT, and that is what the executor passes. The planner
therefore had to learn a column count it did not carry: `RelOptInfo.NCols`,
set from the leaf's schema for a base rel and as the sum over the two inputs
for a join rel (a join row is its inputs concatenated). Feeding the existing
byte-valued `Width` here would have sized the same build ~25× differently on
the two sides of the sibling-path rule.

This **replaced** an unconditional `seq_page_cost · innerRows/100` term added
by M0126-0013, which cited costsize.c:4166 for a page charge upstream does not
make there. Upstream charges pages only under `numbatches > 1`, and charges
them for the SPILL, not for the resident table. The stand-in was monotone in
`innerRows`, so it penalised a 6 M-row build that fits `work_mem` exactly as
much as one that does not — which is precisely the distinction that decides
the plan, and is pinned now by
`TestHashJoinCost_SpillDependsOnFitNotOnSize`.

Two parts of the section did **not** land and are ledgered (2026-08-05):

- **`work_mem` is not per-session in the planner.** `costParams.workMem`
  defaults to `hashsize.DefaultMemLimitBytes` (512 MB), which is the executor's
  own no-work_mem fallback, so the two agree at the default and only at the
  default. Cost time has no session in scope — the same gap `ParallelSettings`
  exists to bridge for the parallel post-pass.
- **The batch-file encoding is narrower than the in-memory footprint.**
  `spillWriter.WriteRow` frames datums with uvarint lengths rather than storing
  48-byte structs, so `spillPages` over-states the I/O of a wide build. That is
  the safe direction (it deters spilling) but it is an approximation, not the
  measurement.

### 4.2 The second stand-in, deleted (M0127-P5.6-d)

§1's delete-list had **two** entries for the same missing term, and §4.1 only
retired one of them. The other lived a level up, in `costJoinCandidate`
(`bushy.go`): above a fixed `largeBuildThreshold = 2_000_000` rows it added
`overshoot² · cpu_tuple_cost · innerRows` to whatever `hashJoinCost` had
returned. M0127-P5.6-d deletes it, so the bushy DP's hash cost is now *exactly*
the cost function, with no second deterrent of a different shape layered on
top.

The reason the honest term is not merely a like-for-like replacement is that
the two key on different quantities. The penalty's threshold was a **row
count**; whether a build spills is decided by **bytes**, and the same 2 M rows
of entries is 144 MB at one column and 3.9 GB at forty. So the stand-in was wrong in both
directions at once against the 512 MB default budget: a 4 M-row single-column
build fits comfortably and was charged 40 000 anyway, while a 1 M-row
forty-column build spills to four batches and was charged nothing. The spill
term asks `hashsize.Choose`, the executor's own geometry function, so the
deterrent now fires on precisely the builds that will really write batch files.

Both halves are pinned:
`TestCostJoinCandidateHasNoRowCountPenalty` asserts `costJoinCandidate` returns
the bare `hashJoinCost` for a 4 M-row build that fits (the case the row-count
form got wrong, and the assertion that fails if any penalty is re-added), and
`TestCostJoinCandidateStillDetersHugeBuilds` asserts the defence the penalty
existed for survives — a spilling 6 M-row build still ranks above a fitting
500 K-row one.

Scope note for anyone reading a benchmark for movement: `costJoinCandidate` is
only reached under `costDrivenJoinOrder`, which is OFF by default, so the
default planner arm is byte-identical across this change.

The **Startup/Total split for LIMIT-over-join** — the second half of P5.7 — is
untouched here: `hashJoinCost` has always returned both numbers and now moves
them independently (the inner write is startup, the read-back is run), but
nothing yet SELECTS on startup under a LIMIT. That needs PG's `tuple_fraction`
plumbed into `grouping_planner`'s choice between `CheapestTotal` and
`CheapestStartup`, and is filed as **M0127-P5.7-b**.

## 5. Inferred edges: from admissibility penalty to selectivity honesty

Today `isInferred` edges carry a ×2.0 cost penalty (`inferredEdgePenalty`,
`bushy.go:67`, applied `:1348`). Under the new DP, inferred equalities are
ordinary clauses for **admissibility** (they satisfy
`hasRelevantJoinClause`), but they contribute **no additional selectivity**
when the equivalence class already applied one of its members — PG's
equivalence-class rule (`equivclass.c`: an EC of n members yields n−1
independent clauses, not C(n,2)). This removes the arbitrary multiplier and
the double-counting it papered over.

## 6. What "success" costs must reproduce (calibration targets)

| query | required emergent behaviour | mechanism |
|---|---|---|
| Q9 | fact-outermost order (lineitem streams; dimensions build) beats dimension-first by a wide margin | §3 FK clamps kill the fake intermediate explosions; §4 nbatch pricing penalises 6M-row builds |
| Q5 | keep the 7.1× cost-driven win (`stage3-order-ab.txt`); allow `BuildLeft` at the deep level where the composite (1.99M) < base (6.0M) | §1 currency + [02](02-plan-shape-contract.md) §2 commutation paths |
| Q2 | no cross products; small-dimension chain order | connectivity rule ([03](03-join-search-pg-dp.md) §4) + honest small-build costs (replaces `IsSmallDimensionSide` pinning, `cardinality.go:333` — the 1024-row heuristic retires with the bushy DP's `buildJoinFromDP`) |
| Q21 | order unchanged (neutral in stage0 A/B) but survives via spill | [06](06-hash-spill-and-memory.md) |

These are calibration checks, not tuning knobs: if a target fails, the fix
must be a named PG-faithful mechanism (a selectivity rule, a cost term with a
`costsize.c` citation), never a constant adjusted until the plan flips —
the second-try bundle's §7 discipline, restated as binding here.
