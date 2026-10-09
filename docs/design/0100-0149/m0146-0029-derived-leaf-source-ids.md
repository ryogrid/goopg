# M0146-0029 — derived leaves publish unknown source ids

Status: landed 2026-09-28. Code: `projectWithUnknownSources`,
`planSubqueryRangeVar` (internal/optimizer/planner.go). Test:
`internal/optimizer/derivedleafsource_test.go`.

## Defect

Regress `join.sql:1768` (the "variable-free join alias" query) panicked in
`assertSearchedTreeNeedsNoReconcile`: "name resolution moves x from column 1
to 3". Two derived legs of a USING join both output `x` — ss1's is computed
(`123 AS x`), ss2's renames `q2`.

`SchemaColumn.SourceTableIdx` is numbered per query level and restarts at 1
in every scope. A reference in the outer scope (`ss0.x` → ss1's `x`) carries
the outer binding's id, 1. The derived leaf, however, published its root
Project's schema with the inner scope's ids: `x/0` for ss1's computed column
and `x/1` for ss2's renamed `q2`. The by-(Name, SourceTableIdx) re-resolver
`reresolveExprByName` matched `(x, 1)` to ss2's column and moved the
reference. Production skips that pass on searched trees; the assertion ran it
and stopped the backend.

## Rule

`planSubqueryRangeVar` already documented the intended contract: a derived
leaf's columns have no base-table identity at the outer scope and "stay at
0" (unknown), while the binding keeps its own id so qualified references can
still tell sibling bindings apart. The `SubqueryScan`-labelled form built its
own schema at 0. The unlabelled form — a simple subquery the M0146-0005w
triviality rule leaves bare — leaked its root's schema. It now publishes a
copy of the root Project with every SourceTableIdx reset to 0. The copy
matters because a planned root can be shared, e.g. a CTE body. Resolution
then falls back to names, and a genuine ambiguity abstains, as the resolver
documents.

## Residuals (ledgered)

- EXPLAIN's qualifier uses the same per-level ids and prints `i1.f1` for the
  outer `i0.f1` sort key in this query (display only; rows equal PG's).
- Unlabelled leaves whose root is not a Project (a bare scan or Filter for
  `SELECT *`) still publish inner ids. With a single inner relation every
  column shares one id, so the lookup is ambiguous and abstains; the durable
  fix is a statement-wide identity (RTID) instead of SourceTableIdx.
- `join.sql` now reaches line 3217 and stops on a separate pre-existing panic
  (M0146-0030); regress `arrays.sql:682` has another (M0146-0031).
