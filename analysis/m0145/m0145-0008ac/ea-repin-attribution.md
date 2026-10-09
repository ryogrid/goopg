# M0145-0008ac — ea-ratchet re-pin attribution (AGENT.md G4, PG-shared extension)

Change under review: `c317b037b`. A CTE reference in a sublink body is now
pulled up as a base rel (`GOOPG_PULLUP_CTE_LEAF` retired). It changes the
join orders of TPC-DS Q14, Q23 and Q95. Values are identical: SF0.25 sweep
96/96, and the fire set at SF0.25 and SF1 introduced no timeouts.

Capture: the gate's own `make ea-ratchet` run on the candidate
(`tmp/c20a/ea-capture.txt`, re-scored with `EA_CAPTURE`).

## Ratchet result before the re-pin

9 fixed, 8 new. All 8 new keys are new relation sets that exist only because
the join order changed. The scorer marks every one UNMATCHED-IN-PG, because
PG joins the same relations in a different order.

## Per-key attribution: each misestimate is PG-shared

| key | goopg est | actual | PG's estimate at the nearest scope |
|---|---|---|---|
| Q14:cte:cross_items+store_sales | 44 | 688034 | 44 at {cross_items, item, store_sales} (plans-pg/Q14.txt line 85); actual 688034 (PG EXPLAIN ANALYZE) |
| Q14:catalog_sales+cte:cross_items | 22 | 344663 | 22 at {cross_items, item, catalog_sales} (line 105) |
| Q14:cte:cross_items+web_sales | 11 | 172139 | 11 at {cross_items, item, web_sales} (line 125) |
| Q14:cte:cross_items+date_dim+store_sales | 1 | 21135 | 1 at {cross_items, item, store_sales, date_dim} (line 84) |
| Q14:catalog_sales+cte:cross_items+date_dim | 1 | 11597 | 1 (line 104) |
| Q14:cte:cross_items+date_dim+web_sales | 1 | 6094 | 1 (line 124) |
| Q23:catalog_sales+cte:frequent_ss_items | 102108 | 0 | 103254: PG 18.3 (`:65438`, tpcds025) on `frequent_ss_items` + `catalog_sales WHERE cs_item_sk IN (SELECT item_sk FROM frequent_ss_items)` — the same two-relation scope |
| Q23:cte:frequent_ss_items+web_sales | 51539 | 0 | 51474, measured the same way with web_sales |

The Q14 error comes from PG's own `rows=1` estimate for the
`cross_items` CTE scan, which goopg reproduces. The Q23 error is the semi
join's size over a 200-group unique-ified CTE, and PG computes the same
number for the same scope.

## Fixed keys

- The old Q14/Q23 keys are relation sets the new join orders no longer
  form. Their removal is the plan change, not an estimate change.
- Q95's key is gone for the same reason.
- `Q92:date_dim+web_sales` is kept in the baseline on purpose. Its
  "fix" is M0146-0049d1's instrument artefact: the node no longer
  executes, because an empty-inner hash join skips it, so it is unscored.
  The estimate itself is unchanged.
