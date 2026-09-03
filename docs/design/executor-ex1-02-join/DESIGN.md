# EX1-02 Design — Consumer walk past Join and Project

Item: `TODO_EXECUTOR.md` EX1-02 (gate: EX0-04 decode slice down on
witnesses; values + pin). Status: design for review.

## 1. Remainder vs EX1-01

EX1-01 narrows Q6-class (Aggregate→Filter→SeqScan) and terminates to
full at every Join and non-identity Project — so Q9's scans (decode
30.2% of window) never narrow. EX1-02 pushes the existing walk through
exactly those two terminators, SeqScan-path only. Index/Bitmap/IOS
threading is the sequenced follow-up EX1-02b (different decode paths +
outer-coordinate risk — not "trivial", not this commit).

## 2. Mechanism (merged-space remap — keys are NOT side-local)

Join keys and residuals live in MERGED (left ++ right) space
(`createplanjoin.go:205-246` re-basing, `:327-328`, `:371-399`;
executor evaluates against the merged row, `join_merge_key.go:67-70`).
There is no remap-free narrowing. The single mapping rule, applied to
keys, residual refs, AND above-join refs alike:

- `leftWidth = len(Left.Output())`. Canonical source: `Join.Predicate`
  split by the `exprSide` cutoff (`Index < leftWidth` → left as-is,
  else right at `Index - leftWidth`; `planner.go:6097-6107`) —
  uniformly for keys and residual (they are folded into one
  Predicate); `LeftKey`/`RightKey` are the same merged-space
  information, not a second source.
- Above-join refs map through the join's OUTPUT layout: inner/left/
  right/full output is left++right (split by leftWidth); semi/anti
  output is left-only (all above refs to the left; the right side
  narrows by keys+residual only). Any other/unknown shape →
  decline BOTH sides to full.
- Range-check every mapped index against its side's output width;
  unmappable (negative, out of range, non-ColumnRef) → full on that
  side. Forgetting a consumer is wrong answers (`scan_deform.go:119-
  123`); the safe direction is full, never narrow-on-keys-only
  (the first draft's dropped-above-refs hole — recorded, not shipped).
- Each side's bound = union(remapped above refs on that side,
  remapped keys/residual on that side, below-walk refs), still
  prefix-truncated on full-width rows. NLI outer follows the left-side
  rule (`NestedLoopIndexJoin.Outer`); NLI inner rescans stay OUT
  (EX1-02b).
- Project-through: a `Project` whose every `Target` is a bare
  `ColumnRef` maps the bound through (`bound = max(mapped indices)`,
  permutation-safe under prefix truncation); any non-bare target
  (expression, funcall, const widening past child width) resets to
  full. (No Project resjunk in goopg — resjunk-ctid lives on
  LockRows/rowmark.)
- Aggregate/Distinct/terminators/whitelist/poison: unchanged from
  EX1-01. `deformBoundNone/Full` stickiness, Gather closure capture,
  tail-poison flag: unchanged, extended by the same tests.

## 3. Why values cannot change (same argument, wider walk)

Every new propagation is still prefix-truncation on full-width rows,
and the walk now unions (not intersects) every consumer class: keys,
residual, remapped above-refs, below-walk refs. Exclusion requires
PROOF of non-reference (whitelist decline on anything else).
Sequencing debt, recorded: "whole-row deform survives only on
WHOLEROW/all-keys" stays aspirational until EX1-02b (index/bitmap
paths); the witnesses this item must move: Q9 (join-side narrowing);
Q4 is 02b-gated (NLI/index-heavy) and is NOT claimed here. Expected
Q9 movement is single-digit % of window (wide referenced set) — the
value is generality + TPC-DS breadth.

## 4. Verification (gate — EX1-01 strength)

- Unit: join-key mapping (both sides, width-cap), project-through
  (bare-refs map, expression resets, reorder resets), poison runs over
  the new arms, Q9-shape bound test (bound < ncols under a join).
- TPC-H values-diff 24/24 MATCH (`tpch-runner -diff` pre/post);
  TPC-DS sweep PASS=95; plan-gate 22/22 `changed=0`.
- Timing + alloc arms on Q6 (must not regress — Q6 chain has no join
  below... the walk change must leave the Q6 bound identical: assert
  the Q6-shape bound test pins 3/8) + Q9 (decode slice down; width
  re-recorded).
