(idle — nothing in flight)

Last completed (loop #110, 2026-07-04): closed deferred item (5) of the
root-0025 (updatable-views) deferral row — `updateOp.updateViaIndex`'s
residual-predicate gap, fixed at the root as suggested by loop #109's
carry. `updateViaIndex` (`internal/executor/operators_storage.go`) drove
its initial B-tree range-scan purely off the index's own equality key and
never consulted `o.pred` until an EPQ concurrent-update recheck — so a
`Filter`-wrapped `IndexScan`'s residual predicate (a view's own WHERE qual
folded on by `planUpdate`, or any future planner change producing this
shape) was silently unenforced in the common uncontended case. Fixed by
evaluating `o.pred` against each decoded row right after the HOT-chain
follow (skip on NULL/false, matching `scanMatching`'s per-row semantics),
before building the SET row. Removed the workaround this exposed:
`planUpdate` (`internal/planner/planner.go`) no longer skips the index
fast-path when a view qual is present — it now takes the index path
unconditionally for `WHERE <indexed-col> = ...` shapes and folds the view
qual into the same `Filter` layer `extractScan` already merges with the
index's synthesised equality predicate, mirroring `planDelete`'s
pre-existing unconditional-index-path shape. This recovers the O(log n)
index probe for view-target UPDATEs that the earlier workaround had
traded away for correctness.

New test `TestUpdatableViewWhereQualEnforcedThroughIndexPath`
(`internal/executor/view_dml_test.go`) asserts both that the planner
actually picks `Update{Child: Filter{Child: IndexScan}}` for this shape
(so it can't silently pass via an unrelated fallback path) and that the
view qual is enforced through it (row excluded by the qual stays
unchanged; row included by the qual updates normally). All 6
pre-existing view_dml_test.go tests still pass unchanged.

Design doc `docs/design/root-0025-updatable-views.md` updated with a
"Follow-up" note under the latent-bug section (workaround replaced by the
root fix) and Deferred item 4 struck through as resolved;
`docs/design/README.md`'s root-0025 index row updated to match. Deferral
ledger row appended (status `resolved`) referencing the original item (5)
row; no new deferrals — this was a self-contained, previously-scoped fix
with no further discovery.

Committed (`82313ee3`) and pushed to `align-data-structure-with-pg`.

Next step: pick up one of the three options loop #109 left open (still
valid, none touched this loop): (a) continue the M0119-0004 pg_dump
catalog-view parity battery — re-verify whether FDW HANDLER/VALIDATOR
function references (`internal/parser/ddl.go:464`) are still genuinely
open before working on it (deferral-ledger triage has repeatedly found
"open" items already fixed by later slices); (b) pick a fresh unresolved
DU-002 slice — `TestPort_PgDumpConnectionSetup` has been the discovery
engine for slices up to 445 (all resolved as of this loop's ledger read),
so re-run it and probe for the next blocker the same way loops #104-#109
did; (c) resume root-0025's remaining deferred items (1)-(4) — column
subset/reorder/rename views (needs a `colMap []int`), view-of-view
chaining, `UPDATE...FROM`/`DELETE...USING` a view, or CHECK OPTION on
partition/inheritance-child-routed rows — each is a bounded, independently
scoped follow-up already documented in the design doc's Deferred section.

Gates run this loop: `go build ./...` clean; `go test ./internal/executor/...
./internal/planner/... ./internal/parser/...` PASS; `go test -race
./internal/executor/...` PASS (full suite — this touches concurrent
UPDATE/EPQ code, WAL/MVCC practice-card gate); `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pgbench smoke PASS via `.githooks/pre-commit` at
commit time; `make ralph-state-guard` self-repaired the same recurring
benign progress.json "completed" artifact noted in prior loops' carries
(not new).
