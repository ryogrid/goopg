# R21 slice 1 — the join rel now leaves the search

*Round 21, slice 1 of 3. Design: `DESIGN.md` (committed `3b0322c11`).
Landed 2026-09-08.*

## 1. Verdict

Landed, and its gate — the only one that matters for an additive
refactor — **passes on both corpora**:

| gate | result |
|---|---|
| TPC-H plans vs the pre-slice build | **byte-identical** |
| TPC-DS plans vs the pre-slice build | **byte-identical** |
| optimizer + executor suites | green |

Nothing consumes the new value yet, so nothing could move, and the
measurement confirms nothing did.

## 2. What landed

- `joinlistRel` gains `rel *RelOptInfo`, documented with what the upper
  stages need from it (`PartialPathlist` for K23,
  `Pathkeys` for K12 slice B).
- `searchOneProblem` sets it from the chosen path's own parent
  (`p.Rel`) at the point where the rel used to end.
- `planJoinlistSearch` returns `(Node, *RelOptInfo, error)`.
- `tryPGShapedJoinSearch` takes it and discards it with an explicit `_`
  and a comment saying so — deliberately temporary, and the marker for
  slices 2 and 3.

Test callers updated for arity only; no expectation changed.

## 3. Why this slice exists separately

The seam refactor and the behaviour changes it enables are different
risks. Carrying a value out cannot move a plan; consuming it will move
many. Splitting them means slice 1 has a gate that is both cheap and
absolute — **byte-identical, or something is wrong** — and slices 2 and
3 start from a tree known not to have drifted.

This session has twice been misled by a change whose effect was
invisible until measured (K19's Gather-blind walkers, K22's headline
outrunning its own caveat). A slice whose correctness is a byte
comparison is the antidote to both.

## 4. A note on the TPC-DS diff

The first TPC-DS comparison reported differences. They were **K18
again** — psql error text carrying the capture script's temp path, and
the R9 baseline predates the fixed-filename fix from R9. Normalising
both forms of the path gives byte-identical.

K18 said this artefact "must not be reintroduced into anything that
diffs two captures". It was not reintroduced; the *baseline* simply
predates the fix. Worth noting because any comparison against a
pre-R9 capture will hit it, and reading it as plan movement is exactly
the mistake it caused the first time.

## 5. Next

- **Slice 2 (K23)** — partial aggregation from `PartialPathlist`, built
  below the Gather as `create_partial_grouping_paths` does. Success
  test: `aggregation-strategy`'s 10 → 14 under the flip disappears.
- **Slice 3 (K12 B)** — pathkeys for the ordering contest.
- The flip stays reverted until slice 2 measures out.
