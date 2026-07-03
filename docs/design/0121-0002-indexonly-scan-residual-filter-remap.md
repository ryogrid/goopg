# 0121-0002 — IndexOnlyScan promotion: residual Filter ColumnRef remap

Status: accepted (implemented)
Milestone: M0121-0002 (seeded from M0120-0002, `.ralph/deferral_ledger.md` row 2026-07-04)

## Problem

WordPress's `wp_set_object_terms()` runs
`SELECT term_taxonomy_id FROM wp_term_relationships WHERE object_id = ? AND
term_taxonomy_id = ?` against a table shaped like:

```
wp_term_relationships(object_id bigint, term_taxonomy_id bigint, term_order int,
  PRIMARY KEY (object_id, term_taxonomy_id))
```

Whenever `object_id` matched an existing row, the goopg backend connection
crashed:

```
panic: runtime error: index out of range [1] with length 1
  internal/executor.(*Slot).Get (opnode.go:99)
  internal/executor.evalFastExpr (exprnode.go:222, ExprColumnRef case)
  internal/executor.filterOpNext (opnode.go:717)
```

The per-connection panic recovery kept the server alive, but the client saw
"server closed the connection unexpectedly" — this is what backed the
`wp post update` / `wp post delete` (trash) failures in M0120-0002's write
sweep.

Not reproducible by replaying the isolated SQL text over a single fresh
connection with an `object_id` that had since been deleted from the seed data
— the scan then probed zero rows and `filterOpNext`'s loop body (where the
predicate is evaluated) never ran. Reproduces deterministically once
`object_id` matches an existing row, no session/plan-cache state or prior
statement sequence required.

## Root cause

Two planner passes compose incorrectly:

1. `rewriteScanInputsWithSingleTablePredicates` /
   `absorbConjunctsIntoSubtree` (`internal/planner/mhj_input_rewrite.go`)
   rewrites `Filter(SeqScan, object_id = 9 AND term_taxonomy_id = 1)` into
   `Filter(IndexScan{Key: 9}, term_taxonomy_id = 1)` — the `object_id`
   conjunct becomes the index probe key and is dropped from the predicate;
   the residual `term_taxonomy_id = 1` conjunct's `ColumnRef.Index` is left
   unchanged (`1`, its position in the full 3-column table schema). This
   step is correct: the `IndexScan`'s output schema is still the full row,
   so index `1` is still valid.

2. `tryPromoteIndexOnlyScan` (`internal/planner/planner.go`) then promotes
   `Project(Filter(IndexScan))` to `Filter(IndexOnlyScan)` whenever every
   `Project` target is a plain `ColumnRef` covered by the index. It built
   `Covered` (and the `IndexOnlyScan`'s output schema) **solely from the
   `Project`'s target list** — here just `[term_taxonomy_id]`, a single
   column — without checking whether the surviving `Filter.Predicate` also
   referenced a column. It also never touched `Filter.Predicate`'s
   `ColumnRef.Index` values. The residual filter's `ColumnRef{Index: 1}`
   ("`term_taxonomy_id`'s position in the pre-promotion 3-column schema")
   now pointed one past the end of the promoted scan's 1-column output —
   `Slot.Get` panics on the next row the narrowed scan actually returns.

## Fix

`tryPromoteIndexOnlyScan` (`internal/planner/planner.go`) now:

1. Builds `covered`/`coveredIdx` from the `Project`'s targets exactly as
   before (unchanged behavior when there is no filter, or the filter only
   touches already-selected columns — no `Project` is reinstated, matching
   the existing direct-passthrough shape).
2. When a `Filter` survives, walks its `Predicate` (`walkColumnRefs`,
   `internal/planner/pushdown.go`) and, for every referenced column not
   already in `covered`, appends it — provided the index actually carries
   it. If the filter needs a column the index doesn't carry, or references
   something `walkColumnRefs` treats as out of scope (outer/subquery refs),
   the promotion is abandoned (`return proj`) and the plan falls back to
   the pre-existing, correct `IndexScan` + `Filter` + `Project` shape —
   safe, just forgoes the optimization for that shape.
3. Remaps `Filter.Predicate`'s `ColumnRef.Index` values from their
   pre-promotion (full-row) positions to their position in the final
   `covered` layout, via a new `remapColumnRefsToSchema` — a name-keyed
   tree rewrite kept in lockstep with `shiftColumnRefsBy`'s traversal (same
   case list, so a future expression kind added to one should be added to
   both, per the existing `walkColumnRefs`/`shiftColumnRefsBy` convention).
4. If the filter pulled in an extra column beyond the `Project`'s target
   list, `covered` is now wider than the desired output — an explicit
   `Project` is reinstated on top, with each original target's `ColumnRef`
   re-pointed at its new position in `covered`.

In the motivating query, the residual filter only needed
`term_taxonomy_id` — already in `covered` from the `SELECT` list — so no
`Project` is reinstated; only the remap (step 3) was needed:
`ColumnRef{Index: 1}` → `ColumnRef{Index: 0}`.

## Blast radius

Only queries that promote to `IndexOnlyScan` **and** retain a residual
`Filter` are touched (an `IndexOnlyScan` with no surviving filter, or a
target list not entirely covered by the index, are both unchanged). Verified
via `docs/design/`-adjacent test `TestIndexOnlyScanResidualFilterColumnRemap`
(`internal/executor/indexonly_residual_filter_test.go`) plus the full
`internal/planner` and `internal/executor` suites.

## Verification

- New regression test `TestIndexOnlyScanResidualFilterColumnRemap`
  (`internal/executor/indexonly_residual_filter_test.go`): a minimal 3-column
  table with a composite PK, reproducing the panic pre-fix (confirmed by
  reverting the planner change and re-running — same panic signature) and
  asserting correct row filtering post-fix.
- `go test ./internal/planner/...` and `go test ./internal/executor/...`:
  PASS (no regressions).
- Live end-to-end confirmation against the running `goopg-wp` (`:5544`)
  instance: the exact WordPress-shaped query
  (`SELECT term_taxonomy_id FROM wp_term_relationships WHERE object_id = ?
  AND term_taxonomy_id = ?`) and a fresh probe table both return correct
  rows with an `Index Only Scan ... Filter: (b = ?)` plan and no panic.
- `scripts/tpch-spotcheck.sh`: pre-commit gate (executor/planner change).

## Oracle

PostgreSQL's `IndexOnlyScan` correctness relies on `ExecInitIndexOnlyScan`
building `IndexOnlyScanState.ioss_TableSlot`/`ioss_VMBuffer` machinery whose
`Var` nodes are re-targeted (via `fix_indexqual_references` /
`fix_upper_expr` in `postgres/src/backend/optimizer/plan/setrefs.c`) against
the index's own tlist during `create_indexscan_plan` — i.e. upstream always
re-resolves *every* surviving qual's `Var`s against the physical scan's
actual output tlist as part of promotion, never leaves a stale reference.
goopg's `tryPromoteIndexOnlyScan` now does the equivalent narrowly-scoped
remap for the one node kind (`Filter.Predicate`) it can leave dangling.
