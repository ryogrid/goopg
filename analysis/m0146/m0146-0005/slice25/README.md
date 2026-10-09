# M0146-0005 slice 25: `M0146-0005x` — `subquery_push_qual` aggregate arm + tlist-regime-aware `trivial_subqueryscan`

Implementation slice. Filed by the slice-24 closeout: TPC-DS Q34/Q73
moved INTO `jointree-search` once the `SubqueryScan` label existed —
PG pushes `cnt BETWEEN 15 AND 20` into the grouped derived table's
HAVING via `subquery_push_qual` (allpaths.c:4023, called from
`set_subquery_pathlist`) and then deletes the qual-free wrapper via
`trivial_subqueryscan` (setrefs.c). goopg kept `Subquery Scan on dn`
with the qual as a leaf-level Filter.

## What landed

**The pushdown** (`subquerypushdown.go`, hooked at the seam's
leaf-local attach in `joinsearchseam.go`): for a `*SubqueryScan` leaf,
each safe local conjunct is localized to the leaf binding, rebased
through the subquery child's passthrough wrappers (`*Filter`, `*Sort`,
`*IncrementalSort` position-transparent; `*Project` mapped through
`Targets` — bare-ColumnRef targets only, a strict subset of PG's
`ReplaceVarsFromTargetList`), and spliced into one `*Filter` directly
above the subquery's `*Aggregate` — the plan-level equivalent of
appending to `subquery->havingQual`. The rebased ref adopts the
aggregate's own output name (`count`, not the leaf alias `cnt`) so the
existing `expandAggOutputRef` render guard admits it and EXPLAIN prints
`count(*)`. Stacked outermost of the aggregate-adjacent Filter run, so
pushed text prints after any native HAVING — `make_and_qual` order.

**Safety** (`subqueryQualPushdownSafe` on `walkExprRefs`/`scopeVeto` —
the inventory gate forbids new hand-written Expr switches): no
inner-scope/subplan children (upstream's contain_subplans refusal), no
OuterColumnRef/ParamRef/ExecParamRef/CTIDExpr/TableOidExpr, no planned
InExpr sublink, no volatile builtin, and at least one leaf column
(pseudoconstants stay above). Nullable-side leaves decline outright —
ON-qual-derived conjuncts must evaluate post-null-extension (FULL JOIN
probe verified: the qual stays a join-level filter). On any decline the
helper returns nil and the original leaf tree is untouched — the splice
shallow-copies only the passthrough wrappers and SHARES the aggregate
subtree, preserving `PartialSource` Finalize↔Partial linkage.

**The strip correction** (`subqueryscan_strip.go`): slice 24's
consumption-identity proxy turns out to be only the pathtarget-regime
half of `trivial_subqueryscan`. PG's real test is structural — no scan
qual AND the scan tlist regurgitates the subplan's tlist
position-for-position — and which tlist the scan carries is decided by
the flags each ancestor passes `create_plan_recurse`:

- bare `0` / `CP_LABEL_TLIST` / `CP_IGNORE_TLIST` (joins —
  `*Join` AND `*NestedLoopIndexJoin`, both created by
  create_nestloop_plan semantics; `*Aggregate` for
  create_agg_plan/create_group_plan; `*ProjectSet`) →
  `use_physical_tlist` fires → the scan emits the subquery's whole
  output in attno order → identity by construction → strip whenever
  qual-free, REGARDLESS of consumption;
- `flags | CP_SMALL_TLIST` (Sort, IncrementalSort, Memoize, WindowAgg)
  or `CP_EXACT_TLIST` (top level, Gather/GatherMerge, RecursiveUnion,
  append/setop arms) → pathtarget regime → the consumption-identity
  check decides;
- `flags` / `flags|CP_LABEL_TLIST` passthrough (Limit, LockRows,
  Unique/Distinct) → propagate the incoming regime;
- goopg `*Project` is spine-continuing: a SubqueryScan path is
  projection-capable, so PG folds a computed select list into the
  scan's own tlist (verified live: `u.c+1` under Limit keeps the
  label), never into a separate node that would reset the demand.

The walk tracks the regime per level (a breaker sets it, a reset node
clears it, a passthrough propagates it, a region boundary restarts at
EXACT) plus the width-equality guard for coordinate honesty.

Every cell verified against live PG 18.3 on :65438: subset consumption
at top level / under Sort / under Limit / under Unique(Distinct) keeps;
under NL / under a top Aggregate / a subset set-op leaf under a join
strips; a computed select list keeps (projection-capable scan carries
the expr in its own tlist).

## Witness

Q34 SF0.25: `Nested Loop -> GroupAggregate` with
`Filter: ((count(*) >= 15) AND (count(*) <= 20))`, no `Subquery Scan`,
87 rows = oracle. Q73: same shape (`count(*) >= 1 AND <= 5`), 0 rows =
oracle. Q8 regression: `Subquery Scan on a1` retained (INTERSECT arm
consumes `ca_zip` only — nontrivial).

## Known residue (ledgered)

- The NON-aggregate arm of `subquery_push_qual` — a qual on a
  non-grouping subquery re-enters its jointree as an inner restriction
  in PG (deeper replan than a leaf splice; enables index-driven
  elections).
- Volatile conjuncts: PG admits them into grouped subqueries; goopg
  declines (keeping one leaf-local is never wrong).
- The physical-regime strip approximates createplan flags by node
  kind; the one unmodelable cell (merge join directly over a leaf with
  sortkeys demanded → CP_SMALL_TLIST) has no corpus consumer.

## Tests

`internal/optimizer/subquerypushdown_test.go` pins the admit/decline
matrix (agg push, invalid/missing schema, computed project target,
volatile, correlated, planned-subplan, nullable-side LEFT-degenerate
vs FULL-join refusal, set-op refusal). `subqueryscan_leaf_test.go`
gained the regime matrix (pathtarget keep cases at top level and under
Sort/Limit/Distinct; physical strip cases under join and top
Aggregate). `internal/executor/subqueryscan_explain_test.go` re-pinned
the label-keep case to a top-level subset set-op (the earlier join-arm
pin encoded the pre-regime model; PG strips it) and
`explain_alias_source_keys_test.go`'s comment corrected — the nested
wrapper under an outer aggregate is stripped.
