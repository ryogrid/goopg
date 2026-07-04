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
(`TestUpdateViaIndexFansOutToInheritanceChildrenWithParentRow` — bounds that
a direct parent-owned row's PK-equality UPDATE is unaffected by the fix
alongside a sibling child row reached by the same statement shape) and its
genuinely novel SELECT-side discovery (below) into this tree; the redundant
branch was then deleted.

## SELECT-side twin — FIXED

While bounding the fix above (on the reconciled branch), a **SELECT**-side
twin was found: a bare `SELECT val FROM parent WHERE indexed_col = X` also
missed a child-only row (returned zero rows), while `SELECT val FROM parent
WHERE non_indexed_col = X` (which forces a SeqScan/Append plan instead of an
`IndexScan`) found it correctly. This is a different operator (`indexScanOp`,
the executor-side implementation of a planner-level `IndexScan` node for a
bare read) from `updateOp.updateViaIndex`, and was initially recorded as an
open row in `.ralph/deferral_ledger.md` (2026-07-04, task-id `M0119-0004
(root-0026 follow-up, this loop)`) rather than fixed immediately.

This gap is now closed, on a third isolated branch/worktree
(`root-0026-select-index-fanout`, `/tmp/wt-root0026-select`) started while
the main tree was still occupied by the loop that landed the `updateOp` fix
above — same isolation rationale as the reconciliation this doc already
describes.

**Root cause is at the planner level, not the executor.** Unlike `updateOp`,
`indexScanOp` (`internal/executor/operators_index.go`) has no fan-out
fallback of its own to gate — it is a pure physical operator that always
opens exactly one B-tree scoped to `o.plan.Table`'s own `RelFileNode`. The
decision of *whether* to attach an `IndexScan` node at all is made entirely
in the planner, by `planIndexScanFromWhere` / `tryRangeIndexScan`
(`internal/planner/planner.go`). That function already had a guard for
partitioned parents:

```go
if len(tbl.PartitionKey) > 0 {
    return nil, false, nil // M0100-0005
}
```

but no equivalent guard existed for plain `INHERITS` children — the fallback
plan shape planScanRangeVar already builds for a table with accessible
inheritance children (a left-deep `SetOp{All: true}` chain of per-table
`SeqScan`s, goopg's UNION ALL) is already correct; `planIndexScanFromWhere`
just never deferred to it.

### Fix

`planIndexScanFromWhere` gained a new `enforceInheritanceFanout bool`
parameter. When true, it additionally returns `(nil, false, nil)` — meaning
"no index scan, caller must fall back to whatever plan shape it already
had" — whenever `tbl` has any `catalog.AccessibleInheritanceChildren`:

```go
if enforceInheritanceFanout {
    if im := inMemoryCat(cat); im != nil {
        if len(catalog.AccessibleInheritanceChildren(im.InheritanceChildren(tbl.OID), currentTempOwner(cat))) > 0 {
            return nil, false, nil
        }
    }
}
```

Because `tryRangeIndexScan` (the range-predicate variant, e.g. `WHERE id > 0
AND id < 5`) is only ever called from inside `planIndexScanFromWhere`, this
single guard placed right after the existing `PartitionKey` check covers
both the equality-key and range-key code paths — no change to
`tryRangeIndexScan`'s own signature was needed.

The parameter is threaded from the SELECT call site in `Plan`
(`internal/planner/planner.go`, the `isSimpleSingle` branch) as
`!fromOnly`, where `fromOnly` captures the single FROM table's `rv.Only`
flag: `SELECT ... FROM ONLY parent WHERE indexed_col = X` deliberately
excludes descendants (PostgreSQL semantics), so the index path remains safe
and is left enabled in that case.

`planUpdate` and `planDelete` also call `planIndexScanFromWhere` (their own
`WHERE indexed_col = key` fast-path probe) but both pass
`enforceInheritanceFanout=false`, deliberately preserving their pre-existing
behavior instead of widening this fix's scope:

- `planUpdate`: `updateOp.Next()` (see above) already gates its own index
  fast path on `len(updateScanTables) == 1`, so a plan-time check here would
  be redundant — `o.idxScan` being non-nil no longer matters once the
  executor-side gate is in place.
- `planDelete`: `deleteOp.Next()` never consults the plan's index node for
  its fan-out decision at all (see "Problem" above) — it always recomputes
  `scanTables` itself from `o.plan.Table`. A plan-time change here would have
  zero effect on DELETE's correctness either way.

Scoping the fix to the SELECT call site only (rather than also flipping the
UPDATE/DELETE call sites to `true`, which would have been an equally correct,
slightly more uniform change) was a deliberate choice to keep this branch's
diff disjoint from the concurrently-landing `updateOp` fix above, avoiding an
unnecessary textual overlap in the same shared function during eventual
merge/reconciliation.

### Tests

`internal/executor/storage_ddl_test.go`:

- `TestSelectIndexScanFansOutToInheritanceChild` — a row that exists **only**
  in an `INHERITS` child is reached by both an equality (`WHERE id = 1`) and
  a range (`WHERE id > 0 AND id < 5`) predicate on the parent's indexed PK
  column. Confirmed RED on the pre-fix tree (`git stash` of just the
  `planner.go` hunk, 0 rows instead of 1) and GREEN after. Also confirms
  `SELECT ... FROM ONLY parent` still correctly excludes the child (the
  index path remains safe/enabled for `ONLY`).

### Gates

- `go build ./...` — clean.
- `go vet ./internal/planner/... ./internal/executor/...` — clean.
- `go test ./internal/executor/... ./internal/planner/... ./internal/catalog/...
  ./internal/parser/... ./internal/server/...` — PASS.
- pgbench smoke (`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`)
  — PASS (0 failed transactions across all three TPC-B/`-N`/`-S` workloads),
  run manually in the isolated worktree (no `postgres/local_install` checkout
  without a manual symlink there — submodule content isn't checked out into a
  new worktree by default).
- `scripts/tpch-spotcheck.sh` — not run against real TPC-H data this loop
  (SKIPs in a worktree with no runtime data dir, same rationale as the
  `updateOp` fix above); TPC-H's schema uses neither `PARTITION BY` nor
  `INHERITS`, so this change cannot alter Q12/Q13's plan or row counts.

## Cross-references

- [root-0025 — Auto-updatable views + WITH CHECK OPTION enforcement](root-0025-updatable-views.md)
  (item 7 in that doc's residuals list is the `updateOp` half of this gap;
  closed by this doc).
- `.ralph/deferral_ledger.md`, 2026-07-04 rows: the closed `M0119-0004
  (root-0025 item 5 follow-up...)` row, the `M0119-0004 (root-0026
  follow-up, this loop)` row for the SELECT-side twin (now flipped
  `resolved` once this branch is merged), and a new `resolved` row for this
  fix appended on the `root-0026-select-index-fanout` branch.
