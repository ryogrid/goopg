# 0125-0004 — TPC-DS Q75: single-side quals must reach the inner join's inputs

Status: implemented (2026-07-30)
Date: 2026-07-28
Milestone: M0125-0004 (§13.5 action 3; ledger row `tpcds-round2 Q75-eval-order`)

## Problem

Q75 is a failure this programme created — and the pass it replaced was false.

Before RC-1b (`5db0a067`), Q75 returned 100 rows at SF=1 and PASSed the SF0.5 gate. §13.4
item 1: "The pre-fix pass was **false**: the mispushed predicate was silently computing roughly
**half** of Q75's `all_sales` CTE totals (1,057,469 vs PG's 2,368,670 for 1998), and `LIMIT 100`
made the row-count oracle blind to it." With RC-1b the CTE aggregates are identical to PG on
every year and column — and the query now errors, deterministically (3/3):
`ERROR: division by zero`.

**This is also a live CI break.** Query 75 is in the nightly TPC-DS qualifying set with
`Q75,100,pinned` at `ci/batch/tpcds-row-anchors.csv:46` and **no** `expected-failures.csv`
entry, so the next nightly reports it and M-NIGHTLY — the standing preempting milestone — will
pick it up. That item *is* this task; do not open a second workstream for it.

### Root cause

`query75.sql`'s final block is an inner (comma) join of the `all_sales` CTE to itself:

```sql
FROM all_sales curr_yr, all_sales prev_yr
WHERE curr_yr.i_brand_id=prev_yr.i_brand_id
  AND curr_yr.i_class_id=prev_yr.i_class_id
  AND curr_yr.i_category_id=prev_yr.i_category_id
  AND curr_yr.i_manufact_id=prev_yr.i_manufact_id
  AND curr_yr.d_year=2002
  AND prev_yr.d_year=2002-1
  AND CAST(curr_yr.sales_cnt AS DECIMAL(17,2))/CAST(prev_yr.sales_cnt AS DECIMAL(17,2))<0.9
```

The CTE carries no year filter, so with correct data a `d_year = 2003` group with
`sales_cnt = 0` exists — PG has it too (`zerogroups = 1`). goopg evaluates the only side-mixed
**non-equality** conjunct, the division, as the hash-join residual on every matched pair,
*before* the outer `Filter`'s `d_year` equalities can exclude the pair. PG pushes the two
`d_year` quals to scan level first, so its division never sees the zero.

This is PG's architecture, not an accident of its evaluator: a qual referencing exactly one
relation is a **restriction clause**, and `distribute_restrictinfo_to_rels`
(`postgres/src/backend/optimizer/plan/initsplan.c`) attaches it to that relation's
`baserestrictinfo`, where it is applied at scan time; only clauses spanning two relations become
join quals. PG additionally cost-orders the survivors (`order_qual_clauses`,
`postgres/src/backend/optimizer/plan/createplan.c`).

Vanilla-PG compatibility is absolute here, so matching PG's qual **placement** is the fix — not
a special case for division.

## Design

### D1. Primary fix — push single-side quals onto inner-join inputs

Add the inner-join sibling of `pushOuterQualsIntoLaterals`
(`internal/planner/pushdown.go:132`) — equally, the binary-join sibling of RC-1b's
`pushSingleSourceFiltersAfterRemap`: when a conjunct above an inner join references columns from
exactly one input, wrap that input in a `Filter`.

Four load-bearing properties, three of them lessons already paid for:

1. **Run AFTER `remapWithBindings`**, in the MHJ-output coordinate space, and validate every
   `ColumnRef` **positionally by name**, declining on mismatch. This is RC-1b's entire lesson:
   the same pass running before the remap is what produced Q47/Q50.
2. **Run after join-order selection, and duplicate rather than move.** Leaving the conjunct in
   the residual as well makes the transformation idempotent on the *result set* — only the
   *error* behaviour changes, intentionally, to match PG — and guarantees the join **order**
   cannot move as a side effect.
3. **Place the `Filter` on the join's input node, never inside the CTE body.** `all_sales` is
   referenced twice with different restrictions; rewriting the body would apply one branch's
   filter to both.
