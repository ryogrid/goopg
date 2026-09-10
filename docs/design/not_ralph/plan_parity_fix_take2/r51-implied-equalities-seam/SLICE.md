# R51 Slice — implied-equality seam switch (plan, 2026-09-10; review pending)

*Design basis: `K26-join-order-implied-equalities.md` §§7–9 (mechanism,
obstacle chain, falsification). This slice lands the candidate-generation
half; the costing half stays open per K26 §9.2.*

## 0. Change (ONE line + adjudications)

`joinsearchseam.go:458`: `inferEquivClassConstants(conjuncts)` →
`inferTransitiveEqualities(conjuncts)`. Both are the same
`inferEqualitiesClosure` with the `emitTransitive` flag false/true
(`equiv_class.go:141-147`), so the switch is a flag flip, not new code.

Current-tree re-verification (K26 was measured 2026-09-09; tree moved
R44–R50 since):

- `nliProbeKeys` + its K26 comment live in `createplannl_test.go:309`;
  `TestPreDPPinnedSemiKeysResolveAfterDP` uses it (2 call sites in
  `predp_test.go`). The §9.1 kept-fix is in the tree — confirmed
  2026-09-10 before flipping.
- Transitive half's other caller unchanged: `pushPredicatesIntoCrossJoins`
  (`pushdown.go:47`, M0119-0011).

## 1. Expected blast radius (from K26 §7/§9, to be re-measured)

- 3 tests broke on 2026-09-09: `TestPreDPPinnedSemiKeysResolveAfterDP`
  (then a nil-deref in `nliIn`; now via `nliProbeKeys` — expected PASS),
  `TestSlice3LiveQ9ShapeDerivation` + `TestSlice3SelfJoinInDerivedTable`
  (keep-assertions; adjudicate against PG per R14 precedent, re-baseline
  only if PG agrees the new shape).
- Corpus probe (2026-09-09): TPC-H join-order 18→18 (NOT falling —
  costing half open), join-method 12→10, agg 10→11. Re-measure both
  corpora; success = join-method movement and/or join-order fall, ZERO
  unadjudicated flips. Match count is not a criterion (K26 §5).

## 2. Values bind (same gates as R49/R50)

- TPC-H canonical digest 24/24 MATCH + Q12/Q13 spotcheck (planner change
  moves plans — the digest binds).
- TPC-DS SF0.5 sweep all-zero (foreground per memory
  `sf05-sweep-must-run-in-foreground`).
- Units: `internal/executor` + `internal/optimizer` suites green (no
  `-count=1`).

## 3. Out of scope (explicit)

- The costing half (DP choosing PG's order from the same candidate set —
  K26 §9.2). If join-order stays 95/18 after this slice, that is the
  confirmed next item, not a failure.
- C-04a nullable-side guard interaction: the switch sits at line 458,
  BEFORE the C-04a outer-link block (line 461+), so the closure still
  never merges across a link. Any new decline naming the seam is a
  finding, not background noise.
- `GOOPG_PGSHAPED_DP_TRACE=1` re-verification that `{part}|{partsupp}`
  is enumerated on Q9 (K26 §5's verify-first rule).
