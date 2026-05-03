# 0041-0003 — Q7 and Q9 Final Parity Fixes

**Status:** landed
**Parent milestone:** M0041
**Date:** 2026‑05‑04

## 1. Objective

Close the last two non‑precision divergent TPC‑H queries so
`TestTPCHResultParity` is green: identical ≥ 19, divergent = 3 (the
Q1/Q8/Q14 numeric‑precision allowlist), errored = 0.

## 2. Starting state (after 0041‑0002 batch 2)

```
identical=17 divergent=5 goopg-errored=0
```

Failures of the parity gate:

- Q7: goopg returns 3 rows, upstream returns 1.
- Q9: goopg row=3 col=2 = `5570.0000`, upstream `5795.0000`. Row count
  matches (6/6).

(Q1/Q8/Q14 are allowlisted numeric‑precision deltas — out of scope.)

## 3. Q7 — `nation n1, nation n2` self‑join

Q7 query (abbreviated):

```sql
SELECT supp_nation, cust_nation, l_year, sum(volume) AS revenue
FROM (
  SELECT n1.n_name AS supp_nation, n2.n_name AS cust_nation,
         extract(year FROM l_shipdate) AS l_year,
         l_extendedprice * (1 - l_discount) AS volume
  FROM supplier, lineitem, orders, customer, nation n1, nation n2
  WHERE s_suppkey = l_suppkey
    AND o_orderkey = l_orderkey
    AND c_custkey = o_custkey
    AND s_nationkey = n1.n_nationkey
    AND c_nationkey = n2.n_nationkey
    AND ((n1.n_name = 'FRANCE' AND n2.n_name = 'GERMANY')
      OR (n1.n_name = 'GERMANY' AND n2.n_name = 'FRANCE'))
    AND l_shipdate BETWEEN date '1995-01-01' AND date '1996-12-31'
) shipping
GROUP BY supp_nation, cust_nation, l_year
ORDER BY supp_nation, cust_nation, l_year;
```

### 3.1 Symptom in the dump

The inner inline‑view's `Project` after rewrites:

```
Project targets=[Col[1:n_name], Col[1:n_name], extract(year, Col[32:l_shipdate]),
                 (Col[35:l_extendedprice] * (1 - Col[34:l_discount]))]
```

Both `supp_nation` (n1.n_name) and `cust_nation` (n2.n_name) ended
up at index `1`. They should land at the offsets of n1's
`n_name` (4+1 = 5 in the OID‑sorted MHJ) and n2's `n_name`
(0+1 = 1 in the OID‑sorted MHJ) respectively, given the MHJ table
order `[n2, n1, supplier, customer, orders, lineitem]`.

So both refs collapsed onto **n2.n_name** (col 1). In the WHERE
clause, the analogous collapse would mean a row where
`n2.n_name = 'FRANCE'` and `n2.n_name = 'GERMANY'` was matched
against the same single column twice — producing extra rows that
satisfy a degenerate condition.

### 3.2 Root cause

`buildBindingsPosMap` in `internal/planner/bushy.go` keys scans by
`scanKey{table*, alias}` — that part already disambiguates the two
nation scans. But the **bindings themselves** carry the alias on
each `rangeBinding`, and the lookup in the posMap closure searches
bindings sequentially:

```go
for i := range bindings {
    b := &bindings[i]
    ...
    if oldIdx >= b.offset && oldIdx < b.offset+w {
        k := scanKey{table: b.table, alias: b.alias}
        if scanOff, ok := scanMap[k]; ok {
            return scanOff + (oldIdx - b.offset)
        }
        return oldIdx
    }
}
```

This is correct for an `oldIdx` that uniquely lands in one binding's
offset range. The problem is that the **inline‑view's Project
targets** are resolved against the inline‑view's own context, where
n1 and n2 each have their own binding with distinct offsets. When
`remapTopProjection` (added in batch 2) feeds those FROM‑order
indices through `buildBindingsPosMap(out, savedBindings)`, the
posMap maps the n1.n_name FROM offset to its own scan offset and
the n2.n_name FROM offset to its own scan offset. That should
work.

