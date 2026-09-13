# R124 SCOPE — narrow non-table leaves: the decline they hit costs a statistic that does not exist

R123 resolved TPC-DS's confound to one cause with a measured identity:
**32 non-table leaves** — `CTEScan` 14, `Project` 9, `SetOp` 7, `Filter`
2, and **zero ordinary tables** — decline on arm (c) `nil ColVarBytes`,
and each poisons every join above it, producing **all 42,679** mixed
pairs (34.2% of join costings; admitted-on-arrival 59.6%).

This is the pre-registered row-2 lever.

## 1. The cut is smaller and safer than it looks

`relNarrowedWidths` declines when `len(rel.ColVarBytes) == 0`
(`narrowcostinputs.go:99-101`). The decline exists to prevent
contributing a **silent zero** where a real statistic exists but cannot
be attributed to a kept column — it fails HIGH, which is the direction
that cannot under-size a hash build.

**For these 32 rels there is no statistic to lose.** On a level-1 search
rel both fields are set together and only under the table guard
(`joinsearch.go:403-411`:
`ri.table != nil && ri.table.Stats != nil && len(Stats.Columns) > 0`),
so a non-table leaf has `ColVarBytes == nil` **and** `AvgVarBytes == 0`
— the zero value, never assigned.

So today's un-narrowed fallback for such a rel is already:

```
ncols   = len(rel.baseLeaf.Output())   (relNCols fallback, path.go)
avgVar  = rel.AvgVarBytes = 0
```

**The cut therefore changes exactly one number.** Narrow `ncols` to the
keep-set while carrying `avgVarBytes = 0` — the *same* variable-payload
figure the code already uses for these rels. No estimate is invented, no
statistic is dropped, and the currency is unchanged. `EntryBytes` goes
from `48*full + 24` to `48*kept + 24`, which is strictly closer to the
truth in the model's own units.

### The guard that keeps it honest

Narrow on nil `ColVarBytes` **only when `rel.AvgVarBytes == 0`**. If a
rel has a nonzero relation-wide figure but no per-column map, the
statistic exists and cannot be attributed — the original decline is
correct and must stand.

**Correction to an earlier draft of this section:** "the two are always
assigned together" is **false**. Five sites assign `AvgVarBytes` with no
`ColVarBytes` today — `upperrel.go:187`, `groupingpaths.go:145`,
`distinctpaths.go:112`, `windowsetoppaths.go:141` and `:323` (all
`nodeAvgVarBytes(cols)`). The combination exists in the codebase right
now. What is true is narrower: it **cannot arise on a level-1 search
rel**, whose only assignment site is `joinsearch.go:403-411`, and
`relNarrowedWidths` has exactly one production caller
(`narrowBaseRelCostWidths`, `relfromjoinlist.go:723`) which iterates
`s.joinrels[1]` only.

So the guard is a live trip-wire, not a dead one: **any successor that
extends narrowing to upper rels will trip it on real rels** and silently
lose the narrowing there. Pin it, and record that a firing guard is
correct behaviour rather than a bug.

### What is NOT claimed

`avgVar = 0` is an under-statement for a leaf that really emits text
columns. **This round does not fix that and must not pretend to** — it
is the pre-existing state for all 32 rels on both the narrowed and
un-narrowed paths, so the cut neither improves nor worsens it. A real
per-column width basis for CTE/sub-problem outputs is a separate, larger
piece of work (the sub-problem knows its own narrowed widths internally;
a `CTEScan` and a `SetOp` do not, and inventing one is forbidden).

`OutputWidth` is NOT affected by this gap: `TupleWidth(kept)` derives
from the kept columns' declared types, which a non-table leaf's schema
carries. (`tupleWidth` clamps to ≥ 1, `relsize.go:394-396`, so a
degenerate schema cannot break the all-three-or-none rule.)

### The predicate keys on the STATISTIC, not on leaf kind

