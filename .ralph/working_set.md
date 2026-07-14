(idle — nothing in flight)

Last completed (this loop, about to commit): M-NIGHTLY (run 20260715-010036
triage) — closed the LAST open item, `internal/initdb`/`internal/mvcc`'s
ambiguous-SIGQUIT `units`-lane ("hang") timeouts.

No product code touched. Re-ran the exact nightly `units`-lane repro
(`ci/batch/stages/stage-units.sh`'s own command: all 44 non-excluded
packages, `GOOPG_MEM_HIGH=6G MEM_MAX=8G MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB
GOFLAGS=-p=4`, `scripts/goopg-test-run.sh`, `-timeout 30m`) now that last
loop's `amcheck` debug-instrumentation fix is in place — `internal/initdb`
(237.79s) and `internal/mvcc` (1.30s) both PASS cleanly, 0 FAIL anywhere, no
`signal: killed`/SIGQUIT/panic in the log. Confirms their nightly timeouts
were the same collateral resource-starvation class as `cmd/goopg`/`amcheck`'s
already-classified resource kills (amcheck's pre-fix debug-tracing-bloated
stress test was hogging the shared memory-capped co-load window). This
closes ALL 11 `AI-20260715-010036-*` items from the nightly triage thread —
no open M-NIGHTLY items remain as of this loop.

fix_plan.md: new [x] item appended after the btree/amcheck fix (search
"internal/initdb`/`internal/mvcc` last-open items confirmed resolved").
Deferral ledger: new `resolved` row appended (last row in the file) closing
the still-open row from 2 loops ago. Design doc
`docs/design/root-0028-amcheck-realtree-stress-debug-instrumentation-cleanup.md`
"Follow-up (2026-07-15)" section + README index row updated.

Next step: check `ci/logs/action-items.md` for a NEW nightly run first
(regenerates nightly; 20260715-010036 is now fully closed) per the M-NIGHTLY
preemption rule. If none, resume normal fix_plan.md priority work — the
M-NIGHTLY queue is empty for the first time in several loops.

Gates run: full 44-package nightly-config repro run (0 FAIL, this loop's
actual verification); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` — first
attempt hit 1 transient pgbench failure (0.009%, 11761 txns), unrelated to
this loop's docs/ledger-only diff (no Go code touched); retry PASS clean (0
failed, all 3 workloads). `make ralph-state-guard` — found+auto-repaired 1
stale status/progress inconsistency (prior loop's clean-exit marker, same
pattern as every recent loop), consistent after repair.

In-flight: none. About to commit (docs/ledger/fix_plan only — no product
code, no test code). Untouched foreign/stray files present at loop start
(`analysis/tpch-explain-baseline.md`, `ci/logs/launch.log`, `postgres`
submodule dirty, `weekly_loc.*`, `analysis/perf-optimize3/runs/*`) — same as
every prior loop, left alone (not part of this loop's diff). Also noted: a
long-lived leaked `goopg` server process from a prior `TestPort_RegressSuite`
run (PID ~3848262, since Jul14, port 44791) is idle-resident but harmless —
not part of this loop's work, left alone (belongs to another
session/process, not this ralph loop).
