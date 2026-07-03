(idle — nothing in flight)

M0121-0002 CLOSED (2026-07-04, commit bd170262, pushed). Root cause: planner's
`tryPromoteIndexOnlyScan` (`internal/planner/planner.go`) narrowed a promoted
`Filter(IndexOnlyScan)`'s covered/output schema using only the `Project`'s
target list, never checking whether the surviving `Filter.Predicate` also
referenced a column, and never remapping that predicate's `ColumnRef.Index`
off its pre-promotion (full-row) position — panicked `Slot.Get` whenever
WordPress's `wp_set_object_terms` query matched a real `object_id` row.
Fixed by extending `covered` with any column the residual filter still needs
(index-permitting; else abandon the promotion) and remapping via new
`remapColumnRefsToSchema`. Design doc
`docs/design/0121-0002-indexonly-scan-residual-filter-remap.md`; regression
test `internal/executor/indexonly_residual_filter_test.go` (confirmed it
panics with the same signature when the fix is reverted). Re-verified
end-to-end via WP-01..WP-03 against the real WP-CLI/docker instance after a
`reset_wp_schema.sh` re-seed (schema had drifted stale again, unrelated to
this fix) — post update/trash now succeed, no panic in `wp/goopg-wp.log`.

Next recommended task: **M0121-0001** — trivial one-line tick. M0120-0005's
aggregate `report.md` already confirmed M0121-0002 was the *only*
goopg-attributable failure seeded from the M0120 triage sweep (all others are
harness/pg4wp-limitation, documented in `CHECKLIST.md`, no ledger row
needed). With M0121-0002 now done, M0121-0001's "populate the task list"
scope is fully satisfied — just flip its checkbox to `[x]` in
`.ralph/fix_plan.md` (was left unchecked this loop only to respect the
one-task-per-loop rule) and consider M0121 milestone CLOSED. After that,
resume **M0110** (pg_dump TAP, paused) per the Current Priority banner, or
check for a newer directive at the top of `.ralph/fix_plan.md`.

Gates run this loop: `go build ./...` clean; `go test ./internal/planner/...
./internal/executor/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/
Q13=33); `make ralph-state-guard` (self-repaired the same recurring stale
"completed" progress marker pattern as prior loops, then passed); pre-commit
pgbench smoke (TPC-B/simple-update/select-only, 0 failed transactions) PASS.
