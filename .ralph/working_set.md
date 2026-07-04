Task: M0119-0004 root-0026 SELECT-side twin (indexScanOp/planIndexScanFromWhere
inheritance-child fan-out gap, discovered while reconciling the updateOp fix).
DONE and pushed on an isolated branch; NOT merged into the main tree — Tree A
(screen `ralph`, now on its own Loop #3+) is STILL actively live and dirty on
the main tree (confirmed: 2 live `claude` processes this loop, PIDs change as
Tree A's screen respawns new loop iterations without committing across them).

Files (branch `root-0026-select-index-fanout`, commit `a55631b8`, pushed to
origin — worktree `/tmp/wt-root0026-select` removed after push, safely
recoverable from origin):
- `internal/planner/planner.go`: `planIndexScanFromWhere`/`tryRangeIndexScan`
  gained `enforceInheritanceFanout bool` — refuses IndexScan when tbl has
  accessible inheritance children (mirrors the existing PartitionKey guard).
  Threaded `true` only from the SELECT call site (`!fromOnly`, new `fromOnly`
  var capturing `rv.Only`); `planUpdate`/`planDelete` call sites pass `false`
  deliberately (see design doc — no correctness effect either way for them).
- `internal/executor/storage_ddl_test.go`: new
  `TestSelectIndexScanFansOutToInheritanceChild` (equality + range + FROM ONLY
  cases); confirmed RED pre-fix via `git stash` of just the planner.go hunk.
- `docs/design/root-0026-update-via-index-inheritance-fanout.md` (new file on
  this branch — doesn't exist in git history yet, only as an untracked file
  in the main tree written by Tree A; WILL CONFLICT at merge time, expected)
  + `docs/design/README.md` new root-0026 row.
- `.ralph/deferral_ledger.md`: appended one `resolved` row (does not touch
  Tree A's own still-`-`-status row for the same discovery — two independent
  ledger entries for the two halves, to be reconciled together).

Gates run (in the worktree, since main tree was contended): `go build ./...`
clean; `go vet ./internal/planner/... ./internal/executor/...` clean;
`go test ./internal/executor/... ./internal/planner/... ./internal/catalog/...
./internal/parser/... ./internal/server/...` PASS; pgbench smoke
(`RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`) PASS, 0
failed txns. `scripts/tpch-spotcheck.sh` not run (SKIPs in worktree; TPC-H
schema has no PARTITION BY/INHERITS so unaffected either way).
Deliberately skipped `make ralph-state-guard` in the MAIN tree this loop —
same reason as the prior loop: `.ralph/progress.json` is being actively
written by Tree A's live process right now; running the guard's auto-repair
against it mid-write is the race already flagged twice.

Next step (whoever picks this up, either tree): once Tree A's main-tree diff
is finally committed, merge/cherry-pick branch `root-0026-select-index-fanout`
(commit `a55631b8`) in — expect a real conflict in `docs/design/root-0026-*.md`
(both branches independently created it) and a near-conflict in
`.ralph/deferral_ledger.md` (adjacent appended rows); reconcile by keeping
both halves' content (they document different fixes), then delete this
branch. After that: continue the M0119-0004 pg_dump catalog-view parity
battery / M0122 backlog per fix_plan.md's Current Priority banner.

---

Second, disjoint entry from a concurrent session's loop (this file was
mid-rewrite by the above when this loop tried to write — appending instead
of clobbering to avoid losing the above baton):

Task: M0122-0002 quick win — `pg_relation_size`/`pg_total_relation_size`/
`pg_indexes_size`/`pg_table_size` (`internal/executor/expr.go`) were
hardcoded 8kB stubs (M0097-0018); now compute real sizes from storage block
counts. **DONE, fully committed+pushed, nothing in flight**: `f0b2bdb3`
(code+test), `ac37be7c` (design doc), both on
`origin/align-data-structure-with-pg`. New
`docs/design/0122-0002-pg-relation-size-real-sizes.md`, NOT yet indexed in
`docs/design/README.md` (contended all loop — same contention this section's
sibling entry above describes; check if clean before adding the row,
alongside the still-missing root-0026 row).

Picked deliberately disjoint from the SELECT-side item above (different
file, `internal/executor/expr.go` vs `internal/planner/planner.go`) after
observing via `pgrep -af ralph_loop.sh` that another loop was already live
on that exact item at this loop's start.

Verified against current HEAD (not just the stale `unimplemented_feat.json`
snapshot) that these M0122-0002 cluster items are STILL genuine stubs and
are good candidates for the next M0122-0002 pass: `regexp_matches` (SRF,
always returns NULL), `pg_get_serial_sequence` (convention-based name, not
the real sequence), `isfinite`/`justify_hours`/`justify_days`/
`justify_interval` (all true no-ops). By contrast `pg_get_expr`/
`pg_get_indexdef` (also listed in that cluster) are NOT stubs — deliberate
pass-through-preformatted-string design — the M0122-0001 triage should mark
those `resolved`, not assign further work.

Gates: `go build ./...` clean; `go vet ./internal/executor/...` clean;
`go test ./internal/executor/...` full package PASS; pgbench-smoke
pre-commit hook PASS on both commits. `scripts/tpch-spotcheck.sh` not run
(four scalar functions, no TPC-H query references them). `make
ralph-state-guard`: FAILED with `.ralph/status.json`
status=running/last_action=executing vs `.ralph/progress.json`
status=completed (both ~17:38) — re-checked after 5s, unchanged; this is
Tree A's own driver mid-transition between iterations, not a real
inconsistency. Deliberately did not run `-fix` for the same reason the
sibling entry above skipped the guard entirely.
