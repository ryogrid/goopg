# M0145-0008ad recon: why TPC-H Q18 keeps a Hash Semi Join that PG plans as a Hash Join

Status: **RECON COMPLETE 2026-09-25**. No production code changed. Task:
`.ralph/fix_plan.md` M0145-0008ad (Kind: recon, Parent: M0145-0008ab).
Evidence: `analysis/m0145/m0145-0008ad/`. Filed: **M0145-0008ae**.

## Question

After M0145-0008ab, Q18's `orders ⋈ ANY_subquery` pair is offered both as a
SEMI join and as a unique-ified inner join. goopg elects the Hash Semi Join
and PG elects the Hash Join. The task filed this as a costing question.

## goopg side

Instrument: a private TPC-H clone, `GOOPG_PGSHAPED_DP_TRACE=1`,
`scripts/tpch-estimate-audit-arm.sh` with `PLAN_ONLY=1`, `-queries 18`,
`-serial=false`. Its `DPPATH` records for relids `{1,3}` are in
`goopg-dppath-orders-anysubquery.txt`.

- The Hash Semi Join and the unique-inner Hash Join cost **exactly** the
  same: 246725.1975 serial and 233111.13 partial.
- On the exact tie `addPath` keeps the incumbent. The SEMI arm is offered
  first, so the unique-inner paths come back `dominated`.

## PG side

Instrument: the M0144-0005 instrumented build
(`tmp/pg18-optdebug/install`, `debug_plan_candidates`). The PG TPC-H
reference refuses replication connections, so `orders`, `lineitem` and
`customer` were copied with a read-only `pg_dump` into a private cluster on
port 5562, with the reference's planner GUCs, and ANALYZEd there. Its Q18 plan
has the reference's shape: an inner Hash Join over the grouped `lineitem`,
with costs within sampling noise. Its `PLANCAND` records for `rel=(b 2 5)`
are in `pg-plancand-orders-anysubquery.txt`.

**Every one of PG's 63 candidates for the pair has `jt=0` (JOIN_INNER); none
is a SEMI join.** So the question was not a cost tie at all. PG removes the
semijoin before the join search even starts:

- `reduce_unique_semijoins`
  (`postgres/src/backend/optimizer/plan/analyzejoins.c:844`, called from
  `query_planner`, `postgres/src/backend/optimizer/plan/planmain.c:234`) scans
  `join_info_list`. For each JOIN_SEMI whose `min_righthand` is a single
  baserel that `rel_supports_distinctness`, it asks
  `innerrel_is_unique(..., JOIN_SEMI, restrictlist, true)`.
- If the RHS is unique for the join clauses, it **deletes the
  SpecialJoinInfo**.
- For an RTE_SUBQUERY the proof is `query_is_distinct_for`, which
  M0145-0008ab already ported. For an RTE_RELATION it is a unique index
  covering the clause columns.
- With no SpecialJoinInfo left, the pair is an ordinary inner join
  everywhere: legality, estimation (`eqjoinsel_inner`, not
  `eqjoinsel_semi`) and path generation.

goopg has no port of `reduce_unique_semijoins`. Every pulled semijoin keeps
its SJInfo, so the search keeps offering the SEMI arm and wins the tie with
it.

## Consequence

The fix is not in hash-join costing. It is the missing pre-search pass,
filed as **M0145-0008ae**. It must handle two RHS kinds:
- a derived `ANY_subquery` whose `jtPulledBody.distinct` holds;
- a base relation with a unique key covering the semi join's RHS columns
  (`uniqueKeyColumnSets`, `internal/optimizer/joinkeyproof.go`).

The base-table half reaches beyond Q18: any `IN (SELECT pk FROM …)` becomes
an inner join in PG.

Movement: none (recon).
