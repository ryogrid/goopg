# CRITICAL — the merge join returns WRONG ANSWERS, and the inflated `work_mem` default is hiding it

**Status:** FIXED in `13d53603f`. Pre-existing; not caused by any commit in this bundle.
**Severity:** silent wrong answers on ordinary SELECTs. Row counts are
unaffected, so a row-count gate passes it.

## Claim

When goopg's merge join uses a strict subset of the available equi-clauses, the
remaining clause is not applied, and the join emits the unfiltered product.
Results are wrong by exactly the dropped clause's selectivity.

The bug is latent at goopg's shipped configuration only because the `work_mem`
BootVal is 512 MB — 128x PostgreSQL's 4 MB. That budget keeps the planner on a
hash path. **Setting `work_mem` to PostgreSQL's own default is enough to get
wrong answers.**

## Reproduction

TPC-H SF=1, `bench/tpch`, the query is unmodified Q9.

```
work_mem = 64MB (bench conf), planner default 512MB  ->  correct
work_mem BootVal lowered to PG's 4MB                 ->  WRONG
```

```sql
select sum(amount) from (
  select l_extendedprice * (1 - l_discount) - ps_supplycost * l_quantity as amount
  from part, supplier, lineitem, partsupp, orders, nation
  where s_suppkey = l_suppkey and ps_suppkey = l_suppkey
    and ps_partkey = l_partkey and p_partkey = l_partkey
    and o_orderkey = l_orderkey and s_nationkey = n_nationkey
    and p_name like '%green%') profit;
```

| | value |
|---|---|
| PostgreSQL 18.3 (oracle, same data) | 7 528 869 517.19 |
| goopg, `work_mem` = PG's default | **30 270 658 609.88** |

Ratio **4.02x**. The full Q9 returns **175 rows in both cases** — the correct
count — with different tuples. `cmd/tpch-runner -digest -diff` reports
`Q9 VALUE-DIFF same 175 rows, different tuples`; a row-count comparison reports
nothing.

## Mechanism

`lineitem x partsupp` carries TWO equi-clauses:
`ps_partkey = l_partkey` and `ps_suppkey = l_suppkey`. The chosen merge join
uses one:

```
Merge Join  (cost=0.75..826630.32 rows=24989610 width=1094) (actual rows=24005020.00)
      Merge Cond: (partsupp.ps_partkey = lineitem.l_partkey)
      ->  Index Scan using partsupp_part_fkidx on partsupp    (actual rows=800000)
      ->  Index Scan using lineitem_part_supp_fkidx on lineitem (actual rows=6001255)
```

- **No `Join Filter:` line.** `ExplainNode`'s equivalent label exists and is
  reachable (`operators_explain.go:806`), so its absence is meaningful here.
- **24 005 020 rows emitted** where the two-clause join produces 6 001 255 —
  the single-key product, unfiltered. 24.0M / 6.0M = 4.00, which is the 4.02x
  seen in the sum.

## Root cause (found)

`generateMergeJoinPaths`' FIRST candidate (`joinpathsmergeouter.go:178`) built
its merge-clause list from `findMergeClausesForOuterPathkeys`, which returns only
the groups the outer path's ordering serves, and then passed the ORIGINAL
`residual` through untouched. Clauses in an unmatched group were dropped from the
merge keys and never added to the residual, so nothing evaluated them.

The other two call sites in the same function already demoted their dropped
clauses with `demoteDroppedMergeClauses`, so the rule was known; this path missed
it. The fix adds `demoteUnmatchedGroupClauses`, the identity-based sibling —
`demoteDroppedMergeClauses` may subtract by POSITION because
`trimMergeClausesForInnerPathkeys` appends in order and stops, whereas the set
dropped here is chosen by which GROUPS the outer's pathkeys serve and is not a
prefix.

## Verification

With `work_mem` at PostgreSQL's default, all 24 TPC-H queries now match the
known-good run **on values** (previously `Q9 VALUE-DIFF`). At the shipped
configuration the fix is neutral: 239.72 s vs 240.73 s, 24 MATCH.

## Original analysis

The path itself is built with the residual attached
(`joinpathsmerge.go`: `Residual: residual`), and `createMergeJoinPlan` passes it
on (`createplanjoin.go:563`, `Predicate: in.joinPredicate("PathMergeJoin",
pairs, p.Residual)`), and the executor implements residual rejection
(`join_merge_stream.go`, `mergeResidualMatch`). So all three layers have the
machinery; the clause is being lost between them. The exact site is NOT yet
identified — that is the next step, and it should be found by construction, not
by inspection: force a merge join over two equi-clauses in a unit test and
assert the row count.

Note `enable_hashjoin = off` does **not** currently force the shape (goopg still
produced a Hash Join with it set) — that is P2-05's unwired `enable_*`, so a
reproducer must reach the merge path some other way.

## Not caused by this bundle

The digest of the wrong result (`efe2e0d17836811d`) is **identical** before and
after `c281b0830` (the merge-join cost fix), so the cost change neither causes
nor worsens it. It reproduces on the session-start tree. What the cost fix does
is make merge joins *more* expensive, i.e. it reduces exposure.

## Consequences

1. **P2-02b must not land** until this is fixed. Correcting `work_mem` to PG's
   default is a wrong-answer change today, independent of its 26.7 % cost.
2. **Any user who tunes `work_mem` down is exposed**, as is any workload whose
   shape reaches this path at the current default. The default is not a
   safeguard, it is camouflage.
3. Every gate that compares **row counts** rather than values is blind to this
   class. `tpch-runner -digest` caught it; `spotcheck_expected.env` and
   `ci/batch/tpch-row-anchors.csv` would not.

## Suggested next steps

1. Build a unit-level reproducer that forces a merge join with a residual (via
   the path builders directly, since `enable_hashjoin` is inert) and asserts the
   emitted row count against the two-clause answer. It should fail on the
   current tree.
2. Walk `keys` / `residual` from `splitJoinClauses` through `sortInnerAndOuter`
   and `demoteDroppedMergeClauses` to `joinPredicate`, checking at each hop that
   the second clause is still present.
3. Only then revisit P2-02b.
