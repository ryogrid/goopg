# M0146-0150 — an aggregating statement's join search hands up its cheapest-total path

Status: done 2026-10-10 (41140c552). Parent: M0146-0149.

## Problem (as filed)

M0146-0149's trace of TPC-DS Q70 showed something that looked like a wrong
input to aggregation. The MixedAggregate was priced on the ordered,
unique-ified nested-loop inner join (38440.70), although the 4-relation
joinrel held a cheaper Hash Semi Join (38367.56). The task assumed the
LIMIT fraction at the join search root had picked the nested loop.

## PG behaviour

- **Where the fraction applies.** `root->tuple_fraction` is resolved only
  at `final_rel`: `best_path = get_cheapest_fractional_path(final_rel,
  tuple_fraction)` (postgres/src/backend/optimizer/plan/planner.c:439).
- **What grouping reads.** The grouping step reads the scan/join rel
  through `input_rel->cheapest_total_path` (:4038, :7122) for its hashed
  and plain arms, and through each presorted path for its sorted arm.
- **What the fraction still does.** Below `final_rel` it only sets each
  rel's `consider_startup`.

## Change

- **The flag.** `resolveContext.rootCheapestTotal`
  (`needsAggregateStage`) is stamped beside the fraction and travels
  through `joinlistProblem` to `searchCtx`.
- **The root pick.** `searchCtx.rootPathOf`, used by `finalPath`, hands
  up `CheapestTotal` under the flag. GEQO's root does the same.
- **What does not change.** The fraction still sets `ConsiderStartup` on
  every rel. Presorted inputs still reach sorted grouping through the
  search-candidate loop.

## What the measurement showed

- **The premise was wrong.** Q70's LIMIT belongs to the outer
  `select * from (…) sub` wrapper, so the grouped query runs with
  `tf=0`, and its root pick was already `CheapestTotal`.
- **The real cause is a fuzzy near-tie.** A debug pass on the private
  SF0.25 clone showed the paths of the very search that produced the root:
  - the unique-ified ordered nested-loop inner join costs 38326.14;
  - the Hash Semi Join costs 38266.63, 0.16% less;
  - their startups are also within `STD_FUZZ_FACTOR`;
  - so `add_path` keeps the path that has pathkeys, as PG's would.
- **Where Q70 now routes.** PG's own costs land on the hash side of this
  tie, so Q70 routes to COSTTIE.
- **TPC-DS fire set.** No query moves (matches 56 / 42).
- **TPC-H.** Plans are byte-identical. Q2, Q3, Q10, Q18 and Q21 aggregate
  under a LIMIT, and their cheapest-total and fractional picks already
  agree.
- **Regress A/B** (32 cases): unchanged.
- **Gates.** Units (including
  `TestSearchRootHandsUpCheapestTotalUnderAnAggregate`), TPC-H spotcheck,
  SF0.25 sweep (PASS=99) and ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **Window and DISTINCT stages.** The flag covers only the aggregate stage.
  A window or DISTINCT stage also reads `cheapest_total_path` in PG, but
  goopg's root still applies the fraction under them.
- **No corpus witness** for the aggregate-plus-LIMIT case.
