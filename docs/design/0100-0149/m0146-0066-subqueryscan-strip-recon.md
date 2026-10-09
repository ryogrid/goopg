# M0146-0066 — recon: where goopg's Subquery Scan strip differs from PG's

Status: recon done 2026-10-07. Parent: M0146-0005. Evidence:
`analysis/m0146/m0146-0066/` (probe SQL and both engines' plans).

## Question

`stripTrivialSubqueryScans` (M0146-0005w) replicates setrefs.c's
`trivial_subqueryscan`. At SF0.25 the count of `Subquery Scan` nodes still
differs from PG 18.3 (goopg/PG):

| query | goopg | PG |
|---|---|---|
| Q5 | 3 | 4 |
| Q23 | 2 | 0 |
| Q44 | 0 | 4 |
| Q49 | 3 | 6 |
| Q67 | 0 | 1 |
| Q71 | 0 | 3 |
| Q77 | 0 | 1 |
| Q78 (SF1) | 0 | 1 |

The census pairs are as filed. PG's verdict depends on the scan's own
tlist: `trivial_subqueryscan` keeps the node when the tlist length differs
from the subplan's, or when any entry is not the Var for the same position.
The questions are where goopg's proxy for that tlist (consumption order,
plus the pathtarget-or-physical regime) parts from PG's, and what lies
outside the pass's reach.

## Findings

PG plans were taken read-only from the reference cluster (`:65438`, db
`tpcds025`, EXPLAIN only); goopg plans from the SF0.25 sweep capture. Small
probes were run against scratch PG 18.3 and a throwaway goopg.

### A. Over-keep: the pass never enters a sublink body

- **Witness.** Q23's `max_store_sales` CTE: goopg keeps `Subquery Scan on
  __sq_1a7` under the InitPlan's Aggregate, and PG strips it.
- **Probe.** `b > (select max(cs) from (select a, sum(b) cs from t group by
  a) x)`.
- **Mechanism.** `stripTrivialSubqueryScans` runs once at `Plan()`'s tail
  over the statement's tree. A sublink or InitPlan body is a separately
  planned plan hanging off an expression, so the pass never visits it,
  while PG's setrefs walks every subplan.
- Filed **M0146-0091**.

### B. Over-strip under and over a WindowAgg

- **B1, a leaf below a WindowAgg.**
  - `make_window_input_target` (planner.c) builds the WindowAgg's input
    target with the sort/group-ref columns (partition and order keys)
    first, in final-target order, and the remaining Vars after them.
  - A subquery leaf below the window's Sort therefore gets a reordered
    tlist, `(v.s, v.a)` over a subplan emitting `(a, sum(b))`, and PG keeps
    it as out of order.
  - goopg's proxy is first-reference order over the region, which reads
    `[0, 1]` and strips.
  - Witnesses: Q44 `v1`/`v2`, Q49 `in_web`/`in_cat`/`in_store`, Q67 `dw1`,
    and probe 3.
- **B2, a subquery over a WindowAgg body.**
  - The WindowAgg's tlist carries its input columns (sort keys) besides
    the window results. A consumer reading only the subquery's outputs
    sees a tlist shorter than the subplan's, and PG keeps the node.
  - Probe: `select item, rnk from (select item, rank() over (order by
    ratio) rnk from … in_web) w where rnk <= 10` keeps `Subquery Scan on
    w`, and goopg strips it.
  - Q44's `v11`/`v21` are this shape under a Sort that goopg does not plan
    (class D).
- Both halves filed as **M0146-0092**.

### C. Over-strip: appendrel members that are joins

- **Witnesses.** Q71's `ws`/`cs`/`ss` members, and Q5's `"*SELECT* 2"`.
- **Mechanism.** `pull_up_simple_union_all` keeps each member that is not
  itself simple as a subquery RTE, planned on its own and wrapped in a
  `Subquery Scan on "*SELECT* n"` under the Append. The parent consumes a
  subset of the member's outputs, so the wrapper stays.
- **Probe.** Two `s1 ⋈ d` / `s2 ⋈ d` members: PG shows two `"*SELECT* n"`
  scans, and goopg hash-joins the members bare.
- Filed **M0146-0093**.

### D. Not the strip pass: a plan-shape difference upstream decides the label

- **Q77 `cr`.** PG puts a Materialize on the nested loop's inner, and
  Materialize's `CP_SMALL_TLIST` makes the subset-consumed scan
  non-trivial. goopg elects no Materialize, and under the bare nested loop
  (physical tlist) stripping is right.
- **Q44 `v11`/`v21`.** PG sorts the window output for the merge join
  (`Sort Key: v11.rnk`); goopg merges on the WindowAgg directly.
- Both belong to the join-order and materialization work, not this pass.

### Incidental: a member-constant qual stays on the Append

- **Probe.** `… (select v, k, 1 as src from s1 union all select v, k, 2 as
  src from s2) u … where src > 0`.
- **PG** pushes the qual into each member, where `1 > 0` folds to true, and
  shows a bare Append.
- **goopg** leaves `Filter: (src > 0)` on the Append. This is a
  qual-placement divergence, filed **M0146-0094**.

### Q78 (SF1 only)

Not reproduced in this recon; the SF1 capture was not re-run. Left to
M0146-0093's census, since Q78 is UNION ALL-shaped.

## Not covered

No code changed. The census counts above are the filed ones and were not
re-measured.
