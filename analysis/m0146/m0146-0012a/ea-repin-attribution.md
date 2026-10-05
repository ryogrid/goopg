# M0146-0012a slice B — ea-ratchet re-pin attribution (AGENT.md G4, 2026-10-05)

The code commit pricing a correlated SubPlan in join quals makes TPC-DS
Q92 elect PG's Hash Join with the SubPlan as Join Filter. goopg's join
order differs from PG's: it joins web_sales to date_dim first, PG joins
web_sales to item first. That forms one new relation set:

| key | goopg est | actual | pg_est (scorer) |
|---|---|---|---|
| Q92:date_dim+web_sales | 232 | 4795 | null (PG forms no such node) |

## The misestimate is PG-shared

PG 18.3 was EXPLAINed read-only on :65438 (db tpcds025). The query is the
same two-relation join with Q92's date window:

```
EXPLAIN SELECT 1 FROM web_sales, date_dim
 WHERE d_date BETWEEN '2000-02-01' AND (cast('2000-02-01' as date) + interval '90 days')
   AND d_date_sk = ws_sold_date_sk;
Gather  (rows=234)  ->  Parallel Hash Join  (rows=98)
SELECT count(*) …  →  4795
```

PG estimates 234 rows, goopg 232, and the actual count is 4795. The error
is the date-window selectivity, which both engines share. goopg's estimator
did not regress.

This is the same key slice 1 reported as FIXED (`c21a02c93`). That FIX was
the relation set disappearing from goopg's plan, not an estimate change.
