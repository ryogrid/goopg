(idle — nothing in flight)

Last completed (this loop, 2026-07-04): closed root-0025 deferred item 3
(`UPDATE ... FROM` / `DELETE ... USING` against an auto-updatable view).
`planUpdate`/`planDelete` (`internal/planner/planner.go`) no longer
special-case `len(s.From) > 0` / `len(s.Using) > 0` into an unconditional
`viewNotUpdatableError` before even computing the view's auto-updatable
chain — `viewAutoUpdatableChain`/`viewQual`/`resolveTbl` are now always
computed when the target is a view, and the FROM/USING cross-product
branches reuse the same `resolveScope` (`resolveTbl` proxy when the target
was a view, else `tbl`) the FROM/USING-free branches already used for
`SET`/`WHERE`/`RETURNING` resolution. `viewQual` (the AND of every chain
level's own WHERE, translated onto `base`) is ANDed into `FromPred`/
`UsingPred` so the cross-product still only matches rows the view itself
would expose. `WITH CHECK OPTION` is threaded through too: the FROM
branch's `Update` node now carries `ViewCheckQual`/`ViewCheckName`
(previously only set in the FROM-free branch), and
`updateWithFrom` (`internal/executor/operators_storage.go`) gained a
`checkViewCheckOption` call at the point `parentNewRow` is finalized,
gated to `fst.tbl == o.plan.Table` (the exact base relation only —
matches the FROM-free path's existing partition/inheritance-child
exclusion, deferred item 5, left unchanged). `DELETE ... USING` needed no
CHECK OPTION wiring (PG never enforces it on DELETE).

Found and fixed a previously-untested latent bug spanning every view-DML
form, not just this one: `viewProxyTable`'s synthetic table keeps `base`'s
own `Name` (needed for ordinal-keyed lookups elsewhere, e.g. partition
routing), so a DML statement's resolve-context binding — built with
`s.Target.Alias` as the alias at every `resolveTbl`-driven site — left an
unaliased qualified reference to the view by its *own* name
(`UPDATE v SET x=1 WHERE v.id=1`, no `AS`) unresolvable (`42703`): the
qualifier matched neither the binding's empty alias nor the proxy's
borrowed base name. This was latent because no existing view-DML test
qualified a column reference with the view's own name;
`UPDATE...FROM`/`DELETE...USING` needed it to disambiguate the target from
the FROM/USING relation(s), exposing it immediately. Fixed via new
`viewResolveAlias(explicit, viewName string) string`
(`internal/planner/view_dml.go`), applied at all four `resolveTbl`-driven
binding sites: `planInsert`'s RETURNING context, `planUpdate`'s FROM and
FROM-free contexts, `planDelete`'s USING and USING-free contexts.
`INSERT ... ON CONFLICT` resolution is unaffected (it already bypasses the
proxy entirely — item 1's "Known residual").

Test `TestUpdatableViewUpdateFromDeleteUsing`
(`internal/executor/view_dml_test.go`) covers: UPDATE...FROM and
DELETE...USING through a renamed-column, WHERE-qualified view rewriting
onto base and leaving rows outside the view's own qual untouched even when
the FROM/USING table would otherwise match them; WITH CHECK OPTION still
rejects (44000) an UPDATE...FROM that would produce a row outside the
view's qual; an aggregation view (outside the auto-updatable subset) still
rejects both forms 55000.

Design doc `docs/design/root-0025-updatable-views.md` gained a "Follow-up:
`UPDATE ... FROM` / `DELETE ... USING` a view" section; `docs/design/
README.md`'s root-0025 row updated. Deferral ledger row appended (status
`resolved`) closing item 3. Root-0025's remaining open items: (5) CHECK
OPTION on partition/inheritance-child-routed rows, and the narrower
`ON CONFLICT`-against-renamed-view residual (item 1's "Known residual").

Committed (`fe6c33aa`) and pushed to `align-data-structure-with-pg`.

Gates run this loop: `go build ./...` clean; `go vet ./internal/planner/...
./internal/executor/...` clean; `go test ./internal/planner/...
./internal/executor/... ./internal/catalog/... ./internal/parser/...
./internal/server/...` PASS (new test as above; all pre-existing
view_dml_test.go tests re-verified passing); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke via the pre-commit hook PASS (0 failed
across TPC-B, simple-update, select-only). `make ralph-state-guard`
self-repaired the same recurring benign progress.json "completed" artifact
noted in prior loops' carries (expected every loop, not a defect).

Next step: no work in flight. Pick the next item from
`.ralph/deferral_ledger.md` (status `-`) or `docs/design/README.md`'s open
items. Good bounded candidates on the root-0025 line: item 5 (CHECK OPTION
on partition/inheritance-child-routed rows — remap `newRow`/`parentNewRow`
through the child's column map before the CHECK OPTION eval in
`updateScanTables`'s child branch, `internal/executor/operators_storage.go`),
or the `ON CONFLICT`-against-renamed-view residual (thread `resolveTbl`/
colMap into `planOnConflict`'s `tbl` param and `resolveArbiterIndex`'s
target-column lookup, translating back to base ordinals before the actual
index lookup, `internal/planner/planner.go`). With those two the entire
root-0025 milestone would be fully closed. Alternatively resume the
M0119-0004 pg_dump catalog-view parity battery via
`TestPort_PgDumpConnectionSetup`, or pick the next unresolved DU-002 slice
from the deferral ledger.
