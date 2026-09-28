# M0146-0019 — why goopg seq-scans where PG runs an Index Only Scan (recon)

Status: recon done 2026-09-28. Evidence: `analysis/m0146/m0146-0019/`
(probe SQL; the numbers below come from a private clone of the TPC-H bench
cluster on :5582 and read-only SELECTs on the PG references).

## Question

TPC-H Q13 (`customer_pk` under a Hash Join) and Q16 (`partsupp_pk` under a
Parallel Hash Join) show PG's Index Only Scan against goopg's Seq Scan. The
first step asked whether goopg generates the index-only path at all, or
generates it and loses on cost.

## Findings

1. **The goopg benchmark data is never vacuumed.** Index-only pricing is
   `cost_index` reducing heap fetches by `baserel->allvisfrac`
   (`heapPagesAfterVM`), and goopg reads the real visibility map. Every
   TPC-H table on the goopg bench cluster has `relallvisible = 0`: customer
   0/3575, orders 0/26545, partsupp 0/18029, lineitem 0/115293. The PG
   reference is fully all-visible: customer 3662/3662, orders 27814/27814,
   partsupp 18047/18047, lineitem 108026/129346. PG's TPC-DS references
   are all-visible on 15 of 25 tables through autovacuum (customer
   2853/2872). No goopg loader (`bench/tpch/build_schema_goopg.sh`,
   `scripts/tpcds-load.sh`, `scripts/tpcds-sf025-regression.sh
   load-goopg`) runs VACUUM. On a cold visibility map an index-only path
   costs exactly the equivalent index scan, so it cannot win. This is a
   corpus-state divergence, not a planner one.
2. **Q13 generates the path and loses on cost twice over.** After `VACUUM
   customer` on the private clone, `relallvisible` becomes 3575/3575 and the
   full index-only path drops from 9154.38 to 5570.38. It still loses to
   the seq scan at 5075. PG prices it at 3906.42 against a 5162 seq scan.
   The gap is exactly 415 index pages × `random_page_cost` 4: goopg's
   `indexProbeCostMultiplier` (2) also multiplies the index-page term of a
   single, non-repeated full index scan (`btreeIndexAMCostPages`,
   `numScans <= 1` arm). That multiplier is the owner-parked M0142-0005c
   calibration, left untouched here.
3. **Q16 never generates the path.** `addIndexOnlyPaths` refuses a leaf that
   keeps a local qual not consumed as an index qual. The qual's
   `ColumnRef.Index` values address the full leaf schema, and the refusal
   is documented in `pathindexonly.go`'s header. Q16's `partsupp` keeps
   `NOT (ps_suppkey = ANY (hashed SubPlan))`. PG keeps it as the Index Only
   Scan's Filter. Even `enable_seqscan = off` yields no index-only path on
   partsupp. That would be the first implementation step: remap the
   residual predicate onto the covered-column schema. It moves no plan
   until finding 1 is resolved.
4. **TPC-DS SF1 Q9's `reason_pkey`** is not a visibility-map case (PG's
   `reason` is one page with 0 all-visible) and stays open under this task.

## Routing

- Finding 1 needs an owner decision: VACUUM the goopg benchmark clusters,
  or add VACUUM to the loaders and reload. The loop does not write to or
  reload those clusters.
- Finding 2 is M0142-0005c lineage.
- Finding 3 is filed as M0146-0019a (implementation).
