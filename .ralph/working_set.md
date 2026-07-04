(idle — nothing in flight)

Last completed (this loop, 2026-07-04): closed root-0025 deferred item 2
(view-of-view chaining) — a simple auto-updatable view (single base
relation, unrenamed in-order full-column passthrough, no
joins/aggregation/set-ops) can now be defined `FROM` another simple
auto-updatable view and INSERT/UPDATE/DELETE against the outer view rewrites
all the way down to the real base table, mirroring PostgreSQL's
`rewriteHandler.c` recursive view rewrite.

`viewAutoUpdatableBase` renamed to `viewAutoUpdatableChain`
(`internal/planner/view_dml.go`) and now recurses when the FROM relation is
itself a view, returning the full chain (outermost to innermost) plus the
ultimate base table. Column names/ordinals are identical at every level by
construction (the passthrough requirement), so no column-mapping plumbing
was needed. Had to fix a latent eligibility bug found while implementing
this: `catalog.InMemory.CreateView` sets `Table.Virtual = true` on EVERY
view (not just goopg's system-catalog virtual relations) — the old
single-level code's `b.Virtual` guard only worked because the separate
`b.View != nil` check already excluded views; now that a view IS a valid
intermediate, the guard had to become `b.Virtual && b.View == nil` to keep
excluding real system catalogs (`pg_class` etc.) without also excluding
chainable views.

New `viewChainQuals` (`internal/planner/view_dml.go`) combines each chain
level's own resolved WHERE qual two ways: `all` = unconditional AND of every
level (the UPDATE/DELETE row-visibility restriction — a chained view only
exposes rows visible through every level's own SELECT); `checked` = AND of
only the CASCADED/LOCAL-CHECK-OPTION-eligible levels, exactly mirroring
`rewriteHandler.c`'s propagation (~line 3791-3843): a CASCADED check option
(default for unqualified `WITH CHECK OPTION`) forces every inner level to be
checked too regardless of that level's own setting; LOCAL checks only
levels that themselves declare CHECK OPTION. `planInsert`/`planUpdate`/
`planDelete` (`internal/planner/planner.go`) call `viewAutoUpdatableChain` +
`viewChainQuals` instead of the old single-level pair.

Design doc `docs/design/root-0025-updatable-views.md` gained a "Follow-up:
view-of-view chaining" section; `docs/design/README.md`'s root-0025 row
updated. Deferral ledger row appended (status `resolved`) closing item 2 of
the earlier root-0025 ledger entry — items (1) column subset/reorder/rename
and (3) `UPDATE...FROM`/`DELETE...USING` a view, and (4) CHECK OPTION on
partition/inheritance-child-routed rows remain open exactly as recorded
there. Noted one residual fidelity gap (not worth fixing in a chaining-
focused loop): `viewCheckName` in the 44000 error always names the outermost
DML-targeted view even when an inner level's qual is what failed —
PostgreSQL's per-level `WithCheckOption.relname` would report the specific
inner view instead. Purely cosmetic (the row is still correctly rejected).

Committed (`d7e0dd4e`) and pushed to `align-data-structure-with-pg`.

Gates run this loop: `go build ./...` clean; `go vet` clean (only pre-existing
unrelated lint notes); `go test ./internal/planner/... ./internal/executor/...
./internal/catalog/... ./internal/parser/...` PASS (new tests:
`TestChainedViewInsertUpdateDeleteRewriteToBase`,
`TestChainedViewCheckOptionCascadeReachesInnerView`,
`TestChainedViewCheckOptionLocalDoesNotForceInnerCheck`,
`TestChainedViewCheckOptionInnerEnforcedRegardlessOfOuter`,
`TestChainedViewInnerNotAutoUpdatableRejectsWholeChain`, all in
`internal/executor/view_dml_test.go`, plus all 6 pre-existing view_dml tests
re-verified passing); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pgbench smoke via the pre-commit hook PASS (0 failed across all 3 phases
this run's SCOPE=smoke path exercised — TPC-B, simple-update, select-only).
`make ralph-state-guard` self-repaired the same recurring benign
progress.json "completed" artifact noted in prior loops' carries (expected
every loop, not a defect).

Next step: no work in flight. Pick the next item from
`.ralph/deferral_ledger.md` (status `-`, ~155 open rows) or
`docs/design/README.md`'s open items. Good bounded candidates on the
root-0025 line: item (1) column subset/reorder/rename (needs a `colMap
[]int` threaded through `Set`/`Returning`/row-assembly in
`internal/planner/view_dml.go`/`planner.go`, replacing the
`tbl.Columns == base.Columns` positional-identity assumption — the
`viewTargetsPassthrough` helper this loop factored out is the natural place
to generalize into a column-map builder), or item (3)
`UPDATE...FROM`/`DELETE...USING` a view (thread the view qual into
`FromPred`/`UsingPred` in `planUpdate`/`planDelete`'s FROM/USING branches).
Alternatively resume the M0119-0004 pg_dump catalog-view parity battery via
`TestPort_PgDumpConnectionSetup` (has been the discovery engine for DU-002
slices through at least 445).
