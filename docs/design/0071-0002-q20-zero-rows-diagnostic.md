# Design 0071-0002 — Q20 Zero-Rows Diagnostic

**Milestone:** M0071-0002
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor`

## Context

TPC-H Q20 was previously a 1200 s cancel. M0069-0005
(commits `ebb267d` + `5f120c1`) extended the planner's
IN-subquery unnest to handle the non-correlated case as a
SemiJoin (`unnestNonCorrelatedInExpr` in
`internal/planner/unnest.go:~1095`). After that landed, Q20
completes in 30 s but **returns 0 rows** — the canonical
TPC-H SF=1 result is ≈ 186 rows.

The 0-rows cause is **undocumented** in
`analysis/tpch-m0069-baseline-2026-05-08.md`,
`analysis/tpch-m0070-baseline-2026-05-08.md`, and the
existing milestone docs. M0071-0002 opens this as a
diagnostic + fix task per the user's directive
("**Q20 の調査タスクも加えて**" — add a Q20 investigation
task).

## Q20 query shape

```sql
SELECT s_name, s_address
FROM   supplier, nation
WHERE  s_suppkey IN (
         SELECT ps_suppkey
         FROM   partsupp
         WHERE  ps_partkey IN (
                  SELECT p_partkey FROM part WHERE p_name LIKE 'forest%'
                )
         AND    ps_availqty > (
                  SELECT 0.5 * sum(l_quantity)
                  FROM   lineitem
                  WHERE  l_partkey  = ps_partkey
                  AND    l_suppkey  = ps_suppkey
                  AND    l_shipdate >= date '1994-01-01'
                  AND    l_shipdate <  date '1994-01-01' + interval '1 year'
                )
       )