4. **INNER joins only.** For an outer join a restriction on the nullable side changes which rows
   are null-extended. PG 18.3 no longer has the old `check_outerjoin_delay` guard — it was
   removed in the `nullingrels` rework, and safety is now expressed through a clause's
   nulling-relids and `is_pushed_down`. goopg has no nullingrels model, so this pass must simply
   **decline** on any non-INNER join, with a unit test pinning the decline.

Pin the pass after the last `applyJoinTreePosMap` call: that walker remaps `n.Filters` and
returns without recursing into `n.Tables[i]`, which is §12/D3's permanence hazard — a conjunct
pushed before it would never be revisited.

### D2. Scoping — so it cannot re-open the Q8/Q21 regression

Pushing filters toward leaves is exactly what `shouldAttachBeforeMHJ`
(`internal/planner/local_filters.go:154`) deliberately withholds: its comment records that
without the `SmallDimension` guard, "Slice A regresses Q8 / Q21 from PASS to CANCEL". Round-5 §6
quantifies the direction: MHJ-dropping cascades made Q5 and Q21 hang, Q9 time out, Q10 11.4× and
Q18 4.3× slower — **with identical row counts**.

Scoping rule:

1. fires only on an **inner** join;
2. fires only when the target input is a **CTE reference or derived-table scan**, never a
   base-relation leaf that is a candidate member of a `MultiHashJoin` subset;
3. base-relation leaf filters continue through `attachRelationLocalFilters`, which the
   `SmallDimension` gate governs and whose plans are snapshot-pinned.

**The blast radius is not zero.** TPC-H **Q15** (`q15_main.sql`) is `from supplier, revenue0`
where `revenue0` is a view, plus a scalar `(select max(total_revenue) from revenue0)`. If goopg
expands that view to a derived-table scan — which rule 2's own wording admits — Q15 is inside
the scope. The Gate below
therefore does **not** waive the timed TPC-H run on an unverified claim; it requires the
plan-diff to demonstrate the absence of a TPC-H hunk, and runs the timed sweep if one appears.

Nor is an empty plan diff claimed: adding a `Filter` **is** a plan-tree change. §12/B1 already
recorded a "no TPC-H risk by construction" argument being false for RC-1b. The falsifiable
criterion is instead: *the diff contains only added `Filter` nodes on inner-join inputs*, checked
against a firing set enumerated from the plans beforehand.

### D3. Secondary — cheap-quals-first residual ordering

Independently, order a join node's residual conjuncts cheapest-first, mirroring
`order_qual_clauses`.

- Defence in depth for shapes D1's scoping declines.
- **Not** a correctness mechanism, and must not be described as one: SQL does not guarantee that
  a `WHERE` conjunct protects a sibling from division by zero, and PG's own documentation says
  so. goopg's obligation is to reproduce PG's observable behaviour on this query set, which D1
  does structurally; D3 only narrows the residual window.
- goopg has no absolute cost model (`EXPLAIN` renders `cost=0.00..0.00`), so "cheapest" is a
  **syntactic** rank — equality/comparison on plain column refs, then function calls, then
  arithmetic containing `/` or `%`. Say so in the comment; do not imply `cost_qual_eval`.

**A decision rule the ledger's resume point lacks:** if the `d_year` equalities sit in the outer
Filter rather than in the join residual — which is the expectation — then D3 alone *cannot* fix
Q75, because the quals that must run first are not in the residual at all. Verify which it is
before investing in D3; if D1 alone closes Q75 and the SF0.5 gate is clean, defer D3 with a
ledger row, since it touches every join node in the executor.

## Verification

The row-count oracle is **not** sufficient — that is this defect's whole lesson (§13.4 item 3),
and it is why M0124-0005 is a prerequisite. Three parts:

