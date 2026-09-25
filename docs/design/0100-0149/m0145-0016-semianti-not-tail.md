# `semianti-not-tail`: the leaf reorder, scoped (M0145-0016)

Status: **BUILT AND LANDED 2026-09-21.** `semianti-not-tail` is 0 on the
corpus and Q78 is oracle-verified. The subsumption check (step 0, previous
loop) had established the remap was genuinely in scope.

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

## What made it tractable — one translation point

The inventory above reads as a large change, and it is not, because the
walk-to-problem index translation lives in exactly ONE place:
`remapWalkOrderFlatToSpans`' `problemIndex` closure, which already encoded the
pulled-splice shift. A permutation is a second translation composed there, not
a new mechanism threaded everywhere.

The property that makes the composition safe is that the permutation is
**column-neutral**. `buildLeafSpans` assigns real spans in position order and
synthetic spans out-of-band after the real total; a STABLE partition preserves
both relative orders, so every span's `lo` is unchanged and only the index
holding it moves. Nothing is renumbered — which is why the quals already
rebased into walk-order-flat space survive untouched.

## What landed

- `stableSyntheticTailPerm` — the stable partition (real leaves keep relative
  order, synthetic leaves move to the tail). Q78's `[real, synthetic, real]`
  maps to `[0, 2, 1]`, matching the measured `synthetic=0x0002 want=0x0004`.
- `permuteRelSet` / `applyLeafPerm` — the remap over the inventory above. The
  shared `sjinfo` is mutated in place, as its own comment requires.
- `identityLeafPerm` — every chain whose synthetic leaves are already the tail
  takes the identity, so the machinery costs those chains nothing.
- `remapWalkOrderFlatToSpans` gained the `leafPerm` argument.

## Measurement

Seam decline census, TPC-DS SF0.25, knob arm, all 99 queries:

```
                              before   after
semianti-not-tail                  6       0
leaf-count                        19      19
residual-hits-pad                  6       6
outer-over-derived                 6       6
semianti-not-tail-with-pulled      -       0
semianti-perm-desync               -       0
```

**Correctness — the task's own acceptance bar, met.** Exactly Q78 moves across
all 99 plans (knob arm, against a pre-change binary from a worktree at HEAD),
and Q78 is verified against the **PG oracle**, not merely row-counted:

```
goopg before  15 rows  ck 4a8a89aae2584676   4041 ms
goopg after   15 rows  ck 4a8a89aae2584676   3855 ms
PG 18.3       15 rows  ck 4a8a89aae2584676
```

The admitted problem reaches the search, and the search elects a parallel shape
for the `ws` CTE (`Gather` + `Finalize HashAggregate` where a serial
`Nested Loop` over a `Hash Anti Join` stood). Worth noting for whoever owns
cost work: the estimated cost ROSE (Limit 4724 -> 8184) while measured time
fell, which is the same estimate/measurement disagreement M0145-0013 recorded.

## Both arms, not just the knob arm

The scoping assumption carried into this loop was "knob arm only". It is
wrong, and the gate caught it: `tryPGShapedJoinSearch` is the SEAM, shared by
both pipelines, so the `semianti-not-tail` decline — and its repair — exist on
the default arm too. The SF0.25 default-arm gate reports
`PLAN-SHAPE: same=98 changed=1`, and the sole mover is Q78, the same query, at
`PASS 15 rows ck=c06cf981a7819a37` against the git-tracked PG oracle with
`verdict-changes=none` and `runtime-moves=0`.

That is the correct outcome rather than a surprise to paper over: the decline
was never pipeline-specific, so neither is lifting it. It does mean the change
is load-bearing on the default arm and its acceptance rests on the oracle
comparison, which is exactly why the task specified one.

## Scope bound, deliberately kept

A chain that needs the partition AND has pulled leaves spliced in still
declines, as `semianti-not-tail-with-pulled`. With both present the
non-synthetic set is itself two populations — emitting FROM items and
non-emitting pulled bodies — whose relative order `splicePulledLeaves`
established at `nReal`, and a single stable partition does not preserve that
three-way split. No corpus shape exercises the combination (the census shows 0),
so it is declined rather than guessed at. Ledgered.
