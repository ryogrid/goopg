# Cost-side narrowing — implemented, measured, **REVERTED**

Date: 2026-09-06. Fourth and final measurement in the chain
D-04 → entry-width → bucket-charge → this. Patch preserved at
`tmp/d05p3-costside-narrow.patch`.

**This one names the real defect.**

## The defect, confirmed with a live witness

`pathNCols`/`pathAvgVarBytes` fall through to the whole relation for every
path except index-only, so `hashJoinCost` prices a build the executor never
builds:

| chosen build | COST ncols / avgVar / entry / nbatch | EXEC ncols / avgVar / entry / nbatch |
|---|---|---|
| Q9 `orders` | 9 / 74.3 / **530.3** / **8** | 2 / 0.0 / **120.0** / **4** |
| Q9 `partsupp` | 5 / 124.6 / 388.6 / 4 | 3 / 0.0 / 168.0 / 2 |
| Q14 `part` | 9 / 97.8 / **553.8** | 2 / 20.6 / **140.6** |

## The fix and its error direction

The exact keep-set is derived over the *chosen* tree, so it does not exist
at costing time. The approximation used the statement-wide `NeededCols`
filter over the build subtree's leaf schemas, composed additively.

**It errs HIGH, never low** — under-stating is the OOM direction. Proof
chain: the keep-set is `OutputCols ∪ ancestor-quals ∪ parent-quals`,
`outputColumnNames` is a documented subset of `neededColumnNames`, and both
fallback arms of the executor's narrowing *are* that filter. Four
deliberate additional over-statements (nested already-narrowed builds
counted un-narrowed, index-only children keeping their own figure,
unattributable columns declining to the relation sum, empty/full keeps
declining entirely).

After the fix the cost side reads Q9 `orders` 2 / 0.0 / 120.0 / 4 —
**exact agreement with the executor**.

## Result: correct, and it costs 10%

| arm | best of 2 reps |
|---|---|
| base | **123.60 s** |
| fix | **136.36 s (+10.3%)** |

Base A/A spread is 1.5%, so this is far outside noise.

Six queries moved shape:

| query | base | fix | Δ |
|---|---|---|---|
| Q5 | 3.67 | 11.81 | **+215.8%** |
| Q10 | 2.62 | 6.81 | **+162.2%** |
| Q9 | 6.54 | 11.63 | **+73.5%** |
| Q7 | 5.42 | 8.67 | **+62.4%** |
| Q18 | 31.22 | 25.08 | −19.4% |
| Q12 | 11.70 | 10.23 | −11.6% |

Values 24 MATCH on all five pairings. TPC-DS SF0.5 **PASS=95 MISMATCH=0
CKMISMATCH=0**, 1 of 99 plans changed, total delta −0.1%. Plan parity moved
**toward** PG (`join-method` 12→10, `scan-type` 11→10), and the two queries
that got *faster* went Merge Join → Hash Join, which is PG's shape.

## Why it loses — the finding

Q5, Q7, Q9 and Q10 all flip **which side is built**. Narrowing shrinks a
wide table proportionally more (`lineitem` 16→4 columns against `orders`
9→2), and it applies only to the **inner**, so the orientation that builds
on the small side still pays `2 × outerPages` at `lineitem`'s full width
while the orientation that builds on the big side got its inner spill
charge cut ~4×.

**The inflated build width was masking a different mis-price.**
`hashJoinCost` charges a large build only `cpu_tuple_cost × rows` plus the
child cost. It models neither the five private per-worker copies (sharing
is declined for a spilling build) nor the real table memory. Remove the
accidental deterrent and the model happily picks a 6 M-row build.

## The coupling hypothesis was CONFIRMED

Re-applying the bucket-charge patch on top: **Q14 no longer flips** —
0.42 s base → 0.43 s, against 14.55 s (+3364%) when the bucket charge was
landed alone. The phantom 9-column build is gone, so the honest
`MapSlotBytes = 96` no longer pushes it past the budget. Plans byte-identical
to the fix arm.

So the bucket charge is now *free*, and the combination still costs +11.1%
— all of it the cost-side fix's own collateral.

`TestSlice3LiveQ9ShapeDerivation` fails under the combination only, with
the same build-side-flip signature. It is a ready-made guard.

## Verdict: revert, and name the next blocker

A 10% loss for "the model now says what the executor does" is not a trade
worth taking **while the reason it loses is a different unmodelled term**.

**The next blocker, new and precisely located: `hashJoinCost` under-prices
BUILDING a large hash table.** The un-narrowed width was an accidental
deterrent doing that job. The honest sequence is:

1. Charge what a build actually costs — the per-worker private-copy
   multiplier for a declined shared build, and the bucket array in the
   **resident** term, not only the spill term.
2. *Then* narrow the width, and re-apply both preserved patches.

Q5/Q7/Q9/Q10's build-side flips are the witness set;
`TestSlice3LiveQ9ShapeDerivation` is the guard.