Worth stating plainly because the round is *named* after non-table
leaves: the new arm is `nil ColVarBytes && AvgVarBytes == 0`, which also
admits an **ordinary un-ANALYZEd table** (`joinsearch.go:403`'s guard
fails for `Stats == nil` too). That is deliberate and the same argument
covers it — such a table's `avgVar` is likewise 0 on both the narrowed
and un-narrowed paths, so no estimate is invented there either. But it
retires part of the rationale in `narrowcostinputs.go:85-89`, which
lists "un-ANALYZEd table" alongside CTE/subquery as the population the
decline protects. **P1's exact counts (32 → 0, 590 → 622) assume zero
un-ANALYZEd tables in the corpora**, which R123 measured to be true at
this epoch and which a different dataset could falsify.

## 2. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | Flag OFF bit-identical to pre-round HEAD, both corpora | leak ⇒ STOP |
| P1 | Non-table leaves narrow: TPC-DS `REL-DECLINE arm=c` drops 32 → **0**, `REL-NARROW` rises 590 → **622**; the `AvgVarBytes != 0` guard is pinned and never fires on either corpus | arm (c) nonzero ⇒ the cut missed a shape; a guard hit ⇒ investigate before proceeding |
| P2 | **The FULL bucket vector**, not just the mixed share: MIXED 42,679 → near zero; the three buckets still sum to the same denominator; and **BOTH-UNNARROWED (8,588) must NOT rise**. Re-measured with R123's committed counter shape, zero-valued arms emitted explicitly | mixed still >10% ⇒ re-audit; **BOTH-UNNARROWED rising ⇒ NEW mixed pairs were created and must be attributed, NOT read as "R123 was wrong"** — R123 root-attributed only the MIXED bucket, never the 8,588, so a pair in a `NeededColsKnown`-true problem whose other side declines for a different reason (rule-4 wrapper gap — R123's own TPC-H mechanism) becomes MIXED after this cut |
| P3 | Values unchanged: TPC-DS SF0.25 sweep PASS=96 MISMATCH=0 with the arm stamped; TPC-H digest on any query whose plan moves | ANY values move ⇒ STOP |
| P4 | No parity regression: TPC-H match ≥ 6 **and R122's gains hold** (`join-method` 9, `scan-type` 8); TPC-DS match ≥ 2; no category rises on either corpus | a match lost or category rises ⇒ STOP |
| P5 | **TPC-DS categories become READABLE and are reported either way.** With the currency uniform, a category move can finally be attributed to narrowing rather than to the confound | — |

**Bound on what P5 can reveal (N5):** expect **no `aggregation-strategy`
movement** from these 32. `costAgg`'s spill arm is gated
`armLive := inAvgVarBytes > 0` (`cost_funcs.go:502`), which stays false
at `avgVar = 0` on both arms, so R120's arm cannot fire for them either
way. Do expect `sort-strategy` exposure: `costSortRunWithWidth` consumes
`pathNCols`/`pathWidth` (`joinpathsmerge.go:493`).

**No prediction claims a TPC-DS parity gain**, and the honest prior is
that there will not be one: R122/R123 both sized the ceiling — TPC-DS's
dominant categories are `join-order` (90) and `parallelism` (86), which
this chain does not touch, so the reachable upside is a subset of
`join-method` (62) and `scan-type` (57). P5 exists so that a null is
reported as a *readable* null rather than a confounded one.

## 3. Hazards

