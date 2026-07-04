(idle — nothing in flight)

Last completed (this loop, 2026-07-04): closed root-0025 deferred item 5
(CHECK OPTION on partition/inheritance-child-routed rows).
`updateScanTables`'s per-row callback (`internal/executor/operators_storage.go`,
the FROM-free UPDATE path) gated `checkViewCheckOption` to `scanTbl == tbl`;
`updateWithFrom`'s FROM cross-product branch gated it to
`fst.tbl == o.plan.Table`. Both gates were unnecessarily conservative — each
function already computes a `parentNewRow` in the base table's own column
ordinal space before the gate: true inheritance children remap through
`buildInheritColMap`/`remapChildRowToParent`/`remapParentRowToChild`, and
partition children need no remap at all (PG requires a partition's columns
to exactly mirror the parent's). Fixed by lifting `parentNewRow` to always
be populated in `updateScanTables` (the non-`isInheritChild` branch now sets
`parentNewRow = newRow`) and checking it unconditionally in both functions.

New test `TestViewCheckOptionEnforcedOnPartitionAndInheritanceChildRows`
(`internal/executor/view_dml_test.go`): a partition-routed row via plain
UPDATE and UPDATE...FROM, an inheritance-child row via plain UPDATE — each
confirms the rejected write (44000) leaves the child-routed row unchanged
and a subsequent in-qual UPDATE still succeeds. Verified RED on the pre-fix
tree (git-diffed out just the operators_storage.go hunk, reran, confirmed
`err=nil` instead of 44000, then reapplied via `git apply`).

Note: the inheritance-child sub-test deliberately avoids `WHERE <pk> = ...`
(that routes through `updateViaIndex` instead of `updateScanTables`) —
which surfaced a **new, unfixed, project-wide, view-independent discovery**:
`updateOp.updateViaIndex` has NO partition/inheritance-child fan-out at all
(unlike `updateScanTables`/`updateWithFrom`, which both build a scan-target
list from `catalog.InMemory.PartitionChildren`/`InheritanceChildren`). So
`UPDATE parent SET ... WHERE indexed_col = X` (no `ONLY`), whenever the
planner finds a usable index on `parent` itself, silently skips a matching
row that lives only in a plain-inheritance child's own storage — reproduces
on any two plain tables joined by INHERITS, no views involved. Deferral
ledger row appended (status `-`, open) with a resume point: mirror
`updateScanTables`'s fan-out loop inside `updateViaIndex`, applying the same
parent-ordinal remap helpers already proven out this loop.

Design doc `docs/design/root-0025-updatable-views.md` gained a "Follow-up:
CHECK OPTION on partition/inheritance-child-routed rows" section (closing
item 5) plus a "New discovery" note (the `updateViaIndex` gap, not fixed);
`docs/design/README.md`'s root-0025 row updated. Deferral ledger gained two
rows: one `resolved` (item 5) and one `-` (the new discovery).
**Root-0025 is now fully closed except the narrow `ON CONFLICT`-against-
renamed-view residual (item 1's "Known residual").**

Gates run this loop: `go build ./...` clean; `go vet ./internal/planner/...
./internal/executor/...` clean; `go test ./internal/planner/...
./internal/executor/... ./internal/catalog/... ./internal/parser/...
./internal/server/...` PASS (new test as above; all pre-existing
view_dml_test.go tests re-verified passing); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke via the pre-commit hook PASS (checked at
commit time). `make ralph-state-guard` self-repaired the same recurring
benign progress.json "completed" artifact noted in prior loops' carries
(expected every loop, not a defect).

Not yet committed at the time this file was written — commit is the very
next action (message covering both the operators_storage.go fix and the
docs/ledger/fix_plan updates), then push to `align-data-structure-with-pg`.

Next step: no work in flight once committed/pushed. Pick the next item:
(a) the `ON CONFLICT`-against-renamed-view residual — thread `resolveTbl`/
colMap into `planOnConflict`'s `tbl` param and `resolveArbiterIndex`'s
target-column lookup, translating back to base ordinals before the actual
index lookup (`internal/planner/planner.go`) — this is root-0025's last
internal-scope item; (b) the new `updateViaIndex` inheritance-fan-out
discovery (project-wide, bigger, out of root-0025's scope — start with a
plain non-view two-table INHERITS regression test to bound the gap before
touching `updateViaIndex`); or (c) continue the M0119-0004 pg_dump
catalog-view parity battery / next unresolved DU-002 slice from the
deferral ledger.
