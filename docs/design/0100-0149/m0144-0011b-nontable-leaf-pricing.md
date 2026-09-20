# M0144-0011b (recon) — Q8's join election, and the leaf-pricing defect underneath it

Status: RECON COMPLETE 2026-09-20 — impl filed as M0144-0011b-1, which has
since landed; the task row was ticked 2026-09-20 (it had carried a stale
`[ ]` after the recon finished).
Kind: recon
Parent: M0144-0011
Milestone: M0144 (plan-parity harness, measurement-first era)
Evidence: `analysis/m0144/m0144-0011b-q8-dppath-head.txt`,
`analysis/m0144/m0144-0011b-q8-plan-head.txt`

## 1. The filed premise was one layer too high — again

M0144-0011b was filed as "reprice the parameterised NL inner PG-faithfully",
on the reading that goopg prices the nested loop too dear at Q8's rel
`{0,1,2,3}`. Measured at HEAD `97eceacfc` on the private `:5595` lane, that
reading does not survive:

```
producer=join.hash     relids={0,1,2,3} total=19455.50 verdict=accepted  outer={0,1} inner={2,3}
producer=join.nestloop relids={0,1,2,3} total=19852.53 verdict=dominated outer={0,1} inner={2,3}
```

goopg's NL is 19852.53. **PG's own NL for the same join is 28502.37.** goopg
does not price the nested loop too dear — it prices it far too CHEAP, and its
hash join cheaper still. Repricing the NL upward, as the task asked, would have
been tuning toward a shape (R6) on a false premise.

## 2. The actual divergence, visible in the EXPLAIN output

goopg's Q8 inner side, verbatim:

```
->  Hash Join  (cost=1.27..11.28 rows=32 width=100)
      Hash Cond: (substr(ca_zip, 1, 2) = substr(store.s_zip, 1, 2))
      ->  HashSetOp Intersect  (cost=6457.53..7360.42 rows=535 width=32)
      ->  Seq Scan on store  (cost=0.00..1.12 rows=12 width=676)
```

**A Hash Join whose total cost is 11.28 sits directly above a child costing
7360.42** — the parent is 650x cheaper than its own input. PG's corresponding
inner costs 9327.62 and is wrapped in `Materialize`.

The DPPATH trace names the mechanism exactly. The set-op subquery enters the
join search as a prebuilt leaf:

```
producer=joinsearch.prebuilt relids={3} rows=535 total=8.35 width=32
```

**8.35**, for a subtree whose own estimate is 7360.42.

## 3. Root cause

`internal/optimizer/joinsearch.go:437`, the leaf seeding loop:

```go
p := newPrebuiltPath(rel, leaf)
scanPages, scanTuples, scanQualOps := baseSeqScanCostInputs(ri, leaf, rows, width)
p.Cost = costSeqscan(cp, scanPages, scanTuples, scanQualOps)
```

**Every** leaf entering the join search is priced as a sequential scan. For a
heap-table leaf that is right. For a leaf that is a finished SUB-PLAN — a
set-op, a subquery, a CTE, a VALUES or function scan, an already-built join
subtree — `baseSeqScanCostInputs` falls into its non-table branch:

```go
if _, ok := leafBaseScan(leaf).(*SeqScan); !ok || ri.table == nil || ri.baseRows < 1 {
        return estScanPages(fallbackRows, fallbackWidth), fallbackRows, 0
}
```

which INVENTS a page count from the row count and charges a seq scan over it.
The sub-plan's own cost is never added. The function's own doc comment already
records the gap without drawing the conclusion: *"a subquery or CTE leaf has no
`baserel->tuples` to speak of."*

### What PG does

PG never prices a subquery leaf as a scan of invented pages. `cost_subqueryscan`
(`postgres/src/backend/optimizer/path/costsize.c:1457`) starts from the
subpath's cost and adds overhead on top:

```c
/* postgres/src/backend/optimizer/path/costsize.c:1491-1493 */
path->path.disabled_nodes = path->subpath->disabled_nodes;
path->path.startup_cost = path->subpath->startup_cost;
path->path.total_cost = path->subpath->total_cost;
```

then adds the restriction-qual cost and `cpu_tuple_cost` per row. A CTE leaf
goes through `cost_ctescan`, a function scan through `cost_functionscan`, each
with the same shape: the child's work is paid for.

## 4. Why this is the layer that matters for Q8

The outer side of Q8's top join is priced almost identically by both engines
(goopg `Gather` 19430.30 vs PG `Gather` 19021.90). The inner side is where they
part: goopg 11.28, PG 9327.62. With ~7350 of real work missing from the inner,
every join method above it is adjudicated on a fiction, and the 397-cost margin
that separates goopg's hash join from its nested loop is an order of magnitude
smaller than the missing input. The NL-vs-HJ election cannot be diagnosed —
let alone fixed — until the inner is priced.

`Materialize` (M0144-0011c) is a real and separate gap, but it is NOT what
decides this election: PG's Materialize wrapper adds ~65 over its 9327.62
child. The missing ~7350 is the leaf.

## 5. Blast radius

32 of 99 TPC-DS SF0.25 queries have a non-table leaf feeding a join:

```
Q1 Q2 Q4 Q5 Q8 Q11 Q14 Q23 Q24 Q30 Q31 Q33 Q38 Q39 Q47 Q51 Q54 Q56 Q57 Q58
Q59 Q60 Q64 Q74 Q75 Q77 Q78 Q80 Q83 Q87 Q95 Q97
```

Every one of them plans its joins over at least one leaf whose subtree is free.

## 6. Why the fix is filed, not landed in this loop

The port itself is surgical — one call site plus its partial twin. The design
question is not.

goopg's sub-plan nodes mostly do NOT carry a real `Path` cost. `SetOp` has no
`PlanCost` field, so its 7360.42 comes from `DeriveLegacyDisplayCost`
(`internal/optimizer/plancost.go:116`), whose own header states the prohibition
in terms this task cannot simply ignore:

> "this is NOT a cost model and nothing may plan against it."

So the honest options are not interchangeable:

1. **Use the leaf's carried `PlanCost` where it has one** (a searched subtree
   does), and only fall back to the legacy estimate otherwise. Correct in the
   cases it covers; leaves set-ops and CTEs — Q8's case — still on the legacy
   number.
2. **Give the sub-plan classes real upper-rel paths** (`cost_subqueryscan` /
   `cost_ctescan` ports with genuine inputs). This is the PG-faithful answer
   and is the larger piece of work.
3. **Plan against the legacy display estimate** for these leaves. Fastest,
   and it directly contradicts a stated in-repo scope rule.

Choosing between them is a design decision with a 32-query blast radius, and
making it silently inside the loop that discovered the defect would be the same
mistake this lineage has already had to correct twice. It is filed as
**M0144-0011b-1** with the options stated and the measurement named.

## 7. Status of M0144-0011b itself

Stays `[ ]`, BLOCKED on M0144-0011b-1, with its premise corrected in place: the
NL is not under-priced relative to PG, its inner input is. It completes when
the leaf is priced and Q8's rel `{0,1,2,3}` election is re-measured.

`Movement: none` — this loop changed no production code; it is a recon and the
parity instruments were not re-run against a change.
