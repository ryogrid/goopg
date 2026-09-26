# M0146-0015c: a nested EXISTS that reads the outermost query

Status: slice 1 (PG's scope gate) landed 2026-09-26, plan-neutral; the
pull-up of the outer EXISTS is open.

## PG behaviour

Query shape: `a … EXISTS (b … EXISTS (c WHERE c.x = a.y AND c.z = b.w))`.

`pull_up_sublinks_qual_recurse` (prepjointree.c) converts the outer
EXISTS into a semi join a ⋈ b first. It then recurses into the pulled-up
quals, offering a nested sublink two targets: the rels above the parent
body (`available_rels1`) or the parent body's own rels (`child_rels`).
`convert_EXISTS_sublink_to_join` (subselect.c) requires the sublink's
upper vars to be a subset of one of them. The inner EXISTS reads `a` and
`b`, fits neither, and stays a SubPlan. make_subplan's
`convert_EXISTS_to_ANY` then turns it into a hashed ANY over the
correlation columns. The result is a Hash Semi Join with
`Join Filter: (ANY ((a.y = (hashed SubPlan 2).col1) AND (b.w = (hashed
SubPlan 2).col2)))` (`analysis/m0146/m0146-0015c/pg-oracle.txt`).

## goopg

Both sublinks stay nested SubPlans. The answers are correct, but the
outer EXISTS is never a semi join. Two things stood in the way:

1. goopg tried to pull the inner EXISTS too. Its reference to `a`
   (grandparent) failed the rebase (`nested-body-emitting-ref`), and the
   whole pull-up fell back.
2. Keeping the inner sublink as a qual of the pulled outer body needs its
   correlation re-based. At pull-up time the inner plan still holds raw
   OuterColumnRefs (PARAM_EXEC lowering runs later). A reference to `b`
   must move from body-local to problem-space coordinates, and one to `a`
   must drop a level. `bodyQualsAdmitSublinkList` declines such a qual
   (`nested-sublink-convertible` / `-correlated`) because no re-base
   exists: the old `remapOuterRefsInSubplan` is gone, and
   `clonePlanReplacingOuter` only decorrelates.

## Slice 1 (landed): PG's scope gate

`nestedBodySpansScopes` refuses a nested body (depth > 0) whose quals read
Level 1 and Level >= 2 together, in both the EXISTS and the ANY pull-up
arms (`nested-spans-scopes`). That is the `available_rels` test above. The
nested sublink is then left in the parent body's quals, as in PG. Blocker
2 still declines the outer pull-up, so no plan changes: the sweep, fire
set and regress output are identical to HEAD.

## Next slices

- **Slice 2:** carry a correlated SubPlan in a pulled-up qual. Clone the
  inner plan (the original is shared with the declined-search fallback)
  and re-base its OuterColumnRefs by depth:
  - a reference to the parent body keeps its level and maps to
    problem-space coordinates (`bodyLeafOf` / `pullSpans`, source
    identity + `srcOffset`);
  - a reference to the emitting scope drops one level;
  - deeper references drop one level.
  Every plan node and expression kind the clone meets must be enumerated
  (fail closed). Then relax `bodyQualsAdmitSublinkList` for re-based
  sublinks.
- **Slice 3:** `convert_EXISTS_to_ANY`, a hashed ANY for an EXISTS whose
  correlation is equality pairs.
