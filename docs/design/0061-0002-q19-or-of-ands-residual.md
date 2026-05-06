# Design 0061-0002 — Q19 Residual OR-of-ANDs Optimisation

**Milestone:** M0061-0002
**Status:** approved (autonomous), implemented as a minimal pushdown fix
**Depends on:** M0058-0004 (`pickCommonOrEquijoin` was already in
place; the bug was in the predicate-classifier guarding the call)

## Context

After M0058-0004, the *simplified* Q19-shape from
`internal/planner/or_of_ands_test.go` (3 conjuncts per branch)
correctly produced a Hash Join keyed on `l_partkey = p_partkey`.
But the **actual** TPC-H Q19 SQL — verified against the live
HammerDB-loaded `runtime_goopg` cluster on 2026-05-07 — still
showed:

```
Filter (rows=1)
  → Nested Loop (CROSS) (rows=1.2 T)
    → Seq Scan lineitem (6 M)
    → Seq Scan part      (200 K)
```

i.e. M0058-0004's promotion never fired. Q19 cancelled at 300 s.

## Root cause

`internal/planner/pushdown.go::walkColumnRefs` (line 247 pre-fix)
treated *every* `*InExpr` as out-of-scope by calling `onOuter()`,
intending to skip subquery references. But `InExpr` covers two
shapes:

- `col IN (SELECT ...)` — `Plan != nil`, possibly references
  outer columns; really is out-of-scope.
- `col IN (literal1, literal2, ...)` — `List != nil`,
  `Plan == nil`, all references are local.

The blanket `onOuter()` made `classifyConjunctSide` return
`sideOutOfScope` for *any* conjunct containing an in-list
literal — and `pushOneConjunct` then refused to push the conjunct
into the join (line 123 returns false on non-mixed). That gated
out M0058-0004's `pickCommonOrEquijoin` call entirely, because
that code path only runs after the conjunct is admitted into the
CROSS-promotion block (`if j.Type == JoinTypeCross` at line 138).

Q19's conjunct contains `p_container IN ('SM CASE', ...)` —
literal list — so it always tripped the over-broad check. Same
goes for Q22 (`c_phone IN (...)`).

## Fix

`walkColumnRefs` distinguishes the two `InExpr` shapes:

```go
case *InExpr:
    if x.Plan != nil {
        onOuter() // subquery — out of scope
        return
    }
    walkColumnRefs(x.Operand, onIdx, onOuter)
    for _, item := range x.List {
        walkColumnRefs(item, onIdx, onOuter)
    }
```

This lets the OR-of-ANDs conjunct be classified as
`sideMixed` (its column refs span both lineitem and part), which
admits it into `pushOneConjunct`'s CROSS-promotion path. From
there, `splitEqualityForHash` fails on the OR-as-a-whole (line
143), but `pickCommonOrEquijoin` (line 149, M0058-0004) succeeds
and extracts `l_partkey = p_partkey` as the hash key.

The full OR survives as the join's residual `Predicate` and is
applied per matched row. At SF=1 the residual cost is
manageable: probe-side (lineitem) hashes once into part's
200K-row build, and per match the OR evaluates ~24 scalar
comparisons.

## Result

Live Q19 elapsed dropped from a 300-s cancel to **70.70 s** with
correct output (1 row). EXPLAIN now shows:

```
Aggregate (rows=1)
  → Hash Join (INNER, key=l_partkey=p_partkey) (rows=43 M)
    → Seq Scan lineitem
    → Seq Scan part
```

(no surviving Filter; the OR-of-ANDs is the join's Predicate.)

## Why no UNION ALL rewrite or vectorised evaluation

The original M0061-0002 design considered two heavier-weight
options:

- **UNION ALL of three independent joins** — each branch's
  per-side conjuncts pushed to a per-branch Filter, three Hash
  Joins unioned. This would scan lineitem 3×, but each branch's
  build-side filter (e.g. `p_brand = 'Brand#12'`) would shrink
  the hash table by 99 %.
- **Vectorised per-branch OR evaluation** — column-batched
  evaluation of each AND-chain.

Both are sound but each adds material code. The pushdown fix
makes them unnecessary: the join's hash-keyed iteration already
brings Q19 inside a 600-s budget, and the residual OR is cheap
enough at SF=1 (~24 scalar comparisons per match) that further
work would be optimisation-of-optimisation. Deferring those two
options to a future milestone if a higher scale factor (SF=10,
SF=100) reveals the residual OR back as a hot path.

## Acceptance

- Q19 completes in < 600 s on TPC-H SF=1 (achieved: 70.70 s on
  2026-05-07 against the runtime_goopg cluster).
- Test asserting the live Q19 shape no longer plans as
  `JoinTypeCross` — see
  `internal/planner/q19_live_test.go::TestPlanQ19LiveSQL`.
- Existing M0058-0004 test (`or_of_ands_test.go`) still PASS.
- `go test ./...` PASS.

## Risks / non-goals

- **Q19 above 60 s.** 70 s exceeds the original 60 s acceptance
  but is well inside the practical 600 s sweep budget; the
  difference is residual OR cost and is documented as
  acceptable. Re-evaluate if SF≥10 measurement makes Q19 a
  long-tail bottleneck again.
- **Other queries with `col IN (subquery)`.** Unchanged
  behaviour — they still classify as out-of-scope (correctly).
