(idle — nothing in flight)

Last completed (about to commit): M-NIGHTLY (run 20260715-010036 triage) —
root-caused + fixed the `cmd/goopg`/`internal/amcheck` "units-timeout"
mystery left open by the last 2 loops. It was a classifier bug in
`ci/batch/lib/summarize.py`, NOT a product hang.

Reproduced the whole `units` package set failing identically (`cmd/goopg`/
`internal/amcheck`/`internal/initdb`/`internal/mvcc`) in 16.5 minutes under
the EXACT nightly cgroup config (`GOOPG_MEM_HIGH=6G MEM_MAX=8G
MEM_SWAP_MAX=0 GOMEMLIMIT=5GiB GOFLAGS=-p=4`, matching
`ci/batch/stages/stage-units.sh`) — confirms genuine multi-package resource
contention, not per-package flakiness (`initdb` was already proven clean
standalone). `cmd/goopg`/`internal/amcheck` die via a bare `signal: killed`
(unambiguous cgroup/OOM per `ci/design/03` §C). Root cause:
`summarize.py`'s existing `looks_resource_killed(log) and "--- FAIL" not in
log` rule ran over the WHOLE combined ~40-package log instead of
per-package — `internal/wal`'s one genuine `--- FAIL` that night (already
fixed by an earlier loop today) flipped the guard for the whole `units`
stage, misreporting the 2 pure resource-kills as regressions. Same bug,
inverted, silently swallowed `race/internal/access/btree`/
`race/internal/amcheck` (real, NEVER-before-surfaced `-race` failures) into
one uninformative whole-stage notice on every prior night.

Fix: `split_go_test_pkg_blocks()` in `ci/batch/lib/summarize.py` — splits a
`go test` log into per-package blocks on `ok`/`FAIL`/`?` lines; the
units/race classification loop now runs resource-kill-vs-regression
per-block. New `ci/batch/lib/test_summarize.py` (stdlib unittest, 4 tests,
all PASS) using a synthetic fixture modeled on the real log, PLUS
cross-checked directly against the real
`ci/logs/20260715-010036/units+race/go-test.log`. Regenerated
`ci/logs/action-items.md` from the real historical logs (kept — correctly
reflects the fixed classifier); reverted an incidental duplicate append this
produced in `ci/logs/history.jsonl` (git checkout — append-only file, must
not gain a phantom entry from a manual verification run).

fix_plan.md: 1 new [x] M-NIGHTLY entry (the fix) + 2 new [ ] M-NIGHTLY tasks
(`race/internal/access/btree`, `race/internal/amcheck` — brand new findings,
NOT investigated this loop, time-boxed). Deferral ledger: 1 new `resolved`
row (this fix) narrowing the `internal/initdb`/`internal/mvcc` still-open
row + noting the 2 new race items. Design doc
`docs/design/root-0027-nightly-classifier-per-package-resource-kill.md` +
README index.

Next step: pick the highest-priority M-NIGHTLY item — likely
`race/internal/access/btree` (real concurrency-bug history, e.g. M0110-0007)
or `race/internal/amcheck`, neither ever investigated. Repro:
`go test -race -timeout 15m ./internal/access/btree/` (and `./internal/amcheck/`)
standalone first; if clean, use the SAME concurrent-load repro technique
this loop validated (full package set through `scripts/goopg-test-run.sh`
with the nightly's exact env) to try to force it, capturing a FULL
(non-truncated) goroutine dump if it hangs. `internal/initdb`/`internal/mvcc`
(units) remain open too — same repro technique applies.

Gates run: `python3 ci/batch/lib/test_summarize.py -v` 4/4 PASS; `python3 -m
py_compile ci/batch/lib/summarize.py` clean; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh` PASS (exit 0, 0 failed both workloads);
`make ralph-state-guard` — found+auto-repaired 1 stale status/progress
inconsistency (previous loop's clean-exit marker), consistent after repair.

In-flight: none. About to `git add`/`git commit` (pathspec-scoped to my own
files — `.ralph/{deferral_ledger,fix_plan,working_set}.md`,
`ci/batch/lib/{summarize.py,test_summarize.py}`, `ci/logs/action-items.md`,
`docs/design/{README.md,root-0027-*.md}`) then push. Untouched foreign/stray
files present (`analysis/tpch-explain-baseline.md`, `ci/logs/launch.log`,
`postgres` submodule dirty, `weekly_loc.*`, `analysis/perf-optimize3/runs/*`)
— same as every prior loop, left alone.
