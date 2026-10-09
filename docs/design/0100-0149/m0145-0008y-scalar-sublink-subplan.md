# M0145-0008y — keep a correlated scalar sublink as a SubPlan

Status: done 2026-10-05. Parent: M0145-0008n. The task was held `[!]` on
M0146-0012 from 2026-09-25 to 2026-10-05.
Evidence: `analysis/m0145/m0145-0008y/` (2026-09-25 prototype fire set and
the 2026-10-05 re-measure).

## PG mechanism

- `pull_up_sublinks` (prepjointree.c) converts only ANY and EXISTS sublinks
  into joins. An EXPR_SUBLINK, a scalar subquery, always stays a SubPlan
  (`make_subplan`, subselect.c).
- Its correlation becomes a PARAM_EXEC Param. Inside the body that is a
  base restriction the planner can use as an index key; across relations
  the sublink's clause is a join clause.

## goopg before

- The post-planning unnest pass (`canUnnestSubquery` →
  `unnestScalarSubquery`) rewrote a correlated scalar whose body was not
  probe-cheap into a grouped hash join.
- That was goopg's substitute for parameterised probes. The 2026-09-25
  prototype of this decline measured TPC-H Q2 at 1.50 s → 307 s, so the
  task was held on M0146-0012.

## Change

- `canUnnestSubquery` refuses every scalar sublink unless
  `GOOPG_SCALAR_UNNEST=on` (test API `SetScalarUnnestEnabled`). The switch
  defaults off and is in the flag provenance table.
- The machinery stays for one cycle as a rollback path. The 15 tests that
  pin its coordinates and guards enable the switch
  (`enableScalarUnnestForTest`) and keep exercising it.

## Result (2026-10-05)

- **TPC-H:** all 22 plans are identical to HEAD; Q2/Q17/Q20 already keep
  PG's SubPlans since M0146-0012 and 0012a. Q2 runs in 0.20 s, where the
  prototype measured 307 s. 24/24 MATCH.
- **TPC-DS SF0.25:** only Q6 changes. It keeps PG's SubPlan; 402 → 485 ms,
  checksum equal.
- **Fire set:**

  | category | SF0.25 before → after | SF1 before → after |
  |---|---|---|
  | MATCH | 42 → 42 | 33 → 34 |
  | join-order | 49 → 49 | 53 → 52 |
  | join-method | 25 → 24 | 22 → 21 |
  | parameterisation | 25 → 25 | 33 → 32 |
  | aggregation-strategy | 9 → 8 | 17 → 15 |

  No category regresses.
- **Regress `join`:** `t1.unique1 = (select min(...) where t2.unique1 =
  unique1)` keeps a SubPlan, as in PG; it was a HashAggregate join.

## Open (ledgered)

- **Q6's SubPlan placement:** it is a Filter above the Gather. PG files it
  as a base restriction of `item` (its Args are `item`'s Vars) on the
  parameterised index scan. Slice A of M0146-0012a pre-lowers only
  multi-relation clauses, and M0146-0005bu places single-relation ones only
  on binding 0.
- **Regress `join`'s SubPlan body:** goopg seq-scans `tenk1` where PG runs
  an InitPlan'd `Limit → Index Only Scan` (the min/max optimisation inside
  a correlated body).
- **Rollback machinery:** delete it, with the tests that only pin it, once
  a cycle shows no need for `GOOPG_SCALAR_UNNEST=on`.
