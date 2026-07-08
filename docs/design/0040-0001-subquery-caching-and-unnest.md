# 0040-0001 — Subquery Result Caching and IN‑Subquery Unnest

**Status:** accepted
**Parent milestone:** M0040
**Date:** 2026-05-03

## 1. Objective

Eliminate the per‑outer‑row re‑execution of correlated subqueries in goopg v0
via two complementary mechanisms:

1. **Executor‑level caching** — Materialise correlated subquery results keyed
   on outer‑column values so each distinct outer value triggers at most one
   inner‑plan execution.

2. **Planner‑level unnest** — Extend the M0033 subquery‑unnest pass to
   recognise `IN (subquery)` expressions and rewrite them as hash semi‑joins,
   removing the nested‑loop evaluation entirely.

## 2. Background

### 2.1 Current behaviour (v0, pre‑M0040)

Every correlated subquery is executed **per outer row**:

```go
// executor/expr.go:641‑677 — collectInValues
func collectInValues(x *planner.InExpr, row Row, ctx *Context) ([]Datum, error) {
    ctx.OuterRows = append(ctx.OuterRows, row)
    defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
    op, err := Build(x.Plan)          // RE-BUILD per call
    op.Open(ctx)                       // RE-SCAN from block 0
    for { r, _ := op.Next(); drain }
    op.Close()
}
```

The same pattern holds for `evalSubquery` (lines 720‑755). For TPC‑H Q20
this means the `lineitem` SeqScan (4.4M rows) is re‑opened per partsupp row
(800K rows), yielding ~3.5 × 10¹² tuple probes.

### 2.2 Why the unnest pass (M0033) does not help

`findSubqueryInExpr` (unnest.go:48‑87) only visits `*planner.SubqueryExpr`:

```go
if s, ok := e.(*SubqueryExpr); ok { return s }
```

`InExpr` (`column IN (subquery)`) is simply never found.  The existing
`canUnnestSubquery` / `unnestSubquery` pipeline has all the infrastructure
needed (semi‑join creation, `clonePlanReplacingOuter` for correlated‑column
replacement), but the entry point does not reach `InExpr`.

## 3. Design: Subquery Result Caching (M0040‑0001)

### 3.1 Cache key

The cache is keyed by a hash of the **outer‑row column value(s)** that the
subquery depends on.  For a correlated `IN` with one outer reference, the key
is `datumKey(outerRefValue)`.  For a multi‑column correlation (e.g. Q20's
`l_partkey = ps_partkey AND l_suppkey = ps_suppkey`), the key is a
concatenation of both.

### 3.2 Cache storage

Add a cache to the subquery evaluation path.  The cleanest place is a
per‑query map attached to the plan's `SubqueryExpr` / `InExpr` node, or to
the `Context`.  Choosing the `Context` avoids modifying the planner types.

```go
// In Context (executor/operator.go or a new file)
type subqueryCacheEntry struct {
    values []Datum     // for IN subquery
    scalar Datum       // for scalar subquery
    err    error
}

type Context struct {
    ...
    // subqueryCache maps subquery-id + outer-key → result.
    // Cleared when OuterRows stack depth changes.
    subqueryCache map[subcacheKey]subqueryCacheEntry
}

type subcacheKey struct {
    planID uintptr  // identity of the SubqueryExpr / InExpr node
    key    string   // datumKey(outerColumnValue) or composite
}
```

### 3.3 Cache in `collectInValues`

```go
func collectInValues(x *planner.InExpr, row Row, ctx *Context) ([]Datum, error) {
    // 1. Build cache key from correlated outer refs
    //    (collect OuterColumnRefs from the inner WHERE)
    cacheKey := buildSubqueryCacheKey(x, row)
    if entry, ok := ctx.subqueryCache[cacheKey]; ok {
        return entry.values, entry.err
    }

    // 2. Execute inner plan (unchanged from current code)
    ctx.OuterRows = append(ctx.OuterRows, row)
    defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
    op, err := Build(x.Plan)
    if err != nil { ... }
    op.Open(ctx)
    for { r, _ := op.Next(); collect }

    // 3. Store result
    ctx.subqueryCache[cacheKey] = subqueryCacheEntry{values: out, err: nil}
    return out, nil
}
```