1. **Value check on the CTE.** Re-run RC-1b's A/B probe: `WITH … SELECT d_year, count(*),
   sum(CASE WHEN sales_cnt = 0 …)` must match PG on every year and column, at SF0.5 and SF=1.
2. **Full query.** Q75 returns PG's 100 rows *and* PG's first-page values under the same
   `ORDER BY … LIMIT 100` — i.e. a matching checksum.
3. **The zero-denominator group survives.** `zerogroups = 1`, matching PG — goopg must not have
   "fixed" the error by dropping the row.

Capture an `EXPLAIN` of the final block before and after and attach both to the commit: the
mechanism above is the ledger row's stated mechanism and deserves a plan artifact, not only a
narrative.

Regression tests: `internal/planner` — pin the push on an inner join over two derived-table
inputs, and pin the **decline** on a LEFT JOIN's nullable side and on a base-relation MHJ
member; `internal/executor` — an end-to-end test of the reduced Q75 shape (self-join of a CTE
with per-side equalities and a division residual) asserting no `division by zero`.

## Gate

Units; `scripts/tpch-spotcheck.sh`; `make plan-diff LABEL=tpcds-round2-head` — hunks expected
only where a CTE-input filter appears, and **any TPC-H hunk (Q15 in particular) triggers the
full timed 22-query power run** per 0125-0002 D4; the TPC-DS SF0.5 gate with checksums. After
landing, confirm `ci/batch/tpcds-row-anchors.csv`'s `Q75,100,pinned` passes again.

## Why this is a distinct defect, not a regression to revert

RC-1b did not introduce the evaluation-order divergence; it removed the corruption that hid it.
Reverting RC-1b restores a query returning 100 rows computed from half the data — strictly
worse. §13.4 item 1's accounting stands: "one fewer wrong answer, one more error."

## Outcome (measured 2026-07-30)

Landed as `internal/planner/inner_join_qual_pushdown.go`
(`pushSingleSideQualsIntoInnerJoinInputs`), called from `planSelect` after the last
`applyJoinTreePosMap` inside `remapWithBindings` — pinned there because that walker remaps
`n.Filters` and returns without recursing into `n.Tables[i]`, so a conjunct pushed earlier
would never be revisited.

**Q75 is fixed by value, not by row count.** goopg's full SF0.5 result is now **byte-identical
to PG's** (`analysis/m0125-0004-q75/{goopg,pg}-q75-sf05.txt`, 103 lines / 100 rows, `diff`
clean), as are the `all_sales` CTE aggregates including the `d_year = 2003` group that carries
the genuine `sales_cnt = 0` (`zerogroups = 1`). No `division by zero`. The
`Q75,100,pinned` anchor at `ci/batch/tpcds-row-anchors.csv:48` therefore passes on its
intended meaning rather than on a masked one, and needs no `expected-failures.csv` entry.

**Blast radius measured, not asserted.** EXPLAIN over all 99 SF0.5 queries before and after
(`analysis/m0125-0004-q75/explain-all-{base,fixed}/`) makes the firing set exactly **seven** —
Q4, Q11, Q31, Q39, Q64, Q74, Q75 — and in every one of them the *entire* plan delta is added
`Filter:` lines on a CTE-scan input (`plan-diff-firing-set.txt`). No join reordered, no node
kind changed, which is the shape property D1's "duplicate, never move" was designed to buy.

Value verification of the firing set (`QUERIES="4 11 31 39 64 74 75"`, checksummed SF0.5
gate): **MISMATCH=0 CKMISMATCH=0 ERROR=0**. Q11/Q39/Q74 PASS checksum-verified, Q75 PASS at
100 rows (`ck=n/a` — a saturated `LIMIT` window has no stable row set, which is why the
byte-diff against PG above carries the real evidence). Q4 SKIP: the *oracle itself* is
TIMEOUT, so there is no baseline to diff.

Q31 and Q64 TIMEOUT at the 300 s budget — **pre-existing, and proved so by A/B rather than
assumed from host load.** Re-running both with the single call line disabled reproduced the
timeout at 332 s / 333 s against 332 s / 336 s with it enabled. (The nightly CI batch was
running throughout, so `FORCE=1` was used and *no timing here is a measurement* — the A/B is
a pass/timeout verdict at a fixed budget, which is what the contention permits.)

TPC-H: `make plan-diff LABEL=tpcds-round2-head` reports **22/22 MATCH including
`Q15a-VIEWBODY`** — the `revenue0` view named above as the real blast-radius candidate. TPC-H
plans do not move, so the full timed 22-query power run this doc's Gate section makes
conditional on a TPC-H hunk is **not triggered**. `scripts/tpch-spotcheck.sh` PASS
(Q12 = 2, Q13 = 35).