But the dump shows both collapsing to `Col[1]`. The likely cause is
that `remapTopProjection` walks the Project's ColumnRefs through
`remapByPosMap`, and **`remapByPosMap` walks `*ColumnRef` and then
also recurses through `BinaryOp`/`FuncCall`/etc.** — but it only
fires once per ColumnRef. So that's not the issue.

The real cause is in `applyJoinTreePosMap`'s walk of the **inner
Project** (which lives below the Aggregate). The recursion path is:

```
Aggregate
  Project (inline‑view)        ← targets here
    Filter
      MHJ
```

`applyJoinTreePosMap` stops at Aggregate. So the inner Project is
**never reached** by the join‑tree posMap walk.

Then `remapTopProjection` walks only the OUTER wrappers (Project,
Sort) above the Aggregate — it stops at Aggregate too. The inline‑
view's inner Project is between Aggregate and Filter, never
visited.

The inner Project's targets stay in raw FROM‑order — and the
collapse to `Col[1]` actually came from the *first* batch's
(`remapPosMapAfterRewrite`) Project arm, which uses
`mhjPosMapOf(child)` (`buildMHJPosMap`). For the inline‑view's
Project, the posMap is the OID‑sorted MHJ map. That map confuses
two `n_name` columns because the MHJ output has BOTH (`n2.n_name`
at col 1, `n1.n_name` at col 5) and `buildMHJPosMap` doesn't
distinguish them — it sorts by OID, but two entries have the same
OID (both nation table). The first one wins, so any FROM offset
landing in either nation's range maps to the **first** nation
table's MHJ offset (n2 at col 0–3) plus the column‑within‑table
offset.

### 3.3 Fix

`buildMHJPosMap` must key by `(OID, alias)` — i.e. `scanKey` — and
the inline‑view's Project must be reached by a remap pass that
uses the **bindings posMap** (which already keys by `scanKey`),
not by `buildMHJPosMap`.

Two‑part fix:

1. **`buildMHJPosMap` alias‑aware**: switch from sorting by OID to
   sorting by `scanKey{OID, alias}`, and look up by `scanKey` instead
   of OID alone.
2. **Inline‑view Project reach**: extend `remapPosMapAfterRewrite`
   so that when an `Aggregate` sits above the join tree, the walker
   continues past the Aggregate into the inner Project / Filter
   that feed it (the inline‑view's body) — but only when those
   nodes' refs are in scan‑coord space (i.e. they describe the
   inline‑view's column expressions, not aggregate output).

   Detection: the Aggregate's `GroupExprs` and `Aggs[i].Arg` are
   ColumnRefs into the inner Project's output (e.g. `Col[2:amount]`
   in Q9). The inner Project is between Aggregate and Filter‑or‑
   Join; we just walk through it once after fixing the Aggregate's
   own refs. So in `remapPosMapAfterRewrite` on `*Aggregate`, after
   the existing remap, also recurse into `n.Child` as if it were a
   fresh subtree (it is — the inline‑view is its own scope).

(2) actually duplicates work the recursive `remapPosMapAfterRewrite`
already does on `n.Child`. The reason it didn't help is that the
posMap derived from `mhjPosMapOf` is OID‑based and collides on
self‑joins. Fixing (1) removes the collision, and the existing walk
will then correctly remap the inner Project.

So the **single fix** is (1): make `buildMHJPosMap` key by
`scanKey{table, alias}`. The MHJ.Tables are `Node`s with `*SeqScan`
children; SeqScan already carries `Alias`.

## 4. Q9 — residual `ps_partkey = l_partkey` not pushed

### 4.1 Symptom

Q9 returns 6 rows (correct count) but row 3 column 2 is
`5570.0000` vs upstream `5795.0000`. The aggregate is