### 3.4 Cache in `evalSubquery`

Same pattern — the scalar result is cached per outer‑key.

### 3.5 Cache invalidation

When the `OuterRows` stack changes depth (entering / leaving a subquery
scope), all cache entries from the level‑1 scope are invalidated.  This
can be tracked by the `OuterRows` length:

```go
type subqueryCacheEntry struct {
    values    []Datum
    scalar    Datum
    err       error
    scopeLen  int   // OuterRows length when cached
}
```

When `len(ctx.OuterRows) != entry.scopeLen`, the entry is stale.

### 3.6 Collecting outer refs for the cache key

An `InExpr`'s inner plan may reference `OuterColumnRef` nodes in its
Filter predicates.  These are the correlated columns.  The cache key is
built from the actual values of those columns in the **current** outer
row:

```go
func buildSubqueryCacheKey(x *planner.InExpr, row Row) subcacheKey {
    // Collect OuterColumnRefs from the inner plan's WHERE clause
    // Each OuterColumnRef has an Index in the outer row
    // Build datumKey(row[ref.Index]) for each ref
    // Combine into a single string key
}
```

## 4. Design: IN‑Subquery Unnest (M0040‑0002)

### 4.1 Detection

Extend `findSubqueryInExpr` (unnest.go:48) to also recognise `InExpr`:

```go
func findSubqueryInExpr(e Expr, target *SubqueryExpr) *SubqueryExpr {
    // … existing BinaryOp/UnaryOp/… walk …
    case *InExpr:
        if x.Plan != nil {
            // Return a marker that we can process
            return nil // or extend the return type to handle InExpr
        }
}
```

Alternatively, add a new function `findInExprInExpr` that mirrors
`findSubqueryInExpr` but looks for `*planner.InExpr`.

### 4.2 Unnestability check

An `IN (subquery)` can be unnested when its inner plan is a simple
`SELECT col FROM table WHERE col = outer_ref`:

- The inner plan must be a `Project(SeqScan)` with a `Filter` that
  contains one or more equijoin pairs (`OuterColumnRef = ColumnRef`).
- All other filters in the inner plan must reference only the inner
  table (single‑side).
- No aggregates, no GROUP BY.

### 4.3 Rewrite

The rewrite creates a **semi‑join** (`JoinTypeSemi`):

```
Before: Filter(s_suppkey IN (subquery), Join(supplier, nation))
After:  JoinTypeSemi(
            Join(supplier, nation),
            Project(SeqScan(partsupp)) (with dedup on ps_suppkey),
            Predicate: s_suppkey = ps_suppkey
        )
```

The `JoinTypeSemi` operator already exists in `planner.JoinTypeSemi`.  The
executor's hash join handles it by building a deduplicated set of the inner
side and probing.

### 4.4 Implementation steps

1. **Extend `findSubqueryInExpr`** to return an interface (or use a
   separate function) that can hold either `*SubqueryExpr` or
   `*InExpr`.

2. **Add `canUnnestInExpr`** with a relaxed precondition (no aggregate
   required — plain `SELECT col FROM table` is fine).

3. **Add `unnestInExpr`** that:
   - Collects `unnestParam` pairs from the inner WHERE clause
   - Creates the semi‑join plan tree using
     `clonePlanReplacingOuter` (same as M0033)
   - Returns the rewritten node

4. **Wire into `unnestSubqueriesInPlan`**: after processing
   `SubqueryExpr`, also process `InExpr` in the same filter.

### 4.5 Complexity

| Component | Files touched | Est. lines |
|-----------|--------------|------------|
| `findSubqueryInExpr` extension | `unnest.go` | ~15 |
| `canUnnestInExpr` | `unnest.go` | ~30 |
| `unnestInExpr` (semi‑join construction) | `unnest.go` | ~60 |
| Wiring in `unnestSubqueriesInPlan` | `unnest.go` | ~10 |
| **Total** | | **~115** |

