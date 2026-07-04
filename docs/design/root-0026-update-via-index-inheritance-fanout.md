# root-0026 — `updateViaIndex` partition/inheritance-child fan-out

Status: accepted. Source: fix_plan.md "Current Priority" banner (2026-06-20
directive, "Next up" item), tracing back to a new-discovery row appended while
closing [root-0025](root-0025-updatable-views.md) item 5 (deferral ledger,
2026-07-04, task-id `M0119-0004 (root-0025 item 5 follow-up, new discovery,
this loop)`).

## Problem

PostgreSQL's `UPDATE`/`DELETE` against a named table implicitly targets that
table **and all of its descendants** (partition children and plain
`INHERITS` children) unless the statement uses `ONLY`. goopg's `updateOp`
(`internal/executor/operators_storage.go`) has two execution paths:

- The **SeqScan+fan-out path** (`updateOp.Next()`'s fallback branch): builds
  `updateScanTables := [tbl] + PartitionChildren(tbl) +
  AccessibleInheritanceChildren(tbl)` and scans every one of them, remapping
  inheritance-child rows to the parent's column ordinals
  (`buildInheritColMap`/`remapChildRowToParent`/`remapParentRowToChild`) so
  `SET`/`WHERE`/`RETURNING` resolve correctly. This path is correct.
- The **index fast path** (`updateOp.updateViaIndex`, used whenever the
  planner attaches an `IndexScan` — typically a PK/unique-column equality
  `WHERE`): opens exactly **one** B-tree, scoped to `o.plan.Table`'s own
  `RelFileNode`, and writes only to that one table's storage. It has no
  awareness of partition or inheritance children at all.

Before this fix, `updateOp.Next()` chose the index path unconditionally
whenever the planner produced an `IndexScan` (`if o.idxScan != nil { return
o.updateViaIndex(...) }`), **regardless of whether `tbl` had any children**.
Consequence: `UPDATE parent SET val = 1 WHERE id = X` (no `ONLY`), where `id`
is indexed and `X` identifies a row that lives only in a plain-`INHERITS`
child's own heap file, silently updated nothing — no error, no NOTICE, just a
smaller-than-PostgreSQL affected-row count. This is a **silent row-count
regression** in the same class the project's TPC-H Q12/Q13 pre-commit gate
exists to catch, just triggered by DDL shape (inheritance) rather than a
planner/cost change.

Partitioned parents were not actually affected in practice: a pure
`PARTITION BY` parent has no heap storage of its own and no index of its own
to match, so `planner.planIndexScanFromWhere` never attaches an `IndexScan`
to the parent in that case — the plan always falls through to the
SeqScan+fan-out path already. The bug is specific to **plain table
inheritance** (`INHERITS`), which is exactly what the fix_plan banner's "start
with a plain non-view two-table `INHERITS` regression test" guidance called
for.

## Fix

`internal/executor/operators_storage.go`'s `updateOp.Next()`: the parent +
partition/inheritance-children scan-target list (`updateScanTables`) is now
built *before* the index-path decision (previously it was only built inside
the SeqScan fallback branch, after the index path had already returned). The
gate becomes:

```go
if o.idxScan != nil && len(updateScanTables) == 1 {
    return o.updateViaIndex(rel, cols)
}
```

When `tbl` has any partition or accessible inheritance children,
`len(updateScanTables) > 1` and execution falls through to the existing
SeqScan+fan-out branch unchanged — that path was already correct, it just
wasn't being reached. No new fan-out logic was needed inside `updateViaIndex`
itself.

This is deliberately the simpler of two possible fixes. The alternative
(sketched in the originating deferral-ledger row) would teach
`updateViaIndex` itself to probe each child's own compatible index (or fall
back to a per-child SeqScan+Filter), reusing the same parent-ordinal remap
machinery — preserving the O(log n) lookup for tables that happen to also
have children. That is materially more code (per-child index compatibility
detection, remapping through the B-tree scan's callback, trigger/FK-recheck
plumbing duplicated per child) for a narrow combination (PK-equality
`UPDATE`/`DELETE` **and** the target has `INHERITS`/`PARTITION BY` children).
Disabling the fast path is a one-loop, low-risk, easily-verified correctness
fix; revisiting it for the lost O(log n) benefit is deferred (see below) and
not expected to be worth doing unless profiling shows it matters in practice
— inheritance-heavy schemas with hot PK-equality UPDATE loops are uncommon.

