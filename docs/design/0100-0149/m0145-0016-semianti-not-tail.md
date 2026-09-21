# `semianti-not-tail`: the leaf reorder, scoped (M0145-0016)

Status: **VERIFIED NOT SUBSUMED, scoped, not built.** The task's own first
instruction — check whether M0145-0005's slice (a) already retired the decline —
is discharged: it has not. The remap is in scope, and this document is the
inventory the build loop needs so it does not re-derive it.

Task: `.ralph/fix_plan.md` M0145-0016. Kind: impl. Parent: M0145-0005.

## Step 0 — subsumption check: NOT subsumed

M0145-0005 is still `[ ]` with only slice 1 landed, and the corpus agrees.
Seam decline census on current HEAD (TPC-DS SF0.25, knob arm,
`GOOPG_PGSHAPED_DP_TRACE=1`, all 99 queries):

```
seam-decline reason=leaf-count           19
seam-decline reason=semianti-not-tail     6     <- 3 per Q78 run
seam-decline reason=residual-hits-pad     6
seam-decline reason=outer-over-derived    6
```

`semianti-not-tail` fires on **Q78 only**, 3 times, which matches the task's
"3 fires" and its named witness.

## The shape, measured rather than inferred

`traceSeamNotTail` now prints the two masks that determine the fix:

```
SEAMNOTTAIL synthetic=0x0002 want=0x0004 nprefix=2 nleaves=3
```

Three leaves; the synthetic (Semi/Anti RHS) leaf sits at walk position **1**
while the construction contract requires it at position **2**. That is exactly
the `[real, synthetic, real]` walk the task predicts for the demoted-ANTI
mid-chain shape `web_sales ANTI web_returns JOIN date_dim`.

So the permutation is a **stable partition**: real leaves keep their relative
order and synthetic leaves move to the tail — here `[0, 2, 1]`. Stability is
not a nicety. Real leaves carry the emitting column space; reordering them
among themselves would move column coordinates, whereas a stable partition
moves only the POSITION masks and leaves every column offset attached to its
own leaf.

## What the remap has to touch — the inventory

Everything below is indexed by walk position at the point of the check
(`joinsearchseam.go`, after `splicePulledLeaves`, before `spans` is built):

1. **`scans []Node`** and **`widths []int`** — parallel leaf tables; permute.
2. **`walkWidths []int`** — the PRE-splice walk table. This is the dangerous
   one: `remapWalkOrderFlatToSpans(e, widths, spans, pulledBase, pulledLeaves)`
   already encodes ONE leaf-index translation, across the pulled-leaf
   insertion. A permutation is a SECOND translation, and the two compose. Every
   qual rebased into the walk's flat column space — `semiAntiChainLink.pred`,
   `.bodyQuals`, and the chain quals — goes through it.
3. **`semiAnti []semiAntiChainLink`** — `lhs` and `rhs` RelSets, plus
   `sjinfo *SpecialJoinInfo`'s four RelSet fields (`SynLefthand`,
   `SynRighthand`, `MinLefthand`, `MinRighthand`). The `sjinfo` is a SHARED
   pointer whose doc comment says it must never drift from the link's own
   fields, so it is mutated in place, not rebuilt.
4. **`outerLinks []outerChainLink`** — `preserved` and `nullable` RelSets.
5. **`onQuals []chainOnQual`** — `belowNullable` RelSet.
6. **`jl`'s leaf items and `ctx.bindings[:nprefix]`** — the contract that
   `jl` names exactly `[0, nprefix)` and pairs with the real bindings is what
   the partition restores; it must hold after the permutation, not before.
7. **`spans`**, built later from `widths` — the span records move WITH their
   leaves. Their `lo` column offsets must not be renumbered; that is what
   keeps already-rebased quals valid.

## The risk, named

Q78's historical `translateToLayout` panic is this assumption violated, and the
M0145-0014 attempt hit the same class from a different direction: a clause
placed at a join that cannot evaluate it fails at `createPlan`, not at the
seam. So the acceptance bar the task sets — "every admitted shape needs a
values check, not just a green census" — is the right one, and Q78 must be
oracle-verified, not merely row-counted.

A cheap diagnostic learned in loop 46 and worth reusing here: when
`translateToLayout` refuses, print the columns that WERE available at that node
before theorising about which mechanism owns the failure.

## Landed this loop

Only `traceSeamNotTail`, the shape line above. The permutation itself is not
built: it is a coordinate change in the seam's most position-sensitive
function, composing with an existing translation, and the honest unit of work
is to start it from this inventory rather than to half-land it.