## 5. Verification

### 5.1 Unit tests

| Test | Verifies |
|------|----------|
| `TestCacheInSubquery` | Correlated `IN` subquery evaluated at most once per outer value |
| `TestCacheScalarSubquery` | Correlated scalar subquery cached |
| `TestCacheInvalidation` | Cache cleared when outer scope changes |
| `TestUnnestInExpr` | Simple `IN (SELECT … WHERE col = outer.col)` rewritten to semi‑join |
| `TestUnnestInExprRejectNoEquijoin` | Non‑equijoin `IN` subquery left uncached/unnested |

### 5.2 Integration tests

| Test | Expected |
|------|----------|
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS (no regression) |
| `TestTPCHResultParity` | identical ≥ 13, errored = 0 |
| HammerDB SF=1 power test | Q20 ≤ 120 s |

## 6. Reference

- `internal/executor/expr.go:601‑677` — `evalInExpr`, `collectInValues`
- `internal/executor/expr.go:720‑755` — `evalSubquery`
- `internal/planner/unnest.go:45‑87` — `findSubqueryInExpr`
- `internal/planner/unnest.go:91‑114` — `canUnnestSubquery`
- `internal/planner/unnest.go:307‑443` — `clonePlanReplacingOuter`
- `internal/executor/operators_join_agg.go` — hash join, semi‑join
- `internal/planner/plan.go:106‑115` — `InExpr`
- `analysis/tpch-q20-bottleneck-analysis.md` — Q20 complexity analysis

## 7. Follow-up (2026-07-07, M-NIGHTLY tpch/Q20-timeout): outer-IN unnest blocked by an over-eager correlation check

This section's §4's `unnestNonCorrelatedInExpr` (M0069-0005) landed and
correctly handles the STRUCTURAL side of unnesting a non-correlated
`IN` into a SemiJoin. Q20 still hit its 1200s query budget in the
2026-07-07 nightly run — not because the unnest rewrite was missing,
but because `IsNonCorrelated` (computed once at bind time via
`planHasOuterRef`, `internal/planner/planner.go`) was WRONG for Q20's
outermost `s_suppkey IN (SELECT ps_suppkey FROM partsupp WHERE ...)`.

`planHasOuterRef` descends into every nested `SubqueryExpr`/`InExpr`/
`ExistsExpr`'s own `.Plan` looking for `OuterColumnRef` nodes, but
treated ANY hit as "the tested plan escapes its own scope" — without
consulting `OuterColumnRef.Level` (a hop-count field: 1 = the
reference's own immediate parent scope, 2 = grandparent, ...; see
`resolveColumnRefAt`). Q20's partsupp subquery embeds a further-nested
scalar subquery (`ps_availqty > (SELECT 0.5*sum(l_quantity) FROM
lineitem WHERE l_partkey=ps_partkey AND l_suppkey=ps_suppkey ...)`)
whose `Level=1` `OuterColumnRef`s resolve entirely within partsupp's
OWN scope — not an escape past partsupp at all. The old code counted
this as "partsupp-subquery is correlated to whatever contains it",
so the OUTERMOST `s_suppkey IN (...)` was marked correlated and never
got the fast-path SemiJoin rewrite; it was left as a raw `InExpr` in
the outer Filter (visible in `EXPLAIN` as literally `Filter:
(<*planner.InExpr> AND (n_name = 'CANADA'))`), forcing the executor to
re-run the entire partsupp+lineitem-aggregate subtree once per
`supplier` row (10000 rows at SF1) instead of building it once as a
hash-join probe side.

