# R102 SCOPE — LATERAL visibility through an explicit joined left side

R101 (`deeca1f5d`) established that native PostgreSQL 18 can price both
parenthesized Q96 first-two-dimension orders, but Goopg rejects both forms
before planning: the final `LATERAL` subquery cannot resolve
`store_sales.ss_sold_time_sk`. This is a query-semantics blocker, not cost
evidence.

The ordinary JOIN-chain path already appends `item.Base` and previous joins to
`rels` before it analyzes a LATERAL right side. R101 instead crosses a parser
representation boundary: `tryParseParenJoin` lowers an unaliased grouped JOIN
to a synthetic `SELECT * FROM ...` range variable. The later LATERAL sees that
synthetic `__sq_*` relation but not the grouped relation's source bindings.

## Authorized change

Make the grouped-join synthetic-range lowering preserve exactly the lexical
bindings PostgreSQL exposes to a later LATERAL item. The correction must be
limited to an unaliased parenthesized JOIN's synthetic representation; the
ordinary JOIN-chain LATERAL path is a no-regression control, not the target.
Preserve source order and alias / `qualifiedOnly` / `usingHidden` semantics.
Do not broaden ordinary non-LATERAL visibility or leak bindings through an
explicitly aliased joined expression.

Add analyzer-level PG-compatible acceptance tests using small catalog tables
for all of the following:

1. a final LATERAL subquery can qualify a column of either base relation in an
   unaliased parenthesized left INNER join, including base aliases within that
   join;
2. an ordinary, unparenthesized JOIN-chain LATERAL query keeps its current
   behavior;
3. an explicitly aliased grouped join has the exact PostgreSQL behavior:
   underlying base names are rejected and the join alias's output columns are
   accepted. If the AST cannot represent the latter safely, stop and report;
4. duplicate names and a `USING` join retain PostgreSQL's qualified and
   unqualified visibility behavior; and
5. an otherwise identical non-LATERAL right-side subquery still rejects the
   outer reference with the PG-compatible error class.

Use a disposable native-PG18 query only to establish the exact expected
success/error behavior for cases 1–4. Record full SQL, output/error SQLSTATE,
and PG version in the implementation report. The test must prove execution
values, not merely parse or analyze success, for the successful cases.

After tests pass, rebuild an isolated Goopg binary and rerun R101's two
already-pinned SQL files unchanged. Record whether both now return 266 and
whether their plans preserve the requested first-two join leaves and final
`time_dim` probe. This rerun is only a post-semantic-fix measurement; it does
not authorize any cost adjustment or natural-order conclusion.

## Boundaries

R102 changes analyzer namespace construction only if the PG probes and tests
confirm the stated rule. It must not alter parser grammar, planner costs,
join enumeration/election, executor algorithms, GUC defaults, benchmark SQL,
`postgres/`, the R100 reference cluster, or statistics. If correlated value
execution requires any planner or executor origin/binding change, stop and
report that blocker rather than expanding this analyzer-only scope. Do not add
hints, disable join methods, flatten SQL, or use the forced SQL forms as
natural plan election evidence.

If aliases, parenthesized join representation, or executor lowering makes a
small analyzer-only correction unsafe, stop and report the blocker rather
than leaking base names through an explicitly aliased join. Before production
edits: agent review, revise if necessary, `git commit -n`, and push this
scope. After implementation: run focused analyzer tests, affected package
tests, full required parity gates, `git diff --check`, then commit/push the
code and English report separately from unrelated work.
