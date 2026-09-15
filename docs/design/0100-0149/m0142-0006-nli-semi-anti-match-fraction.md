# M0142-0006 — apply the SEMI/ANTI match fraction in `estimateNLIndexJoin`

**Status:** accepted, 2026-09-15.

## Problem

`estimateNLIndexJoin` (`internal/optimizer/cardinality.go`) is the cardinality
estimator for `*NestedLoopIndexJoin` plan nodes. Its sibling `estimateJoin`
(the hash/merge `*Join` estimator) has a dedicated SEMI/ANTI arm
(`cardinality.go:617-631`) that sizes the output as the OUTER input scaled by
a match fraction (`semiJoinMatchFraction * joinResidualSelectivity`), mirroring
PostgreSQL's `calc_joinrel_size_estimate` JOIN_SEMI/JOIN_ANTI arms
(`costsize.c`). `estimateNLIndexJoin` had no such arm: it returned
`EstimateRows(j.Outer)` unconditionally for every join type, including
SEMI/ANTI — a SEMI join over an index probe never narrowed at all, and an
ANTI join never excluded anything. Filed as `m0137-0013-nli-semi-anti-match-fraction-gap`
(pattern: `pattern_sibling_paths_must_agree` — a defect visible only by
comparing two sibling estimators, not from either one in isolation).

## Why the ledger's "mirror lines 621-628" framing does not literally work

The naive fix is to wrap `j.Outer`/`j.Inner` in a synthetic `*Join` and call
`semiJoinMatchFraction`/`joinResidualSelectivity` exactly as `estimateJoin`
does. That compiles and passes every existing test, but it is a **silent
no-op** for the common case, because of a coordinate-space fact specific to
`*NestedLoopIndexJoin`:

- `Join.Predicate` carries the WHOLE join condition (equi-pairs included);
  `joinEquiPairs` recovers the equi-pairs from it.
- `NestedLoopIndexJoin.Predicate` carries only the **residual** filter.
  `createplannl.go:418-422` strips every index-clause column into
  `is.Key`/`is.Keys` (the probe's bound parameters) *before* building
  `Predicate` from `p.Residual` — the leftover, non-index-clause conjuncts.

For a fully-bound equi-probe (the case `estimateNLIndexJoin`'s existing
doc comment already describes: "the inner side is an equality index probe"),
`Predicate` is `nil` and `joinEquiPairs(syntheticJoin)` returns zero pairs.
`semiJoinMatchFraction` over zero pairs returns `1.0` — the loop's initial
`sel := 1.0` never gets multiplied by anything — so the naive fix reproduces
the pre-fix `EstimateRows(j.Outer)` behavior exactly, just through more code.
This was caught by instrumenting the naive version against a hand-built
SEMI-NLI fixture before committing to it (see Verification below); a fix
that only passed pre-existing tests would not have surfaced this, since none
of them exercise an NLI SEMI/ANTI node's *narrowing* — only its pass-through
behavior (`TestEstimateRowsNLIndexJoinCarriesOuter`).

## Fix

The equi-condition an NLI is keyed on lives in `Inner.Key`/`Inner.Keys`
(bound to `Index.Columns` in declared order, `nliInnerProbe`'s contract) —
so the key term must be computed from there, not from `Predicate`.
`nliSemiMatchFraction` (new, `cardinality.go`) does this directly:

1. `nliInnerProbe(j.Inner)` recovers the index and its bound key
   expression(s).
2. For each bound key `i`: resolve the OUTER key expression's column stats
   against `j.Outer` via the existing generic `resolveBaseColumn` (handles
   any wrapper chain — `Filter`/`Project`/`Join`/nested `NestedLoopIndexJoin`
   — the same resolver `keyColumnStats`/`columnNDistinctForChild` already use
   for the `*Join` arm), and resolve the bound index column's stats directly
   against `j.Inner` — an `*IndexScan`/`*IndexOnlyScan` is itself a
   `resolveBaseColumn` leaf (`baseColumnOfTable`), so no merged-coordinate
   arithmetic is needed on that side either.
3. Feed both sides' `(stats, ndistinct)` into `eqjoinselSemiCore` — the exact
   same core formula `semiPairMatchFraction` uses for the `*Join` arm,
   including the `ResolvedNDistinct` negative-fraction handling and the
   unique-index override, and the same `innerRows` clamp on `nd2`
   (`m0137-0013`'s ledger companion, `C-05 plan-node-semi-nd2-rel-rows`) —
   here sourced from `tableRows(idx.Table)` rather than a search-time
   `RelOptInfo.Rows`, since no `RelOptInfo` exists at the plan-node level.
4. Multiply the per-key fractions (mirrors `semiJoinMatchFraction`'s loop).

`estimateNLIndexJoin` then applies `joinResidualSelectivity` on top for any
genuine leftover residual (same as the `*Join` arm), clamps, and inverts for
ANTI.

Deliberately NOT reused: `semiJoinMatchFraction`/`semiPairMatchFraction`/
`rightExprStats` themselves — they assume the merged left‖right coordinate
convention (`ColumnRef.Index >= leftWidth ⇒ subtract and resolve against
Right`), which does not hold for an NLI's asymmetric Outer/Inner shape and
would silently mis-resolve whenever the bound index-column position happens
to numerically collide with the outer relation's width.

## Verification

- `go build ./...` clean.
- `go test ./internal/optimizer/...` — full package, all pre-existing tests
  pass unchanged (no row-count regression on any existing fixture).
- New test `TestEstimateRowsNLIndexJoinSemiScalesByMatchFraction`
  (`cardinality_propagation_test.go`): same inputs as the existing `*Join`
  sibling test `TestEstimateJoinSemiScalesOuterByMatchFraction`
  (nd1=1000, nd2=100 → sel=0.1), wired the NLI way (`Inner.Key`, not
  `Predicate`). Confirms SEMI narrows 1000→100 and ANTI gives 1000→900;
  both assertions would fail under the naive synthetic-`Join`-on-`Predicate`
  approach described above, since that leaves the estimate at 1000.
- `scripts/tpch-spotcheck.sh`: RESULT=PASS (Q12=2, Q13=34).
- `scripts/tpcds-sf025-regression.sh sweep`: PASS=96 MISMATCH=0 CKMISMATCH=0
  ERROR=0 SKIP=3 (unchanged from baseline); plan-shape channel
  `same=99 changed=0` — no plan in the SF0.25 corpus currently selects an
  NLI SEMI/ANTI node whose row estimate this changes enough to move a plan
  choice at this scale factor, so this shows up as a pure correctness fix
  with no corpus-visible shape effect yet, not as a regression.
- `make ea-ratchet`: baseline findings 112 → current 112 (PASS, unchanged) —
  consistent with the sweep's plan-shape finding above.

## Scope note for the next reader

This closes the cardinality half of `m0137-0013`. It does **not** touch
`M0142-0005` (the Memoize/probe-multiplier interlock) — that gap is about
which plan *shape* the search picks for an NL-index probe, orthogonal to
whether a SEMI/ANTI node's own row estimate is correct once chosen.
