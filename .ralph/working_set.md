Task: M0141-S2a-fix1-sweep-b — narrow WINDOW's internal sort cost inputs
(costWindow/addWindowPaths/sizeWindowRelFromNode). DONE and committed this
loop (`cd7767b8b` code, `0ead98e6f` docs/fix_plan). M0141-S2a-fix1-sweep's
own two filed children (sweep-a, sweep-b) are BOTH now closed. **NEXT LOOP
should re-read the banner first** (S1 precedence) — if it still names item
3's order as "fix1-sweep's own filed children (done), then
M0141-S2b-6-resume, then M0139-0007c", select **M0141-S2b-6-resume** next
(fix_plan.md ~line 3350, gated on the M0142-0003k TPC-H cluster reload —
verify that gate is actually satisfied at `:65433` post-P0-E6/E7 restore
before assuming it's still blocked); otherwise follow whatever the banner
says.

Files: new `internal/optimizer/window_sort_narrow.go`
(`deriveWindowChainNarrowKeeps`, `deriveWindowRelNarrowKeep`,
`narrowWindowRelWidth`). `internal/optimizer/windowsetoppaths.go`
(`createWindowPaths` gained trailing `chainKeep [][]int`/`relKeep []int`
params; `addWindowPaths` gained trailing `chainKeep [][]int` + new
`narrowedWindowCols` helper). `internal/optimizer/planner.go`
(`buildWindowStage` gained a `starPS *ProjectSet` param; computes both
keep-sets once, right after its per-group loop and before
`createWindowPaths`, via a throwaway `windowSurface` byte-identical to its
own later return value; its own call site at ~planner.go:1880 now passes
`ps`). 4 test call sites in `windowsetoppaths_test.go` updated to the new
params. Design doc:
`docs/design/0100-0149/m0141-s2a-fix1-sweep-b-window-sort-width-currency.md`
(new). `docs/design/README.md` (new index row). `.ralph/fix_plan.md` (task
ticked `[x]` with DONE sub-bullet).

Key symbols: `windowWindowInputNames`/`stampWindowInputTarget`
(`window_input_target.go` — the pre-existing B-01c compute-only stamp this
task's own recon-time premise wrongly said didn't exist; it existed but was
never consumed for costing, which is exactly what this task did),
`finalSelectOutputNames` (`ordered_input_narrow.go`, sweep-a's helper,
reused verbatim), `costWindow`/`addWindowPaths`/`sizeWindowRelFromNode`
(`windowsetoppaths.go`).

Finding: **measured provably inert today, not just corpus-inert** — unlike
sweep-a's ORDERED rel (which at least carries a `PlanCost` `EXPLAIN` could
show under a future 2nd candidate), the WINDOW rel's own design doc states
NO second candidate ever competes at this rel (`addWindowPaths` always
offers exactly one `PathWindow` chain) AND `*WindowAgg` carries no
`PlanCost` at all — so this narrowing literally cannot move any observable
output under today's single-candidate WINDOW design, confirmed rather than
assumed (TPC-DS SF0.25's 99-query corpus, the one with real window-function
density, shows `PLAN-SHAPE changed=0`). Methodology trap hit and resolved
this loop: the first TPC-H acceptance-arm attempt used `PGSHAPED=0`
(copied from an unrelated P0-E7 A/B note in a stale mental model) and got
`VERDICT: FAIL` — Q9 timing out identically in both arms at exactly the
`PER_Q` cutoff (900.0xs both times, twice, at two different `PER_Q`
values) is a symptom of the WRONG planner shape (`GOOPG_PGSHAPED_DP` is
`unset(on)`, i.e. DP-shaped, by default; `PGSHAPED=0` forces the retired
non-DP path). Re-running with `PGSHAPED=1` (sweep-a's own precedent, the
actual default) got a clean `VERDICT: PASS, 24/24 MATCH` on the first try
— **always pass `PGSHAPED=1` for a values-comparison arm unless the task is
specifically an on/off A/B**, the `PGSHAPED=0` figure in older working-set
notes was for a *different*, unrelated experiment, not a general default.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` PASS
(full package). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34), staged-tree
stamp `code_tree=7cdbef9e...` PASS. `scripts/tpcds-sf025-regression.sh
sweep`: PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shapes
99/99 identical, staged-tree stamp PASS. `scripts/tpch-acceptance-arm.sh`
before/after digest diff (`PGSHAPED=1`, private port 5583,
`GOOPG_ANALYZE_SEED=20260905`, `NO_BUILD=1` two pinned binaries): VERDICT
PASS, 24/24 labels MATCH, staged-tree stamp PASS. `python3
scripts/ralph_protected_regions.py check-designdocs` exit 0. Pre-commit
hook's pgbench smoke: PASS x2 (one per commit). `make ralph-state-guard`:
clean, no repair needed this time (prior loop's own repair from last time
held).

In-flight: none. Two detached worktrees this loop created
(`/tmp/wt-sweepb-baseline`, replaced by `/tmp/wt-sweepb-baseline2` after a
methodology correction — both built the pre-change baseline binary at HEAD
`9852fda5f`) were removed via `git worktree remove --force`, confirmed via
`git worktree list`. All private-port servers (5583 across 4 separate arm
invocations this loop, two of them the `PGSHAPED=0` false-start) were
stopped by their own scripts' EXIT traps; verified only the legitimate
shared `:65433` reference server remains running (`systemctl --user
list-units 'goopg-*'`). All temp binaries/output files under `/tmp/`
removed after use. No shared cluster (`:65432`/`:65433`/`:65437`/`:65438`)
was started, stopped, reset, or written beyond read-only
`pg_basebackup`/`SELECT`/`EXPLAIN`.