```
sum(l_extendedprice * (1 - l_discount) - ps_supplycost * l_quantity)
GROUP BY n_name, extract(year FROM o_orderdate)
```

A single‑row sum mismatch with correct row count is consistent with
extra (or missing) `(lineitem, partsupp)` pairs in one group.

### 4.2 Root cause

`enumerateBushyPlans` correctly emits `ps_partkey = l_partkey` as
a residual (verified via the debug print in batch 2). The Filter
above the bushy tree thus has:

```
Filter (ps_partkey = l_partkey AND p_name LIKE '%green%')
```

with the equality's ColumnRef indices in **global FROM‑order** (the
binding offsets at the time `tryBushyDP` consumed them):

- ps_partkey: 32 (partsupp@32 + col 0)
- l_partkey:  23 (lineitem@16 + col 7)

`pushPredicatesIntoCrossJoins` then walks the bushy tree and asks
`pushOneConjunct` to push the equality. Inside `pushOneConjunct`,
`classifyConjunctSide(c, leftWidth, totalWidth)` uses **width‑based
classification** — does `ColumnRef.Index` fall in `[0, leftWidth)`
(left side) or `[leftWidth, totalWidth)` (right side)?

But `leftWidth` of the inner Join above MHJ is 36 (the MHJ's 4 tables
in **subset‑FROM‑order**: supplier 0–6, lineitem 7–22, orders
23–31, nation 32–35). The conjunct's index 32 (global, ps_partkey)
falls in `[0, 36)` → classified as left. Index 23 (global,
l_partkey) also in `[0, 36)` → classified as left. Both on left
→ `sideLeft` → not pushable here.

Recursing into the left subtree (the MHJ‑pre‑rewrite binary chain)
hits ever‑smaller `leftWidth`s; the conjunct continues to look
"left only". `pushOneConjunct` returns false at every level. The
conjunct stays in the Filter.

When the Filter is later remapped (`applyJoinTreePosMap` →
`remapByPosMap`), the bindings posMap maps ps_partkey FROM=32 to
its actual MHJ output offset (38) and l_partkey FROM=23 to 27. The
Filter's predicate ends up correct.

So **what's actually happening at runtime**:

The Filter does evaluate `ps_partkey = l_partkey` after the join.
But the inner Join (MHJ ⋈ partsupp on `l_suppkey = ps_suppkey`)
produces a Cartesian over partsupps that share the same suppkey —
so for each `(supplier, lineitem)` pair, multiple partsupp rows
match (one per partkey carried by that supplier). The Filter
removes those whose `ps_partkey ≠ l_partkey`. That's
**semantically correct** but wasteful.

…So why does the result differ from upstream?

The clue is the OUTER Join above (joining the inner Join's output
with `part`). The outer Join's `LeftKey` is `Col[27:l_partkey]`,
keyed for hashing against `Col[41:p_partkey]`. The hash join takes
ONE matching row per probe key. If the inner Join produced multiple
`(supplier, lineitem, partsupp)` tuples with the SAME `l_partkey`
value (because the partsupp join only constrained `ps_suppkey`),
the hash on `l_partkey` collapses them after the upper Filter
fires — but the partsupp values that survive are those whose
`ps_partkey == l_partkey`, which is *exactly one* per
`(supplier, lineitem)` pair.

The actual divergence then must be **the ordering of which
ps_supplycost is picked**. The upstream order: PostgreSQL's hash
join doesn't pre‑filter; it includes the AND in the join predicate
or lets the Filter fire after. Either way, only matching rows
survive.

Hmm. So where does 5570 vs 5795 come from? Likely the issue is
that goopg's Filter is evaluated **AFTER** the outer Join (which
joins on l_partkey=p_partkey). The outer Join uses the hash on
l_partkey of an UNFILTERED `(s, l, ps)` tuple stream. If for one
`l_partkey`, there are multiple (s, l, ps) tuples sharing that
`l_partkey` (e.g. partsupp has the right partkey but multiple
ps_suppkey), **the hash join drops to one**, and which one survives
is non‑deterministic. Then the post‑Filter `ps_partkey =
l_partkey` may filter it out, leaving the group empty.

