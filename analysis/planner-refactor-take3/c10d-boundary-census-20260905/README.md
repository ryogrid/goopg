# C-10d — FROM-subquery boundary census (measurement half)

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
C-10d. This is the **measurement**; the decision it feeds is recorded as a
recommendation, not taken here.

Method: parsed every query with goopg's own parser (`internal/parser.Parse`)
and walked the AST counting `RangeVar` nodes with a non-nil `Subquery` —
**not** a regex, because the scoping doc's ~41/100 figure was regex-derived
and flagged as over-counting. Throwaway tool deleted after the run; it is
~200 lines and reconstructable from this description.

Two AST traps worth recording, both of which silently doubled the first run:

- `SelectStmt.From` and `SelectStmt.FromExprs` are BOTH populated, so every
  FROM item is reachable twice. Dedupe on node `pos`.
- `Having` and `Limit` are `Expr` INTERFACES, so a `Kind()==Ptr` nil test
  reports every query as having both.

## Census

| suite | queries | parse failures | queries with a boundary | total boundaries | max nesting |
|---|---|---|---|---|---|
| TPC-H (22 + Q15 view body) | 23 | 0 | **5** (q07 q08 q09 q13 q22) | 5 | 1 |
| TPC-DS (query1…query99) | 99 | 0 | **41** | **89** | 5 (query49) |

**The ~41/100 figure was right at query level** (AST says 41/99). What the
regex could not give is the number the item needs: there are **89
boundaries**, and only about half sit where they hurt.

Two corpus facts found on the way:

- `queries/query_0.sql` is the all-in-one concatenation of query1…query99
  (103 statements). The earlier "100" almost certainly counted it.
- `query36`, `query70`, `query86` are **malformed corpus files, not parser
  gaps**: each is `select * from ( … limit 100; ) as sub`, with the
  template's terminating semicolon left inside the parentheses. PG rejects
  them too, which is why the SF0.5 oracle marks them SKIP.

## Classification

**ABOVE-BLOCKING** = the derived table contains a join tree AND the outer
level carries GROUP BY / HAVING / DISTINCT / WINDOW / aggregate / ORDER BY /
LIMIT. That is the shape putting the scan/join rel one planning level below
the upper rels — what would defeat C-11's struct and P4-01's narrowing.

| suite | ABOVE-BLOCKING | BELOW-HARMLESS |
|---|---|---|
| TPC-H | 4 boundaries / 4 queries (q07 q08 q09 q13) | 1 |
| TPC-DS | 42 boundaries / 28 queries | 47 |

Sub-split that matters more than the headline — is the derived table the
outer query's **sole** FROM item (the true Q9 shape, where the outer
scan/join rel degenerates to one prebuilt leaf and the entire join search
lives one scope down)?

| suite | Q9 shape | one-of-several |
|---|---|---|
| TPC-H | 4 boundaries / 4 queries | 0 |
| TPC-DS | 24 boundaries / 17 queries | 18 / 13 |

**Q9 verified rather than assumed:** 1 boundary, alias `profit`, depth 1,
**6 inner leaves**, inner join tree true, sole FROM item true, no inner
upper clauses, outer GROUP BY + aggregate + ORDER BY. Exactly the asserted
shape.

Two corrections to the scoping README: **q22 is NOT ABOVE-BLOCKING** (its
`custsale` derived table has a single leaf plus two sublinks, so no join
tree below the boundary), and **q13 is ABOVE-BLOCKING but not pullable**
(its `c_orders` has its own GROUP BY, which upstream PG does not pull up
either).

## The number that decides it

Using "the derived table has no upper clause of its own" as an UPPER BOUND
on what `is_simple_subquery` would accept:

| suite | ABOVE-BLOCKING | of which pullable | narrow-pullable (also sole FROM item) |
|---|---|---|---|
| TPC-H | 4 | 3 (q07 q08 q09) | 3 |
| TPC-DS | 42 | 15 (12 queries) | 9 (8 queries) |

**A full `pull_up_subqueries` port removes 18 of 46 ABOVE-BLOCKING
boundaries — 39% — leaving 28**, including q13, which PG would not pull up
either.

## Recommendation (not a decision)

**Declare the boundary permanent for C-11's struct** (upper rels may sit
above a foreign planning scope) and file the pull-up separately.

The reasoning is the 39%: C-11 has to solve the boundary-crossing case in
general no matter what, so the port is an optimisation **on top of** the
struct rather than an alternative to it. Putting a 400–700 LOC high-risk
item on C-11's critical path buys a 39% reduction in a problem C-11 must
still solve.

Two caveats stated against that recommendation:

- **Q9 — P4-01's own witness — IS in the pullable set.** If the goal is
  specifically "make the P4-01 witness work the way PG does", the port is
  the direct answer.
- A **narrow** pull-up (sole FROM item, no inner upper clause, non-LATERAL)
  covers 12 boundaries in 11 queries and should scope well under 400 LOC.
  No corpus query in either suite uses LATERAL, so the hardest
  `is_simple_subquery` arm is untested by the gate either way.

## What "permanent boundary" costs the downstream items

Extending what `relfromjoinlist.go` already ledgers:

- **C-12 (upper-rel `PathSort`)** — the enclosing scope receives only the
  sub-problem's cheapest-TOTAL node, so an outer ORDER BY above a boundary
  can never be met by choosing a differently-sorted path inside; C-12 must
  always stack a full Sort on the leaf. Worst on the 28 Q9-shape
  boundaries, where the ordering keys are outer and every base rel is
  inner. The fix needs level lists indexed by joinlist ITEM rather than
  base-relid popcount — a representation change, not wiring.
- **C-13 (bounded / top-N sort)** — the sub-problem is priced for "produce
  all rows", so an outer LIMIT cannot push a bound in. Nearly every TPC-DS
  query ends `order by … limit 100`, and on the 21 Q9-shape queries the
  whole join tree is below the boundary — so C-13's headline
  `ORDER BY … LIMIT` win is unavailable on exactly those queries unless the
  bound is made to cross.
- **C-17 (`tuple_fraction` end-to-end)** — the same fact as a parameter.
  The fraction stops at the boundary and the sub-problem is always planned
  at 0 (all rows), so "end-to-end" means "end-to-end within one planning
  scope". The item should be re-worded when the decision is taken.

## Scope correction: `RangeVar.Subquery` is one of THREE routes

The same opaque prebuilt leaf is also reached by WITH-list CTEs
(`CTEScan`) — **70 CTEs in 30 of 99 TPC-DS queries, 61 with a join tree in
the body**, zero in TPC-H — and by views, which take a *fresh top-level
planning run* and are therefore a harder boundary (TPC-H Q15 uses one). A
pull-up written at the `RangeVar.Subquery` site covers only the first
route. This materially changes the item's "~46 corpus queries" sizing.

## Not determined

- Whether `is_simple_subquery`'s other declines (volatile target-list
  functions, set-op bodies, LATERAL with outer refs, security-barrier
  views) cut the pullable counts further. "No upper clause of its own" is
  an upper bound, so the true pullable count is ≤ 18.
- Whether the CTE and view leaves classify the same way against the upper
  rels. Structurally they are the same prebuilt-leaf admission and the view
  path is worse, but classifying their boundaries is a second census.
