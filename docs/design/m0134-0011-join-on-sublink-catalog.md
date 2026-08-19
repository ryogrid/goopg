# M0134-0011 — `IN (subquery)` in a `JOIN ... ON` clause: threading the catalog into per-join resolve contexts

Status: accepted
Date: 2026-08-19
Task: M0134-0011 (`subselect.sql` sizing → smallest shippable root cause)

## Context

`subselect.sql` is a `failed` regress case. Sizing at HEAD
(`scripts/pg-regress-runner.sh --verbose subselect`, report in the loop's handoff
trail) put it at roughly 90–120 of ~335 statements diverging (~30–35%), 2831 diff
lines, spread over **seven independent root causes**. It has no missing
prerequisite: `parallel_schedule` attaches the "depends on `create_misc`" note to
`join`, not `subselect`, and every table `subselect.sql` touches comes from
`test_setup.sql`, which the runner already runs.

The case is therefore **PARKED** — see the fix_plan entry and the deferral rows.
This document designs and lands the one root cause that is genuinely contained.

### The seven buckets, and why only one ships here

| # | bucket | stmts | disposition |
|---|---|---|---|
| 1 | nested parenthesized-JOIN scoping | ~14 | REFACTOR — see below |
| 2 | VALUES-to-Array + correlated elements | ~13 | same root cause as 1 |
| 3 | **`IN (subquery)` rejected inside `JOIN ... ON`** | **5** | **THIS DOC** |
| 4 | `SubPlan parameter $0 read before assignment` | 2 | needs executor tracing |
| 5 | `EXPLAIN VERBOSE` node/`Output:` fidelity | dozens | systemic, cross-file |
| 6 | array-subscript-on-subquery column naming | 3 | cosmetic, smaller value |
| 7 | ten unrelated single-statement bugs | ~10 | ten separate causes |

Buckets 1 and 2 are the highest-value pair (27 statements) and share one root
cause, but that cause is **architectural, not a bug in a function**. goopg's
parser does not represent a parenthesized join as a nested join tree at all:
`internal/parser/select.go:1175-1245` `tryParseParenJoin` deliberately
synthesises `SELECT * FROM <join>` and returns a `RangeVar{Subquery: …}`, and
`internal/parser/ast.go:697-704` `JoinExpr.Right` is a flat `RangeVar` with no
representation for a nested subtree. So the inner aliases (`t2`, `t3`) are sealed
inside a derived table before the planner ever sees them, and the outer `ON`
clause legitimately cannot resolve `t2.q2`.

PostgreSQL treats parenthesization in `FROM` as **pure syntactic grouping with no
scoping effect**: `postgres/src/backend/parser/parse_clause.c:1149`
`transformFromClauseItem` recurses into `j->larg`/`j->rarg` and returns
`list_concat(l_namespace, r_namespace)` (`:1218`) — a *flat concatenation of the
individual `ParseNamespaceItem`s*, never a collapsed single item — so every alias
nested arbitrarily deep stays individually visible to the enclosing `ON` clause
and target list (subject to `setNamespaceLateralState`'s SQL:2008
visible-but-error rule for RIGHT/FULL at `:1194`).

Adopting that model in goopg means changing the parser AST, `tryParseParenJoin`,
`planFromItem`'s flat `for _, j := range item.Joins` loop, **and** the eight
optimizer files that consume the flat join-tree shape (`collapse.go`'s
`deconstructJointree`, `reduce_outer_joins.go`, `joinorder.go`, `with.go` and
their corpora). Every multi-table query — all of TPC-H and TPC-DS — runs through
those consumers. That is a multi-slice refactor with a real regression risk to
currently-passing queries, not a loop's slice. Ledgered; not attempted here.

## The bug this doc fixes

`internal/optimizer/planner.go:12595`:

```go
if ctx == nil || ctx.cat == nil {
    return … "IN (subquery) not supported in this context" (0A000)
}
```

The guard is sound; the problem is that `ctx.cat` is nil on exactly one path.

- `newResolveContext(bindings, schema)` (`planner.go:486-493`) **never sets
  `.cat`**. It is the constructor used for every per-join context.
- `planFromItem` (`planner.go:2594-2642`) builds `leftCtx`, `rightCtx` and
  `mergedCtx` with that constructor (`:2600`, `:2622`, `:2634`) and resolves each
  join's `ON` clause against `mergedCtx` via `planJoinPredicate` →
  `resolveExpr(join.On, mergedCtx)` (`planner.go:5467-5482`).
- The **top-level** `rctx` that `planFromClause` returns does get a catalog, but
  only in a post-hoc patch-up at `planner.go:1085-1088`
  (`if ctx != nil { ctx.cat = cat; … }`) that runs *after* `planFromItem` has
  already resolved every `ON` clause.

Hence the asymmetry the regress diff shows: `IN (subquery)` in a target list or
`WHERE` clause works (resolved against the patched-up top context), while the
identical sublink in a `JOIN ... ON` clause is rejected with `0A000`. The
statements are of the form

```sql
SELECT * FROM tenk1 A LEFT JOIN tenk2 B
  ON A.hundred IN (SELECT c.hundred FROM tenk2 C WHERE c.odd = b.odd);
```

PG pulls the sublink into the join qual and plans a Nested Loop Left Join with a
`SubPlan`; goopg errors out before planning.

## Design

Set `.cat` on the per-join resolve contexts at the point of construction, from
the `cat` the enclosing planner function already holds — no new plumbing, no new
parameter, no change to the guard itself.

The fix is **additive by construction**: it only affects code paths that today
terminate in the `0A000` error. A resolve context that legitimately has no
catalog (a bare-expression context) is unchanged, because the change reads `cat`
from a caller that has one.

Once `.cat` is non-nil, `planInExpr` proceeds to
`planSelectWithParent(x.Subquery, ctx.cat, ctx)` (`planner.go:12598`) — the same
call used by the already-working `WHERE`/target-list path — so the existing
correlated-subquery / `SubPlan` machinery handles the ON-clause sublink with no
new executor work.

### Deliberate non-goals

- No sublink **pull-up** (PG's `pull_up_sublinks` converting a semijoin-shaped
  `IN` into a real `JOIN`): goopg plans it as a `SubPlan` in the join qual. Row
  results are correct; the `EXPLAIN` plan shape will still differ from PG for
  these statements. That is bucket 5's territory and is ledgered separately.
- No change to the guard's error for genuinely catalog-free contexts.
- Nothing for buckets 1, 2, 4, 5, 6, 7.

## Verification

- Targeted optimizer tests asserting the five ON-clause sublink shapes plan
  without error and produce PG's row set (FAIL-pre / PASS-post: the pre-state is
  the `0A000` error).
- The correlated case (`c.odd = b.odd`, referencing the *right* side of the outer
  join from inside the sublink) is the one that could surface bucket 4's
  `SubPlan parameter $0 read before assignment`; it is asserted explicitly rather
  than assumed.
- Regress delta measured with `scripts/pg-regress-runner.sh --verbose subselect`.
- Planner change ⇒ `scripts/tpch-spotcheck.sh` (canonical Q12=2 / Q13=35) and the
  units pre-commit suite, per Hard-won Rule #1.

## Outcome

Recorded in the commit message and `.ralph/working_set.md`. `subselect.sql`
remains `not-tried`/`failed` in
`docs/test-port/postgres-oracle-target-inventory.csv` — this change does not
close the case, so **no `make regen-testport`**.