Wait — that doesn't explain a *different* sum of 225 lower than
upstream. Let me think again.

Actually, the more likely cause: lineitem×partsupp on suppkey alone
yields **N×M tuples** instead of **N tuples** (where M is the
number of partkeys per supplier). The Filter then keeps the
correct ones. But the per‑group sum is computed across ALL kept
tuples — if the kept set is correct, the sum should match.

The 225 diff tracks down to one specific (l_partkey, l_suppkey)
combination. Without the constraint moved into the join, goopg
might have a different multiplicity at the outer join. Specifically,
the outer Join hash‑joins `(s, l, ps_filtered)` with `part`. The
hash table for `part` is built once and probed by each
left‑side row. The left‑side stream after the inner Filter has
duplicate `l_partkey` values only if there are multiple
distinct `(s, ps)` pairs per `l_partkey` — which there aren't,
once the residual filter is enforced.

The 225 discrepancy is empirical, not theoretical. Let me hypothesise
once more: **the residual conjunct stays in the Filter, but the
Filter's remap subsequently changes its indices INCORRECTLY**,
turning the equality into a different one (e.g. `Col[X]=Col[Y]`
where X and Y are not partkeys). That would let extra rows through.

### 4.3 Fix

Avoid the global/subset coord mismatch entirely by pushing the
residual onto the most‑specific Inner Join AS PART OF its Predicate
**at the point of bushy DP construction** — not later via
`pushPredicatesIntoCrossJoins`. After bushy DP returns
`(plan, residual)`, walk `residual` and for each conjunct, find the
deepest Join in `plan` whose subtree contains both columns'
tables, and AND the conjunct onto that Join's `Predicate`.

The classification can be by **column NAME via SeqScan output
schemas**, not by width:

