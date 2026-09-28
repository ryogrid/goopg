# M0146-0009c — per-level decomposition of the 5 NEW ea-ratchet findings

`Kind: recon` · `Parent: M0146-0009` · filed by M0146-0009b · closed
2026-09-28. Evidence: `analysis/m0146/m0146-0009c/` + `tmp/m0146-0009c/`.

## Question

The corrected ea-ratchet (real SF0.25 clone on a private lane instead of the
stale empty clone + foreign :5534 server that produced the earlier vacuous
PASS) surfaced 5 NEW findings, all on plans byte-identical
baseline-vs-candidate. Per the 0009a recipe: decompose each flagged relset
level-by-level on both engines and classify — goopg defect, PG-shared
estimate artifact, or search-shape divergence.

## Method

- goopg `EXPLAIN ANALYZE` on private clone `tmp/c20a/data-sf025` (:5541,
  `goopg-c0009c.scope`); PG 18.3 `EXPLAIN ANALYZE` on read-only oracle
  `:65438`/`tpcds025`.
- Standalone `EXPLAIN` probes of the contested relsets with the queries'
  complete predicate sets on **both** engines (the S2b-14 protocol —
  omitting a predicate once manufactured a 15x phantom divergence).
- `GOOPG_PGSHAPED_DP_TRACE=1` enum trace for Q85 (`server-trace.log`) to
  check whether the PG-order candidates exist in goopg's search space.

## Findings

### (a) Q85 ×3 — PG-shared estimate, relset-key churn

All three findings (`reason+wr+ws` GM/NL, `cd+reason+wp+wr+ws` NL; est=1 vs
~596/580) cascade from one node: `Hash Join {ws ⋈ wr} rows=1` on
`(ws_item_sk, ws_order_number) = (wr_item_sk, wr_order_number)` —
sales↔returns natural-key correlation invisible to the independence
assumption. Standalone `ws ⋈ wr` under the full predicate set: **both
engines est rows=1**; PG's own plan ends at rows=1 too (probe-order tree).
The findings score because goopg's elected tree materializes relsets PG's
tree never builds (`pg_est=null`). DP trace shows the PG-order candidates
exist (`{ca+wr+ws}` npaths=6 cheapest=nli); election lost by epsilon, not by
absence. Same disposition as M0141-S2b-14. **No fix.**

### (b) Q78 — PG-shared correlation underestimate

`Merge Left Join {dd+sr+ss+wr+ws}` goopg est 1,407 vs PG's semantically-
equivalent node est 1,371 — 1.9% apart, both ~90x under actual 123,049
(store↔web customer/channel correlation). **No fix.**

### (c) Q83 — real estimator gap (narrow, attainable)

`Merge Join {cr+dd+item+sr}` goopg est=1 vs actual 22; legs are lone-
`GROUP BY i_item_id` CTE outputs (est 20/10 — identical to PG's leaf ests).
PG est 10 standalone / 5 in-corpus via `examine_simple_variable`'s
RTE_SUBQUERY lone-`groupClause` arm (selfuncs.c:5876-5883): `isunique` →
relative-unique → `nd = leaf->tuples` → sel = 1/max(20,10). goopg:
`resolveJoinVarColumn` fails on `table==nil` `*CTEScan` leaves and
`subqueryUniqueOutput` (joinsearchseam.go:1034) is set only for pulled
ANY-subquery leaves → tuples=0 → nd=200 default → sel=0.005 → est=1.

This is exactly the arm M0145-0020 identified as PG's *one* statistics-
producing CTE case — 0020's own fires all hit the early exits
(setOps/groupingSets/multi-key group), so its measured-no-gap closure does
not cover Q83. **Filed M0146-0009e** (impl): propagate `isunique` +
`tuples=leaf.baseRows` to `examineJoinVar`'s `!ok` arm for FROM-clause
derived leaves (`*CTEScan`/`SubqueryScan`) when the probed output column is
the body's lone GROUP BY/DISTINCT key. Sibling coverage: Q95 `cte:ws_wh`
est=1 vs 22 likely same class.

## Ledger

Three M0146-0009c rows flipped `resolved`: (a)/(b) PG-shared, no defect;
(c) routed to M0146-0009e.
