(idle — nothing in flight)

Last completed (this loop, 2026-07-04): closed root-0025 item 1's last
"Known residual" — `INSERT ... ON CONFLICT` against a renaming
auto-updatable view.

`planOnConflict` (`internal/planner/planner.go`) previously always resolved
the conflict-target column list (`resolveArbiterIndex`), `DO UPDATE SET`/
`WHERE`, and the `excluded` pseudo-relation against `tbl` — already
reassigned from the view to `base` by the time `planInsert` calls it —
instead of `resolveTbl` (`viewProxyTable`'s column-name proxy every other
view-DML resolution site already substitutes). A renamed arbiter column
either failed to match its unique index (42P10) or `DO UPDATE SET` against
a renamed column raised a spurious 42703.

Fixed: `planOnConflict`/`resolveArbiterIndex` now take `resolveTbl` (nil for
a real-table target) and the view's pre-rewrite name, deriving a
`scopeTbl`/`scopeAlias` pair (`resolveTbl`/`viewResolveAlias(targetAlias,
viewName)` when set, else unchanged) used for: the arbiter-expr context; the
`plainWanted` column-name translation inside `resolveArbiterIndex` (view
name → base name via `cat.LookupColumn(resolveTbl, name)`, *before* matching
against `idx.Columns` which are always base names — an unresolvable name now
raises 42703 naming the column instead of a spurious 42P10); and the
DO-UPDATE 2-binding merged scope (primary + `excluded`).
`resolveDefaultDoNothingArbiter` (bare `ON CONFLICT DO NOTHING`, no explicit
target) needed no change — it resolves the chosen index's own stored
`ColExprs`, already written in base vocabulary.

New test `TestUpdatableViewOnConflictRenamedColumn`
(`internal/executor/view_dml_test.go`): a view renaming both columns
(`id AS rid, val AS rval`), `ON CONFLICT (rid) DO UPDATE SET rval =
voc1.rval + excluded.rval` (arbiter target + primary-alias-qualified bare
ref + `excluded`, together), a conflict-free insert through the same view,
and `ON CONFLICT (rid) DO NOTHING` leaving the row untouched. Confirmed RED
on the pre-fix tree (`git stash push -- internal/planner/planner.go`, reran,
got `42P10: there is no unique or exclusion constraint matching the ON
CONFLICT specification`, then `git stash pop`).

Design doc `docs/design/root-0025-updatable-views.md` gained a "Follow-up:
`INSERT ... ON CONFLICT` against a renamed view column" section (closing
item 1's residual) plus updated the item-1 summary and the top-level
"Known residual" note; `docs/design/README.md`'s root-0025 row updated.
Deferral ledger gained one `resolved` row.

**Root-0025 is now fully closed — no open items remain within this
milestone's own scope.**

Gates run this loop: `go build ./...` clean; `go vet ./internal/planner/...
./internal/executor/...` clean; `go test ./internal/planner/...
./internal/executor/... ./internal/catalog/... ./internal/parser/...
./internal/server/...` PASS (new test as above; all pre-existing
view_dml_test.go tests re-verified passing); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke via the pre-commit hook (runs at commit
time). `make ralph-state-guard` self-repaired the same recurring benign
progress.json "completed" artifact noted in prior loops' carries (expected
every loop, not a defect).

Committing and pushing immediately after this carry.

Next step: no work in flight once committed/pushed. Pick the next item:
(a) the `updateViaIndex` inheritance-fan-out discovery (project-wide, out of
root-0025's scope entirely now — start with a plain non-view two-table
INHERITS regression test `UPDATE parent SET val=1 WHERE id=X` where id=X
exists only in a child, to bound the gap before touching `updateViaIndex` in
`internal/executor/operators_storage.go`; mirror `updateScanTables`'s
fan-out loop over `catalog.InMemory.InheritanceChildren` + the
`buildInheritColMap`/`remapChildRowToParent`/`remapParentRowToChild` helpers
already proven out in root-0025's item-5 fix); or (b) continue the
M0119-0004 pg_dump catalog-view parity battery / next unresolved DU-002
slice from the deferral ledger; or (c) pick up one of the still-open
top-level fix_plan items: M0095-0003 (pg_basebackup 010/011/020),
M0110-0002/0003 (pg_waldump/pg_amcheck TAP server tier), M0119-0005/0006/0007
(their M0119 promotions).
