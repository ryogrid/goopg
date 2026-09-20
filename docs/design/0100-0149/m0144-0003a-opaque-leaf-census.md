# M0144-0003a — the opaque leaf is a `*Project`, not a `*Filter`

Status: FIX SHAPE CORRECTED 2026-09-20 by corpus census; implementation is
the next step and its admission predicate is chosen
Kind: recon
Parent: M0144-0003
Milestone: M0144
Evidence: `analysis/m0144/m0144-0003a-opaque-leaf-census.txt`

## 1. What the task assumed

`M0144-0003a` is filed with a specific mechanism and a specific fix:

> Phase B's chain `spineJoins[0]` is the outermost pinned Semi/Anti join whose
> `Left` carries `Filter{sunk}(origChain)`; `extractSearchLeaves` counts that
> Filter as one opaque leaf … **Fix shape: make the walk descend a `*Filter`
> whose conjuncts re-base into the chain's WHERE/qual space.**

The count it cites is `leaf-count×62`, from `M0144-0003`'s own census.

## 2. Census at HEAD

Throwaway instrumented build (probe reverted, not committed) printing every
opaque leaf's Go type at the `leaf-count` decline, over all 99 TPC-DS SF0.25
queries on the private `:5595` lane, `EXPLAIN` only:

```
opaque-leaf node kinds (by occurrence)      per-decline leaf-kind SETS
  Project                 53                  Project                        23
  SeqScan                  4                  Project + SeqScan               2
  NestedLoopIndexJoin      1                  Gather + NestedLoopIndexJoin     1
  Gather                   1

26 declines across 11 queries:
  Q14 Q23 Q33 Q56 Q58 Q60 Q83 Q95   Project
  Q16 Q94                            Project, SeqScan
  Q35                                Gather, NestedLoopIndexJoin
```

**There is not a single `*Filter` opaque leaf in the corpus.** The dominant
kind is `*Project` — 53 of 59 occurrences, present in 10 of the 11 declining
queries, and the *only* kind in 23 of the 26 declines.

The decline count has also moved: **26, not 62**, which is consistent with
`M0142-0008-producer` and the `-plumbing` chain landing between the two
censuses.

## 3. Why `*Project`, and why that is good news

`M0142-0008a-3i-verify` already established where these Projects come from,
while trying to confirm a different hypothesis:

> `x.Right`'s top node is `*Project{Child: *Join{Inner}}`, not a bare `*Join`
> — `unnestExistsExpr` clones the EXISTS body's own already-planned subquery
> tree, and every planned `SELECT` (even `SELECT 1`) carries its own
> top-level output-list `*Project`.

So the opaque leaf is the EXISTS/IN body's output-list Project standing in
front of the body's real leaves.

That is a materially easier fix than the filed one, because a Project carries
**no conjuncts to re-base**. The whole "re-base into the chain's WHERE/qual
space" half of the filed fix shape disappears; what is left is purely
structural — descend, and let the walk flatten the body's own join tree into
real leaves.

## 4. The admission predicate is already written and tested

Descending an arbitrary `*Project` is unsound for the same reason
`inputNodePathkeys` refused one before `M0144-0011a-3`: a Project is where
output positions are re-assigned, and the seam's coordinate machinery
(`buildLeafSpans`, `remapWalkOrderFlatToSpans`, `pgShapedOffsetChecksOK`) is
written against positions.

`M0144-0011a-3` already landed exactly the predicate this needs:

```go
// internal/optimizer/upperorderedinput.go
func projectIsPositionalIdentity(p *Project) bool
```

— target `j` is `ColumnRef{Index: j}` for every `j`, equal widths, not
`IsolatedScope`. Under it a Project re-assigns nothing and can only rename, so
the coordinate machinery sees the same positions before and after the descent.
It has its own refusal-table test
(`TestProjectIsPositionalIdentityRefusesEverythingElse`).

**Decision, recorded before any implementation (AGENT.md C3):** the fix shape
for `M0144-0003a` is *descend a `*Project` that satisfies
`projectIsPositionalIdentity`*, not *descend a `*Filter`*. The `*Filter` arm
is not implemented at all — it has no witness in this corpus.

## 5. What the implementing loop must still establish

Not assumed here, because this loop did not code it:

1. **Do the body's exposed leaves line up with `ctx.bindings`?** `nprefix`
   counts 4 for Q16 while `scans` is 3; the fix only works if the EXISTS
   body's relations are among the `nprefix` FROM items the bindings describe.
   Verify before relying on the arithmetic, with the same byte-offset trace
   technique this census used.
2. **The P0-H11 `cumulativeFromSpans` span round-trip must close in the same
   change** — it is latent only because nothing gets through today
   (`M0142-0008a-3i-reach-verify`), and this is the change that would first
   let a link through.
3. **Q35 is out of scope.** Its opaque leaves are a `*Gather` and a
   `*NestedLoopIndexJoin` — already-planned composites, the
   `M0142-0008a-3i-leafcount` finding — and no Project predicate admits them.
   Expect 10 of 11 queries, not 11.

## 6. Reporting

`Movement: none` — a census and a decision; no production code changed (the
probe was reverted before commit) and no parity arm was run against a change.
