# R27 — make outer-join reduction drive the plan, without its analysis normalisation

*Design. Implements K28, which R26/2 established. Target: TPC-DS Q49's
3 `Hash Left Join` where PG has none, and 7 of the 13 seam declines.*

## 1. The defect, already located

`planFromClause` builds the node tree from `s.FromExprs` and only THEN
calls `reduceOuterJoins`, which mutates those same items in place. The
demotion therefore reaches the jointree deconstruction (which correctly
builds no `SpecialJoinInfo`) but not the plan (which keeps
`JoinTypeLeft`). The two disagree, the fail-closed
`outerLinksHaveSJInfos` guard declines the statement, and per K27 a
declined statement can never converge on PG's plan.

Parity evidence, TPC-DS Q49: **PG 6 × `Nested Loop`, no outer join at
all; goopg 3 × `Hash Left Join`.**

## 2. Why the one-line fix fails — measured, not predicted

Moving the call above the node-building loop (PG's own position) breaks
six tests, two on VALUES:

```
got 0 rows, want 1
rows = [], want [102,,] — a WHERE qual on a RIGHT JOIN's nullable arm
                          was evaluated below the join producing the NULLs
```

## 3. Root cause of THAT failure: two jobs in one mutation

`applyDemotion` does two different things to `*parser.FromExpr`:

| | what | safe for deconstruction? | safe for node building? |
|---|---|---|---|
| **A. Join-type demotion** | LEFT→INNER, LEFT→ANTI, FULL→RIGHT | yes | **yes — this is the point** |
| **B. S9.4 RIGHT→LEFT flip** | **swaps `Base ↔ Right`** | yes | **NO** |

(B) is an *analysis normalisation*: RIGHT is flipped to LEFT so the
LEFT-specific reductions apply. PG does the same
(`reduce_outer_joins_pass2`, prepjointree.c:3366-3376) and it is safe
there because PG's downstream planning references columns by `Var`, not
by FROM-item position.

goopg's node builder is position-sensitive: `planFromItem` assigns
`SchemaColumn.SourceTableIdx` and binding offsets in FROM order. Swapping
`Base↔Right` before the build therefore re-points column references —
which is precisely the `SELECT rj_c.id, rj_a.id, rj_b.id` failure above,
and it is a wrong ANSWER, not a moved plan.

**So the demotion has never driven a plan, and its plan-safety was never
established** (K28). This design separates the two jobs rather than
assuming either is fine.

## 4. The change

Split (A) from (B):

1. `applyDemotion` runs its analysis on a **copy** of the item, so the
   S9.4 flip normalises only the analysis.
2. Only the resulting **join-type changes** are written back to the real
   `*parser.FromExpr`, mapped through the flip so a flipped join's
   verdict lands on the join it came from.
3. `reduceOuterJoins` then moves above the node-building loop, PG's
   position, so the demotion reaches the plan AND the deconstruction and
   the two agree by construction.

Nothing about *which* joins demote changes — only what the mutation is
allowed to touch. That is deliberate: the strictness analysis is the
part with the least evidence behind it, so this round does not also
rewrite it.

## 4a. REVISION — the flip is contractual, so it cannot be suppressed

The §4 plan ("run the analysis on a copy, write back only join-type
verdicts") was implemented and **fails 8 tests**, including five that
pin the flip as observable behaviour:

```
TestReduceOuterJoinsRightToLeftFlipFirstPosition
TestReduceOuterJoinsRightFlipThenAnti
TestReduceOuterJoinsRightDemotion / RightNoDemotion / FullDemotionOneSide
```

So the `Base<->Right` swap reaching `s.FromExprs` is **intended and
pinned** — the jointree deconstruction consumes it, and suppressing it
changes what the deconstruction sees. §4 was wrong to treat it as an
internal detail of the analysis.

**Revised change.** Do not replace `reduceOuterJoins`; SUPPLEMENT it:

1. **Early**, before the node-building loop: run the demotion analysis
   on a COPY of `s.FromExprs` and collect the per-join verdicts. Nothing
   is mutated.
2. Build the node tree with those verdicts applied to the join TYPES
   only — so the plan gets LEFT->INNER without ever seeing the flip.
3. **Late**, unchanged: `reduceOuterJoins(s.FromExprs, …)` exactly as
   today, so the deconstruction and all five pinned tests are untouched.

The two consumers then agree on join TYPE (which is all the
`outerLinksHaveSJInfos` guard compares) while each keeps the
representation it needs — the deconstruction its flipped-and-demoted
jointree, the plan its original column order.

Cost of the revision: step 2 threads the verdicts into `planFromItem`,
which §4 avoided. That is the price of not disturbing a contract five
tests hold.

## 5. Gates

- The six tests §2 names are the primary gate, `TestRightJoinSpineKeepsNullExtendedRows`
  and `TestLeftJoinSearchAdmissionValues` above all — they are the ones
  that caught the naive attempt and they must pass unchanged, not be
  re-baselined.
- Suites; TPC-H values 22/22; TPC-DS sweep all-zero (currently PASS=95
  TIMEOUT=0 — must stay).
- Parity both corpora. **Expected: TPC-DS `outer-link-no-sjinfo`
  declines 7 → 0**, Q49's 3 `Hash Left Join` → inner joins, matching
  PG's shape on that axis.
- `seam-decline` re-audit (R26's method) as the admission gate.

## 6. Prediction, in advance

- Q49 loses its `Hash Left Join`s and is admitted to the search.
- Declines drop from 13 to ~6 (`outer-over-derived` 3, `outer-spine` 2,
  `lateral` 1 remain — different causes).
- **Match count does not move.** Q49 still faces join-order, which
  blocks 95/99. Per `ROADMAP-to-all-match.md` this round is judged on
  its category and on admission, not on matches.

## 7. Risks

- Outer-join semantics are the highest-risk area in this codebase
  (the practice card's own warning). The mitigation is that the two
  values tests which caught the naive attempt are the gate, and that
  the strictness analysis itself is untouched.
- If write-back-through-the-flip proves fiddly for deeper RIGHT joins,
  decline those (leave them un-demoted) rather than guess: an
  un-demoted join is today's behaviour, a mis-mapped one is wrong rows.
