Task: M0122-0016 — Autovacuum: honor `autovacuum_enabled` reloption
(`unimplemented_feat.json` task M0086). COMPLETE this loop, committing next.

Files: internal/autovacuum/launcher.go (needsVacuum/needsAnalyze gate on
tbl.AutovacuumEnabledSet/AutovacuumEnabled), internal/autovacuum/
launcher_test.go (2 new tests), internal/catalog/catalog.go (stale doc
comment on AutovacuumEnabled fixed — field is no longer catalog-only),
docs/design/0122-0016-autovacuum-enabled-reloption.md (new),
docs/design/README.md (index row added), .ralph/fix_plan.md (M0122-0016
checkbox -> [x]), unimplemented_feat.json (M0086 status open->resolved,
65/181 resolved now).

Key symbols: Launcher.needsVacuum/needsAnalyze (internal/autovacuum/
launcher.go), catalog.Table.AutovacuumEnabled/AutovacuumEnabledSet
(pre-existing, just newly consumed).

Findings: this was a small, well-scoped M0122 backlog pickup (selected via
an Explore-agent survey of the 117 open unimplemented_feat.json entries —
other candidates surveyed but not picked: m0097-0009 pg_get_serial_sequence
convention-guessed name, m0097-0004 isfinite() always-true stub,
M0021-0002 bare-aggregate-in-FOR-UPDATE not rejected, M0087 loadTables
no-op for non-InMemory catalogs — any of these would be reasonable next
quick-wins). Upstream reference: postgres/src/backend/postmaster/
autovacuum.c relation_needs_vacanalyze (~line 3054) — av_enabled check
gated by !force_vacuum (anti-wraparound overrides the user's disable);
goopg's pre-existing RelFrozenXID/autovacuumFreezeMaxAge anti-wraparound
check already ran first in needsVacuum, so only had to add the disable
gate below it (and mirror it in needsAnalyze, since upstream disables
both). Confirmed non-vacuous via git stash on launcher.go alone (new test
fails without the fix). M0119-0004's own next milestone-scale blocker
(collation "builtin_coll" already exists — no per-database catalog
namespace at all, confirmed NOT a 1-loop task by the survey) and
M0119-0006's sole residual (005_opclass_damage.pl, needs a pg_amproc
Virtual-UPDATE path AND btree opclass/comparator dispatch — also not
1-loop) were surveyed and explicitly passed over in favor of this smaller
task; they remain the next M0119 candidates whenever a multi-loop
architectural task is in scope.

Next step: commit this work (pathspec: the files listed above, NOT
`postgres` submodule which shows modified/untracked pre-existing at
session start — untouched by this loop). Then pick the next task: either
another unimplemented_feat.json open quick-win (see Findings list above),
or start scoping M0119-0004's collation/per-database-catalog-isolation
gap or M0119-0006's 005_opclass_damage as a proper multi-loop milestone
item (needs its own design doc + explicit multi-loop scope acknowledgment
first, per the M0119 per-task rule).

Gates run: go build ./... clean. go test ./internal/autovacuum/... PASS
(3/3, including 2 new tests). RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh PASS (0 failed transactions, all 3
workloads: standard/simple-update/select-only). make ralph-state-guard
PASS (auto-repaired 1 benign issue, identical pattern to every prior loop).

In-flight: none (no background agents or long-running processes left
running; the Explore-agent survey used for task selection already
returned and is fully incorporated above).
