# M0146-0005 slice 22 (recon): Q82 is a btree skip-scan gap; Q8 decomposes

Recon slice — no production code. Two of the task's remaining untraced
SF0.25 records are attributed here and filed as children
(`M0146-0005v`, `M0146-0005w`).

## Q82 — `join-method` depth=7: PG Nested Loop vs goopg Hash Join

Witness: `q82-pg-sf025.txt` (PG 18.3, `:65438/tpcds025`),
`q82-goopg.txt` (private clone on :5533, HEAD binary),
`q82-dppath.txt` (GOOPG_PGSHAPED_DP_TRACE=1 on the same clone),
`q82-pkey.txt`.

PG drives three nested loops from the 1-row `item` filter:

    Nested Loop
      -> Nested Loop
        -> Nested Loop
          -> Parallel Seq Scan on item            (1 row est)
          -> Index Scan using inventory_pkey      Index Cond: (inv_item_sk = item.i_item_sk)   cost 0.43..1822 rows=199
        -> Index Scan using date_dim_pkey
      -> Index Only Scan using store_sales_pkey

`inventory_pkey` is `btree (inv_date_sk, inv_item_sk, inv_warehouse_sk)`
— `inv_item_sk` is the SECOND column. PG 18 executes this via its skip
arrays (`_bt_skiparray`, nbtutils.c / nbtpreprocesskeys.c): the scan
cycles through the 209 distinct `inv_date_sk` values
(`q82-pkey.txt`: pg_stats n_distinct 209 = count(distinct)), doing one
descent per distinct prefix. `btcostestimate` (selfuncs.c:7342+) models
it exactly so: `num_sa_scans *= get_variable_numdistinct(skipped attr)`.

goopg admits index keys only on a gapless leading prefix
(`pathbitmap.go:304`'s explicit `break`, "PG 18 would use a btree skip
scan" comment; same shape in `pathindexrestrict.go`'s key collectors).
Its `{1}` pathlist offers only:
- `index.parameterised reqouter={2}` — leading-column `inv_date_sk`
  probe (rows=4280),
- `index.parameterised reqouter={0,2}` and `{2,3}` — full-prefix probes,
- `index.ordered` full-index scan (160395),
- bitmap variants of the same.

No `reqouter={0}` path exists, so `NL (item -> inventory probe)` is
never priced; `{0,1}` elects `Parallel Hash Join` (partial 25913) and the
first divergence lands at depth=7 `join-method`.

The gap was already ledgered (deferral_ledger, M0145-0029-2b item 4)
with no measured consumer; Q82 is that consumer. Filed as
`M0146-0005v` (impl) with the admission/costing/executor resume point —
see the fix_plan entry.

## Q8 — `join-order` depth=3: same NL, different leaf sets

Witness: `q8-pg-sf025.txt` (fresh PG EXPLAIN) vs slice21's
`candidate.plans.txt` Q8 capture. Decomposition of the leaf-set
mismatch:

- (a) **Missing `Subquery Scan on a1` leaf.** PG plans each leaf arm of
  a set operation via `subquery_planner`; the subquery arm is a
  `SubqueryScan` RTE and counts as ONE leaf. goopg plans the arm
  inline, so `customer`/`customer_address` leak into the parent's leaf
  set. Filed `M0146-0005w`.
- (b) **Missing `Materialize` x2** — the rescanned inner of the top NL
  (`Materialize (NL ...)`) and the `Materialize (HashSetOp ...)`. That
  is `materialize_inner`/`Materialize` coverage — M0146-0010's
  territory, cross-referenced not re-filed.
- (c) **Arm order.** PG's INTERSECT smaller-groups-left swap puts the
  grouped arm left (its est 1041); goopg kept written order, meaning its
  `setOpArmGroups` for the `substr(ca_zip,1,5)` scan arm estimates ≤
  the aggregate's 1070 — an expression-statistics gap
  (`ca_zip` n_distinct = 3124 on PG). Estimate-level; noted, not filed —
  candidate fodder for M0146-0009.

## Residual-map bookkeeping

Post-slice21 census (SF0.25): `join-method` Q1, Q23, Q30, Q55, Q65,
Q79, Q82, Q92; `join-order` Q4, Q8, Q11.

| record | cause | owner |
|---|---|---|
| Q23, Q30, Q55, Q79 | stale `char(n)` heaps + probe multiplier (triage table) | owner reload / M0142-0005c |
| Q82 | btree skip-scan missing | `M0146-0005v` (this slice) |
| Q8 | Subquery-Scan leaf + Materialize + arm-order estimate | `M0146-0005w` + M0146-0010 + M0146-0009 |
| Q1, Q92 | sublink decorrelation — PG keeps a SubPlan | M0145/unnest family |
| Q65 | aggregation strategy | M0146-0003 |
| Q4, Q11 | PG cost tie (0.02) | re-check after reload |

Q14's earlier depth-7 record is gone from the current census (resolved
by slices 15–20's set-op/estimate work).

## Gates

Recon slice — no production-code change, so no plan/value gate is owed.
`go build ./...` clean; the commit rides the standard pre-commit pgbench
smoke.
