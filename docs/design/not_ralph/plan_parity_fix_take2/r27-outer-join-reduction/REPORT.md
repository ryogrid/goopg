# R27 results — a row-dropping outer-join demotion, fixed and oracle-verified

*2026-09-09. The round set out to fix an ordering bug; it ended by
fixing a correctness bug the ordering bug was hiding.*

## 1. What shipped

`applyDemotion` judged a RIGHT (and FULL) join's **nullable** arm
against `accumulatedNN`, which carries ON-clause strictness merged from
INNER joins **below** it. Those constraints do not survive the join
that null-extends their result. Both arms now consult `upperNN` — quals
from ABOVE — which is PG's rule (`reduce_outer_joins_pass2`).

The change is conservative by construction: `upperNN ⊆ accumulatedNN`,
so it can only *decline* demotions, never add one.

## 2. It was a wrong-answer bug, not a missed optimisation

```sql
FROM rj_a JOIN rj_b ON rj_a.id = rj_b.aid
RIGHT JOIN rj_c ON rj_b.cid = rj_c.id
WHERE rj_a.id IS NULL
```

The inner ON is strict on `{rj_a, rj_b}`, so those entered
`accumulatedNN`; the RIGHT join then read its own nullable arm as
non-nullable and demoted to INNER — destroying the null-extended rows
that are the *only* rows the query returns. **0 rows where 1 is
correct.**

## 3. Verified against the oracle, not argued

Three optimizer tests pinned the old behaviour, one named
`TestReduceOuterJoinsInnerOnPropagatesToRightDemotion`. Rather than
trust either my reasoning or the tests, the shape was put to PG 18.3:

```sql
select * from ra join rb on ra.x = rb.y right join rc on rb.z = rc.id;
```

| | result |
|---|---|
| **PG 18.3** | `Merge LEFT Join`, **2 rows** (matched + null-extended) |
| goopg, before | demoted to INNER — would return 1 |

PG does not demote. The three tests were pinning a row-dropping bug and
are corrected to assert the PG-verified behaviour, each with the oracle
result recorded in the failure message so the next reader does not have
to re-derive it.

## 4. Gates

| gate | result |
|---|---|
| optimizer + executor suites | green |
| the 5 executor VALUES tests that caught every earlier attempt | **now pass** |
| TPC-H values, 22 queries | byte-identical |
| TPC-DS sweep | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| TPC-H / TPC-DS parity | unchanged, every category |

Parity is unchanged because neither corpus contains this shape — which
is exactly why the bug survived: **no benchmark query exercises it.**
It was found only by trying to move the reduction and reading what
broke.

## 5. What this unblocks, and what it does not

K29 said the `outerLinksHaveSJInfos` decline was load-bearing because it
masked an incorrect demotion. That is no longer true: the demotion is
correct now, so the decline is masking nothing and R27's original
ordering fix (§4a's transplant) can proceed on its own merits.

It does **not** by itself retire the 7 seam declines or move any match.
The sequence K29 set out — fix the analysis, then the ordering, then
the declines — has completed step one.

## 6. Four attempts, each falsified by running it

1. move `reduceOuterJoins` earlier → breaks 6 tests, 2 on values;
2. suppress the S9.4 flip → breaks 5 tests that pin it as contractual;
3. transplant verdicts only → optimizer green, values still wrong —
   which isolated the verdict as the culprit;
4. fix the verdict → green, and PG confirms.

Only (4) was a real fix, and (3) is what made it findable.
