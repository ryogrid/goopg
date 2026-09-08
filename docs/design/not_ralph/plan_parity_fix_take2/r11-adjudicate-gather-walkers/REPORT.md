# R11 — 15 → 8, and most of it was one bug in three walkers

*Round 11 of `../TODO.md`. Adjudicates R10's failing set. Landed the
walker fixes; the flip itself stays reverted pending the remaining 8.*

## 1. Verdict

R10 reverted rather than bulk-updating 15 tests. Adjudicating them
found that **the majority were not testing what they appeared to be
testing**: a single defect — walkers that cannot descend through a
`Gather` — accounted for 7 of the 15, and one of those walkers is
**production code**.

| stage | failures under the flip |
|---|---|
| R10 (as found) | 15 |
| after `rfjJoins` gains `Gather`/`GatherMerge` | 11 |
| after `seamLeafLocalFilters` gains them | 7 (+1 executor) |
| after **`boundaryWalkChildren`** gains them | **8** |

All three fixes are landed. They are **green at the shipping default**
(the knob is still off), so they ship on their own merit.

## 2. The bug that made R10 look catastrophic

R10's failure messages read like a broken planner:

```
searched tree has 0 joins, want 2 for 3 relations
found 0 leaf-local filters, want 1
an ON qual was dropped, which is a cross product, not a slow plan
```

None of that was happening. `rfjJoins` (`relfromjoinlist_test.go:157`)
switches on `*Join`, `*Project`, `*Filter`, `*Sort` — and **nothing
else**. Once partial paths are admitted, a `Gather` sits at the root of
the searched tree, so the walker stopped at the first node and reported
zero of whatever it counted. `seamLeafLocalFilters` had the identical
gap.

**The values gate proved the planner was fine before any of this was
diagnosed**: TPC-H results under `GOOPG_GATHER_PATHS=all` are 22/22
byte-identical to the default-off baseline. A "cross product" that
returns identical rows is not a cross product. Running the cheap
decisive check first is what kept a walker gap from being written up as
a search regression.

## 3. The production one

`boundaryWalkChildren` (`createplanroot.go:414`) is **not** a test
helper. Its own contract:

> *"The listed set is every kind that can sit between a statement's root
> and a spliced searched subtree."*

Under the flip a `Gather` sits exactly there, and the enumeration did
not include it — so the walk ended at the Gather and never reached the
boundary. Two production callers (`narrowoutput.go:610`,
`createplanroot.go:392`) depend on it.

Both wrappers are single-child pass-throughs, so enumerating them is
correct regardless of the knob, which is why this lands now rather than
with the flip.

## 4. The remaining 8, classified

`remaining-under-the-flip.txt`. These are **not** walker artefacts and
each needs its own judgement:

- **Superseded stage pins (2)** — `TestPartialPathIsNeverTheFinalPath`
  ("join rel has partial paths before C-19d") asserts a staging
  invariant this work removes on purpose;
  `TestC19fPathModelGatherExecutesAsAParallelHashJoin` fails on *"mode
  off produced a Gather; the control arm is not serial"* — its control
  arm reads the process default and must force `off` explicitly once
  the default moves.
- **Generated artefact (1)** — `TestFlagProvenanceEnvIsGenerated`:
  regenerate `scripts/planner-flags.env`, do not hand-edit.
- **Needs real adjudication (5)** —
  `TestSeamPlansARightLinkInsideOneSearchProblem` (*"2 join preserves 1
  leaves and null-extends 1, want 1 and 3"* — an outer-join
  null-extension claim, the most likely of the set to be a genuine
  defect), `TestSplitEqualityForHashMultiKey` (*"fell back to Nested
  Loop"*), `TestOwnedBuildPoisonPrebuiltBoundary`,
  `TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`.

## 5. Why the flip still is not landed

Five tests make substantive claims that could be real regressions, and
one is about outer-join null extension — the class where a wrong answer
is silent. R10's reasoning holds for those five exactly as written; it
simply no longer applies to the ten that have been explained.

## 6. Filed

- **R12 — the remaining 8**, in the three groups above, starting with
  `TestSeamPlansARightLinkInsideOneSearchProblem`. Then land the flip
  and run R10 `DESIGN.md` §5's gates.
- Worth noting for that round: TPC-H **values are already known
  identical under the flip** (§2), so a values break there would be new
  information rather than a first look.