- **B4 is avoided in the `ncols` dimension and CREATED in the `avgVar`
  dimension. Half of the earlier draft's claim was wrong.**
  R123's SCOPE §6 warned that a cost-only set on a collector-declined
  statement inverts `joinKeepSet ⊆ buildKeepSet ⊆ neededKeepSet`. That
  warning was about **arm (a)**; this round touches only **arm (c)**,
  where `NeededColsKnown` is TRUE, so `narrowBuildInput` really does run
  and really does emit a `Project` — its final line is unconditional
  (`narrowoutput.go:65`), and for a `PathPrebuilt` leaf
  `innerNode.Output()` IS `rel.baseLeaf.Output()`, so even
  `sameOutputColumns` holds. **ncols: consistent.**

  But one level up it inverts, and this is NEW to R124. Take a build
  side `{CTEScan, t}`: its `ColVarBytes` is `unionColVarBytes(nil,
  t.map)` = `t`'s map only, and its `AvgVarBytes` = `0 + sum(all of t)`
  (`joinsearchlevel.go:633-643`). The executor's `buildAvgVarBytes`
  (`entrywidth.go:59-68`) meets a CTE-derived column absent from that
  map and **declines to the whole-relation sum**. Before this round the
  join path declined too (rule 1, `narrowcostinputs.go:253`), so the
  planner fell back to `joinrel.AvgVarBytes` — *exactly the executor's
  value*, and they agreed. After this round the planner publishes
  `0 + kept-bytes(t)`, which is **below** what the executor charges, by
  the dropped table columns' widths (Q9-scale ≈ 74 B/row).

  Cost-only, nothing under-sized (path fields never reach the executor —
  it reads `plan.AvgVarBytes` from `buildAvgVarBytes` over **rel**
  fields, `createplanjoin.go:572` → `operators_join_agg.go:796`). But it
  is planner under-pricing, i.e. R120's defect in reverse reached by a
  different door, and P4 is the only thing checking it. Add a pin: a
  joinrel with a nil-`ColVarBytes` child must not publish an `avgVar`
  below what `buildAvgVarBytes` would charge — or record the divergence
  as knowingly accepted.
- **Under-stated `avgVar` now reaches more paths.** Narrowing these 32
  rels means their (already zero) `avgVar` propagates into join sums via
  R122's Slice B. The absolute error per row is unchanged, but it now
  participates in more comparisons. Since the alternative is an
  un-narrowed `48*full + 24`, which is *further* from the truth, this is
  an improvement — but P4's no-regression bar is what actually checks it.
- **Second mechanism exists.** R123 found TPC-H's 3 mixed pairs root at
  `wrapperGap-indexOnlyChild`, not at this cause. This round does not
  address those and must not claim to.
- goopg's Sort does not project (carried, still undischarged):
  `sort-strategy` stays on P4's watch list.

## 4. Gates (FOREGROUND)

1. Suites green; `go vet`.
2. Pins: a non-table leaf with `AvgVarBytes == 0` narrows; the
   `AvgVarBytes != 0` trip-wire still declines (the existing `ncRel`
   fixture sets `AvgVarBytes = 999`, so the current
   "no ColVarBytes" decline pin converts into this trip-wire for free);
   a table with real stats is unaffected; OFF-inert; the
   all-three-or-none triple still holds; and the N1 pin — a joinrel with
   a nil-`ColVarBytes` child must not publish an `avgVar` below what
   `buildAvgVarBytes` would charge, or the divergence is recorded as
   knowingly accepted.
   Implementation detail: the new arm must still run the bounds check
   and build `kept` for `TupleWidth(kept)`; only the `ColVarBytes`
   lookup and the `avg` accumulation are skipped, and `avgVarBytes` is
   written as a **literal 0** rather than left to defaulting.
3. `scripts/tpch-spotcheck.sh` PASS.
4. Values P3, sweep artefact stamped with the arm.
5. Parity both corpora OFF and ON; re-take the OFF baseline (R123's runs
   opened another epoch). TPC-H via `estimate-audit -plan-only`; TPC-DS
   via `capture-tpcds.sh`; `work_mem` pinned.
6. **Re-run R123's census** (P1, P2) using the same counter shape, and
   **commit the raw dump** as R123 did.
7. `make plan-gate`: run or record as a reasoned omission.
8. REPORT.md → agent review → `commit -n` + push.
