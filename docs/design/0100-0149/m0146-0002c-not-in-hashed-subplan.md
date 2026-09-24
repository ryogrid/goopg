# M0146-0002c: plan `NOT IN` as a SubPlan, as PG does

Status: landed 2026-09-24 `c2339a072`; Movement: none; follow-up M0146-0002d.
Task: `.ralph/fix_plan.md` M0146-0002c (Kind: impl, Parent: M0146-0002b).
Recon: `m0146-0002b-not-in-anti-join-census.md`.

## Change

`pull_up_sublinks_qual_recurse` converts ANY_SUBLINK and EXISTS_SUBLINK only
(`./postgres/src/backend/optimizer/prep/prepjointree.c:665/731`); under a NOT
it converts only `NOT EXISTS`. goopg's legacy IN unnest now declines:
- a negated IN (`canUnnestInExprDepth`);
- the effective negation of `NOT (x IN (...))` in both `unnestInExpr` paths.

NOT IN therefore stays a sublink and plans as a SubPlan filter. The null-aware
anti-join arm is unreachable from the planner. It stays in the tree until the
legacy-deletion slice, and the hand-built spill shapes still pin the executor
mechanism.

## Verification

- Values: unchanged by construction; the recon's NULL probe had already found
  the old anti join value-correct. TPC-H Q16 is 18215 rows, identical to the
  baseline, in 0.31 s.
- Regress, against a HEAD baseline:
  - identical for the NOT IN files type_sanity, expressions, opr_sanity,
    misc_sanity and collate;
  - rowsecurity's 12-line delta is run-to-run noise (leftover views, Go
    pointer text in a policy expression), and a second candidate run differs
    from the first;
  - subselect could not complete: a nested EXISTS/NOT EXISTS query over
    `tenk1` runs for more than an hour at HEAD too (pre-existing; M0146-0015).
- Gates: units, tpch-spotcheck, sf025 (PLAN-SHAPE same=99, as the census
  predicted), acceptance arm, fireset.

## What Q16 shows now (`analysis/m0146/m0146-0002c/q16-goopg-plan.txt`)

The NOT IN is a SubPlan filter, PG's route, but two differences remain, so
Q16's categories did not move:
- **Placement:** the filter sits on the `Hash Join`, where PG pushes it to
  the `partsupp` scan (it references only `partsupp`). The join therefore
  still sits above a post-pass Gather and is not a Parallel Hash.
- **Label:** goopg renders `NOT (ps_suppkey = ANY (SubPlan 1))`, PG
  `NOT (ANY (ps_suppkey = (hashed SubPlan 1).col1))`.

Both are filed as M0146-0002d.
