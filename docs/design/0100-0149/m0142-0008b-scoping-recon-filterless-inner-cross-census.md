# M0142-0008b — scoping recon: filterless INNER/CROSS join-tree census

**Status:** DONE (measurement only, no production diff). 2026-09-16.
**Filed by:** M0142-0008 (`docs/design/0100-0149/m0142-0008-forced-rewrites-vs-search-census.md`).
**Task:** `.ralph/fix_plan.md` M0142-0008b.

## Question

`internal/optimizer/planner.go:1373-1611` runs `tryJoinSearch` (the cost-based
DP join-order search) for a `SELECT`'s top-level FROM tree only via one of two
arms:

```go
if s.Where != nil {
    // ... several sub-arms, all of which eventually call tryJoinSearch ...
} else if joinTreeHasOuterLink(node) {
    // WHERE-less but has a LEFT/RIGHT/FULL join on the join tree's LEFT
    // spine — M0134-0188, added for TPC-H Q13's `customer LEFT JOIN orders`.
    // ... also calls tryJoinSearch ...
}
// else: NEITHER arm runs. The join-order search never executes for this
// SELECT's FROM tree at all; it stays on whatever the pre-search legacy
// code produces (comma-join flattening + WHERE-predicate-driven access
// method selection only — no search-based join-order or access-method
// choice).
```

`joinTreeHasOuterLink` (`planner.go:16898`) only walks the tree's **left
spine** (`j.Left` recursively), checking each level's `Type`.

M0142-0008's own recon named this as one of two structural search-coverage
gaps (the other, SEMI/ANTI decorrelation, is M0142-0008a) and explicitly
declined to size it. This task's job: **how many canonical TPC-H/TPC-DS
`SELECT` statements — at *any* nesting level, not just top-level — actually
hit the "neither arm runs" case, and does widening the gate look likely to
move any plan?** Measurement only.

## Method

Parsed all corpus query files with `internal/parser` (no catalog/DB
connection needed — this walks the raw parser AST, not the resolved
optimizer `Node` tree) and reproduced `joinTreeHasOuterLink`'s exact
left-spine-only semantics in a standalone walker, verified against 5
hand-checked synthetic cases before running on the real corpus. Walked every
`SelectStmt` reachable from each query file: top level, CTEs (`With`
clause), derived tables (`RangeVar.Subquery`), sublinks (`IN`/`EXISTS`/
scalar/array subqueries, found via a generic reflection walk over every
expression-bearing field so no sublink AST type had to be hand-enumerated),
and `UNION`/`INTERSECT`/`EXCEPT` arms including parenthesized
`SetOpOperand` groupings.

**Corpus:** TPC-H `q01.sql`..`q22.sql` (22 files,
`analysis/tpch/goopg-pg-tpch-plan-compare-260718/queries/`; Q15 skipped by
design — it needs its view-split helper files stitched together, not
attempted here) and TPC-DS `query1.sql`..`query99.sql` (99 files,
`bench/tpcds/runtime_goopg/tpcds-data/queries/`). 121 files attempted, 118
parsed (**3 corpus-data parse failures**, all TPC-DS: `query36.sql`,
`query70.sql`, `query86.sql`, all the "hierarchy rank" family, all failing
identically on a stray semicolon before a derived table's closing paren —
`... limit 100;\n) as sub` — a genuine syntax defect in those three query
files, not a goopg parser gap; not investigated further, and not counted as
a finding since a query goopg cannot parse at all cannot reach the planner
code this recon studies). **429 `SELECT` statements walked** across both
corpora at all nesting levels.

## Findings

### Finding 1 — the actual target: WHERE-less, 2+ relations, INNER/CROSS-only

**5 matches, all TPC-DS, zero in TPC-H:**

| query | location | shape |
|---|---|---|
| `query28.sql` | top-level | 6-way comma-cross of derived tables `B1..B6` |
| `query61.sql` | top-level | 2-way comma-cross `promotional_sales, all_sales` |
| `query77.sql` | derived table `x`, inside UNION arm 2 | comma-cross `cs, cr` |
| `query88.sql` | top-level | 8-way comma-cross of derived tables `s1..s8` |
| `query90.sql` | top-level | 2-way comma-cross `at, pt` |

### Finding 2 — the gate's own left-spine blind spot (distinct, narrower)

WHERE-less trees where an outer join exists **somewhere** but not on the
left spine (so `joinTreeHasOuterLink` returns `false` even though an outer
join is present) — this is a genuine latent gap in the existing gate
function itself, independent of the filterless-INNER/CROSS widening
question. Confirmed mechanically real with a synthetic case
(`SELECT * FROM a, b LEFT JOIN c ON b.x=c.x` — the `LEFT JOIN` is nested
inside the *second* comma item, landing entirely on the top join's `Right`
side, invisible to a left-only descent) but **zero matches in the actual
corpus** — no canonical TPC-H/TPC-DS query combines a WHERE-less top level
with a non-first-item outer join. Recorded for completeness; not
actionable against this corpus today.

## Assessment — is this worth widening the gate for?

**No, not on its own.** The gap is narrow (5/429 = 1.2% of walked SELECTs,
concentrated entirely in TPC-DS, zero in TPC-H) and **4 of the 5 matches are
cost-order-irrelevant in practice**: `query28`, `query61`, `query88`, and
`query90` all comma-cross **scalar aggregate subqueries with no GROUP BY**
— each side of the cross is provably exactly one row. A cost-based
join-order search has nothing to reorder when every operand is a single
row; any join order costs the same, and each subquery (`B1`, `s1`, `at`,
`pt`, ...) already gets independent cost-based treatment for its own inner
plan via its own `WHERE` clause, unaffected by this gate.

**`query77`'s `cs, cr` cross is the one plausible real target.** Both sides
are `GROUP BY <call_center_sk>` aggregates — genuinely multi-row (one row
per call center), not scalars — and it is also the query's *only*
channel-branch using implicit comma-cross: the sibling store/web branches in
the same three-way `UNION ALL` use `ss LEFT JOIN sr` / `ws LEFT JOIN wr`,
which already reach `tryJoinSearch` via the existing
`joinTreeHasOuterLink` arm. `query77` therefore already carries a
structural plan-shape asymmetry across its own three UNION branches — one
branch's join order is search-driven, two aren't — independent of whether
this specific gate ever widens.

## Sizing / resume point

**Do not schedule a dedicated implementation task from this recon alone.**
A gate-widening change large enough to justify "moves many long-stable
plans at once" (the existing code comment's own caution) is not warranted
by a 5-query, 4-of-5-inert yield. If `query77` specifically shows up as a
`join-order` or `parallelism` category mismatch in a future TPC-DS
floor-measurement pass, the fix there is narrower than gate-widening:
rewrite `query77`'s `cs, cr` cross to use an explicit join matching its
sibling branches (or special-case that one query's derived-table cross),
not a general `joinTreeHasOuterLink` gate change. Re-open this line of
work only if a future corpus addition or query-shape change produces more
matches, or if `query77` is independently confirmed as a live mismatch and
the narrower per-query fix is judged insufficient.

M0142-0008a (the sibling scoping recon for SEMI/ANTI decorrelation) remains
open and unrelated to this finding.
