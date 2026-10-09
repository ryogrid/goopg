# M0146-0148 — a searched sub-joinlist hands its ordered runner-up paths up

Status: done 2026-10-10 (76e9d0f92). Parent: M0146-0014b.

## Problem

`join_collapse_limit` splits TPC-DS Q72's JOIN chain, so its first eight
relations are searched as their own sub-problem.

- **The sub-problem's top rel keeps two paths** (DP trace on a private
  SF1 clone):
  - a Gather at 61130.70;
  - a sorted Gather Merge at 61130.80, with three pathkeys on the
    GroupAggregate's keys.
- **Only one crossed.** The enclosing search received the cheapest-total
  tree, the Gather, as one prebuilt leaf.
- **What PG builds.** PG puts its `d3` and `promotion` nested loops on the
  Gather Merge, and the GroupAggregate takes that ordered input with no
  Sort (M0146-0014b routing, `agent-sort-ios.md`).
- **Where the gap was recorded.** This is the still-open pathlist half of
  ledger row M0127-P5.9-a. Its rows half was M0146-0141.

## PG behaviour

`make_rel_from_joinlist` (postgres/src/backend/optimizer/path/allpaths.c:3352)
returns the sub-problem's RelOptInfo itself. The enclosing search joins
every path `add_path` kept on it, ordered runners-up included.

## Change

- **Building the alternatives** (`searchOneProblem`, relfromjoinlist.go).
  The result carries `subproblemAlts`, a lazy rebuild of up to four
  runner-up paths (`maxSubproblemAlts`):
  - only unparameterised paths with pathkeys are taken;
  - each is built with the winner's base, width and hole-filler;
  - a tree is kept only when it publishes the winner's exact row;
  - a lowering panic declines that path, as `searchedBoundaryRebuild`
    does.
- **Carrying them.** `buildInitialRels` copies them onto the leaf rel as
  `altLeaves`.
- **Offering them** (`addSubproblemAltPaths`, in the leaf-pathkey pass,
  `addCTEScanPathkeys`).
  - Each alternative becomes a further prebuilt path, priced like the
    winner's leaf (`costSubplanLeaf`).
  - Its delivered ordering is translated by `subproblemLeafPathkeys`: the
    tree publishes the binding-order window starting at the leaf's binding
    offset, so published position i is coordinate offset+i in the
    enclosing search.

## Measurement

- **Q72 SF1** (private clone, traced). Both leaf paths are accepted, 0.10
  apart, as in the sub-problem.
  - The enclosing search builds the ordered nested-loop chain over the
    Gather Merge (61228.47).
  - The election stays on the Sort side. goopg's unordered chain plus a
    2-row Sort costs 61228.39. Both totals, and both startups, fall within
    `STD_FUZZ_FACTOR`, so the first-filed Sort candidate is kept, as
    `add_path` would keep it.
  - PG's own costs land on the other side of this sub-unit tie, so Q72 SF1
    routes to COSTTIE.
- **TPC-DS fire set.** No query moves at either scale (matches 56 / 42).
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases): unchanged, apart from `join`'s known
  `int8_tbl` row flap.
- **Gates.** Units (including
  `TestSubproblemLeafPathkeysTranslateToBindingCoordinates`), TPC-H
  spotcheck, SF0.25 sweep (PASS=99) and ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **No witness.** No corpus query elects an alternative, so the path is
  untested in execution beyond the schema-identity guard.
- **The cap.** At most four alternatives are rebuilt, where PG keeps every
  path `add_path` kept.
- **The extra row charge.** `costSubplanLeaf` charges a sub-problem leaf
  `cpu_tuple_cost` per row on top of its plan cost. PG's sub-problem rel is
  joined directly, with no such charge.
