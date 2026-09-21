# Nested sublinks in a pulled body's quals (M0145-0014)

Status: PARTIAL. The scalar half landed 2026-09-21. The ANY-recursion half was
ATTEMPTED on 2026-09-21 (loop 45) and **stopped before landing** — the attempt
found a third blocker that is neither of the two this document previously
named, and it is recorded below with its witness.

Task: `.ralph/fix_plan.md` M0145-0014. Kind: impl. Parent: M0145-0003.

## The premise needed splitting first

The task reads the `any-nested-sublink` decline as one population needing one
fix — port PG's recursion. Attributing the census says otherwise. On TPC-DS
SF0.25 the class fires on exactly two queries, and they are different shapes:

```
query58   d_date IN (SELECT d_date … WHERE d_week_seq =  (SELECT …))   scalar nested
query83   d_date IN (SELECT d_date … WHERE d_week_seq IN (SELECT …))   ANY nested
```

Only Q83 needs the recursion. Q58's nested sublink is an EXPR sublink, which
`pull_up_sublinks_qual_recurse` does not convert in PG either — it stays a
SubPlan. Yet goopg declined Q58's OUTER conjunct as well, because the gate was
a blanket `exprHasSublinkPlan(where) || exprListHasSublinkPlan(onQuals)`.

That is over-broad against the oracle. PG converts the outer sublink FIRST —
`convert_ANY_sublink_to_join`'s gates are about correlation and volatility
(`postgres/src/backend/optimizer/plan/subselect.c:1345-1386`) and say nothing
about the body's quals — and only THEN recurses on the pulled-up quals
(`prepjointree.c:682-693`, `:736-747`, NOT arm `:836-845`). A nested sublink PG
would not convert never blocks the outer conversion.

## What landed

`bodyQualsAdmitSublinks` replaces the blanket refusal at **both** pull-up arms
(the EXISTS arm and the ANY arm held the identical gate — a sibling pair):

- a NON-convertible sublink rides along as an ordinary body-local qual;
- a convertible nested ANY/EXISTS still declines, now as
  `nested-sublink-convertible`, because converting it needs the nested body's
  leaves spliced and its link predicate rebased against the OUTER body's leaves
  rather than against the emitting rels;
- a qual carrying both a sublink and a Level-1 outer reference declines as
  `nested-sublink-correlated`.

Admitting the qual was not sufficient on its own. `rebasePulledQual` cloned
with `scopeVeto`, which makes `cloneExprRefs` **abort** at the first inner-plan
slot — so every admitted qual then failed one step later with
`PULLUPCLASSIFY refusal=rebase-failed`, a decline that had merely moved. It now
clones with `scopeSignal`: the crossing is reported and not descended, which is
correct because the subplan's own refs live in the subplan's scope and are not
the body-local coordinates this rebase re-stamps. `OnScope` declines a subplan
that is CORRELATED (`planHasOuterRef`), whose outer refs would point at
coordinates moving underneath it.

Finding that second wall cost a census round, so `noteRebaseFail` now names
which of `rebasePulledQual`'s four failures fired instead of reporting one
string for all of them.

## Measurement

Pull-up census, TPC-DS SF0.25, knob arm, all 99 queries:

```
                                    before   after
any-nested-sublink                      12       0
any-nested-sublink-convertible           0       6    <- Q83, deferred
(pulled)                                42      48    <- Q58, +6
every other class                  unchanged
REBASEFAIL                               -    none
```

**Correctness.** Exactly Q58 moves across all 99 plans (knob arm, against a
pre-change binary built from a worktree at HEAD), and its cost falls
20670 → 13692 with the `Hash Semi Join` retained. Q58 itself returns 0 rows at
SF0.25 in both arms, which is a weak witness, so the mechanism was probed
directly against the **PG oracle**:

```sql
select count(*), sum(ss_quantity) from store_sales
where ss_sold_date_sk in (select d_date_sk from date_dim
                          where d_week_seq = (select d_week_seq from date_dim
                                              where d_date = '1999-09-16'));

goopg before 3654|181827    goopg after 3654|181827    PG 18.3 3654|181827
```

