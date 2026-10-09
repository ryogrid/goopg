# M0146-0019a evidence — index-only paths keep a residual Filter

- `q16-plans.txt` — TPC-H Q16: goopg HEAD (Nested Loop, 8.36M), goopg after
  (PG's Parallel Hash Join; partsupp leaf a Parallel Seq Scan), PG 18.3.
- `tpch-parity-before.txt` / `tpch-parity-after.txt` —
  `scripts/pg-plan-parity-diff.py` over the acceptance-arm EXPLAIN captures
  (HEAD `tmp/arm-m8y-explain.txt`, after `tmp/arm-m19a-explain.txt`) against
  `tmp/m0146-0015d/tpch/m0146-0015d-tpch-pg.plans.txt`. CATEGORIES-EXCL-MATCH
  join-order 9 -> 8, join-method 4 -> 3; Q16 keeps only scan-type.
- `q16-probe-mult1.txt` — the same server with `GOOPG_INDEX_PROBE_MULT=1`:
  PG's exact shape (`Parallel Index Only Scan using partsupp_pk`, Filter
  `NOT (ANY (ps_suppkey = (hashed SubPlan 1).col1))`, Workers Planned 4),
  IOS cost 18812 vs PG 19178. Q16 rows on that plan match `:65433`
  (md5 03bcec29…, 18215 rows).
- IOS cost decomposition (serial, partsupp_pk, allvisfrac 1): index pages
  3078 x random_page_cost 4 x indexProbeCostMultiplier 2 = 24624, plus
  800000 x cpu_index_tuple_cost 0.005 = 4000, plus 800000 x (cpu_tuple_cost
  + 1 op) = 10000 → 38624. PG: 3082 x 4 + 4000 + 10000 + 350 (hashed SubPlan
  startup) = 26678.
- TPC-DS: no plan change at either scale (sweep 99/99 same; fire set no
  changed query). PG's Q23 IOS (`customer_pkey`) has no Filter — not this
  task's shape.
- Regress A/B (14 files): create_index's correlated-subplan inner and union's
  first EXCEPT move to PG's `Index Only Scan` (the union one with PG's
  `Filter: (unique2 <> 10)`); others are known flaps. A first run panicked
  on `onek_with_null` (index (unique2, unique1) covering every column) —
  `baseRelLayout`'s equal-width identity shortcut; fixed and witnessed.
