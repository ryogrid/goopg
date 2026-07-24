# 04 — Pathkeys and Ordering

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-07-22 |
| depends on | [02](02-pg-path-and-cost-oracle.md), [03](03-path-substrate-and-plan-creation.md) |
| premise | keep this sub-system minimal — it is the one part of the Path model that can balloon |

## 0. Why this chapter exists, and why it is short

Pathkeys are the reason a two-number cost model is worth building: they let
`add_path` keep an ordered-but-dearer path alive because it will save a downstream
sort. But PG's pathkey machinery
(`postgres/src/backend/optimizer/path/pathkeys.c`, and the equivalence-class
system it rests on) is one of the largest sub-systems in the planner. Reproducing
it in full is neither necessary for the TPC-H milestone nor wise as a first step.
This chapter deliberately scopes goopg to the **minimum ordering the milestone
needs** and states, explicitly, what is deferred — so the sub-system cannot grow
by accretion.

## 1. What ordering the milestone actually needs

Three consumers, and only three:

1. **`ORDER BY`** — a path already delivering the sort order pays no final sort.
   Several TPC-H queries end in `ORDER BY … LIMIT` (Q2, Q3, Q10, Q18, Q21), where
   the startup/total split and a pre-sorted path interact.
2. **Merge join** — `final_cost_mergejoin` (`costsize.c:3837`) charges a
   `cost_sort` on each input whose pathkeys do **not** already satisfy the merge
   clause. Without pathkeys, a merge join can never be seen as cheaper than a hash
   join, because it would always appear to pay both sorts.
3. **Gather Merge** — `cost_gather_merge` (`costsize.c:485`) requires each worker
   stream to be sorted on the merge key; the partial path below must carry those
   pathkeys ([08](08-parallel-paths-and-degree.md) §4).

Everything else PG uses pathkeys for — merge-append over partitions, incremental
sort, `DISTINCT`/`GROUP BY` order optimisation, window ordering — is out of scope
for the milestone and listed in §4.

## 2. A minimal pathkey representation

```go
// PathKey is one column of a path's ordering.
type PathKey struct {
    Expr      Expr        // the sort expression (a ColumnRef in the common case)
    SortAsc   bool
    NullsFirst bool
    // No EquivalenceClass pointer in the minimal form — see §2.1.
}
```

A path's `Pathkeys []PathKey` is the ordered list of columns it is sorted on.
The one operation the cost model needs is **containment** — "does ordering A
satisfy requirement B" — reproducing `pathkeys_contained_in` (`pathkeys.c:343`):

```go
// pathkeysContainedIn reports whether `keys` is a prefix-compatible ordering
// that satisfies `required`. A path sorted by (a, b, c) satisfies a requirement
// of (a, b).
func pathkeysContainedIn(keys, required []PathKey) bool
```

### 2.1 The deliberate omission: no equivalence classes yet

PG's pathkeys are *canonicalised through equivalence classes*: because `a = b` is
known, a path sorted on `a` also satisfies a requirement to sort on `b`, and PG
represents both by a pointer to the same `EquivalenceClass` so the containment
test is pointer equality. goopg's minimal form compares **expressions
syntactically** instead. The consequence is a **false negative, never a false
positive**: goopg may fail to notice that an `a`-sorted path satisfies a `b`
requirement across an `a = b` join, and insert a redundant sort it did not
strictly need. That is a missed optimisation, not a wrong plan — the sort is
correct, merely unnecessary.

This is the right trade for the milestone: a false negative costs a sort goopg
would have paid anyway under today's planner (which has no pathkeys at all), while
building the equivalence-class system up front would double the size of this
bundle for a refinement TPC-H does not need. goopg *does* already have an
equivalence-class builder for join inference
(`internal/planner/equiv_class.go`); wiring pathkeys through it is the natural
first deferred refinement (§4), not a milestone item.

## 3. Where pathkeys are produced and consumed

**Produced** at three sites:

- A **Sort path** (or the executor `*planner.Sort` a scan path is wrapped in)
  carries the pathkeys of its sort clause, built from `ORDER BY` the way
  `make_pathkeys_for_sortclauses` (`pathkeys.c:1336`) does.
- A **scan path over an ordered index** carries the index's pathkeys, the analogue
  of `build_index_pathkeys` (`pathkeys.c:740`). goopg's index scans are equality-
  probe today (`EstimateRows` treats an `IndexScan` as one row,
  `cardinality.go`), so ordered-index pathkeys are relevant mainly for a future
  range-scan cost; the hook exists but is thinly exercised at the milestone.
- A **Merge join path** carries the pathkeys of its merge clause (its output is
  sorted on the join key).

**Consumed** at three sites, all cost decisions: `add_path` dominance
([03](03-path-substrate-and-plan-creation.md) §2), the merge-join sort charge
([06](06-scan-and-join-path-costs.md) §3.2), and the final `ORDER BY` sort that
`create_plan` inserts only when no chosen path already satisfies it
([03](03-path-substrate-and-plan-creation.md) §3).

## 4. Deliberately deferred

| Item | Why deferred | Reopen when |
| --- | --- | --- |
| Equivalence-class canonicalisation of pathkeys | §2.1 — syntactic comparison is a false-negative-only approximation; the EC builder (`equiv_class.go`) exists but wiring it through pathkeys is a refinement TPC-H does not need | a query is shown to pay a redundant sort that EC-aware pathkeys would elide |
| Incremental sort | goopg has no incremental sort operator; a presorted-prefix path cannot be exploited without one | an executor incremental sort exists |
| Merge Append / partitionwise ordering | goopg has no partitioned tables in the cost model's scope | partitioning enters the planner |
| `DISTINCT` / `GROUP BY` order-aware planning | goopg's grouping is hash-based in every case ([parallel-query/11](../parallel-query/11-partial-aggregation-cost-model.md) §7); there is no sorted-grouping strategy to order for | a sorted aggregate operator exists |

## 5. Divergence from PostgreSQL

- **Syntactic, not equivalence-class, pathkey comparison** (§2.1) — a
  false-negative-only approximation; goopg may insert a sort PG would prove
  redundant. Correctness is unaffected; the cost is one avoidable sort.
- **No incremental sort, no merge-append ordering** (§4) — goopg's executor lacks
  the operators, so the cost model has nothing to plan toward.
- **Ordered-index pathkeys are defined but thinly used** (§3) — goopg's index
  scans are equality-probe-shaped today, so the range-ordered-scan path that would
  exercise them is itself future work.
