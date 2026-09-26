# M0146-0015c: a nested EXISTS that reads the outermost query

Status: slice 1 (PG's scope gate) and slice 2 (kept-subplan re-base)
landed 2026-09-26. The outer EXISTS now plans as a semi join with the
inner EXISTS carried as a pre-lowered SubPlan in the join filter.
Slice 3 (`convert_EXISTS_to_ANY`, PG's hashed-ANY rendering) is open.

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

## Slice 2 (landed): the kept-subplan re-base

`internal/optimizer/pulledsublink.go` carries a correlated SubPlan
inside a pulled-up qual, in two steps.

At qual-rebase time `rebasePulledQual` runs a post-pass
(`rebaseQualKeptSubplans`) over its output: every lowerable sublink in
the qual gets its plan CLONED — the original stays attached to the
untouched conjunct the declined-search fallback runs — and every
escaping OuterColumnRef inside the clone is rewritten to an
`ExecParamRef` with a NEGATIVE sentinel id (`-(i+1)`). Each escape
becomes an `Args[i]` expression in problem space: `Args` are same-scope
children of the sublink expr, so `relidsOfExpr` counts them toward the
clause's relset (join-search attribution) and `translateToLayout`
re-bases them to whichever node layout the qual lands on, exactly like
an ordinary join qual. `lowerSubPlanParams` then renumbers each
sentinel block into the flat per-statement ParamExec space and skips
pre-lowered sublinks so ordinary lowering cannot clobber the binding.

Scope arithmetic (`keptRebase.escapeArg`): inside a kept plan,
`hops = Level - linkDepth - 1` counts scopes above the pulled body —
0 is the body itself (`bodyLeafOf`/`allSpans` + `srcOffset`), hops
through the ancestor chain land on ancestor bodies, exactly `walked`
hops past the topmost body is the emitting scope, anything deeper
declines (`kept-deep-ref`). Refs at `Level <= linkDepth` read frames
an unlowered nested eval still pushes and are untouched. Two bugs the
first attempt had, pinned now: `Visit` returning false to
`walkExprRefs` PRUNES without aborting (failure travels on a flag, as
in `rebasePulledQual`), and the emitting-scope hop count was off by
one.

`bodyQualsAdmitSublinkList` is now per-sublink
(`keptSubplanAdmissible`): cloneable plan, no LATERAL inside, no
volatile work inside, every escaping ref reachable. Excluded eval-site
kinds (row-ctor IN, ARRAY, multi-assign) still refuse when correlated;
a correlated non-lowerable subplan still refuses
(`subplan-correlated`).

Measured, on the canonical shape (`a … EXISTS (b … EXISTS (c WHERE
c.x = a.y AND c.z = b.w))`, throwaway cluster, hand-checked data):
`Hash Semi Join / Join Filter: (EXISTS(SubPlan 1))` with
`Filter: ((x = a.y) AND (z = b.w))` inside the SubPlan — PG's shape,
correct rows; `NOT EXISTS` gives the Hash Anti Join analogue; the
correlated `b.w IN (…)` variant returns correct rows with the ANY kept
the same way.

## Next slices

- **Slice 3:** `convert_EXISTS_to_ANY`, a hashed ANY for an EXISTS
  whose correlation is equality pairs. Until it lands goopg renders
  `Join Filter: (EXISTS(SubPlan N))` where PG prints the hashed-ANY
  form — semantically equivalent, textually different.
- **The `nested-body-emitting-ref` boundary** (qual-level, not the
  kept-subplan path above): a PULLED nested body whose own link qual
  reads the emitting scope still declines — goopg's search cannot
  place a join clause between an emitting rel and a rel inside the
  nested semi join's RHS (`TestNestedSublinkLevel2Bails` keeps the
  pin). That is the original fix_plan framing of this task and needs
  the SpecialJoinInfo min-hand widening PG does, a separate change.