and the plan it now produces is PG's shape — a `Hash Semi Join` whose body leaf
carries the nested scalar as an `InitPlan` filter:

```
Hash Semi Join
  Hash Cond: (store_sales.ss_sold_date_sk = d_date_sk)
  ->  Parallel Seq Scan on store_sales
  ->  Seq Scan on date_dim
        Filter: (d_week_seq = (InitPlan 1).col1)
        InitPlan 1
          ->  Seq Scan on date_dim_1
                Filter: (d_date = '1999-09-16')
```


## The recursion attempt, and the blocker it found (2026-09-21, loop 45)

The deferral below said the missing piece was a coordinate path for a link
predicate spanning two pulled bodies. That was built, and it was not enough.

**What was built and works.** `jtPulledBody` gained `children`/`parent`;
`extractNestedPullups` re-ran the classifier on a body's own conjuncts and
removed each converted one from the parent's qual list (PG's
`prepjointree.c:682-693` / `:736-747` / `:836-845`), depth-guarded;
`flattenPulledBodies` linearised the tree parent-before-child so every
downstream consumer's "body order IS leaf order" assumption held;
`rebasePulledQual` walked `r.Level` steps up the `parent` chain, resolving a
reference that lands on an ancestor body through that body's leaves and falling
through to the emitting scope when the chain runs out (which is what Level 1
always meant for a top-level body); and `classifyPulledQuals` generalised the
hard-coded `emittingBits` left-hand side into a per-body `leftBits` =
emitting ∪ every ancestor's leaves. All of it compiles and the coordinate path
resolves.

**The blocker.** `TestJointreePullupDeclineParity/nested-exists` — an EXISTING
test, not one written for the attempt — fails with:

```
panic: createPlan: join clause references binding column 3 (v),
       which is not among the 3 output columns it is being re-based onto
```

A pulled leaf is **non-emitting**: a SEMI/ANTI join never projects its RHS, so
the parent body's columns exist only at the parent's own join node. Today's
spanning link qual is fine because it BECOMES that join's clause. A nested
body's link qual is different — it reads a parent-body column from a *different*
join node, and goopg's lowering has nowhere to evaluate it.

Restricting the nested SJI's `syn_lefthand` to the ancestor leaves alone
(`leftBits &^ emittingBits`), so the child semijoin is ordered INSIDE the
parent's right-hand side exactly as PG splices it into `j->rarg` with
`available_rels = child_rels`, is necessary but **did not** fix it. The
remaining gap is in the lowering, not in the join ordering.

The attempt was discarded rather than landed: a half-working nested pull-up is
the wrong-answer class this milestone guards hardest against, and the corpus
value gates cannot see a join-ordering fault that still returns the right rows
on the two queries that exercise it.

**Sharpened resume point.** Before rebuilding the coordinate path, answer the
lowering question first: where can a clause that references a non-emitting
pulled leaf's column be evaluated? Either the pulled leaves must project their
columns into the parent's schema for the duration of the search, or the nested
semijoin must be lowered as a subtree of the parent's RHS with its clause
attached there. `TestJointreePullupDeclineParity/nested-exists` is the witness
to re-run — it fails within seconds and needs no cluster.

## Deferred — the recursion itself

Q83's 6 fires stay declined. Implementing them means, for a nested convertible
sublink: binding the nested body in the OUTER body's scope, appending its
leaves after the outer body's in the same `jtPullup`, and synthesising a link
predicate whose left side refers to the outer body's leaves — not to the
emitting rels, which is the only case `outerOperandAsLevel1` and
`rebasePulledQual` currently handle (`*OuterColumnRef{Level: 1}` →
emitting binding). That re-base across two pulled bodies is the machinery this
task did not build, and it is why the split was worth making explicit: shipping
it as "the recursion" would have implied the scalar half needed it too.