`deleteOp` was checked and does **not** have this bug: `deleteOp.idxScan` is
recorded by `newDeleteOp` but `deleteOp.Next()` never branches on it — DELETE
always goes through the scanTables fan-out loop directly (it simply never
had the O(log n) fast path to begin with, so there was nothing to gate).

### Independent duplicate discovery, reconciled

A second, independent Ralph loop running concurrently on the same tree
picked up this same fix_plan item at essentially the same time, landed a
functionally-equivalent fix (a standalone `tableHasScanChildren(ctx, tbl)`
helper gating the same `updateViaIndex` call, rather than reusing the
already-computed `updateScanTables` slice length), and — correctly detecting
the collision mid-loop — committed its version to an isolated branch
(`m0119-updateviaindex-inherit-fanout`, pushed to origin) instead of
force-merging into the contended main tree. Per
[[worktree_isolation_escapes_foreign_wip_block]] and
[[concurrent_ralph_loops_corrupt_tree]], the two versions were reconciled by
keeping the version already landed in the main tree (this one) and porting
the other branch's additional test coverage
(`TestUpdateViaIndexFansOutToInheritanceChildWithParentRow` — bounds that
a direct parent-owned row's PK-equality UPDATE is unaffected by the fix
alongside a sibling child row reached by the same statement shape) and its
genuinely novel SELECT-side discovery (below) into this tree; the redundant
branch was then deleted.

## New discovery (not fixed here): SELECT's own IndexScan has the same gap

While bounding this fix (on the reconciled branch), a **SELECT**-side twin
was found: a bare `SELECT val FROM parent WHERE indexed_col = X` also misses
a child-only row (returns zero rows), while `SELECT val FROM parent WHERE
non_indexed_col = X` (which forces a SeqScan/Append plan instead of an
`IndexScan`) finds it correctly. This is a different operator (`indexScanOp`,
planner-level `IndexScan` node for a bare read) from
`updateOp.updateViaIndex`, is out of scope for this loop, and is recorded as
an open row in `.ralph/deferral_ledger.md` (2026-07-04, task-id `M0119-0004
(root-0026 follow-up, this loop)`) rather than fixed silently.

## Tests

`internal/executor/storage_ddl_test.go`:

- `TestUpdateViaIndexFansOutToInheritanceChild` — a row that exists **only**
  in an `INHERITS` child is reached by a PK-equality `UPDATE` on the parent's
  name. Confirmed RED on the pre-fix tree (verified via `git stash` of just
  the `operators_storage.go` hunk) and GREEN after. Also checks a direct
  `SELECT ... FROM ONLY parent` sees no row (confirming the row truly lives
  only in the child) and that `SELECT ... FROM child` sees the updated value
  and an untouched sibling column.
- `TestUpdateViaIndexFansOutToInheritanceChildWithParentRow` (ported from
  the reconciled branch) — bounds that a direct parent-owned row's
  PK-equality `UPDATE` is unaffected by the fix alongside a sibling child row
  reached by the same statement shape.

## Gates

- `go build ./...` — clean.
- `go test ./internal/executor/...` — full package PASS (including the
  pre-existing root-0025 view/CHECK OPTION suite — partition-routed UPDATE
  via a view still passes, consistent with the "partitioned parents never
  hit the fast path anyway" analysis above).
- `go test ./internal/planner/... ./internal/catalog/... ./internal/parser/...
  ./internal/server/...` — PASS.
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2/Q13=33, fresh server restart).
  TPC-H's standard schema uses neither `PARTITION BY` nor `INHERITS`, so this
  change cannot alter Q12/Q13's plan or row counts, but the gate was still
  run for the record.
- pgbench smoke via the pre-commit hook.

## Cross-references

- [root-0025 — Auto-updatable views + WITH CHECK OPTION enforcement](root-0025-updatable-views.md)
  (item 7 in that doc's residuals list is this gap; now closed by this doc).
- `.ralph/deferral_ledger.md`, 2026-07-04 rows (both the closed
  `M0119-0004 (root-0025 item 5 follow-up...)` row and the new open
  `M0119-0004 (root-0026 follow-up, this loop)` row for the SELECT-side twin).
