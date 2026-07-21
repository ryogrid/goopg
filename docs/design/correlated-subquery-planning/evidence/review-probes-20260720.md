# Review probes — planner-correctness lens, 2026-07-20

| field | value |
| --- | --- |
| date | 2026-07-20 |
| goopg build | HEAD `e4a43ba6` (branch `wal-pg-nodetree`) |
| server | throwaway data dir, cgroup-capped scope `goopg-csq-review` (`GOOPG_MEM_HIGH=6G` / `GOOPG_MEM_MAX=8G`), 127.0.0.1:5599 |
| oracle | vanilla PostgreSQL 18 (`postgres/local_install`) on 127.0.0.1:5598, identical data |
| fixtures | `t1(a,b) = {(1,10),(2,20),(3,30),(4,NULL)}`; `t2(a,b) = {(1,10),(1,11),(3,NULL)}` |
| tag used in bundle text | `[measured-at-HEAD e4a43ba6]` (review probes 2026-07-20) |

All probes ran under `timeout`; a second connection's `SELECT 1` was used to
distinguish a stuck backend goroutine from a stuck server.

## 1. Planner infinite loop — IN sublink under OR / NOT

```sql
EXPLAIN SELECT * FROM t1 WHERE a=1 OR b IN (SELECT b FROM t2);
```

`timeout 15` fires (rc=124); the backend goroutine never returns; server RSS
reached **6.3 GB in ≈15 s** inside the cap (the uncapped 30 GB incident was
this same loop). `EXPLAIN` alone triggers it — the loop is purely in planning.
A second connection still answers, and the goroutine survives client
disconnect.

Independently confirmed hanging minimizations:

```sql
-- 1. non-correlated IN under OR
SELECT * FROM t1 WHERE a=1 OR b IN (SELECT b FROM t2);
-- 2. correlated IN under OR (passes correlatedInOperandSafeToUnnest)
SELECT * FROM t1 WHERE a=1 OR a IN (SELECT a FROM t2 WHERE t2.a=t1.a);
-- 3. NOT-wrapped IN, no OR needed (spelled `NOT IN` it works via Negated;
--    spelled `NOT (… IN …)` it hangs)
SELECT * FROM t1 WHERE NOT (b IN (SELECT b FROM t2));
```

Confirmed NOT hanging: EXISTS under OR (protected by the `topConjunct == nil`
bail, `internal/planner/unnest.go:2012`), IN under CASE in WHERE (stays a
SubPlan), scalar under OR (terminates — but returns wrong results, §3),
correlated-IN-under-OR whose operand is not the correlation column (safe-gate
rejects).

Stack (SIGABRT dump, goroutine `[runnable]`):
`unnestSubqueriesInPlan` (unnest.go:≈38, the IN pull-up loop) →
`unnestInExpr` (:≈1304) → `unnestNonCorrelatedInExpr` (allocating the `Join`
struct at :≈1520), reached from `planSelect` (planner.go:945).

Root cause: `findFilterContainingInExpr` (unnest.go:≈1539) locates the
`InExpr` by pointer **anywhere** in the predicate tree (including under
OR/NOT), but conjunct removal (:≈1374-1385 correlated, :≈1486-1497
non-correlated) filters `splitAnd(filter.Predicate)` by pointer equality with
a **top-level conjunct** — when the `InExpr` is nested under OR/NOT, nothing
matches, the predicate is left untouched, yet `filter.Child = join` is
installed anyway. The driver loop (unnest.go:33-48) re-finds the same
never-removed `InExpr` and wraps another `Join` per iteration forever —
unbounded allocation. The transform is also semantically wrong even at one
iteration (a Semi join applies the IN unconditionally; OR semantics are
lost), so the fix must bail, not merely fix removal.

## 2. Count-bug probes (correlated scalar aggregate)

