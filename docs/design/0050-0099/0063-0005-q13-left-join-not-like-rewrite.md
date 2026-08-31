# Design 0063-0005 — Q13 LEFT JOIN + NOT LIKE residual rewrite

| field      | value |
| ---------- | ----- |
| status     | accepted |
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

## 8. tpch/Q13-regression post-mortem (2026-07-07, M-NIGHTLY)

The rewrite proposed above landed correctly and worked for a long
time (`Q13=33` pinned in `bench/tpch/spotcheck_expected.env` since
the 2026-06-13 reload), but a nightly CI batch run
(`ci/logs/20260707-000712`) surfaced Q13 failing again — first as a
hard error, then (after the first fix below) as a silent row-count
regression. Two independent, previously-latent bugs in the code
paths this design describes were found and fixed in the same loop:

**Bug 1 — inner-only Filter not marked `LeafLocal` (crash).**
`planFromItem`'s LEFT JOIN conjunct-partition step (§4.1 above,
`internal/planner/planner.go` ~line 1899) wraps the single-table
`o_comment NOT LIKE ...` conjunct in a `Filter` over the inner
(orders) plan and shifts its `ColumnRef.Index` from FROM-cumulative
to inner-local coordinates (`shiftColumnRefsBy(c, -leftWidth)`) —
exactly as designed. But that `Filter` was never marked
`LeafLocal: true` (the M0077-0001 convention documented on the
`Filter` struct in `plan.go`). Two post-rewrite passes that run
later in the same `Plan()` call — `remapWithBindings` →
`applyJoinTreePosMap` and `remapExprRefsToMHJ` →
`remapPosMapAfterRewrite` (both in `bushy.go`) — walk every
non-`LeafLocal` `Filter.Predicate` and reinterpret its ColumnRef
index as a stale FROM-cumulative offset needing correction. Applied
to an already-local index, this remaps it a second time. Concretely,
with `customer`(8 cols) and a non-canonical `orders` column order
(`o_orderdate` first, `o_comment` last at local index 8), the
already-correct index 8 got remapped down to 0, silently resolving
`o_comment` to `o_orderdate` (a `Time` value) at runtime and
producing `operator NOT LIKE requires string operands (got
left.Kind=5 right.Kind=3)` (42883). Fix: mark the Filter
`LeafLocal: true` at construction, matching every other leaf-local
Filter site in the planner.

**Bug 2 — NLI's `pickInnerSide` flips LEFT JOIN preservation
(silent row loss, found while re-verifying Bug 1's fix).** Once the
crash above was fixed, `scripts/tpch-spotcheck.sh` still failed:
Q13 ran to completion but returned 32 rows instead of 33 — missing
exactly the `c_count = 0` bucket (the ~50,000 of 150,000 TPC-H SF=1
customers with zero orders, or zero orders passing the NOT LIKE
filter). Root cause: `internal/planner/nl_index_join.go`'s
`tryBuildNLI` prefers `j.Right` as the NLI's indexed inner side, but
falls back to `j.Left` (`pickInnerSide`'s `lss` branch) whenever
`j.Right` is not a bare `*SeqScan` — which is exactly what Bug 1's
Filter wrapper produces once `o_comment NOT LIKE` is split onto the
orders side. That fallback makes `j.Left` (customer) the INNER
(indexed, probed, null-extended-on-miss) side and `j.Right`
(orders) the OUTER (loop-driving) side, without adjusting `j.Type`.
For an INNER join this swap is a harmless no-op (either side may
drive). For LEFT JOIN (and Semi/Anti) it silently changes which
side is preserved: only rows visited by the OUTER loop can ever
appear, so once `orders` drives the loop, a customer with zero
matching orders is simply never visited and vanishes from the
output — the exact missing `c_count = 0` bucket. Fix: `pickInnerSide`
now declines the `j.Left`-as-inner branch whenever `j.Type !=
JoinTypeInner`, falling back to the Hash/Merge join path (which
correctly keeps customer as the preserved probe side, per §3/§4.2
above) instead of silently corrupting LEFT/Semi/Anti semantics.

Both fixes are pinned by regression tests:
`internal/planner/left_join_inner_only_leaflocal_test.go`
(`TestLeftJoinInnerOnlyConjunctFilterIsLeafLocal`, Bug 1) and the
existing `internal/planner/nl_index_join_test.go` suite plus a new
guard in `pickInnerSide` (Bug 2 — no dedicated NLI-flip test was
added since the fix is a one-line type guard covered by the
existing `TestNLIRulePromotesEquiJoinOnIndexedInner`-style suite
plus the end-to-end `tpch-spotcheck.sh` gate). Verified via
`scripts/tpch-spotcheck.sh` (Q12=2, Q13=33) and a fresh-server
`go test ./...` sweep (excluding the two packages that are
deliberately excluded from the default suite:
`internal/testutil/tpch` — heavy scale-load tests gated behind
`-short`/explicit `-run`; `internal/testport` — ported oracle tests
that must be invoked explicitly per `.ralph/PROMPT.md`).