AND   s_nationkey = n_nationkey
AND   n_name      = 'CANADA'
ORDER BY s_name
```

Three nested subqueries:

1. **Outer IN** — `s_suppkey IN (SELECT ps_suppkey FROM
   partsupp WHERE ...)` — non-correlated. M0069-0005 path
   (`unnestNonCorrelatedInExpr`) → SemiJoin on
   `s_suppkey = ps_suppkey`.

2. **Inner IN** — `ps_partkey IN (SELECT p_partkey FROM
   part WHERE p_name LIKE 'forest%')` — non-correlated.
   Same M0069-0005 path; nested unnest via
   `unnestSubqueriesInPlan(innerPlan)` recursive call.

3. **Correlated scalar** — `0.5 * sum(l_quantity) FROM
   lineitem WHERE l_partkey = ps_partkey AND
   l_suppkey = ps_suppkey AND ...` — correlated to the
   partsupp inner. Stays as a SubPlan (M0058-0001 cache);
   per-partsupp-row evaluation.

## Hypotheses (ranked by likelihood)

### H1: Outer SemiJoin's `RightKey` index off by one (HIGH likelihood)

The 5f120c1 fix uses `JoinTypeSemi` with outer-only schema
and computes `innerKey.Index = outerWidth` so the executor's
hash-key extraction reads from the padded build-side row's
column 0 (the inner plan's first output column). If the
inner plan's `Output()` doesn't actually start with
`ps_suppkey` after the recursive `unnestSubqueriesInPlan`
mutates the inner plan (e.g. by nesting another SemiJoin
that widens its output), the `outerWidth` index lands on a
different column.

**Diagnostic:** EXPLAIN of Q20 post-unnest. Verify that the
SemiJoin's right-side `Output()` is `[ps_suppkey]` (a single
column). If wider, the inner unnest of `ps_partkey IN (parts)`
widened the inner plan's schema beyond what the outer
SemiJoin expects.

### H2: Nested inner IN unnest produced an over-wide schema (HIGH likelihood)

`unnestNonCorrelatedInExpr` builds the SemiJoin with
`schema = outerChild.Output()` (outer-only), which is
correct for a SemiJoin's emit semantics. But during the
recursive `unnestSubqueriesInPlan(innerPlan)` step, if the
inner-IN unnest also fires, the inner plan's output may
shift in a way the outer SemiJoin's index calculation
didn't account for.

**Diagnostic:** comment out the recursive
`unnestSubqueriesInPlan(innerPlan)` call in
`unnestNonCorrelatedInExpr`; re-run Q20. If row count > 0,
H2 is confirmed.

### H3: Correlated scalar subquery's `ps_partkey` /
`ps_suppkey` outer refs land on wrong slots (MED likelihood)

The correlated scalar (`SELECT sum FROM lineitem WHERE
l_partkey = ps_partkey AND l_suppkey = ps_suppkey ...`) is
NOT unnested by M0069-0005 (it's a scalar subquery, not an
IN). It stays as a SubPlan that re-evaluates per
partsupp-row. The OuterColumnRef references `ps_partkey` /
`ps_suppkey` resolve against partsupp's runtime row layout.
If the M0069-0005 unnest reordered partsupp's columns
(e.g. via the SemiJoin's merged schema), the OuterColumnRefs
point at wrong slots and the SubPlan returns NaN/null,
making `ps_availqty > NaN` always false.

**Diagnostic:** run Q20 with the `ps_availqty > ...`
predicate stripped (replace with `ps_availqty > 0`); if
row count > 0, H3 is confirmed.

### H4: `s_nationkey = n_nationkey AND n_name = 'CANADA'` is
the actual zero-rows source (LOW likelihood)

Q20's outer scope is `supplier, nation` cross-product
filtered by `s_nationkey = n_nationkey` and `n_name =
'CANADA'`. Even ignoring the SemiJoin, this should produce
a non-empty supplier set joined with the Canadian nation
row. Unlikely to be the cause but should be verified.

**Diagnostic:** strip the IN predicate entirely; verify
`SELECT s_name, s_address FROM supplier, nation WHERE
s_nationkey = n_nationkey AND n_name = 'CANADA'` returns
> 0 rows. If 0, the bug is unrelated to M0069-0005.

### H5: Q20 row count is genuinely 0 at the loaded SF=1
data subset (VERY LOW likelihood)

Some TPC-H data generators produce slightly different
distributions; Q20's predicate is selective. Cross-checking
with upstream Postgres on the same dataset should rule this
in or out.

**Diagnostic:** run Q20 against upstream Postgres
(`internal/testutil/tpch/upstreampg_test.go` patterns); if
upstream also returns 0 rows, the dataset, not goopg, is
the issue.

## Diagnostic plan

Execute the hypotheses in order H4 → H1 → H2 → H3 → H5:

1. **H4 strip-down** (5 min). Run the Q20 outer alone (no
   IN clause). Confirm Canadian-supplier rows exist. If
   not, file the bug under "supplier-nation join" and
   retarget M0071-0002.

2. **H1/H2 EXPLAIN audit** (15 min). EXPLAIN Q20 post-
   unnest; capture the SemiJoin's Output schema and key
   indices. Compare against the inner plan's actual
   `Output()`. If misaligned, H1 or H2 is confirmed; the
   fix is to recompute `innerKey.Index` after the recursive
   inner unnest mutates the inner plan's schema.

3. **H3 correlated-scalar bypass** (15 min). Replace the
   `ps_availqty > (scalar subquery)` with `ps_availqty >
   0`; re-run Q20. If row count jumps, H3 is confirmed; the
   fix is to verify OuterColumnRef resolution after the
   SemiJoin shape change.

4. **H5 upstream cross-check** (5 min). Run Q20 on upstream
   Postgres at the same dataset; document the canonical
   row count.

## Likely fix shapes

- **H1/H2 fix:** in
  `internal/planner/unnest.go::unnestNonCorrelatedInExpr`,
  recompute `innerKey.Index` AFTER the recursive
  `unnestSubqueriesInPlan(innerPlan)` call so the index
  reflects the inner plan's post-unnest `Output()`. Today
  the index is computed before the recursion (line ~1170
  area), which is the structural bug if the recursion
  widens the inner schema.

- **H3 fix:** ensure that during the M0069-0005 SemiJoin
  rewrite, the correlated scalar's OuterColumnRefs are
  re-resolved against the post-unnest column coordinates.
  This may need the M0064 outer-MHJ rebind pattern applied
  to the correlated SubPlan path.

- **H4 fix:** unrelated to M0069-0005; tracked as its own
  bug.

- **H5 outcome:** documented expected row count, not a fix.

## Acceptance

- Q20 row count > 0 (target ≥ 100; canonical ≈ 186).
- Q18 row count preserved at 11 (Q18 also goes through the
  same `unnestNonCorrelatedInExpr` path; regression guard).
- `go test ./internal/planner/...` PASS, including a new
  unit test that pins the Q20 SemiJoin's RightKey index =
  `outerWidth` after recursive inner-IN unnest fires.

## Files

- `internal/planner/unnest.go::unnestNonCorrelatedInExpr`
  (the fix site).
- `internal/planner/q21_live_test.go` pattern for Q20-shape
  regression test (new test file
  `internal/planner/q20_unnest_test.go`).

## References

- M0069-0005 commits `ebb267d` (initial) +
  `5f120c1` (drop-IN + JoinTypeSemi follow-up that
  fixed Q18 but Q20 stayed at 0).
- `analysis/tpch-m0069-baseline-2026-05-08.md` —
  baseline showing Q20 30 s / 0 rows.
- `internal/planner/unnest.go::unnestInExpr` — the
  correlated-IN sibling whose patterns the
  non-correlated path mirrors.
- `internal/planner/unnest.go::unnestSubqueriesInPlan` —
  the recursive unnest driver that walks the cloned inner
  plan and may mutate its Output().
