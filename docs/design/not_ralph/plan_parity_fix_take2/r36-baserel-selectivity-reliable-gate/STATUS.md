# R36 status — design APPROVED and verified; implementation NOT landed

The design (`DESIGN.md`, agent review APPROVE-WITH-NOTES) is committed
and its central claim is verified by instrumentation. The
implementation was written, hit executor-test fallout that needs real
investigation, and was **reverted rather than shipped or papered
over**. The tree is green at HEAD.

## What is established (does not need redoing)

1. **K52 answered: the join SEARCH shares the defect.** Temporary
   `GOOPG_JRS_TRACE` on `calcJoinrelSize`, TPC-H
   `orders ⋈ lineitem WHERE l_shipdate < l_commitdate`:

   ```
   outer.Rows=1500000 inner.Rows=6001255 fkselec=6.67e-07 jselec=1 -> rows=6001255
   ```

   `inner.Rows` is the RAW count; the restricted 2,000,418 never
   reaches the search. Reading `calcJoinrelSize`'s source suggested the
   opposite (its `product()` multiplies restricted counts) — the defect
   is one layer up, in its INPUT.

2. **Root cause**: `applyLocalFilterSelectivity`'s
   `if !sel.reliable { return baseRows }`, a deviation its own comment
   documents and ledger row 2026-08-06 carries at "up to 200x
   divergence, direction makes build sides look too expensive".
   Upstream (`costsize.c:5348-5362`) multiplies unconditionally.

3. **The naive fix is wrong, and review caught it.** `sel.value`
   already carries PG's DEFAULT_* constants (verified:
   `selectivity.go:915,919,933,937`, constants byte-equal to
   `selfuncs.h:34,37`). The defect is the AND *composition*: the
   `…WithSource` twin multiplies conjuncts PAIRWISE
   (`selectivity.go:838-847`, a divergence documented at `:806-812`)
   whereas the plain `clauseSelectivity` twin routes AND through
   `conjunctionSelectivity`, which ports PG's punt rule — either bound
   at DEFAULT_INEQ_SEL collapses the band to DEFAULT_RANGE_INEQ_SEL
   (`rangequery.go:185-192`, `clausesel.c:283-286`). On the
   histogram-less `x >= a AND x < b` band this round targets, deleting
   the gate naively gives 1/3 x 1/3 = 0.111 against PG's 0.005 — **22x
   too loose, a fresh error replacing the old one**.

   **Correct implementation: consume `clauseSelectivity`, not
   `clauseSelectivityWithSource`.** That also makes the search agree
   with the scan-level estimator, which already uses the plain twin.

## Where it stopped

The implementation (route through `clauseSelectivity`) builds, and the
whole optimizer suite passes after re-deriving three tests:

- `TestEstimateBaseRowsKeepsBaseRowsWhenSelectivityFallback` — pinned
  the bug (1,500,000 -> 500,000 at DEFAULT_INEQ_SEL). Inverted.
- `relsize_baserel_placement_test.go` `"unreliable selectivity"` —
  pinned the bug (1000 -> 5 at DEFAULT_EQ_SEL). Inverted.
- `TestRelSizeFallbackPlacementIdenticalWhenStatsAbsent` — **not a
  mere bug pin.** It encodes the argument at `relsize.go:169-177` that
  pre- and post-filter fallback placement "coincide S-cold", which was
  true ONLY because of the gate. Re-derived (13600 -> 68) and the
  comment rewritten: it is a cold-server BEHAVIOUR CHANGE, not a
  re-derivation, and PG does the same thing (block-derived `tuples`
  times a DEFAULT_* punt).

**Three executor tests then failed** and are the blocker:

- `TestC19fPathModelGatherExecutesAsAParallelHashJoin` (3 subtests) —
  "the Gather's subtree carries no join: *optimizer.SeqScan"
- `TestC19fGatheredHashBuildRunsOnceAndIsShared`
- `TestSetOpJoinPromotesToHashJoin` — equi-join no longer promoted;
  planner picks nested loops

These pin MECHANISMS, not numbers, so they must not be re-tuned to
pass. Two diagnostics were run and neither settled it:

- Adding `ANALYZE` to the setop fixture: still fails.
- Adding 4000 rows to both fact tables: still fails — **and the plan
  then showed `Seq Scan on sj_ws rows=1` with no Filter on a table
  holding 4000+ rows**, i.e. the inserted data is not reaching the
  estimates in that harness at all. So that diagnostic is
  inconclusive, not evidence about the mechanism.

## Why it was reverted rather than committed

Shipping would have meant either a red tree or editing tests that pin
parallel-hash and set-operation promotion mechanisms on the strength of
a diagnostic I had already shown to be inconclusive. Neither is
acceptable for a change that alters every baserel row estimate in the
planner.

## Resume point

1. Work out why the setop fixture's inserted rows do not reach
   `baseRows` in that test harness (suspect the in-memory catalog's
   `RowCount` is never refreshed by `INSERT`, so every table in these
   executor tests is sized by a block/fallback path). Until that is
   understood, no conclusion can be drawn from those three tests.
2. Then decide per test whether the mechanism is genuinely unreachable
   under PG-faithful sizing (repair the fixture) or actually broken
   (fix the code).
3. Gates the round still owes: TPC-H values digest, SF0.5 sweep,
   parity BOTH corpora, and `methodology/shape-delta.sh` counts
   reported alongside the categories (K50).
