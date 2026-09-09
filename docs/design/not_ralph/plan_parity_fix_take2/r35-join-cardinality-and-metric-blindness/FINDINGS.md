# R35 — why estimate rounds measure zero, and a join-cardinality defect

*Investigation round following K49. Two findings: one about the
MEASUREMENT (which invalidates how R30/R31b/R34 were judged), one about
the join cardinality estimator. No code change.*

## 1. The parity metric is blind to estimates BY CONSTRUCTION

`scripts/pg-plan-parity-diff.py:44-62` declares its normalisation, and
three rules matter here:

- **N1** — "estimates stripped from shape: `(cost=.. rows=.. width=..)`
  ... Kept in the estimate column." The `rows=` value is removed before
  comparison.
- **N5** — `::type` renderings stripped, including `timestamp without
  time zone`.
- **N6** — Filter / Index Cond / Hash Cond / Merge Cond compare by
  (referenced columns, operator multiset), **NOT by literal values or
  expression shape**.

**Consequence: no estimate change can ever move a parity verdict.** The
metric cannot see one.

This retroactively explains three rounds that were reported as null
results:

| round | what it corrected | why parity showed nothing |
|---|---|---|
| R30 | correlation 0.07 -> 1.0 | N1 |
| R31b | `relpages` 15% inflated | N1 (cost/rows only) |
| R34 | 580x range cardinality; `cast()` -> typed literal | N1 **and** N5 **and** N6 |

R34 is the sharpest case: **18 TPC-DS plans changed** and the parity
categories were byte-identical, because the change altered `rows=` (N1),
a `::date` rendering (N5) and a Filter's literal spelling (N6) — every
one of them normalised away.

K49 ("join-order is not estimate-driven") was inferred from those three
null results. **That inference was unsound**: the experiments could not
have shown movement whatever the estimates did. K49's *advice* — go
look at the search — may still be right, but its *evidence* is void and
it must not be cited as proven.

### How estimate work must be judged instead

Directly, against the oracle, as R34's probe ladder did (8116 -> 13 vs
PG's 14), plus a count of plans whose SHAPE changed. Parity categories
are the wrong instrument for an estimate round, and quoting them as a
verdict on one is a measurement error.

## 2. goopg has two join-cardinality estimators and they disagree

Not a new discovery — `cardinality_two_estimators_test.go` (C-20a)
pins it — but the divergence is larger than "an artefact" and is
recorded here with a live case.

TPC-H, `orders ⋈ lineitem` with a column-vs-column restriction:

```
Gather                          rows=2000418
  -> Merge Join                 rows=6001255      <-- more than its own input
       Merge Cond: (orders.o_orderkey = lineitem.l_orderkey)
       -> Parallel Index Scan on orders    rows=1500000
       -> Index Scan on lineitem           rows=2000418
            Filter: (l_shipdate < l_commitdate)
```

Three different numbers in one plan. The scan applies
`DEFAULT_INEQ_SEL` correctly — 6,001,215 x 0.3333 = 2,000,418, and PG
agrees to the row on the equivalent TPC-DS shape (both engines estimate
479,869 for `ss_list_price < ss_wholesale_cost`) — but the Merge Join
above reports the relation's RAW count, as though no restriction
existed, and the Gather above that reports the restricted count again.

Which clause types trigger it (join-level estimate, `orders ⋈ lineitem`):

| added restriction | join `rows=` |
|---|---|
| `l_shipmode IN ('MAIL','SHIP')` | 1,711,157 (propagates) |
| `l_receiptdate >= '1994-01-01'` | 4,380,420 (propagates) |
| `l_shipdate < l_commitdate` | **6,001,255** (ignored) |
| `l_receiptdate < (date + interval)` | **6,001,255** (ignored) |
| Q12's full filter | **6,001,255** (ignored; PG: 28,127) |

Constant comparisons propagate; column-vs-column and unfolded-interval
bounds are dropped entirely at the join level (selectivity 1.0, not
even the 0.3333 the scan used).

## 3. The open question this round does NOT answer

`EstimateRows` (a walker over the finished plan `Node` tree) is what
EXPLAIN prints; `calcJoinrelSize` (a `searchCtx` method over
`*RelOptInfo`) is what the join search uses. The numbers above are
**EXPLAIN's**, so they establish a defect in `EstimateRows` and say
nothing yet about whether `calcJoinrelSize` shares it.

That distinction decides whether this matters for the goal:

- If only `EstimateRows` is affected, it is a DISPLAY defect. Parity
  cannot see it (§1), and the join order is unaffected.
- If `calcJoinrelSize` shares it, join ORDER is being chosen against a
  3x-to-200x wrong cardinality, and that is a genuine parity blocker.

**Not guessed at.** The next round must instrument `calcJoinrelSize`
directly — per the `planner_verify_both_candidates_generated` lesson,
which is exactly this trap: reading the rendered number instead of the
one the decision consumed.

## 4. Recommended next round

Instrument `calcJoinrelSize` on TPC-H Q12 (a two-relation join — the
minimal case, and one where goopg picks Merge Join with `orders` outer
while PG picks Hash Join with `lineitem` outer). Two relations means
zero enumeration complexity: any divergence is cost or candidate
generation, nothing else. Establish which cardinality the search
consumed before proposing any fix.

---

# CORRECTION (same day, after building the instrument)

§1 above overstated its case. The claim "**no estimate change can ever
move a parity verdict**" is **wrong** and is withdrawn.

`methodology/shape-delta.sh` was written to count plans whose node
STRUCTURE changed, separately from ones whose text changed. Applied to
the three rounds §1 cited:

| round | text-changed | **shape-changed** | category movement |
|---|---|---|---|
| R30 (correlation) | 99 | **6** | scan-type −1, join-method +2 |
| R31b (heap density) | 96 | **10** | qual-placement −1 |
| R34 (cast folding) | 18 | **0** | none |

R30 and R31b **did** change plan structure — on 6 and 10 queries — and
the metric **did** register movement on both. It was not blind to them.

## What is actually true

- N1/N5/N6 strip estimate DIGITS, casts and literal spellings from the
  comparison. That part of §1 is correct and verified from the tool's
  source.
- The consequence is narrower than §1 claimed: an estimate change
  registers **exactly insofar as it changes plan STRUCTURE**, and not at
  all otherwise.
- R34 measured zero because it genuinely changed **no plan's
  structure** (shape-changed=0), not because the metric could not see
  it. Zero was the correct reading, not a measurement artefact.

## What this does to K49

K49 ("join-order is not estimate-driven") stays **withdrawn**, but the
reason is now weaker and must be stated as such. It is not that the
experiments were incapable of showing movement — two of the three
changed shapes. It is that the shape changes they produced were
roughly parity-neutral: `join-order` sat at exactly 95/99 across 16
structural changes. That is *evidence* that join order resists
estimate corrections; it is not *proof* that it is estimate-independent,
and it must not be cited as proof.

## Standing instruction that survives

Report `shape-delta.sh`'s counts alongside the category counts in every
round. A round with `shape-changed=0` has not changed any plan and its
category result is trivially zero; a round with shape changes and no
category movement has moved plans sideways, which is a different and
more interesting result. Conflating the two is what produced the error
above.
