# R15 — the multi-key fallback is a cost question, not a capability one

*Round 15 of `../TODO.md`. Findings round, partial: it resolves the
NATURE of the item R14 flagged as riskiest, and states plainly what is
still unanswered.*

## 1. Why this one was flagged

R14 singled out `TestSplitEqualityForHashMultiKey` from the four
remaining flip failures: it reports *"multi-key JOIN...ON did not
hash-join on any of the AND'd equalities (fell back to Nested Loop)"*,
and **losing a hash join is a genuine shape regression unless PG
declines it too**.

## 2. The test explains its own failure mode

Its header comment (`multikey_hash_join_test.go:68-83`) distinguishes
two arms, and the failing one is the cost-driven arm:

> *"the DEFAULT arm (flag on) is the searched enumerator, which chooses
> from COST. It is given row counts, because a relation whose size the
> planner cannot see is estimated at the one-row floor and **at one row
> per side a nested loop is genuinely the cheaper plan** — so a blind
> fixture would be asserting the cost model is wrong rather than that
> the join is hashable."*

It also records that this is not new and not specific to the flip:

> *"the comma-FROM spelling of this same statement already planned a
> nested loop at the S5 flip, unnoticed, because this test's JOIN
> spelling was the one shape the search could not reach."*

So the fixture is deliberately given row counts to keep the cost
comparison off the one-row floor, and the shape-capability half
(`splitEqualityForHash`) lives in the **other** arm, which still passes.

## 3. What that settles, and what it does not

**Settled**: this is not a lost capability. The predicate is still
recognised as hashable — the kill-switch arm proves it — and the
enumerator is choosing a nested loop on price. The failure is therefore
a *cost* movement under the flip, in the same family as the Slice3
narrow-build changes R14 adjudicated, and not the shape regression the
flag was raised for.

**Not settled**: whether the price is *right*. The test's whole design
turns on the fixture's row counts keeping the comparison meaningful, and
if the flip's partial-path costing shifts the balance back across that
threshold, the fixture may no longer be doing its job. Deciding that
needs PG's answer for the same shape at the same cardinalities, which —
since the fixture is synthetic, not a corpus query — means running the
equivalent SQL on `:65432` rather than reading an existing capture.

That measurement is **not done**, and this report does not assume it.
The item moves from "possible shape regression" to "cost adjudication
pending", which is a real narrowing but not a resolution.

## 4. Status of R10's failing set

| # | item | state |
|---|---|---|
| 1 | `TestFlagProvenanceEnvIsGenerated` | solved; must land WITH the flip (K20) |
| 2 | `TestC19fPathModelGatherExecutesAsAParallelHashJoin` | needs post-pass design decision (K20) |
| 3 | `TestPartialPathIsNeverTheFinalPath` | superseded stage pin; mechanical |
| 4 | `TestSlice3LiveQ9ShapeDerivation` | **adjudicated** (R14): justified re-baseline |
| 5 | `TestSplitEqualityForHashMultiKey` | **narrowed** (here): cost, not capability; PG measurement pending |
| 6 | `TestSlice3FilterColumnSurvivesNarrowing` | not yet adjudicated |
| 7 | `TestOwnedBuildPoisonPrebuiltBoundary` | not yet adjudicated |

## 5. Filed

- **R16 — measure PG's answer** for the multi-key shape at the
  fixture's cardinalities on `:65432`; adjudicate items 6 and 7; then
  land flip + provenance + stage pin in one commit and run R10
  `DESIGN.md` §5's gates.
