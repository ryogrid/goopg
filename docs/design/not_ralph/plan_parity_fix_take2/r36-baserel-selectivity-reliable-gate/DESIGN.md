# R36 — drop the `reliable` gate in baserel sizing (K52 answered)

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md`.
Cost-computation axis. Answers K52, and lands a deferral the ledger has
carried since 2026-08-06 with an explicit resume action.*

## 1. K52, answered by instrumentation

R35 measured EXPLAIN showing `Merge Join rows=6001255` above an input
scan of `rows=2000418`, and deliberately did NOT conclude the search
was affected: EXPLAIN reads `EstimateRows` (a plan-tree walker) while
the join search reads `calcJoinrelSize` (a `searchCtx` method). Only
the latter can move join ORDER.

`calcJoinrelSize` was instrumented directly (temporary
`GOOPG_JRS_TRACE`, reverted) on `orders ⋈ lineitem WHERE o_orderkey =
l_orderkey AND l_shipdate < l_commitdate`:

```
JRSTRACE jt=0 outer.Rows=1500000 inner.Rows=6001255
         fkselec=6.666666666666667e-07 jselec=1 pselec=1 fired=true
         -> rows=6001255
```

`inner.Rows` is the **raw** 6,001,255, not the restricted 2,000,418.
**The search shares the defect.** Reading `calcJoinrelSize`'s source had
suggested the opposite — its `product()` multiplies `outer.Rows *
inner.Rows`, which are supposed to be restricted counts — so this is
the `planner_verify_both_candidates_generated` trap in its general
form: the defect is in the INPUT, one layer up, and only instrumenting
the consumed value found it.

## 2. Root cause: a deliberate, ledgered deviation

`cardinality.go applyLocalFilterSelectivity`, goopg's second half of
`set_baserel_size_estimates` (costsize.c:5378):

```go
sel := clauseSelectivityWithSource(localized, scan)
if !sel.reliable {
    return baseRows          // the PRE-filter count
}
rows := scaleByFloat(baseRows, sel.value)
```

Its own comment names the divergence: *"one deliberate deviation
[from] upstream['s] `reliable` gate: PG always multiplies, falling back
[to] DEFAULT_EQ_SEL / DEFAULT_INEQ_SEL [when there is] no statistic,
goopg keeps [the] pre-filter count."*

Upstream has no such gate. `set_baserel_size_estimates` is
unconditional:

```c
nrows = rel->tuples * clauselist_selectivity(root, rel->baserestrictinfo, ...);
rel->rows = clamp_row_est(nrows);
```

and `clauselist_selectivity` returns `DEFAULT_INEQ_SEL` (0.3333,
`selfuncs.h:37`) for an inequality it cannot estimate, never 1.0.

Deferral ledger, 2026-08-06 (M0127-P5.6), states the consequence and
the fix:

> goopg's `reliable` gate ITSELF deviates from
> `set_baserel_size_estimates` ... **up to 200x divergence, direction
> makes build sides look too expensive**
> Resume: **Drop the `!sel.reliable` early return at
> `applyLocalFilterSelectivity`**

## 3. Why this one can move plan SHAPE where R30/R34 could not

R35/K50 established the parity metric normalises estimates away (N1),
so an estimate change is invisible to it. **This is not only an
estimate change.** `rel.Rows` is what `calcJoinrelSize` multiplies and
what every path's cost is computed from, so a 3x error on one side
changes which side is built, which join method wins, and which order
survives `add_path` — all of which the metric DOES compare
(join-order, join-method, scan-type).

The ledger's note on direction is the point: keeping the pre-filter
count makes a filtered relation look larger, i.e. "makes build sides
look too expensive", which is a systematic hash/merge and
inner/outer distorter.

## 4. Which clause classes are affected

Measured at the join level (`orders ⋈ lineitem`, raw lineitem
6,001,215):

| added restriction | join estimate | verdict |
|---|---|---|
| `l_shipmode IN ('MAIL','SHIP')` | 1,711,157 | propagates |
| `l_receiptdate >= '1994-01-01'` | 4,380,420 | propagates |
| `l_shipdate < l_commitdate` | **6,001,255** | dropped |
| `l_receiptdate < (date + interval)` | **6,001,255** | dropped |
| TPC-H Q12's full filter | **6,001,255** | dropped (PG: 28,127) |

Constant comparisons against a column with statistics are "reliable"
and propagate. Column-vs-column comparisons and non-constant bounds are
not, and are dropped ENTIRELY — selectivity 1.0, not even the 0.3333
the SCAN-level estimator applies. goopg and PG both estimate 479,869
for `ss_list_price < ss_wholesale_cost` at the scan, so the two goopg
paths already disagree with each other.

## 5. Change

Delete the `if !sel.reliable { return baseRows }` early return, so the
selectivity is always applied — upstream's unconditional multiply. This
requires `clauseSelectivityWithSource` to return PG's DEFAULT_* constant
in the unreliable case rather than a value meant to be discarded; if it
returns 1.0 today, the round must supply the defaults instead, matching
`clauselist_selectivity`'s fallbacks. To be confirmed against the code
before implementing, not assumed.

## 6. Gates

Suites; TPC-H values digest byte-identical; SF0.5 sweep all-zero;
parity BOTH corpora on the fresh clone. The ledger names the bar as
"estimate-audit parity ratchet, not the absolute tripwire" — i.e. this
is expected to move many estimates, and the question is whether it
moves them TOWARD PG.

Additional gate this round owes, per K50: report the count of plans
whose SHAPE changed, separately from category counts, since an estimate
round's effect is invisible to the latter.

## 7. Prediction

- Q12's join estimate moves 6,001,255 -> ~2,000,418 (PG: 28,127 — still
  off, because the interval bound stays unfolded until K46/K47; this
  round fixes the DROPPED clause, not the unfolded one).
- Plan SHAPES change on a non-trivial number of queries in both corpora.
- Category movement is genuinely uncertain and is the round's question.
  Unlike R30/R31b/R34 this change CAN move categories, because it moves
  rel.Rows, which the search consumes. If it still moves nothing, that
  is strong evidence the divergence is in enumeration or dominance
  rather than cost — and that is the next round.
- Runtime may regress. Per the goal's standing instruction that is not
  a regression if the plan moves toward PG's.
