(idle — nothing in flight)

Last completed (loop #109, 2026-07-04): closed triage item 2 from loop
#107's shortlist — view `WITH CHECK OPTION` was captured for pg_dump
fidelity (slice 365) but never enforced at runtime. Empirically found the
gap was much bigger than assumed: `planInsert`/`planUpdate`/`planDelete`
never checked `catalog.Table.View` at all, so DML against ANY view (not
just CHECK OPTION ones) silently wrote/matched against the view's own
nonexistent-heap OID — `INSERT INTO v VALUES (...)` reported `INSERT 0 1`
while the row vanished; `UPDATE v SET ... WHERE ...` reported `UPDATE 0`
even against a matching row. Fixed for a restricted "simple passthrough"
auto-updatable subset (single base relation, no joins/aggregation/set-ops,
unrenamed in-order full column list — `SELECT *` or an explicit list
matching the base table 1:1): `viewAutoUpdatableBase`/`viewQualOnBase`/
`viewNotUpdatableError` (new `internal/planner/view_dml.go`) detect
eligibility and rewrite `planInsert`/`planUpdate`/`planDelete`
(`internal/planner/planner.go`) onto the base table; ineligible views
(aggregates, joins, renamed/subset/reordered columns) reject with `55000`
(PG's `error_view_not_updatable`, verified against
`postgres/src/backend/rewrite/rewriteHandler.c` — not `42809`, which is
COPY's separate relkind check). `WITH CHECK OPTION` now raises `44000`
(`ERRCODE_WITH_CHECK_OPTION_VIOLATION`) via new `checkViewCheckOption`
(`internal/executor/operators_fk.go`, sibling to `checkConstraints`)
against the finalized INSERT row / post-SET UPDATE row, wired into
`insertOp.Next` and both `updateOp` write paths (plain SeqScan callback +
`updateViaIndex`) in `internal/executor/operators_storage.go`.

Found+fixed a latent, view-independent bug along the way (caught by my own
regression test, not assumed): `updateOp.updateViaIndex`'s initial B-tree
range-scan only evaluates the index's own equality key (`ix.Key`) and NEVER
consults `o.pred` on that pass — `o.pred` is only read later, during an EPQ
recheck after a concurrent modification. So wrapping an index-eligible
UPDATE's `IndexScan` in an additional `Filter` for the view's qual compiled
fine but was silently ineffective in the common (uncontended) case. Fix:
`planUpdate` now skips the index fast-path entirely whenever a view qual is
present, falling back to the plain `SeqScan`+`Filter` path (`scanMatching`
always honors `o.pred`) — narrow enough (`viewQual != nil` only) to not
affect ordinary UPDATE performance. `planDelete` did NOT need this
workaround: `deleteOp.Next` always drives its scan through `scanMatching`
with the full `o.pred` regardless of any `IndexScan` in the plan (DELETE
has no index-driven fast path to bypass).

New `internal/executor/view_dml_test.go` (5 tests): base-table rewrite for
INSERT/UPDATE/DELETE; view-qual restricts UPDATE/DELETE targets (this is
the test that caught the `updateViaIndex` bug above); INSERT/UPDATE CHECK
OPTION violation+success; non-updatable views (aggregate, renamed-column)
rejected 55000 for all three DML commands with base table left untouched.
Also manually verified end-to-end against a running server with upstream
psql 18.3 (`\set VERBOSITY verbose` confirms SQLSTATEs).

Design doc `docs/design/root-0025-updatable-views.md` (new); indexed in
`docs/design/README.md` (root-0025 row, inserted after root-0024 in the
existing root-* group). Ledger row appended (status `-`) documenting 5
deferred items: (1) column subset/reorder/rename views — needs a `colMap
[]int` threaded through Set/Returning/row-assembly instead of relying on
positional column-shape identity; (2) view-of-view chaining — recurse
`viewAutoUpdatableBase` into an eligible base-that-is-itself-a-view,
CASCADED/LOCAL only becomes meaningful once this lands; (3) `UPDATE...
FROM`/`DELETE...USING` a view — rejected unconditionally, needs the view
qual threaded through `FromPred`/`UsingPred`; (4) CHECK OPTION not enforced
on partition/inheritance-child-routed rows (only the base/parent table's
own rows are checked, guarded by `scanTbl == tbl` in the SeqScan path); (5)
the general `updateViaIndex` residual-predicate gap — fix at its root
(evaluate `o.pred` on the initial scan, not just EPQ recheck) is out of
scope for a view-focused loop.

Not yet committed/pushed — see "Next step".

Next step: `git add` the new/changed files (`internal/planner/view_dml.go`,
`internal/planner/plan.go`, `internal/planner/planner.go`,
`internal/executor/operators_fk.go`, `internal/executor/
operators_storage.go`, `internal/executor/view_dml_test.go`,
`docs/design/root-0025-updatable-views.md`, `docs/design/README.md`,
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`) and commit + push (the
pgbench-smoke pre-commit hook runs at commit time — has NOT run yet this
loop, only the manual gates below). After that: continue the M0119-0004
pg_dump catalog-view parity battery (loop #107/#108's item 3 — FDW HANDLER/
VALIDATOR function references parsed and discarded, `internal/parser/
ddl.go:464`, likely entangled with a general regproc-OID-resolver gap —
re-verify still open first, deferral-ledger triage has repeatedly found
"open" rows already fixed), OR pick a fresh unresolved DU-002 slice, OR
pursue deferred item (5) above (`updateViaIndex`'s residual-predicate gap)
as a standalone correctness fix since it's general (would need the WAL/MVCC
practice-card gates: race detector on internal/executor UPDATE paths).

Gates run this loop: `go build ./...` clean; `go test
./internal/planner/... ./internal/executor/... ./internal/parser/...
./internal/catalog/... ./internal/server/...` PASS (full suites, no
regressions); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench
smoke NOT yet run standalone (will run via `.githooks/pre-commit` at commit
time); `make ralph-state-guard` OK (self-repaired the same recurring benign
progress.json "completed" artifact noted in prior loops' carries — not a
new issue).
