# 0016-0002 — Non-Recursive CTE Planner & Executor

**Status:** accepted
**Milestone:** [0016 — WITH Clause (CTE) Support](../../milestones/0016-with-clause-cte-support.md)
**Spans seam:** planner CTE pre-planning, FROM-clause resolution
substitution, multi-consumer correctness.
**Cross-links:**
[0016-0001](0016-0001-with-parser-ast-and-name-resolution.md)
(parser AST + analyzer scope rules this slice consumes),
[root-0011](../../root/root-0011-planner.md) (planner baseline),
[0003-0008](0003-0008-subqueries.md) (existing subquery infrastructure
this slice's substitution mirrors).

## Context

M0016-0001 step 1 added the parser AST nodes; step 2 wired the
analyzer's CTE scope rules. The analyzer now accepts well-formed
`WITH ... SELECT/INSERT/UPDATE/DELETE` with full name-resolution
diagnostics — but the planner still errors on any FROM-clause
reference that resolves only via a CTE (the catalog lookup fails).

This slice closes that gap: the planner pre-plans each CTE's body
once, threads the planned `Node`s through a process-local CTE scope,
and substitutes them into the FROM-clause resolution path.

## Substitution strategy

For Stage A goopg adopts **inline substitution per consumer**: each
FROM-clause reference to a CTE name returns a freshly-cloned plan
of that CTE's body. Multiple consumers in the same statement each
get their own copy of the plan tree. This is correctness-first; the
"materialise once, feed many" optimisation that upstream uses lands
later under M0016-0004.

### Why inlining is correct for v0

- The CTE body's plan reads from real tables under the same
  statement-level snapshot (`Context.Snap` in the executor) — every
  consumer sees the same source data.
- v0's planner doesn't yet rewrite/optimise across CTE boundaries
  (no predicate pushdown into CTEs, no inlining elision), so two
  inlined copies produce semantically identical row sets.
- The CTE bodies themselves are deterministic (no `random()`,
  `nextval()`, etc. in v0's expression registry that would diverge
  between consumers — the v0 surface is small and side-effect-free).

### What inlining costs

If a CTE's body is expensive (e.g. an aggregate over a 1M-row
table) and consumed twice, we re-execute it. The
materialise-once optimisation is M0016-0004's job. Stage A's bar is
correctness, not throughput.

## Implementation

### plannedCTE

```go
type plannedCTE struct {
    name    string
    body    Node                  // the planned CTE body
    schema  Schema                // body's output schema (alias-renamed if (col, ...) present)
    table   *catalog.Table        // synthetic *catalog.Table for rangeBinding
}
```

### Process-local scope

The planner already has `planParent *resolveContext` (a package
global save/restored across recursive Plan calls). We add the
sibling:

```go
var planCTEs map[string]*plannedCTE
```

`Plan()` enter:

1. If `stmt` carries a non-nil WithClause:
   - Save `prev := planCTEs`; defer-restore.
   - Build a fresh `cur := map[string]*plannedCTE{}` seeded with `prev`'s entries
     so an inner statement can still see outer CTEs (rare but correct;
     the analyzer's scope-chain mirrors this).
   - For each CTE in declaration order:
     - Set `planCTEs = cur` so the CTE body's Plan call sees prior siblings.
     - Recursively `Plan(cte.Query, cat)`.
     - Build the synthetic `*catalog.Table` (alias-renaming via cte.Columns
       if present; matches the analyzer's analyzeWith logic verbatim).
     - Store in `cur[lower(name)]`.
2. Dispatch as today.

### FROM-resolution substitution

`planScanRangeVar` grows a CTE-lookup path executed before the
catalog lookup, gated on `rv.Schema == ""` (CTEs are unschemed):

```go
if rv.Subquery == nil && rv.Schema == "" {
    if ce, ok := planCTEs[strings.ToLower(rv.Name)]; ok {
        // Clone the planned body (Node values are slice-pointer-shaped,
        // so a shallow copy is sufficient for v0; deeper sharing surfaces
        // when M0016-0004 adds Materialize).
        b := rangeBinding{
            table:  ce.table,
            alias:  rv.Alias,                  // CTE name when alias absent
            offset: 0,
        }
        if rv.Alias == "" {
            b.alias = ce.name
        }
        return ce.body, b, nil
    }
}
```

A CTE name that *also* names a base relation now resolves to the
CTE — shadowing matches the analyzer's `resolveTable` and matches
PostgreSQL.

### RECURSIVE rejection

`planSelect` (and the per-DML planners) reject `WithClause.Recursive`
with SQLSTATE `0A000` (mirrors the analyzer rejection from step 2 —
the planner is the second line of defence in case some path bypasses
the analyzer).

### Why not extend resolveContext.ctes

The analyzer threaded CTEs through `scope`. For the planner we use a
package global because:

1. The planner's existing parent-chain is already global
   (`planParent`) — adding a sibling `planCTEs` follows the
   established pattern instead of inventing a parallel one.
2. CTE references can appear anywhere in the FROM list of any
   statement nested inside the WITH'd statement; threading through
   resolveContext would mean every helper that builds a context
   knows about CTEs. The global is the same shape as `planParent`.

## DML support

`planInsert` / `planUpdate` / `planDelete` each grow a brief
"pre-plan WITH if present" entry stanza identical to `planSelect`'s.
For Stage A:

- `INSERT WITH ... VALUES (...)` accepts the WithClause but doesn't
  consume it — the VALUES rows have no FROM clause and can't
  reference a CTE. The CTEs are still planned (so type errors
  surface).
- `UPDATE WITH ... SET ...` and `DELETE WITH ...` similarly accept
  but don't consume the CTE in their predicate today (no FROM /
  USING in v0). Future slices that add `UPDATE ... FROM` / `DELETE
  ... USING` automatically pick up CTE FROM-resolution for free.

## Tests

- `TestPlanWithSimpleCTE` — `WITH a AS (SELECT 1) SELECT * FROM a`
  plans without error and the resulting Node's Output schema has
  one column.
- `TestPlanWithCTEMultipleConsumers` — `WITH a AS (SELECT 1) SELECT
  * FROM a, a b` plans with two consumers (cross-product); both
  succeed and the schema has two columns.
- `TestPlanWithCTEReferencingPriorSibling` — left-to-right: a later
  CTE references an earlier one through the planner's CTE map.
- `TestPlanWithRecursiveRejected` — planner-level 0A000 in case the
  analyzer is bypassed.
- `TestPlanWithCTEShadowsTable` — CTE name matches a catalog table;
  the FROM reference resolves to the CTE.
- `TestPlanWithoutCTEUnchanged` — regression guard: plain SELECT's
  Plan output is unchanged.
- `TestExecuteWithSimpleCTE` — end-to-end via the executor stack,
  pinning that the planner+executor pipeline returns one row for
  `WITH a AS (SELECT 1) SELECT * FROM a`.

## Out of scope

- Materialise-once optimisation for repeated consumers — M0016-0004.
- Recursive CTE execution — M0016-0003.
- EXPLAIN labels for CTE producers — M0016-0004.
- Cross-statement CTE caching (none; each statement re-plans).
