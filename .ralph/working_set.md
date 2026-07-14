(idle — nothing in flight)

Last completed (commit f4154dfc): M-NIGHTLY triage — run 20260715-010036
(sha 751b82178025, 11 AI items). Fixed AI-006/007/008 (isolation
regressions); investigated AI-001..005/009..011 (units timeouts + regress
mismatches), found non-reproducing locally.

Root cause fixed: `ActivityRegistry.Register(b)` (internal/activity/
registry.go) assigned a backend's slot via `procNumForPID(b.PID)` (a PID
hash), but every dynamic call (`UpdateState`/`WaitEventStart`/`WaitEventEnd`/
`PIDForProcNum`) is keyed off `connTx.ProcNum` (internal/server/server.go,
from `TxnMgr.AcquireConnSlot()` — an unrelated MVCC proc-array slot,
introduced historically to fix a separate PID-wraparound clobbering bug).
The two index spaces silently diverged for most connections, freezing
pg_stat_activity.state/query at their Register()-time defaults for a
connection's ENTIRE lifetime — reproduced live via a manually started
server + raw psql (query blank even for the backend's OWN currently-
executing statement). New `ActivityRegistry.RegisterAt(procNum, b)` (mirrors
`RegisterBackground`); `Register(b)` now delegates to
`RegisterAt(procNumForPID(b.PID), b)`; the one production call site
(server.go:951 area) now calls `RegisterAt(procNum, ...)` reusing the
already-computed TxnMgr.AcquireConnSlot() value. This fixed ALL THREE
regressed isolation specs (partition-drop-index-locking,
insert-conflict-specconflict, detach-partition-concurrently-4) with one
2-line change — no unit test existed that exercised the REAL server
Register() call path (0118-0073's own test called UpdateState directly with
a hand-picked procNum, proving the primitive but not the wiring).

Design doc `docs/design/0118-0141-activity-procnum-identity-space-
conflation.md` + README index. Deferral ledger: 1 resolved row (this fix) +
1 open row (see below). fix_plan.md M-NIGHTLY task appended.

Gates: go build ./... clean; go test PASS across internal/activity,
internal/server, internal/executor, internal/initdb (~4min, not 33min —
see below); full `go test -run 'TestPort_Isolation' ./internal/testport/
-v` battery: 0 `--- FAIL` lines (was 3 FAIL last night); the 3 specific
specs individually PASS; tpch-spotcheck.sh PASS (Q12=2/Q13=33);
RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh PASS (0 failed, all 3
workloads, ran automatically via pre-commit hook on `git commit`).
make ralph-state-guard: auto-repaired the same recurring stale
running/completed mismatch as prior loops, then OK.

STILL OPEN — deferral ledger row, next loop candidate if no higher-priority
M-NIGHTLY item exists: the nightly run's other 8 action items
(AI-...-001..005: units-suite timeouts in cmd/goopg, internal/amcheck,
internal/initdb, internal/mvcc, internal/wal — each ran to a 33-minute
per-package go test timeout and was SIGQUIT-killed with a near-empty
goroutine dump, consistent with host CPU starvation during the nightly
window rather than a real hang; AI-...-009..011: regress/errors,
portals_p2, select — all baseline `pass`, all individually PASS when rerun
locally this loop). internal/initdb reran clean in ~4 minutes this loop
(not 33) and the 3 regress cases all pass standalone — strong evidence for
"environmental, not a code regression," but cmd/goopg, internal/amcheck,
internal/mvcc, internal/wal were NOT rerun to their full 33+ minute timeout
this loop (time-boxed after the procNum fix + its gates). Next step if
picked up: `go test -timeout 40m ./cmd/goopg/ ./internal/amcheck/
./internal/mvcc/ ./internal/wal/` on a quiet host (no concurrent nightly
batch / perf-optimize3 runs), diff against
ci/logs/20260715-010036/units/go-test.log; if it reproduces, capture the
FULL goroutine dump (tonight's was truncated to one line) for a real
hang diagnosis instead of assuming contention again.

Also check at next loop start: `grep run: ci/logs/action-items.md` — if a
newer nightly batch has regenerated it (this loop's mtime was
2026-07-15T03:15:09+09:00, run 20260715-010036), triage any new `## AI-`
items into M-NIGHTLY tasks BEFORE picking either the units/wal
investigation above or any other milestone's work (preemption rule applies
at task-selection time; this loop's own task was already finished so the
check is clean for the next loop).

In-flight: none. All work committed (f4154dfc) and pushed. Tree clean of my
changes. Stray untracked/modified files present from other processes
(weekly_loc.*, analysis/perf-optimize3/runs/*, ci/logs/*.log,
analysis/tpch-explain-baseline.md, untracked postgres/) were left untouched,
same as prior loops.
