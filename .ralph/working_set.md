(idle — nothing in flight)

Last completed (this loop, 2026-07-04): closed root-0025 deferred item 1
(column subset/reorder/rename auto-updatable views). A simple auto-updatable
view (single base relation, no joins/aggregation/set-ops) can now expose a
renamed, reordered, and/or subset column list of its base relation and
INSERT/UPDATE/DELETE against the view still rewrites correctly onto the
base table — matching PostgreSQL's real rule (`view_col_is_auto_updatable`:
a view column is updatable iff it's a plain Var over the base relation; no
requirement that every base column appear, in order, unrenamed).

`viewTargetsPassthrough` (`internal/planner/view_dml.go`) replaced by
`viewColumnMap`, which now returns `(colMap []int, ok bool)` — colMap[i] is
the base ordinal that view target-list ordinal i maps onto — instead of a
bool requiring positional identity. `viewAutoUpdatableChain` composes each
level's own colMap down through a view chain, returning `colMaps [][]int`
parallel to `chain`. New `viewColumnNames` (a view's own resolved output
names) and `viewProxyTable` (a synthetic *catalog.Table with base's exact
physical column layout/ordinals, but the exposed ordinals renamed to the
view's own names and unexposed ordinals hidden via an empty, unmatchable
Name) let `planInsert`/`planUpdate`/`planDelete` pass this proxy to
`singleBindingContext` in place of base — so a DML statement's column
list/SET/WHERE/RETURNING resolve using the view's own vocabulary through
the ordinary name-based resolveContext machinery, no separate rewrite pass
needed, while the plan node's actual scan/mutation Table field stays the
true base throughout. `viewQualOnBase` also now resolves each chain level's
own stored WHERE against a proxy shaped like its *immediate* FROM's
exposure (not unconditionally against base as before), closing a
correctness gap the chaining loop's "identical names at every level"
assumption had been hiding. The standalone `len(tbl.ViewColumnAliases) > 0`
gate was deleted outright: `execCreateView` already folds an explicit
`CREATE VIEW v (a, b)` column-name list into `catalog.Table.Columns[i].Name`
exactly the same as a per-target AS alias, so treating the two rename
spellings differently for eligibility was never load-bearing.

Test `TestUpdatableViewColumnSubsetReorderRename`
(`internal/executor/view_dml_test.go`) covers rename-via-AS-alias,
rename-via-explicit-column-list, reorder (column-list-free INSERT mapping
through the view's own order, not base's physical order), and subset
(successful INSERT against the exposed column + 42703 against the hidden
one). `TestNonUpdatableViewDMLRejected`'s renamed-column case (`vren`) was
replaced with an expression-column case (`vexpr`), since renaming a plain
column reference is no longer in the rejected category — only a
target-list entry that isn't a bare column reference still is.

Design doc `docs/design/root-0025-updatable-views.md` gained a "Follow-up:
column subset/reorder/rename" section (also corrected the chaining
follow-up's now-outdated "identical names at every level" claim);
`docs/design/README.md`'s root-0025 row updated. Deferral ledger row
appended (status `resolved`) closing item 1. Noted one deliberate residual:
`INSERT ... ON CONFLICT`'s arbiter-target (`resolveArbiterIndex`) and DO
UPDATE SET/WHERE (`planOnConflict`) resolution still resolve directly
against base, not through the view's proxy — targeting a renamed view
column in `ON CONFLICT (...)` or `DO UPDATE SET renamed_col = ...` fails
42703 (a safe, narrow failure) rather than resolving. Root-0025 deferred
items (3) `UPDATE...FROM`/`DELETE...USING` a view and CHECK OPTION on
partition/inheritance-child-routed rows remain open exactly as recorded in
the earlier ledger row.

Committed (`18bd8222`) and pushed to `align-data-structure-with-pg`.

Gates run this loop: `go build ./...` clean; `go vet ./internal/planner/...
./internal/executor/...` clean; `go test ./internal/planner/...
./internal/executor/... ./internal/catalog/... ./internal/parser/...
./internal/server/...` PASS (new test as above; all 6 pre-existing
view_dml_test.go tests + 5 chaining tests re-verified passing);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via the
pre-commit hook PASS (0 failed across TPC-B, simple-update, select-only).
`make ralph-state-guard` self-repaired the same recurring benign
progress.json "completed" artifact noted in prior loops' carries (expected
every loop, not a defect).

Next step: no work in flight. Pick the next item from
`.ralph/deferral_ledger.md` (status `-`) or `docs/design/README.md`'s open
items. Good bounded candidates on the root-0025 line: item (3)
`UPDATE...FROM`/`DELETE...USING` a view (thread the view qual into
`FromPred`/`UsingPred` in `planUpdate`/`planDelete`'s FROM/USING branches,
`internal/planner/planner.go`), the `ON CONFLICT`-against-renamed-view
residual just noted (thread `resolveTbl`/colMap into `planOnConflict`'s
`tbl` param and `resolveArbiterIndex`'s target-column lookup, translating
back to base ordinals before the actual index lookup), or CHECK OPTION on
partition/inheritance-child-routed rows (item 4/5 — remap `newRow`/
`parentNewRow` through the child's column map before the CHECK OPTION eval
in `updateScanTables`'s child branch). Alternatively resume the M0119-0004
pg_dump catalog-view parity battery via `TestPort_PgDumpConnectionSetup`, or
pick the next unresolved DU-002 slice from the deferral ledger.