Fix: `planHasOuterRef` now delegates to a depth-tracked
`planHasEscapingOuterRef(node, depth)` — `depth` starts at 1 and
increments by one for each subquery level recursed into (the same
convention `bushy.go`'s `remapOuterRefsInSubplan` already established
for the analogous problem of remapping `OuterColumnRef.Index` after a
`MultiHashJoin` rewrite). Only `Level >= depth` counts as an escaping
reference; a `Level=1` ref found one subquery level deep (`depth=2`)
correctly reads as "resolves within the tested plan's own scope",
matching Q9/Q21-style nested-correlation precedent elsewhere in the
planner.

A second, independent bug was found and fixed in the same
investigation: `splitEqualityForHash` (`planner.go`, used by
`planFromClause`'s explicit `JOIN ... ON` handling — a different code
path than this doc's bushy-DP-adjacent unnest machinery) only
recognised a bare single equality predicate. An AND-of-equalities
predicate (e.g. `partsupp JOIN (SELECT ... GROUP BY l_partkey,
l_suppkey) agg ON ps_partkey=agg.l_partkey AND ps_suppkey=
agg.l_suppkey`) fell through to the Nested-Loop default, which against
an expensive derived-table side with no usable index recomputes the
`GROUP BY` once per outer row. Fixed by iterating the predicate's
`splitAnd` conjuncts and hashing on the first one that decomposes into
disjoint sides, leaving the full `Predicate` untouched for the
executor's existing residual-recheck-per-hash-match mechanism
(`joinPredicateMatchSlot`) — the same mechanism TPC-H Q9's bushy DP
already relies on for a two-equality join between base relations.

**Verification:** `tmp/tpch-runner -queries 20` went from `ERROR after
1200.13s (57014)` to `OK elapsed=2.55s rows=92` on a fresh server
restart against `bench/tpch/runtime_goopg/data`; row count
cross-checked against a freshly-started real PostgreSQL 18.3 instance
on an independently-generated SF1 dataset (`bench/tpch/runtime/pgdata`)
— both return exactly 92 rows. `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=33); full `go test ./...` green.

**Regression tests:**
- `internal/planner/non_correlated_subquery_test.go`'s
  `TestPlanHasOuterRef_NestedSubqueryResolvesLocally` — the minimal,
  precise repro (fails without the `planHasOuterRef` fix, passes with
  it); its sibling `TestPlanHasOuterRef_NestedSubquery` was updated to
  use `Level=2` (a genuinely escaping reference) instead of the
  `Level=1` value it previously used to pin the old, over-eager
  behaviour.
- `internal/planner/multikey_hash_join_test.go`'s
  `TestSplitEqualityForHashMultiKey` — fails without the
  `splitEqualityForHash` fix.
- `internal/planner/q20_unnest_test.go`'s
  `TestPlanQ20OuterInFullyUnnested` — asserts no raw `*InExpr` survives
  anywhere in Q20's planned tree. Note: this one does NOT reproduce
  the bug against a minimal synthetic `catalog.NewInMemory()` Q20
  catalog (tried bare columns and added PK/composite-PK indexes; both
  show correct unnesting even with `planHasOuterRef` un-fixed) — some
  structural difference in the real HammerDB-generated bench schema
  changes which code path handles the outer IN, not yet isolated. It
  is kept as an end-state assertion guarding future regressions of
  this shape, not as the bug's original repro.

## Follow-up (2026-07-09, M0122-0011): `NOT IN` → NullAware anti-join

`isUnnestableNonCorrelatedIn` (`internal/planner/unnest.go`) previously
hard-rejected `in.Negated` — the doc comment said NOT IN "requires
anti-semi-join semantics which are out of scope for M0069-0005", so
`x NOT IN (non-correlated subquery)` always fell back to the slower
per-row runtime-cache execution path
(`unimplemented_feat.json` M0069-0005, `code_audit` reconfirmed open
as recently as 2026-07-08).

**The relax is not just dropping the guard.** The existing `Anti`
join (already used for correlated `NOT EXISTS`/`NOT IN` unnesting,
`unnestInExpr`'s `if in.Negated { joinType = JoinTypeAnti }`) is
built for NOT EXISTS semantics: "keep the probe row iff no hash
match is found", with a NULL probe key documented as "never matches
→ keep" (`internal/executor/operators_join_agg.go`'s `nextLazy`,
the M0061-0001 comment block). That is correct for NOT EXISTS
(existence doesn't care about NULL comparability) but wrong for NOT
IN's three-valued-NULL semantics:

- if the subquery produces **any** NULL in the compared column,
  `x NOT IN (subquery)` is NULL (excluded) for **every** outer row —
  even ones whose `x` matches no subquery value — because
  `x = NULL` is always NULL/unknown, and `IN` is `OR`-composed
  across every comparison;
- if the subquery is genuinely **empty**, `x NOT IN ()` is TRUE for
  every outer row, **including** a NULL `x` (the reverse case: an
  empty `OR` chain is FALSE, so `NOT (...)` is TRUE regardless of
  `x`'s nullness);
- otherwise (non-empty, NULL-free subquery), a NULL `x` itself makes
  `x NOT IN (subquery)` NULL (excluded) — the opposite of the plain
  Anti join's "NULL probe key never matches → keep" rule.

A naive relax (drop the `Negated` guard, always emit `JoinTypeAnti`)
would have reused the NOT-EXISTS-shaped rule for all three cases and
silently returned extra rows whenever the subquery or outer value
contained a NULL — exactly the "silent row-count regression" failure
mode `.ralph/PROMPT.md`'s hard-won rules warn about, and worse than
the pre-existing runtime-cache fallback it would have replaced.

**Fix:** new `Join.NullAware bool` field (`internal/planner/plan.go`),
set by `unnestNonCorrelatedInExpr` only when `in.Negated`. The
executor's `openLazyHashJoin` build loop (the right/build side is
always the subquery for `JoinTypeAnti`; `BuildLeft` is forced false
for Semi/Anti) tracks two scalars while `NullAware` is set —
`antiBuildRows` (total build-side rows seen) and `antiBuildHasNull`
(any build-side row's join key was NULL) — no per-row bookkeeping
needed since NOT IN's degenerate cases only depend on these two
aggregates, not on which values matched. `nextLazy` checks them
before the normal per-probe-row hash lookup:

- `antiBuildHasNull`: return `EOF` immediately (empty result, for
  every outer row);
- `antiBuildRows == 0`: pass every probe row through unconditionally
  via `lazyOuterOnlySlot` (no hash lookup, so a NULL `x` is emitted
  too);
- otherwise: normal Anti-join probing, except a NULL probe key
  (`ok == false` from `evalHashKey`) now `continue`s (excluded)
  instead of falling through to "keep".

**Deliberately out of scope — the CORRELATED `NOT IN` path
(`unnestInExpr`'s `if in.Negated { joinType = JoinTypeAnti }`,
reachable today for shapes like
`x NOT IN (SELECT y FROM t WHERE t.corr = outer.corr)`) was found to
have the same class of gap** (it does not set `NullAware`, so it
still uses the NOT-EXISTS-shaped rule) **but was not touched this
loop.** Fixing it correctly is materially larger: the "subquery is
empty" / "subquery has a NULL" facts have to be tracked **per
correlation-key group**, not once globally, since a correlated
subquery conceptually re-runs per outer row — the single pair of
build-side flags this fix uses is only sound for the non-correlated
case's single global build. See the deferral ledger for the resume
point.

**Tests:** `internal/planner/not_in_unnest_test.go`
(`TestUnnestNonCorrelatedNotIn` — NOT IN unnests to
`JoinTypeAnti`+`NullAware`; `TestUnnestNonCorrelatedIn_NotNullAware`
— plain IN stays `NullAware=false`); `internal/executor/
not_in_unnest_test.go` (`TestNotInUnnest_NormalCase`,
`_SubqueryNullPoisonsAllRows`, `_EmptySubqueryReturnsAllRows`,
`_NullProbeExcluded` — all four cross-checked against the three-
valued-NULL rules above). Confirmed non-vacuous via `git stash` on
the three implementation files: the planner test fails to compile
(`NullAware` doesn't exist) and the executor's
`_EmptySubqueryReturnsAllRows` case fails (drops the NULL-`x` row,
2 rows instead of 3) — incidentally proving the **pre-existing**
runtime-cache fallback path itself mishandled that one edge case
(NULL outer value + empty subquery), a latent bug this fix also
happens to close for the queries it now unnests.

**Verification against real data:** TPC-H Q16 (`ps_suppkey NOT IN
(SELECT s_suppkey FROM supplier WHERE s_comment LIKE
'%Customer%Complaints%')`) still returns the current dataset's
correct row count (18192, matching the load-dependent baseline
already on file in `ci/logs/action-items.md`'s non-blocking notices)
— though a live EXPLAIN showed Q16's specific join shape does not
actually route through the new unnest path (the `InExpr` still
appears as a residual filter conjunct there; not yet root-caused —
possibly join-order/NLI-conversion interaction specific to the real
schema's indexes, unreproduced by an equivalent synthetic two-table
probe). Not a regression either way: Q16 simply keeps using the
pre-existing, still-correct runtime-cache path. `scripts/
tpch-spotcheck.sh` (Q12=2/Q13=33) and `RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh` (0 failed, all 3 pgbench workloads)
both PASS.

## Follow-up (2026-07-09, M0122-0011 follow-up): non-ColumnRef LHS operand

`isUnnestableNonCorrelatedIn` (`internal/planner/unnest.go`) also
hard-required `in.Operand` to be a bare `*ColumnRef` — `f(x) IN
(subquery)` or `a + b IN (subquery)` always fell back to the slower
runtime-cache path, even when the inner plan was otherwise trivially
unnestable (`unimplemented_feat.json`'s M0069-0005 entry,
reconfirmed open as recently as 2026-07-08).

**Why the restriction wasn't load-bearing.** `Join.LeftKey`/`RightKey`
are already typed as general `Expr`, not `*ColumnRef` — the hash-join
executor's `evalHashKey` (`internal/executor/operators_join_agg.go`)
evaluates them with the ordinary `evalExpr`, the same evaluator used
for any predicate. `planInExpr` (`internal/planner/planner.go`)
already fully resolves `in.Operand` via `resolveExpr` in the outer
scope regardless of its shape — the ColumnRef requirement was never
protecting a real invariant, just an artifact of
`unnestNonCorrelatedInExpr` reconstructing a fresh `*ColumnRef` by
copying `Index`/`Name`/`Type`/`SourceTableIdx` off the operand instead
of using the operand expression directly.

**Fix:** `isUnnestableNonCorrelatedIn` now only checks `in.Operand !=
nil`, `in.Plan != nil`, and a single-column inner output.
`unnestNonCorrelatedInExpr` builds `outerKey := in.Operand` directly
instead of the field-copy reconstruction — `LeftKey`/`RightKey` accept
it as-is.

**Tests:** `internal/planner/not_in_unnest_test.go`'s new
`TestUnnestNonCorrelatedIn_NonColumnRefOperand` (`x + 1 IN (subquery)`
unnests to `JoinTypeSemi`/`JoinAlgoHash` with a `*BinaryOp` `LeftKey`,
not a `*ColumnRef`). `internal/planner/unnest_test.go`'s
`TestRecursiveUnnestInsideNonUnnestableIN` — which relied on a
non-ColumnRef operand (`a_id + 1`) to keep its outer IN deliberately
non-unnestable while pinning the unrelated M0040-0004 recursive-unnest
invariant — was updated to force non-unnestability via a non-equijoin
correlation (`b_val > a_id`) instead, since operand shape alone no
longer blocks unnesting. Confirmed non-vacuous via `git stash` on
`unnest.go` alone (new test fails: `InExpr survived unnesting`).
Gates: `go build ./...` clean; `go test ./internal/planner/...
./internal/executor/... ./internal/analyzer/...` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
failed transactions, all 3 pgbench workloads).
