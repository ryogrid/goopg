# M0144-0003a — the Project descent is refuted too; leaf admission cannot close this gap

Status: REFUTED 2026-09-20 — the decision recorded one loop earlier is wrong,
and the measurement that shows it is here. `M0144-0003a` is not implementable
as a leaf-admission change; it joins the route-order blocker.
Kind: recon
Parent: M0144-0003
Milestone: M0144
Evidence: `analysis/m0144/m0144-0003a-project-descent-refuted.txt`
Predecessor: `docs/design/0100-0149/m0144-0003a-opaque-leaf-census.md`

## 1. The decision this refutes — my own, from the previous loop

`m0144-0003a-opaque-leaf-census.md` §4 recorded, before implementation and per
AGENT.md C3:

> **Decision:** the fix shape for `M0144-0003a` is *descend a `*Project` that
> satisfies `projectIsPositionalIdentity`*, not *descend a `*Filter`*.

It rested on the census finding that 53 of 59 opaque leaves are `*Project`,
plus the reasonable inference that those Projects are the EXISTS body's
output-list Project and therefore near-identity renames.

That inference was not measured. This loop measured it, before writing any
production code, and it is false.

## 2. Measurement

Throwaway instrumented build (probe reverted, not committed) at the
`leaf-count` decline, printing for each opaque leaf whether
`projectIsPositionalIdentity` admits it and how many leaves its child would
expose. All 11 declining SF0.25 queries, private `:5595` lane, `EXPLAIN` only:

```
Q14  nprefix=3 scans=2 semiAnti=1 want=4 would=2  [P-NONident *Gather w=73 cw=29 nt=73][P-NONident *CTEScan]
Q16  nprefix=4 scans=3 semiAnti=2 want=6 would=3  [P-NONident *Gather w=106 cw=106 nt=106][*SeqScan][*SeqScan]
Q23  nprefix=3 scans=3 semiAnti=2 want=5 would=3  [P-NONident *Gather][P-NONident *CTEScan][P-NONident *CTEScan]
Q33  nprefix=4 scans=2 semiAnti=1 want=5 would=2  [P-NONident *Gather w=97 cw=67][P-NONident *Filter]
Q35  nprefix=3 scans=2 semiAnti=1 want=4 would=2  [*NestedLoopIndexJoin][*Gather]
Q56  nprefix=4 scans=2 semiAnti=1 want=5 would=2  [P-NONident *Gather][P-NONident *Filter]
Q58  nprefix=3 scans=2 semiAnti=1 want=4 would=2  [P-NONident *Join][P-NONident *Filter]
Q60  nprefix=4 scans=2 semiAnti=1 want=5 would=2  [P-NONident *Gather][P-NONident *Filter]
Q83  nprefix=3 scans=2 semiAnti=1 want=4 would=2  [P-NONident *Join / *NestedLoopIndexJoin][P-NONident *Filter]
Q94  nprefix=4 scans=3 semiAnti=2 want=6 would=3  [P-NONident *Gather w=101 cw=101][*SeqScan][*SeqScan]
Q95  nprefix=4 scans=3 semiAnti=2 want=6 would=3  [P-NONident *Gather][P-NONident *CTEScan][…]
```

Two facts, both fatal to the decision:

**(a) Not one Project is a positional identity.** Every `*Project` in the
corpus prints `P-NONident`. Some are not even width-preserving — Q14's is 73
targets over a 29-column child, Q33's 97 over 67. These are wide
re-projections, not the identity renames the decision assumed.

**(b) `would == scans` in every single case.** Descending every
positional-identity Project would expose **zero** additional leaves, so the
gap (want 4-6, have 2-3) does not narrow by one.

## 3. Why, and what it joins

Read the Project children: `*Gather`, `*CTEScan`, `*Filter`, `*Join`,
`*NestedLoopIndexJoin`. These are **already-planned composites** — the same
thing `M0142-0008a-3i-leafcount` found when it looked at the other decline
class, and the same thing `M0142-0008a-3i-lateral` found for the dependency.

A `*Gather` under a 106-target Project is not an EXISTS body's tidy
output-list wrapper; it is a finished plan fragment with a chosen worker count.
No leaf-level admission predicate — Filter, Project-identity, or otherwise —
can turn that back into base relations.

So **M0144-0003a is not implementable as a leaf-admission change.** It joins:

| task | blocked by |
|---|---|
| M0142-0008a-3 increment (i) | relations lowered before Phase B searches |
| M0142-0008a-3 increment (ii) | downstream of (i) |
| M0142-0008a-3i-lateral (Q69/Q10) | dependency lowered before Phase B searches |
| **M0144-0003a (this)** | **relations lowered before Phase B searches** |

All four now have one blocker: **`M0142-0008a-3i-lateral-route`** — Phase B
must search before Phase A lowers anything. That task already overlaps
`M0144-0003`, which is the owner-prioritised route-order item, and this
finding is a fourth independent confirmation of `M0144-0003`'s own thesis:
*"several landed fixes never moved a plan because goopg's processing route
diverges from PG's upstream of the fix."*

PG's arrangement, for the record: `pull_up_sublinks` converts sublinks into
jointree members before `query_planner`
(`postgres/src/backend/optimizer/prep/prepjointree.c:468`, called from
`subquery_planner` at `planner.c:737`), and `create_plan` runs only after the
best path is chosen (`planner.c:441`). Nothing is lowered while a search that
might reorder it is still to come.

## 4. What I got wrong, and the rule it suggests

The previous loop's census answered *which node kind* the opaque leaf is and
then inferred *what that node is like*. The inference — "an output-list
Project is a near-identity rename" — was plausible, was written down as a
decision, and was wrong.

The census cost one loop; testing the inference cost part of another. Both
were cheaper than implementing the arm and discovering it exposed zero leaves
after a full gate battery. The rule worth carrying: **a census of node KINDS
does not license an assumption about those nodes' SHAPE — probe the predicate
you intend to gate on, not just the type switch.**

## 5. Reporting

`Movement: none` — a refutation and a redirect; no production code changed
(the probe was reverted before commit) and no parity arm was run against a
change.
