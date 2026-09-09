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

---

# K54 ANSWERED — the fixtures cannot express row counts

The resume point above asked why inserted rows never reach `baseRows`
in the executor test harness. Probed directly (`ctx.Catalog.LookupTable`
right after loading the fixture and running `ANALYZE` on every table):

```
PROBE sj_ws   Stats.RowCount=0 cols=4
PROBE sj_item Stats.RowCount=0 cols=4
PROBE sj_td   Stats.RowCount=0 cols=4
```

**`ANALYZE` populates the per-column statistics and leaves
`Stats.RowCount` at 0.** With `RowCount == 0`, `estimateBaseRelInfo`
returns `baseRows == 0` and every relation falls to
`applyRelSizeFallback`'s block-derived count — which in this in-memory
harness is ~1. That is why a table holding 4000+ rows rendered as
`Seq Scan on sj_ws rows=1` with no Filter, and why adding rows and
adding ANALYZE both failed to move anything.

## What this means for the three blocked tests

They are not repairable by adding data or by calling ANALYZE: the
harness cannot represent a row count through either path. At HEAD they
only produce hash joins because the reliability gate DISCARDS the
default selectivity, so `sj_item` keeps 5 rows and `sj_td` keeps 11
instead of collapsing to the 1-row floor. Under PG-faithful sizing
those become 1, and a nested loop is then the correct plan — PG would
choose one too on tables that size.

So the mechanisms are almost certainly intact and the fixtures are
measuring the gate rather than the mechanism. "Almost certainly" is
not good enough to re-tune three mechanism tests on, which is why the
implementation stays reverted.

## Revised resume point

Two independent pieces of work, in this order:

1. **Fix or characterise `ANALYZE`'s `RowCount`.** Either it genuinely
   fails to stamp `RowCount` on this path (a defect in its own right,
   affecting any test that reasons about cardinality), or this harness
   bypasses the stamping. Establish which. Note the related known
   issue: `internal/initdb/open.go` builds `TableStats{Columns: ...}`
   with `RowCount` left zero on restore, so a zero `RowCount` beside
   populated `Columns` is a shape that already exists elsewhere.
2. **Then re-run R36.** With row counts expressible, seed the three
   fixtures via `SetTableStats` (what the optimizer-side tests already
   do) at sizes where the promoted plan is genuinely cheaper, and the
   tests will pin their mechanisms rather than the gate. Only then is
   it honest to judge whether R36 breaks anything.

R36's design, its 22x-error finding (K53) and the three re-derived
optimizer tests all stand and need no rework.

---

# K55 refined into K56 — ANALYZE and the scan path disagree on visibility

K55 recorded that `ANALYZE` leaves `Stats.RowCount=0`. Probing one step
further shows it is not that ANALYZE declines to stamp the field — it
stamps a count it genuinely measured as zero, because it could not see
the rows:

```
PROBE count(*) sj_item      = 3     <-- a normal SELECT sees them
PROBE after ANALYZE: RowCount=0 Pages=1 cols=4
```

Same `*Context`, same session, immediately adjacent statements. The
sampling loop increments `stats.RowCount` once per tuple passing
`transam.TupleVisible(t.Header, snap, tx.XID, curcid, combo, mxs)`
(`operators_analyze.go:873`), and `SetTableStats` merely assigns
(`catalog.go:13112-13119`), so a zero can only mean the visibility test
rejected every tuple. `Pages=1` confirms the pages were found and read;
it is the per-tuple test that fails. The four column entries are
computed from the empty reservoir, which is why populated `Columns`
sit beside a zero `RowCount` — the shape K55 described.

**This is a divergence from PG, not merely a harness quirk.** In PG,
`ANALYZE` run in the same transaction as the `INSERT` uses the current
snapshot and counts the rows. goopg's does not.

It is not visible on the live benchmark clusters because those ANALYZE
committed data in autocommit, which is why `reltuples` reads correctly
there (`store_sales` 1,439,608) and why this went unnoticed.

## Filed as K56, and it is NOT R36's to fix

R36 needs only a way to give three fixtures real row counts. The
supported way is `SetTableStats` directly, which the optimizer-side
tests already use and which bypasses this entirely. R36 should take
that route rather than wait on K56.

K56 itself is worth a round of its own: any test or tool that ANALYZEs
inside a transaction and then reasons about cardinality is silently
reading zeros, and the failure is invisible because `Columns` looks
populated.
