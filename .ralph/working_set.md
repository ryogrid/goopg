(idle — nothing in flight)

---

**Loop #37 (this session) — COMPLETE, committed + pushed (2 commits).**

Task: clean up the M0122-0005 (Types/opclasses/casts/collation/domains)
backlog bucket by closing stale audit entries via verify-before-implement
(continuing the pattern from the prior loop's function-cast-dumping and
domain-CHECK-renderer closures).

Landed (bookkeeping only, zero source-code changes):
1. Committed the prior loop's already-verified domain-CHECK-renderer
   stale-entry closure (`.ralph/fix_plan.md`, `.ralph/progress.json`,
   `unimplemented_feat.json`) that loop #36 had left staged pending the
   tpch-spotcheck gate — gate had already passed, so this loop just ran
   `go build ./...` (clean) and committed (`340787cb`).
2. Found and closed a third stale M0122-0005 entry this loop: the
   `pg_ts_config`/`pg_ts_config_map` "OID mapping not implemented" audit
   item. Verified the actual seeded OIDs already match PG18 verbatim
   (`pg_ts_config`=3602 per `postgres/src/include/catalog/pg_ts_config.h:30`,
   `pg_ts_config_map`=3603 per `pg_ts_config_map.h:30`), confirmed by
   `TestNailedLocalRelsContainsPgTsConfig{,Map}{,Indexes}` and
   `TestPgTsConfig{,Map}{IndexInitialEntries,AttrsTypeOIDsMatchPG18}` (all
   PASS). The audit had misread `mappedLocalCatalogPlaceholderOIDs`
   (`internal/initdb/initdb.go:1301`) — it deliberately keeps the old
   incorrect 3764/3765 placeholder OIDs alongside the correct ones (commented
   "stale") purely so `bootstrapMappedLocalCatalogHeaps` stubs an inert,
   unused relfilenode file at the legacy path; no functional gap. Updated
   `.ralph/fix_plan.md` M0122-0005 bucket + `unimplemented_feat.json` entry
   (`status: resolved`), committed (`5687a425`).

Concurrency note: a live peer `ralph_loop.sh` background tree (screen
`ralph`, pid 2960614+ / claude pid 2960620) was actively implementing
`dense_rank()` as a window function throughout this loop (`internal/
analyzer/analyzer.go`, `internal/planner/planner.go`, `internal/executor/
operators_window.go` + their test files + `docs/design/0020-0001-window-
parser-and-ast.md` + `docs/design/README.md`) — left completely untouched
by this loop, per `git status` pathspec discipline. Both bookkeeping commits
this loop used `git commit ... -- <exact files>` to stay disjoint from the
peer's in-flight source edits. `.ralph/fix_plan.md` and `unimplemented_feat.json`
are shared files the peer was *also* concurrently editing (their dense_rank
bookkeeping in the M0122-0004 bucket / a different JSON entry) — one Edit
attempt hit a "file modified since read" error mid-loop; re-read + reapply
resolved it cleanly with no content loss (both edits coexist, verified via
full diff before each commit). This bundling-across-loops pattern is
established/accepted per `.ralph/deferral_ledger.md`-adjacent memory notes,
not a conflict to resolve further.

Next step: M0122-0005 bucket now has only two open sub-items — 1-byte
`char`(OID 18) disambiguation (needs a parser change: `ct.Name="char"`
currently folds both quoted `"char"` and bare `char` together, see
`internal/catalog/codec.go:1356` `TypeNameToOID`) and `pg_collation_for()`
(large — "no collation tracking in v0" by design, likely out of single-loop
scope). Recommend picking up the 1-byte `char` disambiguation next as a
bounded parser-level fix, or move to a different bucket (M0122-0003 remaining
`write_time` I/O-timing hook, M0122-0006 on-disk catalog persistence, or the
next unresolved DU-002 slice in the deferral ledger) if the peer's dense_rank
work leaves M0122-0004 further advanced. Re-check `git status` + `pgrep -af
ralph_loop.sh` fresh at loop start — do not assume this snapshot; multiple
independent loops may still be running concurrently on this tree.

Gates run: `go build ./...` clean (both commits); `go test ./internal/initdb/...
-run 'TSConfig|TsConfig'` PASS; `go test ./internal/initdb/... -run
'TestNailed|TestMappedLocalCatalog'` PASS (broader sweep, no regressions);
pre-commit pgbench smoke PASS (both commits, machine-enforced hook); `make
ralph-state-guard` — 1 skew auto-repaired (stale progress.json completed-marker
from a prior loop's clean exit, not a real project-completion signal), OK
after repair.
