# M0146-0005 slice 8 (M0146-0005g): a set-operation subquery is a unique inner

Witness: TPC-DS Q14's `cross_items` CTE (`q14-cross-items.sql`), first
divergence `join-method` at depth 2 at both scales. PG hash-joins `item` to
the hashed INTERSECT (45505.15..47110.90). goopg ran a merge join (46296)
because its own hash join in that orientation cost 44007..50843
(`goopg-q14-cross-items-dppath-before.txt`).

## What PG charges (`pg-q14-cross-items-hjcost.txt`)

The winning orientation reconciles only as an inner-unique hash join:
45505.15 + 1464 (outer scan) + 135 (hashing 18000 rows on 3 keys) + 6.75 +
0.01 = 47110.90. The 6.75 is the unmatched-probe walk,
0.0075 × 18000 × 1 × 0.05. With about 0 matched rows, the matched walk and
the tuple charge vanish.

PG proves the INTERSECT subquery unique through `innerrel_is_unique` →
`rel_is_distinct_for` → `query_is_distinct_for`
(postgres/src/backend/optimizer/plan/analyzejoins.c). A top set operation
without ALL is distinct for any clause set that equates every output
column, and the three hash clauses equate all three.

goopg's uniqueness proof only knew base relations with unique indexes. The
set-op leaf got no proof, so the default 0.1 bucket (stats-less outputs,
0005c) charged a matched walk of about 5800.

## Change

`setOpLeafDistinctFor` is the set-operation arm of `query_is_distinct_for`
over the search leaf (`*SetOp` with `All == false`, every output column
equated). `innerRelProvenUnique` falls back to it.

After the change goopg's cross_items plan is PG's:
- item is probed against the hashed INTERSECT, 44007.02..45044.77;
- the run cost is exactly 896 + 135 + 6.75.

## Results

- `census-diff-sf025.txt` / `census-diff-sf1.txt`: Q14's record moves from
  `join-method` to `join-order` at the same node, and nothing else moves.
  The residue is rendering: PG deparses the INTERSECT output columns
  through to the leftmost branch (`iss.i_brand_id`), and goopg prints the
  subquery column names (`brand_id`). The census compares Hash Cond text.
  Filed as M0146-0005h.
- `sf025-sweep.txt`: 96/96; one plan changed (Q14).
- `tpcds-fireset.txt`: fires Q14 only, no timeouts.
- `tpch-plan-parity.txt`: TPC-H 5/22, census identical. Spotcheck PASS;
  the acceptance arm has 24 MATCH on values.

## Not ported

The DISTINCT and GROUP BY arms of `query_is_distinct_for` for a subquery
leaf are not ported (ledgered).
