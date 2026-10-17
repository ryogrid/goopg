# M0146-0137 — a grouping subquery member over a Gather offers no whole path

Status: done 2026-10-09 (e26c2f481). Parent: M0146-0014a.

## Problem

TPC-DS Q66 unions two GROUP BY subqueries in FROM (the web and catalog
channels), then groups again.

- **PG** plans a serial `Append` of two `Finalize GroupAggregate ->
  Gather Merge -> Partial GroupAggregate` branches (20447.03 at SF0.25).
- **goopg** planned `Gather -> Parallel Append` over two serial
  GroupAggregates (13758.93). Their scans were still priced per worker:
  `Seq Scan on catalog_sales` at 10521.57, where the serial scan costs
  12962.97.

## PG behaviour

`add_paths_to_append_rel` (allpaths.c) builds its parallel arms from each
child's `partial_pathlist` and from the child's cheapest *parallel_safe*
total path (`nppath`). A UNION ALL member PG keeps as a subquery RTE has
neither when it groups over a parallel input:

- **No parallel-safe whole path.** `subquery_planner` plans the member
  separately. Its final rel's `add_path` keeps the cheaper
  Finalize-over-Gather path and drops the dominated serial one, and
  `create_gather_path` makes that path not parallel_safe. So there is no
  `nppath`.
- **No partial path.** Grouping leaves partial paths only on the
  partially-grouped rel, so the member's final rel has no partial path
  for `set_subquery_pathlist` to offer.

Without either, PG cannot build a parallel Append arm, and Append stays
serial.

## Change

`setOpBranchPick` (windowsetoppaths.go) builds goopg's `nppath` by
stripping the Gather from the branch's winning plan (`StripGather`).
That stand-in is valid for a member PG pulls up, which never carries a
Gather of its own. It is not valid for Q66's members.

- **New input.** `setOpBranchPick` now takes `keptSubquery`, read from
  `SetOp.appendMemberLeft/Right`. That is the M0146-0093 stamp marking a
  member PG keeps as a subquery RTE.
- **The rule.** A kept-subquery member whose plan aggregates over a
  Gather (`aggregatesOverGather`) offers no non-partial pick. Q66's mixed
  arm then has nothing for these members, and the plan stays a serial
  Append, as in PG.

## Why the rule is bounded

A kept-subquery JOIN member still gets the stripped stand-in, because
removing it there cost more parity than it gained:

- **What PG does.** For such a member PG offers a partial pick instead;
  its final rel keeps the join's partial paths.
- **Where goopg falls short.** goopg's branch partial pick does not
  reach every such shape. TPC-DS Q76's `store` member is the witness: a
  Parallel Hash Join with a Memoize-probed nested-loop outer.
- **The measured cost.** Removing the strip for all kept-subquery members
  left Q76 with no Parallel Append at all, and Q76 gained
  `[aggregation-strategy, sort-strategy]` at both scales.

## Verification

- **Test.** `TestSetOpBranchPickKeptSubqueryGatherOffersNoWholePath`: an
  aggregating kept-subquery member offers no pick. A plain kept-subquery
  member and a pulled-up member still offer the stripped plan.
- **TPC-DS fire set.**
  - Q66 → MATCH (only a rendering difference) at both scales. Plan
    matches go SF0.25 51 → 52 and SF1 37 → 38.
  - Q66's results are identical, and no other query fires.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases): only `stats_ext`'s nondeterministic listing
  order changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **Partial pick for kept-subquery join members.** PG gives such a member
  (Q76) a partial pick. goopg still claims some of them whole with a
  stripped Gather and per-worker costs. Q76's `store` member is the case
  to start from.
- **Dominance by cost.** PG keeps a kept-subquery member's serial path
  whenever `add_path` does not dominate it. goopg decides from the
  member's winning plan alone; it keeps no runner-up paths.
