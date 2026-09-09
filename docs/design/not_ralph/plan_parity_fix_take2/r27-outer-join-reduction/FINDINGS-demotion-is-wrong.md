# R27/2 — the demotion verdict itself is wrong, and the seam decline is masking it

*2026-09-09. Implemented §4a, and it exposed something more important
than the ordering bug it was meant to fix. No code shipped.*

## 1. §4a works structurally, and still fails on VALUES

The revised design (analysis on a throwaway copy, transplant only
join-TYPE verdicts, leave `reduceOuterJoins` and the S9.4 flip alone)
does what it claims: the **optimizer suite passes**, so the flip
contract five tests pin is preserved.

The **executor values tests still fail** — the same five. So the
problem is not the flip, and not the ordering. It is the verdict.

## 2. The verdict is demonstrably wrong

Instrumented on `TestRightJoinSpineKeepsNullExtendedRows`:

```
R27DEMOTE join[1] Right(2) -> Inner(0) (flipped=false)
```

for

```sql
FROM rj_a JOIN rj_b ON rj_a.id = rj_b.aid
RIGHT JOIN rj_c ON rj_b.cid = rj_c.id
WHERE rj_a.id IS NULL
```

A RIGHT JOIN's nullable side is its LEFT arm (`rj_a`, `rj_b`), and
`rj_a.id IS NULL` selects exactly the null-extended rows. Demoting to
INNER destroys the only rows the query returns — 0 rows where 1 is
correct.

## 3. Cause: ON-strictness propagates across an outer-join boundary

`applyDemotion`'s RIGHT arm:

```go
case parser.JoinRight:
    if anyNameIn(leftNames, accumulatedNN) { j.Type = parser.JoinInner }
```

`accumulatedNN` starts from the WHERE (correctly empty here — the
`IsNullExpr` arm only credits `IS NOT NULL`) and then **accumulates
ON-clause findings from INNER joins as the chain is walked**. The inner
`rj_a JOIN rj_b ON rj_a.id = rj_b.aid` is strict on both, so `{rj_a,
rj_b}` enters the set — and the RIGHT join above then reads its own
nullable arm as non-nullable.

It is not. The RIGHT join null-extends that arm *after* the inner join
produced it, so strictness established below does not survive. PG's
`reduce_outer_joins_pass2` only lets quals from ABOVE a join constrain
it, which is why upstream does not make this mistake.

## 4. The decline is LOAD-BEARING — and that reframes R27

This wrong verdict does not corrupt anything today, for one reason:
it reaches `root->join_info_list` but never the plan, and the resulting
disagreement is what makes `outerLinksHaveSJInfos` DECLINE the
statement. The fail-closed guard is masking an incorrect demotion.

So R27's premise needs restating. It was: *"the guard declines because
of an ordering bug; fix the ordering and 7 queries become eligible."*
It is actually:

> **The guard declines because the demotion is wrong. Removing the
> decline without fixing the demotion ships wrong rows.**

That inverts the work order. `applyDemotion`'s strictness propagation
must be corrected FIRST — restricted to quals from above, as PG does —
and only then can the verdicts drive a plan and the declines be
retired.

## 5. Why this was not visible before

K28 said the demotion "has never driven a plan, so its correctness as a
plan rewrite is unestablished". That was right but understated: it is
not merely unestablished, it is **wrong**, and a fail-closed guard
elsewhere is the only thing preventing the consequences. Nothing in the
tree recorded that, because nothing had ever run the two together.

Three attempts were needed to get here, each falsified by running it:
move the call (breaks values); suppress the flip (breaks five pinned
tests); transplant verdicts only (optimizer green, values still wrong —
which finally isolated the verdict itself).

## 6. Next

1. Correct `applyDemotion`'s accumulation so ON-clause strictness does
   not cross an outer-join boundary (PG: `reduce_outer_joins_pass2`).
   Gate: the five executor values tests, unchanged.
2. THEN apply §4a's transplant, and re-audit `seam-decline`.
3. Only then expect Q49's `Hash Left Join`s to become inner joins.
