# Design 0063-0005 — Q13 LEFT JOIN + NOT LIKE residual rewrite

| field      | value |
| ---------- | ----- |
| status     | draft |
| date       | 2026-05-07 |
| milestone  | 0063 — TPC-H residual long-tail v2 |
| supersedes | — |

## 1. Problem

Q13 cancels at 600 s on TPC-H SF=1. M0062's cancel-prop fix
(commit `aa2c50e`) made the cancel itself responsive — Q13
returns SQLSTATE 57014 within 60 ms of the
`--cancel-after=600s` deadline. But the query genuinely needs
much longer than 600 s under the current plan because of an
O(N×M) Nested Loop.

Q13 SQL (`internal/testutil/tpch/tpch.go:13`):

```sql
SELECT c_count, count(*) AS custdist FROM (
  SELECT c_custkey, count(o_orderkey) AS c_count
    FROM customer LEFT OUTER JOIN orders
      ON c_custkey = o_custkey
     AND o_comment NOT LIKE '%special%requests%'
   GROUP BY c_custkey
) c_orders
 GROUP BY c_count
 ORDER BY custdist DESC, c_count DESC;
```

The LEFT JOIN's `ON` condition has TWO conjuncts:

1. `c_custkey = o_custkey` — an equi-pair (would qualify for
   Hash LEFT JOIN).
2. `o_comment NOT LIKE '%special%requests%'` — a
   single-table predicate on the inner (orders) side.

The current planner emits a `Nested Loop (LEFT)` because
`splitEqualityForHash` only triggers on a *pure* equality
predicate. With the AND'd NOT LIKE, the splitter passes through
to the NL fallback.

NL on `customer (150 K) × orders (1.5 M)` ≈ 225 G pair
evaluations × per-pair LIKE eval ≈ multiple thousands of
seconds.

## 2. Hypothesis

The NOT LIKE conjunct in the `ON` is **single-table** (only
references `orders.o_comment`), so it can be moved to a
**post-Filter** step on the orders side BEFORE the join, OR
preserved as a residual Filter on the inner of a Hash LEFT
JOIN.

Both rewrites preserve LEFT JOIN semantics: a customer with no
matching `o_comment NOT LIKE '%...%'`-passing order still gets
emitted as `(c_custkey, NULL)`. The output row count is
unchanged.

The simpler shape:

```
LEFT JOIN
  customer
  ( SELECT * FROM orders WHERE o_comment NOT LIKE '%special%requests%' ) o
  ON c_custkey = o.o_custkey
```

This becomes a Hash LEFT JOIN on `c_custkey = o_custkey` — and
the `o_comment` Filter is a single-table predicate on `orders`
applied before the hash build, drastically cutting the build
side and eliminating per-pair LIKE eval.

## 3. Critical code paths

| Path | File:line |
| ---- | --------- |
| `splitEqualityForHash` (decides Hash vs NL) | `internal/planner/pushdown.go` (around line 200-280) |
| LEFT JOIN ON-clause processing | `internal/planner/planner.go::planFromItem` (~line 683-755) |
| Single-table predicate detection | `internal/planner/pushdown.go::classifyConjunctSide` (line 204-234) |
| Hash LEFT JOIN executor | `internal/executor/operators_join_agg.go::joinOp::nextLazy` (line ~478-512, JoinTypeLeft path) |

## 4. Proposed change

1. **Single-table-predicate split in LEFT JOIN ON.** When
   `planFromItem` builds a LEFT JOIN, walk `j.On`'s top-level
   conjuncts and partition them:
   - **Equi-pair conjuncts** (left-col = right-col): keep on
     the join (LeftKey/RightKey).
   - **Single-inner-side conjuncts** (refs only orders cols):
     wrap the inner range-var's plan in a Filter BEFORE
     the join.
   - **Single-outer-side conjuncts** (refs only customer
     cols): for a LEFT JOIN, these CANNOT be moved before
     the join (they would drop unmatched outer rows). Stay
     on the join as residual Predicate.
   - **Cross-side non-equi**: stay on the join Predicate.

2. **Hash LEFT JOIN re-classification.** After the split,
   if the remaining join Predicate is a clean equi-pair,
   route through `splitEqualityForHash` and emit `Algo =
   JoinAlgoHash, Type = JoinTypeLeft`. The executor's
   existing LEFT-with-hash code path
   (`joinOp.nextLazy`'s `JoinTypeLeft` branch at the
   "no matches → emit nullRight" line) handles the rest.

3. **Validation** that the existing residual eval logic
   already understands a Filter wrapped around a SeqScan
   on the inner side of a Hash LEFT JOIN. (It should — the
   inner is just a child operator from the executor's
   perspective.)

## 5. Acceptance

- Q13 OK in < 600 s on TPC-H SF=1 with row-count parity vs
  PostgreSQL.
- EXPLAIN shows `Hash Join (LEFT) → SeqScan customer × Filter
  (o_comment NOT LIKE) → SeqScan orders` instead of `Nested
  Loop (LEFT)` with the LIKE in the join Predicate.
- New test in `internal/planner/pushdown_test.go` (or a new
  file) pins the LEFT-JOIN-ON conjunct partition for the
  shape `LEFT JOIN ... ON A.x = B.y AND <inner-only-pred>`.
- `go test ./...` PASS.

## 6. Risks & rollback

- The conjunct partition must NOT move outer-side
  predicates before the LEFT JOIN, since that would change
  result rows (filter out unmatched outers). The
  classification step must distinguish inner vs outer
  side, which `classifyConjunctSide` already does for the
  CROSS→INNER promotion path.
- If a LIKE pattern is a function with side effects (it
  isn't, in TPC-H), pushing the Filter could change
  semantics. Real PostgreSQL's planner does the same
  push, so accepting the same risk surface.

## 7. Out of scope

- General correlated-subquery rewriting in LEFT JOIN
  (handled by M0061-0001 / M0063-0003).
- The outer GROUP BY + count(*) in the Q13 outer wrapper —
  unaffected by the join shape.
