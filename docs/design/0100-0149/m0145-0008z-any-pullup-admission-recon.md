# M0145-0008z recon: why the pull-up declines ANY sublinks PG pulls up

Status: **RECON COMPLETE 2026-09-25**. No production code changed. Task:
`.ralph/fix_plan.md` M0145-0008z (Kind: recon, Parent: M0145-0008n).
Evidence: `analysis/m0145/m0145-0008z/decline-reasons.txt`. Filed:
**M0145-0008aa**.

## Measured

`GOOPG_NLI_CENSUS=1` prints a `PULLUPCENSUS decline=` reason for every
sublink the jointree pull-up refuses:

| query | sublinks | decline reason |
|---|---|---|
| TPC-H Q18 | 1 (`IN (SELECT l_orderkey … GROUP BY … HAVING …)`) | `any-body-not-simple` |
| TPC-DS Q14 | 5 (`IN (SELECT … FROM cross_items)`) | `any-body-leaf-(*optimizer.CTEScan)` |
| TPC-DS Q23 | 8 | `any-body-leaf-(*optimizer.CTEScan)` |

All of them take the post-hoc route and become Left Semi joins.

## PG

`convert_ANY_sublink_to_join` (`postgres/src/backend/optimizer/plan/subselect.c`)
turns any uncorrelated sub-select into a subquery RTE on the semi side of a
new join. The only gates are available rels, whether the testexpr
references the parent, and volatility. `pull_up_subqueries` flattens that
RTE later, and only if it is simple; otherwise it stays a subquery scan.
The planner can then unique-ify the semi side and join it as an inner join
(`create_unique_path`):
- Q18 becomes an inner Parallel Hash Join over the grouped `lineitem`
  subquery;
- PG's Q14 and Q23 plans carry no semi joins at all.

## goopg

`pullUpAnyBody` (`internal/optimizer/jointreepullup.go`) flattens the body
into semi/anti leaf entries directly, so it demands a simple body
(`sublinkBodyIsSimple`) of base-scan leaves. That gate is goopg's own, not
PG's. The unique-ify path already exists (`createuniquepath.go`), so the
missing piece is admission: a non-simple body should enter the search as one
derived semi-side leaf, carrying the body's own plan. That is the leaf kind
M0145-0013 introduced for pulled CTE scans. Filed as **M0145-0008aa**.

Movement: none (recon).
