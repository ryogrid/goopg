# M0146-0015c: a nested EXISTS that reads the outermost query

Status: slices 1–3 landed 2026-09-26. The outer EXISTS plans as a semi
join and the kept inner EXISTS now renders PG's hashed-ANY form —
`Join Filter: (ANY ((a.y = (hashed SubPlan N).col1) AND (b.w = (hashed
SubPlan N).col2)))`, textually identical to the oracle.

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

## Slice 3 (landed): convert_EXISTS_to_ANY for kept sublinks

PG's `convert_EXISTS_to_ANY` (subselect.c:1731) turns an EXISTS whose
correlation clauses are hash-equalities into an ANY over the inner key
columns: the inner side of every `outervar = innervar` pair becomes a
target of the decorrelated body, the outer side becomes the parent's
testexpr, and the subplan runs hashed with `unknownEqFalse`.

`keptExistsToAny` (pulledsublink.go) applies that to the slice-2 kept
form, where correlation already sits in `ExecParamRef` negative
sentinels + `Args`: every top-level `innercol = sentinel` conjunct of
the body qual holder becomes one projected column plus one operand
element, the produced `InExpr` is `IsNonCorrelated` with no param
binding, `Operand` is a `RowExpr` of the correlated columns (scalar
when one pair survives — currently unreachable: a kept EXISTS needs
refs to both scopes, hence ≥ 2 pairs), and `UnknownEqFalse` carries
the licence — the source EXISTS is two-valued, so its ANY is too.
Declines keep the correct slice-2 EXISTS form (fail-open): a
non-equality or composite-inner conjunct, a stray sentinel anywhere at
body level, a non-simple spine. A kept negated EXISTS converts too —
`NOT EXISTS` resolves as `UnaryOp{OpNot, EXISTS}`, so the ANY lands
under the NOT where PG leaves `NOT (SubPlan)`. The flat `existsToAny`
product gets the flag as well — same conversion, same licence.

Executor side: `evalRowHashProbe` (subplan_hash.go) is the tuple-key
analogue of `evalInHashProbe` — the inner plan materialises once into
a `subPlanRowHash` (length-prefixed `datumKey` composite + per-column
datum family, cached in the scoped store under
`nonCorrelatedCacheKey + "\x00rowhash"`); probes demand pairwise
family equality and answer FALSE on any NULL operand element. The
`unknownEqFalse` collapse — NULL → FALSE — is applied once in
`evalInExpr`'s wrapper, so the tuple hash, the scalar value hash and
both linear fallbacks agree. Rows containing a NULL element are
dropped at build (they can contribute neither TRUE nor a
distinguishable NULL — which is also why goopg needs no partial-match
table). `subPlanUsesHashTable` renders the row-operand ANY `hashed`
only under the flag. `foldconst.go` was found REBUILDING `InExpr`
field-by-field — it was silently dropping `ParParam`/`Args` before
this slice ever touched it; it now retains all three fields.

Measured (canonical shape, throwaway cluster :5533): `Hash Semi Join /
Join Filter: (ANY ((a.y = (hashed SubPlan 1).col1) AND (b.w = (hashed
SubPlan 1).col2)))` — textually identical to PG modulo SubPlan
numbering — with correct rows including a NULL operand element
(excluded) and NULL-carrying inner tuples (never match); `NOT EXISTS`
renders `NOT (ANY (...))`, PG's shape. Corpus movement is still none:
TPC-H/TPC-DS carry no two-scope nested EXISTS (sweep, fire set and
acceptance digest all identical to HEAD). New coverage:
`TestKeptExistsToAnyVariants` (convert/decline matrix),
`TestRowHashedAnyTruthTable`/`TestRowHashedAnyProbeFires` (executor).

## Next slices

- **Inner-expr targets:** PG also converts `innervar_expr = outervar`
  (the expression becomes the child targetlist entry); goopg binds the
  conversion to plain `ColumnRef` inner sides — same bound the flat
  `existsToAny` applies — and keeps EXISTS on `(b.w + 1) = a.v` today.
- **Composite escaping refs in a join clause panic, not decline:**
  `b.j2 = a.v + k` (a conjunct whose outer side mixes two scopes)
  reaches `translateToLayout` inside the pulled body's join planning
  with a raw `OuterColumnRef` and PANICS
  (`join clause carries a *optimizer.OuterColumnRef, which is not
  positional and cannot be re-based`, createplanjoin.go:243). The
  kept-subplan admission needs a composite-ref guard, or
  translateToLayout a fail-closed path.
- **The `nested-body-emitting-ref` boundary** (qual-level, not the
  kept-subplan path above): a PULLED nested body whose own link qual
  reads the emitting scope still declines — goopg's search cannot
  place a join clause between an emitting rel and a rel inside the
  nested semi join's RHS (`TestNestedSublinkLevel2Bails` keeps the
  pin). That is the original fix_plan framing of this task and needs
  the SpecialJoinInfo min-hand widening PG does, a separate change.
