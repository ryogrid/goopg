# R41 — R40 desynchronised leaf numbering from binding order (K72/K74)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Status: DESIGN, **rev 2 after adversarial review** — review refuted rev 1's
root cause and shrank the fix from a coordinate remap to a renumbering.
Successor to R40, which converted Q78's 3 `outer-link-no-sjinfo` declines
into 3 `leaf-count` declines.*

## 1. The measurement (instrumented, not inferred)

A temporary trace at the decline site (reverted before commit), identical
for all three Q78 CTEs:

```
DPTRACE LEAFCOUNT nprefix=3 nscans=2 jl=2      (and the standing trace: nrels=2)
```

- `nrels=2` — `len(ctx.bindings)` (`joinsearchseam.go:226`).
- `nscans=2` — `extractSearchLeaves` yields 2 leaves: the opaque ANTI
  `*Join` node and `date_dim`.
- `jl=2` — the joinlist has 2 items.
- `nprefix=3` — but `jl.nrels()` recurses into the pinned sub-problem
  (`collapse.go:270-279`) and counts `web_sales`, `web_returns`, `date_dim`.

**Three of the four agree at 2. `jl.nrels()` is the only 3.**

## 2. Root cause: R40 broke a documented invariant (corrected in rev 2)

Rev 1 of this design called it a permanent "two coordinate spaces"
condition. **That was wrong**, and the review refuted it with the decisive
fact: `ctx.bindings` is *already* in collapsed space, because R40's own
§4d narrowing skips the binding for a SEMI/ANTI nullable side
(`planner.go:3466-3471` — `leftCtx` is not advanced to `mergedCtx`, and
`planFromItem` returns `leftCtx.bindings`). So `web_returns` gets **no
`rangeBinding` and no columns**.

`bindings`, `scans`, `cumOffsets`, `relidsOfExpr`, `boundaryMap` and
`leafRel` are therefore all in the collapsed space. Only the **joinlist**
and the **SJI scope** still allocate a leaf index for the anti join's
nullable side.

`fromItemRels` (`collapse.go:423`) states the invariant this violates, in
its own comment: *"Exactly the number of `rangeBinding`s `planFromItem`
appends for the same item … which is what keeps leaf numbering and binding
order in step."* **R40 broke that invariant** by removing a binding without
removing the corresponding leaf index. K72 is a defect this round
introduced, not a pre-existing philosophical mismatch — and the honest
framing matters, because it tells the implementer to *restore an
invariant*, not to *build a remap*.

## 3. What the decline is currently protecting against (corrected: a PANIC)

Rev 1 said a naive fix would cause "silently mis-resolved columns". The
review found it is worse and more definite: with `len(ctx.bindings)==2` and
`nprefix==3`, `ctx.bindings[:nprefix]` (`joinsearchseam.go:507`, `:538`) is
an **index-out-of-range panic**. The `leaf-count` decline is the only thing
preventing it; `validateJoinlistProblem` (`relfromjoinlist.go:269-274`) is
a second net but the seam panics first.

So the one-line "compare `len(jl)` instead of `nrels()`" fix is not merely
unsound — it converts a clean decline into a crash. It stays rejected.

## 4. The fix: renumber, do not remap

Because `prob.scans[i]` is **already** the opaque ANTI `*Join` node
(`joinsearchseam.go:509`), a joinlist LEAF naming it resolves correctly
through the existing `leafRel` path with `lo,hi = i,i+1`, the binding is
`web_sales`'s (exactly what the node publishes), and `cumOffsets` /
`boundaryMap` are untouched. No coordinate remap is required.

1. **`deconstructFromItemScoped`** (`collapse.go:447-478`): for a SEMI/ANTI
   link, do not consume a leaf index for `j.Right`, and emit the link as a
   `leafItem(leftLeaf)` rather than a `pinnedItem`.
2. **`fromItemRels`** (`collapse.go:423`): subtract semi/anti links so
   `nextRel` (`:386`) stays in step across comma FROM items.
3. **`newSjiScope`** (`specialjoin.go:275-286`): its `addLeaf`-per-`j.Right`
   numbering must skip the same sides, or every SJI hand to the right of
   the ANTI names the wrong relation.
