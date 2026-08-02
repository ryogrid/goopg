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
  costing (§4) prices big builds honestly.

`defaultCostParams` (`cost_funcs.go:45`) stays at PG 18 boot values; session
GUC threading (`seq_page_cost` etc.) remains the C3.2/C4 TODO it already is
— out of scope here, unchanged.

## 2. Rows once: `RelOptInfo.rows` is the single source

Each `RelOptInfo` carries `rows` set exactly once:

- initial rels: today's `estimateBaseRelInfo` post-filter estimate
  (`internal/planner/cardinality.go:285`);
- join rels: `calcJoinrelSize(outer, inner, clauses)` at find-or-create time
  in `makeJoinRel` — **before** any path is generated, so every method's
  paths for one relset share one output-row figure, PG's
  `set_joinrel_size_estimates` discipline.

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
2. **eqjoinsel semantics** for the residual clauses: selectivity
   `1/max(nd_left, nd_right)` with NULL-fraction correction
   (`selfuncs.c eqjoinsel_inner`), replacing per-edge products of
   `1/nd_side` (`bushy.go:1266-1301`) which double-counts correlated edges.
3. **Clamp discipline**: joinrel rows clamp to the FK-implied bound when a
   validated FK covers the join (rows ≤ referencing side's rows), the
   structural analogue of M0126-0010's `max(l,r)` fallback cap
   (`cardinality.go:400-406`) — keep that cap too for the non-FK fallback.
4. **MCV join selectivity** (doc 14 §3) — staged after 1–3; ledger row if it
   slips past this bundle's implementation window.

Estimate-quality bar: with 1–3, Q9's final joinrel estimate must land within
2 orders of magnitude of actual (175), against 13 orders today — checked by
the [09](09-verification-and-acceptance.md) §5 estimate audit, not by eye.

## 4. Hash-join cost with the spill model

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
| Q9 | fact-outermost left-deep order (lineitem streams; dimensions build) beats dimension-first by a wide margin | §3 FK clamps kill the fake intermediate explosions; §4 nbatch pricing penalises 6M-row builds |
| Q5 | keep the 7.1× cost-driven win (`stage3-order-ab.txt`); allow `BuildLeft` at the deep level where the composite (1.99M) < base (6.0M) | §1 currency + [02](02-plan-shape-contract.md) §2 commutation paths |
| Q2 | no cross products; small-dimension chain order | connectivity rule ([03](03-join-search-pg-dp.md) §4) + honest small-build costs (replaces `IsSmallDimensionSide` pinning, `cardinality.go:333` — the 1024-row heuristic retires with the bushy DP's `buildJoinFromDP`) |
| Q21 | order unchanged (neutral in stage0 A/B) but survives via spill | [06](06-hash-spill-and-memory.md) |

These are calibration checks, not tuning knobs: if a target fails, the fix
must be a named PG-faithful mechanism (a selectivity rule, a cost term with a
`costsize.c` citation), never a constant adjusted until the plan flips —
the second-try bundle's §7 discipline, restated as binding here.
