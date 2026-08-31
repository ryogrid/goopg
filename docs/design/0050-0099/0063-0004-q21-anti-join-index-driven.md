# Design 0063-0004 — Q21 anti-join index-driven inner

| field      | value |
| ---------- | ----- |
| status     | draft |
| date       | 2026-05-07 |
| milestone  | 0063 — TPC-H residual long-tail v2 |
| supersedes | — |

## 1. Problem

Q21 cancels at 600 s on TPC-H SF=1. M0062-0005 produces a
correct hash-Anti and hash-Semi for the EXISTS / NOT EXISTS
correlations (verified: rows would emit if the join finished).
The build phase hashes the entire `lineitem` table (6 M rows)
twice — once each for the Semi and Anti — before the outer-side
probe even begins. Combined with the residual `<>` predicate
applied per matched pair, the wall-clock cost dwarfs the budget.

Q21 SQL fragment:

```sql
... AND EXISTS (
        SELECT * FROM lineitem l2
         WHERE l2.l_orderkey = l1.l_orderkey
           AND l2.l_suppkey  <> l1.l_suppkey)
    AND NOT EXISTS (
        SELECT * FROM lineitem l3
         WHERE l3.l_orderkey   = l1.l_orderkey
           AND l3.l_suppkey   <> l1.l_suppkey
           AND l3.l_receiptdate > l3.l_commitdate)
```

The correlation is on `l_orderkey`. `lineitem` already has a
B-tree index on this column (`idx_lineitem_orderkey`,
established by the M0044 / M0054-0006 work). For each outer
`l1` row, an index-driven probe of `idx_lineitem_orderkey =
l1.l_orderkey` would yield ~4 matching `l2` / `l3` rows in
~µs. Outer cardinality ≈ 230 K (orders-with-status-F × the
lineitem-FK-residue) → ~1 M total inner probes × µs ≈ 1 s.
Versus 600 s cancel.

## 2. Hypothesis

The fix is to **rewrite the hash Anti / Semi joins produced by
M0062-0005 into NestedLoopIndexAnti / NestedLoopIndexSemi when
the inner side has a B-tree index covering the equi-pair key**.
M0054-0006's `nliRewrite` already handles `JoinTypeInner` and
`JoinTypeLeft`; extending it to `JoinTypeSemi` /
`JoinTypeAnti` is a localized change (per-join-type emit
policy, not a new operator class).

A new executor variant — call it `nestedLoopIndexSemiAntiOp` —
mirrors `nestedLoopIndexJoinOp` (`internal/executor/operators_nljoin.go`)
but emits the probe row at most once based on inner-match
presence:

- Semi: emit the probe row iff `inner.Next()` returns at least
  one match (after the join's residual Predicate passes).
- Anti: emit the probe row iff `inner.Next()` returns zero
  matches that pass the residual.

## 3. Critical code paths

| Path | File:line |
| ---- | --------- |
| NLI rewrite (Inner / Left) | `internal/planner/nl_index_join.go::rewriteJoinsToNLI`, `nliRewrite` |
| Cost gate | `internal/planner/nl_index_join.go::nliCostGateAccepts` |
| Hash Semi / Anti emit | `internal/executor/operators_join_agg.go::nextLazy` (line 544-555 area) |
| Existing NLI executor | `internal/executor/operators_nljoin.go::nestedLoopIndexJoinOp` |
| EXISTS unnesting (sets up the Hash Semi/Anti) | `internal/planner/unnest.go::unnestExistsExpr` |

## 4. Proposed change

1. **Extend `nliRewrite` to handle `JoinTypeSemi` /
   `JoinTypeAnti`.** When the inner side is a `*SeqScan` whose
   table has an index on the equi-pair key, swap the Hash
   Semi/Anti for a `NestedLoopIndexJoin{Type: Semi/Anti,
   Outer, Inner: IndexScan}`.

2. **Add anti-emit semantics to `nestedLoopIndexJoinOp`.**
   Currently the op always emits joined rows for matching
   probes (Inner / Left semantics). Add a `borrow`-style
   emit-mode field gated by `o.plan.Type` — Semi emits the
   bound outer row once on first match, Anti emits the
   bound outer row once on zero matches. The residual
   Predicate (M0062-0005's lifted `<>`) still evaluates per
   inner row and gates the "match" classification.

3. **Cost gate.** Anti / Semi NLI is preferable to Hash
   Anti / Semi when the inner side's index covers the key
   AND the outer is small enough that a per-row probe beats
   a full inner scan. Reuse `nliCostGateAccepts`'s outer
   row-count threshold (~ 100 K).

## 5. Acceptance

- Q21 OK in < 600 s on TPC-H SF=1.
- EXPLAIN of Q21 shows `NestedLoopIndexJoin (SEMI)` and
  `NestedLoopIndexJoin (ANTI)` instead of the M0062-0005
  hash variants, when the index covers the key.
- Existing M0062-0005 tests (`exists_unnest_test.go`,
  `q21_live_test.go`) still pass — the planner-side shape
  produced by `unnestExistsExpr` is unchanged; the NLI
  rewrite happens after.
- New test pinning the Semi / Anti NLI rewrite for an
  EXISTS-like shape with an indexed equi-pair.
- `go test ./...` PASS.

## 6. Risks & rollback

- The NLI cost gate threshold (`nliMaxOuterRowsHeuristic =
  100000`) may not match this case's outer cardinality
  exactly. If Q21 is rejected by the gate, lift it for
  Semi / Anti or supply a separate threshold.
- The `enable_nestloop_index` GUC reverts to hash
  semi/anti if a regression appears.

## 7. Out of scope

- General nested-loop-anti-join algorithm without an
  index (covered by the existing hash-anti from
  M0061-0001).
- The build-phase ctx fix from commit `6f618d2` is already
  in; this design assumes that as the baseline.