- `pushResidualByName(plan Node, c Expr, bindings []rangeBinding)`:
  walk `plan`. At each Join, determine which tables (by
  `(table*, alias)` `scanKey`) appear in Left vs Right. For the
  conjunct, its two ColumnRefs reference specific `(table, alias)`
  pairs (recoverable via `bindings` + the conjunct's `Index`).
  If the two refs are on opposite sides of this Join → AND onto
  its Predicate, return true. Otherwise recurse.

This is independent of width; it works regardless of subset
ordering or post‑MHJ rewrites.

Apply right after `tryBushyDP` returns:

```go
if newPred != nil {
    for _, c := range splitAnd(newPred) {
        if !pushResidualByName(newChild, c, ctx.bindings) {
            kept = append(kept, c)
        }
    }
    f.Predicate = combineAnd(kept)   // may be nil
}
```

If all residuals are pushed, the Filter is dropped (already done
by the existing `len(remaining)==0` check via combineAnd nil).

For Q9, `ps_partkey = l_partkey` would land on the inner Join
(`MHJ ⋈ partsupp`), turning its predicate from `(l_suppkey =
ps_suppkey)` into `((l_suppkey = ps_suppkey) AND (ps_partkey =
l_partkey))`. The hash join still uses `LeftKey/RightKey` for
hashing (so suppkey buckets) but now the joined rows are filtered
by partkey before being emitted — eliminating the cartesian
explosion that caused row 3 to drift.

The eventual `reresolveJoinByName` already updates per‑join
predicates by name, so the indices land in the correct
post‑rewrite coordinates.

## 5. Implementation order

1. **Q7 fix**: `buildMHJPosMap` keyed by `scanKey`. Verify Q7
   parity.
2. **Q9 fix**: `pushResidualByName` invoked after `tryBushyDP`.
   Verify Q9 parity.
3. Re‑run full `TestTPCHResultParity`. Expect identical=19,
   divergent=3 (Q1/Q8/Q14), errored=0.
4. Run `go test ./...` to confirm no regressions.

## 6. Verification

| Test | Expected | Actual |
|------|----------|--------|
| `TestTPCHResultParity` | identical ≥ 19, divergent = 3 (Q1+Q8+Q14 precision), errored = 0 | **identical=19, divergent=3, errored=0 — PASS** |
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS | **22/22 PASS** |
| `go test ./...` | no new failures (pre‑existing `tmp/` build error excluded) | **green** |

## 7. What actually landed

The fix turned out to span four distinct issues; (1)‑(2) match the
plan in §3‑§4, (3)‑(4) are additional bugs surfaced once the planner
produced the right shape:

1. **`mhjPosMapOf` returning nil** (`internal/planner/bushy.go`): the
   OID‑sorted MHJ posMap was broken in two ways — it assumed
   FROM‑order == OID‑order (false for most TPC‑H FROM lists) and
   collapsed duplicate OIDs (Q7's `nation n1, nation n2`). Disabling
   it makes the bindings posMap (which already keys by `(table,
   alias)`) the sole authority, fixing the Q7 collapse without
   regressing OID‑sorted cases (the bindings posMap remaps
   correctly).

2. **MHJ.Filters capture in `collectMultiHashTables`** (`bushy.go`):
   `pushOneConjunct` ANDs residual conjuncts onto Inner‑Hash joins
   that the chain detector later absorbs into an MHJ — the MHJ
   only used `j.LeftKey/RightKey`, silently dropping the extras.
   Capturing them into `mh.Filters` (gated by `extraInScans` so
   only conjuncts whose ColumnRef Names live in the MHJ subset are
   captured) routes them to the MHJ executor, which evaluates
   them after each emitted joined row. `applyJoinTreePosMap`'s
   MHJ arm now also remaps `mh.Filters` via the bindings posMap
   so the global FROM‑order ColumnRefs land at MHJ‑output offsets.

3. **MHJ executor multi‑row hash** (`internal/executor/multi_hash_join.go`):
   the hash table was `map[string]Row` (single‑value), losing
   multi‑match semantics whenever a build table had multiple rows
   per key (Q9 partsupp: two ps_partkey values per ps_suppkey).
   Switched to `map[string][]Row` and replaced the chain‑lookup
   loop with `expandChain`, a depth‑first Cartesian expansion that
   materialises all output rows up front (good fit for the
   synthetic dataset; future spill work can revisit). Filters are
   evaluated per leaf so wrong combinations are dropped before
   emission.

4. **`pushOneConjunct` scope guard + lazy hash join Predicate
   filter** (`internal/planner/pushdown.go`,
   `internal/executor/operators_join_agg.go`): width‑based side
   classification produced sideMixed for conjuncts whose ColumnRef
   indices coincidentally fell in a Join's width range while the
   referenced tables were outside that Join's subtree (Q9
   `ps_partkey=l_partkey` mis‑pushed onto a 4‑table inner Join).
   `allColumnRefNamesInScope` validates by Name against the
   subtree's scan outputs before allowing the push. Once the
   conjunct lands on the right Join's Predicate, `nextLazy` (lazy
   hash join) now applies `joinPredicateMatch` per emitted row,
   so the extra ANDed conjunct actually filters at runtime — the
   pre‑existing path skipped Predicate evaluation entirely on the
   assumption that LeftKey/RightKey hashing was the only
   constraint.

5. **`predRebind` two‑sided lookup** (`bushy.go`): when a Join's
   Predicate has been ANDed with a residual whose ColumnRef Index
   was already remapped in an earlier pass, the original‑Index
   side classification can be wrong. `predRebind` now tries the
   suggested side first, then falls back to the opposite side,
   so the Name‑based rebind succeeds when the residual lives in a
   different schema slot than the original Index suggested.