```sql
SELECT * FROM t1 WHERE t1.a > (SELECT count(b) FROM t2 WHERE t2.a=t1.a);
-- goopg: {3}          PG: {2,3,4}     ← WRONG (INNER-join decorrelation
--                                        drops empty groups; count()=0 lost)
SELECT * FROM t1 WHERE t1.a > (SELECT count(*) FROM t2 WHERE t2.a=t1.a);
-- goopg: {2,3,4}      PG: {2,3,4}     ← correct, but only because the
--                                        Star gate bails count(*) into the
--                                        SubPlan path
SELECT * FROM t1 WHERE t1.a > (SELECT sum(b) FROM t2 WHERE t2.a=t1.a);
-- goopg: {}           PG: {}          ← correct (sum is NULL on empty input)
```

goopg plan for the `count(b)` case: `Hash Join (INNER)` + `GroupAggregate` —
exactly D3.4's count bug, live at HEAD. `canUnnestSubquery`
(unnest.go:≈184-199) checks only Aggregate root / single aggregate /
`!Star && !Distinct`; there is **no NULL-on-empty aggregate whitelist**.

## 3. OR-position scalar sublink — wrong results (terminates)

```sql
SELECT * FROM t1 WHERE a=2 OR b > (SELECT sum(x.b) FROM t2 x WHERE x.a=t1.a);
-- goopg: {} (0 rows)  PG: {2}
```

EXPLAIN shows `Filter: ((a = 2) OR (b > sum))` **above an INNER join**: the
row a=2 has no t2 group, is dropped by the join before the OR is evaluated.
The scalar pull-up loop has no top-conjunct/AND-reachability gate.

## 4. Correlated NOT IN, NULL operand × empty inner — executor SubPlan path

```sql
SELECT * FROM t1 WHERE b NOT IN (SELECT b FROM t2 WHERE t2.a=t1.a);
-- goopg: {2}          PG: {2,4}
```

For `a=4` the operand is NULL but the correlated subquery is **empty**;
`NULL NOT IN (∅)` is TRUE in PG (vacuously), goopg drops the row. This is the
un-unnested SubPlan path (the correlated NOT-IN unnest correctly does not
fire here). Non-correlated `NOT IN` with and without inner NULLs verified
correct ({} and {2,4} — the NullAware anti join works).

## 5. Index-dependence of the EXISTS/scalar pull-up loops

```sql
-- with NO index on t2(a): both fire
EXPLAIN SELECT * FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a=t1.a);
--   → Hash Join (?) semi join
EXPLAIN SELECT * FROM t1 WHERE b > (SELECT sum(x.b) FROM t2 x WHERE x.a=t1.a);
--   → Hash Join (INNER) + GroupAggregate

CREATE INDEX t2_a_idx ON t2(a);
-- same two EXPLAINs → Filter: (<*planner.ExistsExpr>) /
--                     (<*planner.SubqueryExpr>)   (SubPlan path)

DROP INDEX t2_a_idx;   -- stats retained
-- same two EXPLAINs → both fire again
```

Controlling variable = an index on the inner correlation column: the inner
planner absorbs the correlation equijoin into `IndexScan.Key`; the unnest
collectors harvest equijoins only from Filter conjuncts, while the
all-accounted walk `walkPlanExprs` **does** visit `IndexScan.Key`
(unnest.go:≈310-319), sees an unaccounted `OuterColumnRef`, and bails.

## 6. Outer-join ON-clause sublinks

```sql
SELECT * FROM t1 LEFT JOIN t2 ON t1.a=t2.a AND EXISTS (SELECT 1 FROM t2 x WHERE x.b=t1.b);
-- goopg: ERROR 0A000 "EXISTS not supported in this context"
--        (planner.go:≈10138; IN variant at :≈10112)   PG: 5 rows
SELECT * FROM t1 LEFT JOIN t2 ON t1.a=t2.a
 WHERE EXISTS (SELECT 1 FROM t2 x WHERE x.a=t1.a);
-- goopg: semi join stacked ABOVE the Hash Join (LEFT); result {(1,10),(1,11)}
--        matches PG (WHERE applies post-join — safe)
```

Sublinks in ON clauses are unreachable by the unnest pass today only because
plan-time resolution rejects them with 0A000 — itself a PG-compat gap; if
that gap is closed without the ch.03 §8.5 guard, the pass gains a hazard
silently.