4. **Drop the ANTI's own `SpecialJoinInfo`** from `join_info_list` — its
   nullable hand has no representable bit. `joinInfoListHas`
   (`relfromjoinlist.go:~380`) and `sjInfosInItemSpace` must not see it.

**New consistency assertion (review's recommendation, adopted):** add
`len(ctx.bindings) == jl.nrels()` at the seam. Today `leaf-count`
incidentally catches a desynchronisation; after this change nothing would,
and the two numberings come from *two separate computations* —
`demotedForPlan` (`planner.go:2915`) for the plan tree versus the in-place
`reduceOuterJoins(s.FromExprs, …)` (`planner.go:2984`) for the joinlist.
If those ever disagree about which links became ANTI, the assertion is what
turns a panic into a decline. It also retires the `ctx.bindings[:nprefix]`
panic exposure.

## 5. Explicitly NOT generalised to FULL

A pinned FULL item **keeps both bindings** (`planner.go:3470` skips only
Semi/Anti), so collapsing it to one joinlist leaf would create a leaf
spanning **two** binding coordinates — which is where the multi-binding
remap and the genuine `boundaryMap` totality risk live
(`createplanroot.go:223-268`, three panics incl. an unfillable hole at
`:263`). FULL must keep declining via `leaf-count`. Stated here because the
symmetry is tempting and the implementer would otherwise "fix" both.

For SEMI/ANTI specifically the `boundaryMap` risk rev 1 cited is
**unfounded**: the nullable side has no binding coordinate, so the collapse
creates no hole.

## 6. Honest scope statement (unchanged, and it must lead the report)

Even with this landed, Q78 is **not predicted to match**. A 2-leaf search
chooses among `{antiPair, date_dim}` orders; PG searches 3 leaves and can
place `date_dim` below the anti join. Eligibility is necessary, not
sufficient — the relationship R27 §4a recorded for Q49, which became
eligible without becoming matched. Per `METHODOLOGY.md` §2 this prediction
is recorded *before* implementation so a flat match count reads as the
expected outcome rather than a disappointment.

Option B (teach the plan walk to descend into ANTI, giving a 3-leaf search
that can reorder as PG does) remains the PG-faithful end state and is
strictly larger: it needs an ANTI path producer — `pinnedUnsearchable`
(`collapse.go:239-241`) refuses ANTI today — plus an "unpublished leaf"
concept in the coordinate model.

## 7. Gates

Unchanged from R40, and the sweep is binding: precommit units; TPC-H values
digest byte-identical AND plan structure identical across all 22; TPC-DS
SF0.5 sweep `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`; decline
census read **by class, not by total** (R40's lesson: a class can be
converted rather than removed — expect `leaf-count` 3 → 0 with no new class
appearing); parity re-measure both corpora with `shape-delta.sh`.

Because this moves leaf numbering, the change is held uncommitted until the
sweep returns clean — the discipline R27 §4a adopted after finding a
row-dropping bug in this same area.

## 8. Review record

Adversarial subagent review, full HEAD re-derivation. It **verified** the
`nprefix` triple-consumer argument (`joinsearchseam.go:261/:281/:290/:298`),
`nrels()`'s recursion, `extractSearchLeaves`' admitted-type set,
`Join.Output()`'s Semi/Anti arm, `joinPinned(parser.JoinAnti)`, and the
`pinnedOverAPinnedSide` comment.

It **refuted three things**, each of which changed the design:

1. Rev 1's root cause (§2). `ctx.bindings` is already collapsed, so this is
   a broken invariant introduced by R40, not an inherent two-space
   condition. This also cross-checks against our own trace, which printed
   `nrels=2` all along — rev 1 mis-attributed that number.
2. Rev 1's stated hazard (§3): a panic, not silent mis-resolution.
3. Rev 1's cost estimate and its `boundaryMap` risk (§4/§5): the fix is a
   renumbering, the `boundaryMap` risk does not apply to SEMI/ANTI, and it
   *does* apply to FULL — which must therefore be excluded explicitly.

It also answered rev 1's §7 open question: the sub-joinlist recursion
(`relfromjoinlist.go:312/:365`) does exist and is live, but is the wrong
door — `pinnedUnsearchable` refuses ANTI, and the recursion would try to
*search* the pair rather than accept the already-built node. §7 is replaced
by §4's leaf-collapse finding.
