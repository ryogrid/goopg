# R36 results — baserel sizing multiplies unconditionally

Implements `DESIGN.md` (review APPROVE-WITH-NOTES; all four requested
changes applied). `STATUS.md` records the first attempt, which was
reverted; this is the landed second attempt after K56 supplied the
missing piece.

## 1. The change

`applyLocalFilterSelectivity` (cardinality.go) drops the
`if !sel.reliable { return baseRows }` early return and multiplies
unconditionally, as `set_baserel_size_estimates` does
(costsize.c:5348-5362). Upstream has no reliability concept;
`clauselist_selectivity` returns DEFAULT_EQ_SEL / DEFAULT_INEQ_SEL for
a clause it cannot estimate, never 1.0 — and 1.0 is what keeping the
pre-filter count amounted to. Ledgered 2026-08-06 at "up to 200x
divergence, direction makes build sides look too expensive".

**The selectivity comes from `clauseSelectivity`, not the
`…WithSource` twin.** This was the review's central finding and it is
what keeps the round from trading one error for another: the twin
multiplies AND-conjuncts pairwise, so a histogram-less
`x >= a AND x < b` band would price at 1/3 x 1/3 = 0.111 against PG's
DEFAULT_RANGE_INEQ_SEL of 0.005 — 22x too loose. `clauseSelectivity`
routes AND through `conjunctionSelectivity`, which ports PG's punt rule
(rangequery.go:185-192, clausesel.c:283-286). The scan-level estimator
already used the plain twin, so EXPLAIN and the search now agree by
construction.

## 2. The defect it fixes, measured

`GOOPG_JRS_TRACE` on `calcJoinrelSize`, TPC-H
`orders ⋈ lineitem WHERE l_shipdate < l_commitdate`, before:

```
outer.Rows=1500000 inner.Rows=6001255 fkselec=6.67e-07 jselec=1 -> rows=6001255
```

The search consumed the RAW count. EXPLAIN showed three different
numbers in one plan — Merge Join 6,001,255 above an input scan of
2,000,418. After:

```
->  Merge Join  (cost=0.75..419479.79 rows=2000418 ...)
      ->  Index Scan on lineitem  (rows=2000418)
            Filter: (l_shipdate < l_commitdate)
```

Consistent, and the join no longer claims more rows than its input.

## 3. Parity — the largest structural movement of any round so far

**24 plan SHAPES changed** on TPC-DS (44 text-changed), against R34's 0
and R30's 6. Reported per K50 alongside the categories.

| category | TPC-DS base | TPC-DS R36 | TPC-H base | TPC-H R36 |
|---|---|---|---|---|
| parameterisation | 44 | **41** | 6 | 6 |
| parallelism | 90 | **88** | 15 | 15 |
| aggregation-strategy | 83 | **82** | 18 | 18 |
| join-method | 79 | 80 | 14 | **13** |
| scan-type | 71 | 72 | 12 | 12 |
| sort-strategy | 82 | 84 | 11 | 11 |
| join-order | 95 | 95 | 20 | 20 |
| match | 0 | 0 | 1 | 1 |

Net **−2** on TPC-DS (−6 improved, +4 regressed) and **−1** on TPC-H.
A real if modest improvement, and the first round to move several
categories in the improving direction at once.

**`join-order` did not move on either corpus.** It has now held at
exactly 95/99 and 20/22 through R30, R31b, R34 and R36 — across 40+
structural changes. That is the strongest evidence yet for the
suspicion K49 was withdrawn for overstating: join order is not
responding to cost-input corrections, and the next join-order round
should target enumeration and `add_path` dominance directly.

## 4. Correctness gates

- `go test ./internal/optimizer/ ./internal/executor/ -count=1` — ok.
- `RALPH_PRECOMMIT_SCOPE=units` — 44 packages ok, zero FAIL.
- TPC-H values digest — **byte-identical** to the r9 reference.
- TPC-DS SF0.5 sweep — `PASS=95 (57 ck-verified) MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`; `verdict-changes=none`;
  runtime **-2.3%**; 36 plans changed on that cluster.

## 5. Tests: three re-derived, three fixtures repaired

Re-derived (each pinned the gate, not a mechanism):

- `TestEstimateBaseRowsAppliesDefaultSelectivityWhenNoHistogram`
  (renamed) — 1,500,000 -> 500,000 at DEFAULT_INEQ_SEL.
- `relsize_baserel_placement_test.go` `"unreliable selectivity"` —
  1000 -> 5 at DEFAULT_EQ_SEL.
- `TestRelSizeFallbackPlacementScalesWhenStatsAbsent` (renamed) — this
  one encoded the argument at `relsize.go:169-177` that pre- and
  post-filter fallback placement "coincide S-cold", true only because
  of the gate. Re-derived, and that comment rewritten: it is a
  cold-server BEHAVIOUR CHANGE, matching PG, not a re-derivation.

Fixtures repaired, not re-tuned:

- **C-19f (2 tests)** — `pqJoinFixture` had no statistics, so
  `f.amt >= 0`, a predicate all 400 rows satisfy, priced at
  DEFAULT_INEQ_SEL and claimed 133 rows; that tipped the gathered hash
  join into a base-rel Gather. Stamped the statistics the fixture's own
  loops imply (400 facts, `amt = i*2` so quartiles 0/200/400/600/798;
  20 dims). Both pass.
- **`TestSetOpJoinPromotesToHashJoin`** — its assertion used the join
  METHOD as a proxy for the promotion, rejecting any `Nested Loop`. On
  2-3 row tables a nested loop is genuinely cheaper under PG-faithful
  sizing and PG would pick one too; the promotion still happened (the
  equi-joins render as `Join Filter` conditions, not residual `Filter`
  conjuncts). The check now rejects a BARE `Nested Loop` — the
  Cartesian product its own comment describes — which is the invariant
  it meant to pin. The fixture was deliberately NOT enlarged:
  `TestSetOpJoinAnswerUnchanged` hand-derives its rows from exactly
  those 3 items / 2 dates / 3 times, and adding rows breaks it
  (confirmed by trying).

`SetTableStats` was used rather than `ANALYZE` because ANALYZE reports
`RowCount=0` for rows written in the same transaction while leaving the
column slots populated (K56), so it would have looked like it worked.

## 6. Follow-ups

- **K57** — `inferAnchoredEqualities` rule (2) (`filteredRows*2 <=
  baseRows`) is now VACUOUSLY TRUE for any relation with a
  default-priced filter, since both defaults are below 1/2. It would
  make every filtered relation an anchor — an enumeration change, and
  over-firing there caused the M0075/M0076 Q9 380-600 s hang. Inert
  today (no non-test caller); a comment now says so at the site.
  Re-derive the threshold before wiring it up.
- **K58** — `scaleByFloat` truncates where PG's `clamp_row_est` rounds
  (`rint`). Harmless while few filters scaled; now every one does.
- **K59** — goopg-only thresholds (`nliMaxOuterRowsHeuristic` 100000,
  `memoizeMinOuterRows` 1000) are crossed far more often now, so some
  of the 24 shape changes are driven by non-PG heuristics rather than
  by cost. Worth separating when attributing the +4 regressions.
