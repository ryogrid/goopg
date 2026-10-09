# M0146-0005 slice 24: `M0146-0005w` — `Subquery Scan` leaf wrapper + trivial-subqueryscan strip

Implementation slice. Filed at slice 22, witness TPC-DS Q8 depth-3
`join-order` record: PG plans a non-simple `FROM` subquery as an
RTE_SUBQUERY with a `SubqueryScan` path, so a set-operation arm renders
`Subquery Scan on a1` — ONE leaf against the enclosing leaf set. goopg
handed the inner subtree to the jointree directly, so the arm's
innermost relations (Q8's `customer` / `customer_address`) leaked into
the parent's leaf set and census pairing failed.

## What landed

**The node** (`plan.go`): `SubqueryScan{Alias, Child, schema, src}` — a
label-only wrapper, executor-transparent like `CTEScan`. `src` records
the leaf binding's per-FROM-clause `sourceIdx`, which the strip pass
needs to find the wrapper's consumers.

**Emission** (`planSubqueryRangeVar`): `!appendrelSubquery &&
derivedSubqueryNeedsScan(...)` wraps every leaf PG's
`is_simple_subquery` would refuse to pull up — set ops, grouping,
window, target SRFs, ORDER BY, DISTINCT, LIMIT/OFFSET/WITH TIES, WITH,
locking; the appendrel (simple UNION ALL) arm keeps its inline path.

**Triviality strip** (`subqueryscan_strip.go`, runs at `Plan()`'s tail
after `assertSearchedBoundariesIntact` — the slot that matches
setrefs.c's `clean_up_removed_plan_level`). The first draft wrapped
every non-simple leaf and the fireset showed it was wrong: PG strips a
SubqueryScan whose scan tlist positionally regurgitates the subplan
(`trivial_subqueryscan`), so `select u.a, u.c` over a two-column grouped
leaf renders the bare `Aggregate`, while `u.a` alone or `u.c, u.a`
keeps the label (all four probed against live PG 18.3).

`stripTrivialSubqueryScans` computes, per wrapper, the ordered set of
leaf-local positions the enclosing scope consumes and removes the
wrapper iff that set is exactly `[0..w-1]` AND no `Filter` sits directly
on the leaf (a leaf-level qual is PG's scan qpqual — non-trivial).
Consumption is bounded per binding scope: `rtableScope.derivedSubtrees`
registers every FROM-subquery subtree root (wrapped or not — an
unwrapped simple subquery is still a separate `sourceIdx` namespace),
CTEScan bodies and set-op arm subtrees are structural boundaries. Ref
mapping is by `(Name, SourceTableIdx)` against the leaf schema —
`ColumnRef.Index` is eval-context-relative, not leaf-local (a ref into a
joined leaf arrives at its jointree-global position). Unresolvable or
ambiguous names dirty the leaf → keep (over-keep only ever costs a
label PG would strip, never removes one PG keeps).

**Walk coverage**: every generic consumer learned the wrapper —
`planChildNodes` picks it up by reflection; explicit arms added in
cardinality, createplanroot, exists_to_any, joinkeyproof, joinsearch,
parallel/subquery_parallel, plancost, pushdown, subplan_lower, unnest,
upper_narrow_apply, view_privilege, `walkPlanExprs`, the executor's
`buildNode`/`deformBoundBelow`, and EXPLAIN's `childNodeOf` /
`setOpResolvedColumn` / `resolveKeySource` (the last stays
position-transparent through the label and does not burn a depth slot).

## Witness

Q8 on SF0.25 now renders PG's leaf structure exactly: `Subquery Scan on
a1` survives (the INTERSECT branch's `ca_zip` is a subset of the arm's
two outputs), the set-op leaf's own label strips (v1's `select ca_zip`
is a 1-of-1 identity), and `HashSetOp Intersect` sits directly under the
NL — matching PG's `Materialize → HashSetOp` subtree shape modulo the
known `Materialize` gap (M0146-0010). Q8 still returns 0 rows = oracle.

## Fireset outcome (a460a3d8c vs staged index)

SF0.25: match 14→14, divergent 85→85, `introduced=none`. Class moves:
Q8 `jointree-search` → `D3-partialpath` — leaf pairing now succeeds and
the residual is the executor substrate. Q34/Q73 `D4`/`D3` →
`jointree-search`: their outer `WHERE cnt between 15 and 20` quals sit
as a leaf-level Filter where PG's `subquery_push_qual` folds them into
the subquery's HAVING — the kept label is faithful
(`trivial_subqueryscan` keeps a qualified scan); the pairing loss is the
pre-existing missing-pushdown gap, not this ticket's strip rule. Q53/Q89
reclassed `D1`→jt-search/D4; Q38/Q87 `D3`→`D2-unionall`.

SF1: match 12→12, divergent 87→87, `introduced=none`; same Q34/Q73
jt-search moves.

Both scales: every fire executed PASS on both arms; no new timeouts.

## Known residue

- **subquery_push_qual** (not this ticket): outer quals on pushable
  non-simple subqueries stay leaf-level in goopg, so the label survives
  where PG pushes+strips (Q34/Q73 at both scales).
- **SubqueryScan quals on un-pushable outputs** (Q89): PG keeps
  `Subquery Scan on tmp1 Filter:` — goopg now renders identically.
- **View bodies / expr-level sublinks** aren't in `derivedSubtrees`
  (separate scope objects); their same-sid refs can only inflate a
  used-set — over-keep direction.

## Tests

`internal/optimizer/subqueryscan_leaf_test.go` pins emission (set-op
arm, grouped leaf, nested), the simple-subquery skip, and all four
triviality branches (full in-order strips; subset and out-of-order keep;
set-op identity strips). `internal/executor/subqueryscan_explain_test.go`
pins the `Subquery Scan on u` label + nested-arm rendering and
transparent execution. Pre-existing `resolveKeySource`/`setOpResolvedColumn`
deparse arms are covered by `TestExplainTransitiveGroupKeyRendersInnerCall`
and `TestExplainHashCondDeparsesThroughSetOperation`.
