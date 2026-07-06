(idle — nothing in flight)

Loop #12 completed and committed: UPDATE / upsert NOT NULL, CHECK, and domain
constraint enforcement (M0122-0005 follow-up, closes the 2026-07-06 deferral
ledger row "UPDATE enforces no table-level NOT NULL/CHECK constraints at
all"). New shared `checkRowConstraintsForWrite` (internal/executor/
operators_fk.go), wired into all 3 updateOp write paths (Next/SeqScan,
updateViaIndex, updateWithFrom) in operators_storage.go, plus upsertOp's
applyInsert/applyUpdate in operators_upsert.go. Each updateOp site had to
check BEFORE tryApplyHOTUpdate (hotUpdateEligible is plan-level, independent
of constraint violations) — a check only in the "!used" branch would miss
every HOT update. Also found+fixed a latent independent bug: updateViaIndex's
"M0111-0001" restore-loop unconditionally reverted any explicit `SET col =
NULL` back to the old value on the indexed fast path (removed both
occurrences). New tests: internal/executor/update_constraint_enforcement_test.go
(6 tests, one per write path). Verified: go build clean; full
`go test ./internal/executor/...` PASS; tpch-spotcheck PASS (Q12=2/Q13=33);
ralph-precommit-test.sh PASS (full suite + pgbench TPC-B/simple-update/
select-only smoke, 0 failed). Design doc + README updated; ledger row
appended (status=resolved) with one residual noted (cross-partition-move
UPDATE checks source table not destination leaf — same shape as insertOp's
pre-existing parent-level CHECK gap, bounded/deferred).

Next candidate (pick ONE): resume the M0110-0001 multi-database isolation
survey (fix_plan.md "Current Priority" banner), or survey
.ralph/deferral_ledger.md for another fresh open (`status = -`) row — e.g.
the cross-partition-move residual just noted above, or the "views inline
with no view-owner identity" RBAC gap noted under M0122-0008.
