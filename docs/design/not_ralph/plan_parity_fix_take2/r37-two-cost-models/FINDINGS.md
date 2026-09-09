# R37 — goopg has TWO seq-scan cost models and they differ by the page term

*Investigation round following R36. No code change. Opened by chasing
the one divergence R36 left on TPC-H Q12.*

## 1. How this was reached

R36 moved Q12's join METHOD onto PG's (both now Hash Join). What
remained was the build side: PG probes with `lineitem` and hashes
`orders`; goopg does the reverse. Two candidate explanations were
eliminated by controlled probe, not by reasoning:

- **Estimate?** No. Hand-folding Q12's `date + interval` bound gives
  goopg `lineitem` = 28,724 against PG's 28,127 — essentially identical
  — and **the build side does not flip**.
- **Parallelism?** No. With `max_parallel_workers_per_gather=0` and the
  folded bound, goopg still hashes `lineitem`.

What did stand out: goopg priced the same `lineitem` seq scan at
**60,299** where PG priced it at **264,331**.

## 2. The finding

goopg's bare seq scans come out as *exactly* the CPU term:

```
lineitem  60012.55 == 0.01 * 6001255      (cpu_tuple_cost * rows)
orders    15000.00 == 0.01 * 1500000
```

with **zero page cost**, though `relpages` reads 136,393 and 28,435 and
`seq_page_cost` is 1.

`cost_funcs.go:192 costSeqscan` implements PG's formula correctly
(`seqPageCost*relPages + (cpuTupleCost + cpuOperatorCost*qualOps)*relTuples`).
Instrumenting its inputs (temporary `GOOPG_SEQCOST_TRACE`, reverted)
shows it is simply **never called** for a single-relation statement:

```
EXPLAIN SELECT * FROM lineitem          -> no SEQCOST line at all; plan cost 60012.55
EXPLAIN ... FROM orders, lineitem ...   -> SEQCOST pages=136393 tuples=6001255 -> total=196405.55
                                           SEQCOST pages=28435  tuples=1500000 -> total=43435.00
```

196,405.55 = 136,393 + 60,012.55, i.e. PG's `cost_seqscan` exactly.

So there are two models:

| | seq scan on `lineitem` |
|---|---|
| **search path** (`costSeqscan`) | **196,405.55** — PG-faithful |
| **legacy path** (no search) | **60,012.55** — page term absent |

A one-relation statement never enters the path search
(`makeRelFromJoinlist` returns at `len(items) == 1`, allpaths.c:3399-3404
— goopg's transcription; C-19h blocker 4 already records this), so it
is priced by the legacy model.

## 3. Why this matters more than a display discrepancy

**Both models appear inside a single plan.** R36's Q12:

```
->  Hash Join  (cost=271780.29..328627.53 rows=28724)
      ->  Seq Scan on orders    (cost=0.00..43435.00)   <-- SEARCH number
      ->  Seq Scan on lineitem  (cost=0.00..60299.79)   <-- LEGACY number
```

`orders` carries the search's 43,435.00. `lineitem` carries 60,299.79,
where the search's own formula with Q12's five quals would give
136,393 + 0.0225 x 6,001,255 = **271,421**.

So the planner is comparing a page-priced `orders` against a
page-free `lineitem` — the relation whose page count is *largest* is
the one priced without pages. `lineitem` looks **4.5x cheaper to scan
than it is**, which is exactly the direction that makes goopg prefer
hashing `lineitem` where PG hashes `orders`.

This is a candidate root cause for `join-method` and for the build-side
half of `join-order`, and it is not an estimate problem — R36 already
showed estimate corrections do not move join-order.

## 4. What is NOT yet established

- Whether the legacy number is what the SEARCH consumed for `lineitem`
  when choosing the build side, or only what EXPLAIN rendered. The two
  models' outputs both reach EXPLAIN; which one `add_path` compared is
  a separate question and must be instrumented, not inferred. This is
  the same trap that R35/R36 fell into twice (`EstimateRows` vs
  `calcJoinrelSize`), and the lesson has been paid for.
- Whether the legacy model omits the page term deliberately (a scan
  whose pages are assumed cached) or by omission.
- The blast radius: how many corpus plans mix the two models.

## 5. Next round

1. Instrument `add_path`/the build-side comparison to record WHICH cost
   each side carried when the orientation was chosen. Answer §4's first
   bullet before touching anything.
2. If the search consumed the legacy number, the fix is to route base
   scan costing through `costSeqscan` everywhere — which is also
   C-19h's successor ("build single-relation base-rel path lists as PG
   does"), already filed and already blocking `MaybeAddGather`'s
   retirement. The two items are the same work.
3. Gate as usual, and report `shape-delta.sh` counts alongside the
   categories (K50).

---

# CORRECTION — K62 answered, and §3 was WRONG

§4 said the consumed-vs-rendered question "must be instrumented, not
inferred", and then §3 had already inferred it. §3 is withdrawn.

Instrumenting `costSeqscan` on Q12's exact shape (temporary
`GOOPG_SEQCOST_TRACE`, reverted):

```
SEQCOST pages=136393 tuples=6001255 qualops=5 -> total=271421.24
SEQCOST pages=28435  tuples=1500000 qualops=0 -> total=43435.00
```

**The search priced `lineitem` at 271,421.24** — pages included, five
quals included, PG's `cost_seqscan` exactly. It is only EXPLAIN that
renders 60,299.79.

## What survives, what does not

- **K60 stands.** Two seq-scan cost models exist and differ by the
  entire page term. A one-relation statement is priced by the legacy
  one; `costSeqscan` is never called for it. Verified twice.
- **K61 is WRONG and is withdrawn.** The planner does NOT compare a
  page-priced `orders` against a page-free `lineitem`. The search saw
  271,421.24 for `lineitem` and 43,435.00 for `orders`, both
  page-priced and mutually consistent. The mixing happens only in the
  RENDERING.
- **The Q12 build-side divergence is therefore still unexplained.**
  Three candidates are now eliminated by measurement: the estimate
  (hand-folding the bound gives 28,724 vs PG's 28,127 and the side does
  not flip), parallelism (disabling it does not flip it), and the page
  term (the search had it all along).

## The defect this leaves, which is real but different

EXPLAIN reports a scan cost the planner did not use — 60,299.79 against
the search's 271,421.24, a 4.5x understatement on the largest relation
in the corpus. That does not affect plan choice, but it does affect
every cost-based artefact this workstream reads: `make plan-gate
MODE=semantic-cost`, the estimate-audit tables, and any human reading
an EXPLAIN to reason about why a plan was chosen. It is the cost twin
of the `EstimateRows` / `calcJoinrelSize` row-count split that
`cardinality_two_estimators_test.go` already pins for rows.

Filed as K63. It is a reporting-integrity fix, not a parity fix, and it
should be labelled as such so nobody expects categories to move.

## Where the build-side question goes next

Since cost INPUTS are now eliminated, the next probe is the comparison
itself: instrument `add_path` for the two hash-join orientations on
Q12 and record both candidates' total costs, or whether the
PG-shaped orientation was generated at all. That is the
`planner_verify_both_candidates_generated` discipline, and it is now
the only untested link.
