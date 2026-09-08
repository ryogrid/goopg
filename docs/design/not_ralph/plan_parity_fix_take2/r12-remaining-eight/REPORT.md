# R12 — 8 → 7, and my "most likely genuine defect" was not one

*Round 12 of `../TODO.md`. Landed a fourth walker fix; the flip stays
reverted. Tree green at the shipping default.*

## 1. Verdict, and a correction

R11 classified the 8 remaining failures and singled one out:

> *"`TestSeamPlansARightLinkInsideOneSearchProblem` … an outer-join
> null-extension claim, **the most likely of the set to be a genuine
> defect**"*

**It was not a defect.** It was K19 for the fourth time. `rfjLeafCount`
(`relfromjoinlist_test.go:108`) has the same missing `*Gather` case, so
a 3-leaf side containing a Gather counted as **one** leaf, and the
assertion reported a jointype preserving and null-extending the wrong
hands — a correctness-shaped message produced by a correct plan.

Adding the two cases fixes it. 8 → 7.

That is the second time in two rounds that the alarming message was the
walker and not the planner, and the second time I ranked it as the
likeliest real bug. The lesson recorded in K19 — *run the cheap
decisive check before diagnosing* — apparently needs applying to my own
triage ordering, not just to the first diagnosis. Noted rather than
smoothed over, because the same ranking error would have sent the next
round hunting in the outer-join code.

## 2. The remaining 7, now with better evidence

`remaining-under-the-flip.txt`.

**Superseded stage pins (2)** — mechanical:
- `TestPartialPathIsNeverTheFinalPath` asserts "join rel has partial
  paths before C-19d", a staging invariant this work removes on purpose.
- `TestC19fPathModelGatherExecutesAsAParallelHashJoin` fails on *"mode
  off produced a Gather; the control arm is not serial and proves
  nothing"* — its control arm reads the process default and must force
  `off` explicitly once the default moves.

**Generated artefact (1)**:
- `TestFlagProvenanceEnvIsGenerated` — regenerate
  `scripts/planner-flags.env` via `go run ./cmd/gen-planner-flag-labels`.

**Real plan differences (4)** — these are the round's genuine finding:
- `TestSlice3LiveQ9ShapeDerivation` and
  `TestSlice3FilterColumnSurvivesNarrowing` **changed character** once
  the walkers could see through the Gather. They no longer report
  `joins = 0`; they now report *"unexpected narrow build
  [o_orderkey o_orderdate]"* and *"missing narrow build [l_discount
  n_name l_orderkey …]"*. That is a different build side being narrowed
  — a real consequence of the flip choosing a different join shape, not
  a counting artefact.
- `TestSplitEqualityForHashMultiKey` — *"fell back to Nested Loop"*.
- `TestOwnedBuildPoisonPrebuiltBoundary` — narrow-build boundary.

These four need adjudicating **against PG**, not against the new
output: the question is whether the shape the flip produces is the one
PG produces, which is the goal's actual criterion. That is R13.

## 3. Why the flip still is not landed

Four tests now demonstrate real plan movement whose correctness is
unestablished. Landing on "the values pass" is not sufficient here —
values passing is what let R11 rule out a search regression, but it
cannot distinguish a plan that matches PG from one that merely returns
the right rows, and this goal is about the former.

## 4. Filed

- **R13 — adjudicate the 4 real ones against PG**, then the 2 stage
  pins and the generated artefact (both mechanical), then land the flip
  and run R10 `DESIGN.md` §5's gates.
- Known and not to be re-derived: TPC-H values are **identical** under
  the flip (R11 §2); R8's probe counts are quarantined (R9 changed the
  producer); `DESIGN.md` §7's acceptance rule stands, including that a
  parity-neutral result is an accept.
