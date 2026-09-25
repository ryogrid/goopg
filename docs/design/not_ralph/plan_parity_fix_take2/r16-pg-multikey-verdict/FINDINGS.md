# R16 — PG hash-joins the multi-key shape, so the flip loses a join PG keeps

*Round 16 of `../TODO.md`. Findings round. Performs the measurement R15
said was owed, and it reverses that item's disposition.*

## 1. The measurement

R15 narrowed `TestSplitEqualityForHashMultiKey` from "possible shape
regression" to "cost adjudication pending", and specified what would
settle it: PG's answer for the same shape, obtained by **running the
SQL** rather than reading a capture.

The fixture turned out not to be abstract — it is TPC-H-shaped, on
`partsupp` and `lineitem` — so it runs directly on the PG reference
cluster at real cardinalities, GUCs pinned as for every other arm:

```
Gather  (Workers Planned: 3)
  -> Hash Join
       Hash Cond: ((ps.ps_partkey = lineitem.l_partkey)
                   AND (ps.ps_suppkey = lineitem.l_suppkey))
       Join Filter: (ps.ps_availqty > (sum(lineitem.l_quantity)))
       -> Parallel Seq Scan on partsupp ps
       -> Hash
            -> HashAggregate
                 -> Seq Scan on lineitem
```

**PG hash-joins on BOTH equalities** and carries `ps_availqty > sum` as
a `Join Filter`. It does not consider a nested loop worth choosing here.

## 2. Verdict: this is a real divergence, not a re-baseline

Under the flip goopg *"did not hash-join on any of the AND'd equalities
(fell back to Nested Loop)"*. PG hash-joins both. The test's assertion
therefore **agrees with PG**, and its failure under the flip is a
parity regression — the opposite disposition from R14's Slice3 finding,
where the moved shape was closer to PG.

So the flip is not unambiguously good. It buys the parallel-join
mechanism PG uses (R14: Q9's `Parallel Hash Join`, absent without it)
and it costs a hash join PG keeps. Both facts are now measured rather
than assumed, and R10 `DESIGN.md` §7's acceptance rule — which demanded
that a category regression be *explained* before acceptance, not
outweighed — applies to this one directly.

## 3. Caveat, stated rather than buried

The test's own fixture supplies synthetic row counts; this measurement
used the real SF=1 cluster's. The shape question it answers — *does PG
hash-join a two-column equi-join carrying a residual filter?* — is
answered decisively and is cardinality-robust at this scale. Whether
PG would still hash-join at the fixture's specific smaller counts is
**not** established here, and if the next round needs that, it must
build the fixture's cardinalities in PG rather than infer from this.

That distinction matters because R15's whole point was that this test
sits near a cost threshold. Answering the shape question does not
automatically answer the threshold question.

## 4. What this changes

| item | R15 state | R16 state |
|---|---|---|
| `TestSplitEqualityForHashMultiKey` | cost adjudication pending | **confirmed divergence from PG under the flip** |

R10's set: 7 items — 1 adjudicated as a justified re-baseline (R14),
**1 confirmed as a real regression (here)**, 1 solved pending the flip,
1 needing a post-pass decision (K20), 1 mechanical stage pin, 2 still
outstanding.

## 5. Consequence for the flip

The flip can no longer be landed on the strength of the mechanism
argument alone. Either the nested-loop choice is fixed so the flip is
strictly a move toward PG, or the round lands with a **named, measured**
regression and says so — which R10's acceptance rule permits only for
regressions that are explained, and this one is not yet explained
(the cause is a cost movement whose direction is known and whose
magnitude is not).

## 6. Filed

- **R17 — find why the enumerator prices the nested loop below the hash
  join under the flip.** Per
  `planner_verify_both_candidates_generated`: instrument `addPath` to
  confirm the hash candidate is generated at all before theorising
  about cost terms. Then adjudicate items 6 and 7 and decide the flip.
