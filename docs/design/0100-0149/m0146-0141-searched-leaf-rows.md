# M0146-0141 — a searched join tree enters the enclosing search with its searched rel's rows

Status: done 2026-10-09 (a4f6b8c48). Parent: M0146-0136.

## Problem

TPC-DS Q72 at SF1. `join_collapse_limit` splits Q72's JOIN chain, so an
8-rel sub-problem is searched first and its result enters the upper
problem as one leaf.

- The leaf's rows came from `initialRelRows`' default arm, which ran
  `EstimateRows` over the built tree: 1623 rows.
- The `RelOptInfo` the search had sized for that tree (the Gather EXPLAIN
  prints) holds 5.
- At 1623 rows, the `d3` nested-loop probe priced 71221.82 and lost to the
  hash join (64211.41). PG plans the probe loop, with 2 rows downstream
  where goopg had 548.

## PG behaviour

- **Sub-problems.** `make_rel_from_joinlist` (allpaths.c) returns the
  sub-problem's `RelOptInfo` itself, and the enclosing search joins it
  with its `rows` as sized.
- **Subquery RTEs.** `set_subquery_size_estimates` (costsize.c) reads the
  subquery's final rel. When only row-preserving nodes sit above the
  subquery's join search, that is the join rel's size.

## Change

- **The arm.** `initialRelRows` (joinsearch.go) now reads
  `searchedJoinInputRelOf(leaf).Rows` when the leaf is a searched tree.
- **Why that accessor.** `searchedJoinInputRelOf` crosses only
  row-preserving wrappers (Project, Sort, Gather, GatherMerge, Memoize,
  Materialize, OrdinalityWrap, uncapped LockRows).
- **Fallback.** An Aggregate, Limit, Filter, Distinct or set-op above the
  tree stops the descent, and the leaf keeps the subtree estimate.

## Verification

- **Test.** `TestInitialRelRowsSearchedLeafReadsItsRel`: a Gather over a
  searched tree reads the rel's 5 rows; an Aggregate over it keeps the
  subtree estimate. It fails without the arm.
- **TPC-DS fire set.** Only Q72 at SF1 fires; both arms execute it (PASS).
  - The `d3` join is now a nested-loop probe above the Gather, with rows
    548 → 2 downstream, as in PG. Total cost goes 64487.36 → 61205.12
    (PG 53219.18).
  - SF1 `qual-placement` 10 → 9. Matches are unchanged (SF0.25 53,
    SF1 39).
- **TPC-H.** Plans are byte-identical; the acceptance arm reports 24 MATCH
  on values.
- **Gates.** Units, TPC-H spotcheck, SF0.25 sweep (PASS=98, SKIP=1) and
  ea-ratchet (1 finding, Q78, pre-existing) all PASS.

## Estimate-parity ratchet repair (M0146-0140 follow-up)

The first ea-ratchet run failed with 2 NEW findings, Q36 and Q86.

- **Cause.** M0146-0140 made these queries plannable, but
  `bench/tpcds/plans-pg/Q36/Q70/Q86.txt` were still `SKIP_QUERYGEN` stubs.
  The ratchet therefore scored goopg's estimates against no PG reference.
- **The 0140 doc was wrong.** It said keeping the stubs would protect the
  ratchet; the stubs were what broke it.
- **Fix.** The three files are now PG 18.3 EXPLAINs from :65438
  `tpcds025`, in the fixtures' `psql -X -t -A` format. A Q35 re-run
  confirmed the format; only PG's own drift since the September capture
  differs. The ratchet is back to 1 finding.

## Q72's remaining divergence (not covered)

Q72 at SF1 is still `[join-order, join-method, scan-type,
parameterisation, sort-strategy, parallelism]`.

- **A plan PG never builds** (filed as M0146-0144). goopg joins
  `promotion` as a `Nested Loop Right Join` (promotion outer, a
  Materialize of the 2-row join inner).
  - PG's `match_unsorted_outer` sets `nestjoinOK = false` for
    JOIN_RIGHT, JOIN_RIGHT_ANTI and JOIN_FULL, so PG builds the LEFT
    nested loop with a Materialized `promotion` inner.
  - goopg's `addNestLoopPath`/`addMaterialNestLoopPath` admit RIGHT on
    purpose (`joinpathsnli.go`'s R64 comment). This is the only
    occurrence in either corpus's goopg plans.
- **The `d3` probe** is a Bitmap Heap Scan (8.26..12.27) where PG uses an
  Index Scan (0.29..0.31). This is unverified; the candidate is the
  index-probe cost family (B8, M0146-0068).
- **The sort.** PG sorts per worker below a Gather Merge for the
  GroupAggregate. goopg sorts above the join.
