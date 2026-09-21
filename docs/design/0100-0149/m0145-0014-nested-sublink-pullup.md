# Nested sublinks in a pulled body's quals (M0145-0014)

Status: PARTIAL, landed 2026-09-21. Half the census population is now pulled up
and the other half is named and deferred. The task's premise needed splitting
before any of it could be implemented.

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
