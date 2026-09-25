# M0145-0008n: which still-live M0145-0001 §6 routes produce first divergences

Status: **RECON COMPLETE 2026-09-25**. No production code changed. Task:
`.ralph/fix_plan.md` M0145-0008n (Kind: recon, Parent: M0145-0008). Inputs:
the §6 audit table in `m0145-0008-del4-pgshaped-knob-and-retirement-audit.md`
and the M0146-0001 census (`analysis/m0146/m0146-0001/`). Evidence:
`analysis/m0145/m0145-0008n/`.

## Method

The live routes already have construction-site counters. With
`GOOPG_NLI_CENSUS=1`, the server prints:
- `NLICENSUS route=search|rewrite`: whether a NestedLoopIndexJoin was
  elected by the join search or built by the post-hoc `rewriteJoinsToNLI`;
- `SUBLINKCENSUS route=jointree-pullup|jointree-posthoc`: whether a sublink
  went through the pull-up or the post-hoc unnest.

Per-query attribution:
- **TPC-H:** one `tpch-estimate-audit-arm.sh --queries N` run per query
  (`PLAN_ONLY=1`).
- **TPC-DS SF0.25:** one private clone and server; each query's EXPLAIN is
  attributed by the census lines it appends to the server log
  (`tpcds-perq.sh`). The per-query pull-up total, 23, matches the sweep's
  corpus-level flow-convergence count exactly.

Each post-hoc query was then crossed with two things: its M0146-0001 first
divergence, and whether PG's plan keeps the sublink as a SubPlan.

## Findings

| class | queries | verdict |
|---|---|---|
| post-hoc unnest decorrelates a correlated scalar subquery that PG keeps as a SubPlan | TPC-H Q2; TPC-DS Q1, Q6, Q32, Q92 | **produces the first divergence.** PG's plans show 2 SubPlans and goopg's none, and the first divergence sits at the resulting join (parameterisation, sort-strategy, parallelism, join-method). Filed **M0145-0008y** (impl). |
| post-hoc semi join where PG's pull-up takes the sublink | TPC-H Q18 (grouped `IN`: PG unique-ifies it into an inner Parallel Hash Join); TPC-DS Q14, Q23 (goopg has more semi/anti joins than PG) | **produces the first divergence** on Q18, likely on Q14/Q23. Filed **M0145-0008z** (recon). |
| post-hoc route, same SubPlan shape as PG | Q16, Q17, Q20, Q30, Q41 (match), Q45, Q54, Q58, Q81, Q95 | refactor-only; waits |
| `rewriteJoinsToNLI` | one node in all three corpora (TPC-H Q20, the nation probe) | refactor-only |
| rowmark passes | no corpus query uses FOR UPDATE / FOR SHARE | refactor-only |
| seam-walk decline families | 2 declines (`leaf-count`) on SF0.25 | covered by M0146-0008's leaf-count re-census |
| `planFromClause`, rule passes (pushdown, scan-input rewrites) | on every query | not attributable per query by counters; each is judged by the task whose divergence it touches |

## Consequence for retirement

Retiring a §6 row is reimplementation, and only two row classes currently
cost parity: the scalar-subquery decorrelation and the pull-up admission
gap. The other live rows keep producing PG's shape on the corpora and wait
until a refactor needs them gone.

Movement: none (recon).
