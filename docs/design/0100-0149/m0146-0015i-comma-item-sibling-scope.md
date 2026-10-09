# M0146-0015i — non-LATERAL comma-item derived table / VALUES no longer sees same-level FROM siblings

| field | value |
|---|---|
| Status | landed 2026-09-27 |
| Kind | bug (permissive acceptance — computes what PG rejects) |
| Parent | none (M0146 family; sibling of M0146-0015h) |
| Movement | none — rejection parity, outside the parity instruments' reach |

## Defect

```sql
select count(*)
from tk o, (select x.u1 from tk x where x.h = o.h) s;
```

PostgreSQL 18.3 rejects with `42P01 invalid reference to FROM-clause
entry for table "o"` — a non-LATERAL subquery in FROM may reference
outer query levels (M0146-0015h) but never same-level FROM siblings.
goopg returned `1000`, silently computing lateral semantics.

The same permissive acceptance held for the VALUES twin and for a
qualified star inside it:

```sql
select count(*) from tk o, (values (o.h)) v;   -- PG: 42P01
select count(*) from tk o, (values (o.*)) v;   -- PG: 42P01
```

## Root cause

`planFromClause` builds `lateralCtx` — a resolveContext holding the
bindings of every FROM item planned so far — whenever
`len(bindings) > 0` (planner.go). That context exists so SRF /
table-function arguments can see earlier siblings (PG treats FROM
functions as implicitly LATERAL). But `planSubqueryRangeVar` consumed
the same ctx for derived tables and VALUES: its `lateralCtx != nil`
arm copied the context wholesale, so sibling names resolved inside
non-LATERAL subqueries too.

The join-right-side arm did not have this defect: M0146-0015h already
replaced `joinLateralCtx` with a binding-free marker there. Comma
items take `planFromClause` directly, not `planFromItem`'s
join-right path, so the marker never reached them.

## Fix

One point, at the top of `planSubqueryRangeVar` (covers both the
VALUES dispatch and the SELECT arm, since `planValuesSubquery` is
called from there):

- when `!rv.Lateral && lateralCtx != nil`, copy the context and strip
  `bindings`/`schema`/`table`/`alias`/`joinlist`, keep `cat`,
  `settings`, `rtScope`, `parent` (defaulting `parent` to `planParent`
  when unset), and force `lateralSibling = false`.

Sibling names then miss at that level and continue up the parent chain
— where a same-level sibling does not exist, so resolution fails
(42P01-class rejection on both engines; goopg currently reports
`column … does not exist` / `SELECT * with no FROM clause`, a wording
difference, not a semantics one).

### Why one hop, not zero

The stripped ctx still counts as one resolve level
(`lateralSibling = false`). That is load-bearing: when the inner
subquery resolves a true OUTER-level ref, the resolved
`OuterColumnRef` makes `planFromClause`/`planFromItem` flip
`Join.Lateral`, and the executor's `openLateral` pushes the left row
onto `OuterRows` per re-evaluation. Outer refs must therefore land at
level 2 — sibling-scope hop plus enclosing-statement hop — matching
the pushed row's stack depth. This is the same accounting the
M0146-0015h marker performs on join right sides.

Explicit `LATERAL` items skip the strip entirely, and the M0146-0015h
marker arriving for a non-LATERAL join right side has no bindings, so
the strip is a no-op there.

## Verification

- Live probes (`analysis/m0146/m0146-0015i/`): filed shape, VALUES
  twin, `o.*` star, and the comma item nested inside a correlated
  scalar subquery — all now reject like PG; explicit LATERAL and
  outer-level refs unchanged (`1000` on both).
- Regression pin:
  `internal/executor/derived_table_outer_ref_test.go`
  `TestDerivedTableCommaItemSiblingScope` — four reject shapes plus
  `lateral-comma` (`1000`) and `outer-through-comma` (`0|1000,1|1000,2|1000`)
  success cases.
- Gates: units PASS; tpch-spotcheck PASS (Q12=2, Q13=33); acceptance
  arm 24/24 MATCH vs `bench/tpch/baseline-digests.txt`; TPC-DS SF0.25
  sweep 96 PASS / 0 MISMATCH, plans 99/99 same; tpcds-fireset PASS
  (no fires, both corpora).

## Residuals

- Error wording: goopg reports `column "h" does not exist` (42703) /
  `SELECT * with no FROM clause` where PG reports 42P01
  `invalid reference to FROM-clause entry for table "o"` with a
  DETAIL. Same rejection class, different code/message — a
  message-fidelity gap, not semantic divergence.
- `values (o.*)` on goopg reports `SELECT * with no FROM clause`
  rather than 42P01 — the qualified star falls through the
  binding-free ctx and resolves as an unqualified star. Rejection
  parity holds; message fidelity is the same residual as above.
