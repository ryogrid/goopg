# M0145-0008b: why TPC-H Q18 and Q22 run slower on the jointree default

Status: recon complete 2026-09-24. Follow-up implementation: M0145-0008e,
M0145-0008f, M0145-0008g.
Task: `.ralph/fix_plan.md` M0145-0008b (Kind: recon, Parent: M0145-0008).
Evidence: `analysis/m0145/m0145-0008b/`. It holds EXPLAIN ANALYZE of both arms
on one binary (`bb90e51a4`, private TPC-H lane, parallel mode, `PGSHAPED=1`,
`JOINTREE=0/1`) and the PG 18.3 probes.

## Measured

| query | legacy (`JOINTREE=0`) | jointree default | PG shape |
|---|---|---|---|
| Q18 | 9.94 s | 19.93 s | HashAggregate semi body; Parallel Hash Join |
| Q22 | 0.51 s | 1.38 s | Nested Loop Anti Join + Index Only Scan on `order_customer_fkidx` |

These are the same ratios as the flip's ledger row (1.6x → 2.0x and 3.2x →
2.7x).

## Q18: two factors

1. **Unnarrowed join tuples.** Both arms build the same `orders ⋈ customer`
   hash (1.5M rows) and probe it with parallel `lineitem`. The legacy arm
   carries narrowed rows: the join width is 268, and the build takes 581 MB in
   one batch. The jointree arm carries full rows: width 1622, 869 MB, and **2
   batches** (it spills). The probe join then takes 5.46 s per worker against
   2.67 s. Q18 needs 5 columns of `customer`/`orders`, and PG's widths are
   25–45. → **M0145-0008e**: the jointree lowering does not apply the column
   narrowing the legacy lowering applies.
2. **HashAggregate executor throughput.** For the grouped semi-join body
   (`GROUP BY l_orderkey HAVING sum > 313`, 6M rows → 1.5M groups), the
   jointree arm elects PG's plan, `HashAggregate` over `Seq Scan`. The legacy
   arm used `GroupAggregate` over the `l_orderkey` index. goopg's
   HashAggregate build takes 12.2 s inside the plan (legacy: 6.0 s). PG runs
   the same HashAggregate in 3.3 s serial. The election is PG's and the slow
   part is goopg's executor. → **M0145-0008f**.

## Q22: an estimate gap that flips the election

- The legacy arm's NL Anti Join + Index Only Scan is PG's shape. Legacy
  reaches it by a rule, and the cost says 265k.
- The jointree arm prices the same NL anti at 265k too, so it elects a Hash
  Anti Join over all of `orders` (66k). That builds an 875 MB hash in 1.08 s,
  where PG's NL anti probes 729 customers per worker.
- The cause is the outer estimate:
  - PG: `substr(c_phone,1,2) IN (7 values)` → 0.035 (scalararraysel, 7 ×
    eqsel's default 1/200 for an expression with no statistics), and
    `c_acctbal > (InitPlan)` → 1/3, so 729 rows per worker.
  - goopg: the IN-list arm of `clauseSelectivity` /
    `clauseSelectivityWithSource` returns `defaultGenericSelectivity` (1/3)
    for a non-`ColumnRef` operand, and the leaf shows 16666 per worker, 23x
    PG. The legacy leaf, which carries only the IN list, shows 50000 per
    worker, i.e. no reduction at all.
- A second lever to check in the same task: goopg charges ≈5.3 per outer row
  for the anti NL, where PG charges ≈0.8 (`final_cost_nestloop` semi/anti
  early exit).
→ **M0145-0008g**.

## Not the cause

- The estimator fixes and the NULL-key guard: already excluded by the flip's
  own triage.
- M0146-0002e: Q22's InitPlan filter moving to the customer scan (PG's place)
  changed neither arm's election. The legacy arm keeps it on the NL anti.
