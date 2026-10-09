# M0146-0027 closure — routing the D3-partialpath records (2026-10-05)

Inputs, captured by the M0146-0020b slice-2 fire-set run at HEAD `11f2eb63e`:

- `divergence-classes-sf025.txt`: `scripts/pg-plan-divergence-class.py`
  output. D3-partialpath n=17.
- `first-divergence-sf025.txt`: `scripts/pg-plan-first-divergence.py` over
  the SF0.25 candidate plans and PG 18.3's.

Every record whose first divergence is a parallel-reach question was worked
in slices 1-6. The remaining 17 route as follows; none is a reach gap.

| query | first divergence (SF0.25) | route |
|---|---|---|
| Q2 | depth 2, CTE: PG Finalize HashAggregate \| goopg HashAggregate | **M0146-0065** (filed): the inlined single-reference CTE `wscs` is a UNION ALL that never becomes an appendrel, so the CTE body has no partial path (repro `q2-inlined-cte-union-all-repro.sql`) |
| Q17 Q25 Q29 | depth 4: PG Nested Loop \| goopg Gather Merge | cost epsilon (slice 6 measurement); M0146-0014 residual |
| Q26 Q33 Q45 | GroupAggregate input: PG Sort (above Gather) \| goopg Gather Merge (worker Sort) | cost tie: on Q26, PG's own formulas put Gather Merge over a worker Sort at 14290.17 and Sort over Gather at 14290.19; the join spines are identical. M0146-0014 residual |
| Q19 Q40 Q61 | PG Finalize (partial) aggregate \| goopg one-phase aggregate | cost margin, the Q62/Q99 precedent from slice 3: both shapes are filed. On Q40, PG's partial path totals 13987.63 against about 13987 for the serial Sort over Gather. M0146-0014 residual |
| Q16 | depth 4: PG Nested Loop \| goopg Gather | rides Q16's join-spine record (slice 6 note); join order |
| Q39 | depth 4: PG Hash Join \| goopg Gather | Gather placement inside the CTE join tree: PG gathers below the `item` hash join. Join order / cost; M0146-0014 residual |
| Q92 | depth 3: PG Nested Loop \| goopg Gather | the correlated SubPlan is not parallel-restricted (M0146-0012a's ledgered row) |
| Q5 Q42 Q52 | join-order at depth 6-9 | join-order family; the first divergence is not parallelism |
| Q76 | depth 6: PG Parallel Hash Join \| goopg Nested Loop in an Append leg | per-leg join method (the slice 5 residue); M0146-0014 residual |

## Q2 mechanism

On a fresh cluster, `analysis/.../q2-inlined-cte-union-all-repro.sql` shows:

- A UNION ALL written as a FROM subquery is pulled up as an appendrel, at
  top level or inside a CTE body: `Finalize HashAggregate -> Gather ->
  Partial HashAggregate -> Parallel Hash Join -> Parallel Append`, as in PG.
- The same UNION ALL reached through an inlined single-reference CTE (`with
  w as (… union all …)`) is joined serially through a plain `Append`.
  The displayed costs are also inconsistent: the Hash Join shows less
  than its own Append input.

PG's `inline_cte` turns the reference into an RTE\_SUBQUERY, and
`pull_up_subqueries` then pulls it up through `pull_up_simple_union_all`.
goopg's inlined body does not reach that path.
