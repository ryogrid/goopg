Task: M0141-S2a-fix1-sweep-a — narrow the ORDERED upper rel's Sort cost
inputs (NCols/AvgVarBytes). DONE and committed this loop (`46102b5d6` code,
`c63ed6eb2` docs/fix_plan). Banner item 3 ("Roll out fix1's success") still
has **M0141-S2a-fix1-sweep-b** open (`[ ]`, selectable) — WINDOW's internal
sort costing, same shape as sweep-a but for `windowsetoppaths.go`'s
`costWindow`/`addWindowPaths`/`sizeWindowRelFromNode`. **NEXT LOOP should
re-read the banner first** (S1 precedence) — if it still names item 3's
order as "fix1-sweep's own filed children, then M0141-S2b-6-resume, then
M0139-0007c", select sweep-b next; otherwise follow whatever the banner
says.

Files: new `internal/optimizer/ordered_input_narrow.go`
(`finalSelectOutputNames`, `deriveOrderedSortInputKeep`,
`narrowOrderedRelWidths`). `internal/optimizer/planner.go` (new
`orderedNarrowKeep` computation right after ORDER BY keys resolve, guarded
`selectSrfPending == nil`; threaded into both `createOrderedPaths` and
`electOrderedGrouping` calls; the other 2 `createOrderedPaths` call sites —
`wrapSetOpSortLimit`, `wrapMinMaxOrderByDistinct` — pass `nil`).
`internal/optimizer/upperordered.go` (`createOrderedPaths` gained trailing
`narrowKeep []int` param, calls `narrowOrderedRelWidths` after
`sizeUpperRelFromNode`). `internal/optimizer/upperorderedgrouping.go`
(`electOrderedGrouping` gained the same param, same call — sibling site,
found only after measurement showed TPC-H Q18 never reaches
`createOrderedPaths` at all). ~16 test call sites across
`upperordered_test.go`/`cost_sort_bound_test.go`/
`incrementalsortpaths_test.go`/`sort_pgrelationbytes_test.go`/
`upperordereddistinct_test.go` updated to pass `nil`. Design doc:
`docs/design/0100-0149/m0141-s2a-fix1-sweep-a-ordered-sort-width-currency.md`
(new, full root-cause writeup). `docs/design/README.md` (new index row).
`.ralph/fix_plan.md` (task ticked `[x]` with DONE sub-bullet).
`analysis/leftdeep-joins/m0141-s2a-fix1-sweep-a-{before,after}.{txt,
plans.txt,pg.plans.txt}` (6 new committed artefacts).

Key symbols: `sizeUpperRelFromNode` (`upperrel.go:178`), `createOrderedPaths`
(`upperordered.go:64`), `electOrderedGrouping` (`upperorderedgrouping.go:178`,
its own `sizeUpperRelFromNode(ordered, agg.node)` call at line ~219 is the
sibling this loop also fixed). Tools used:
`scripts/tpch-acceptance-arm.sh` (values A/B, `ACCEPT_BASELINE=`),
`scripts/tpch-estimate-audit-arm.sh PLAN_ONLY=1 --ref-port 65432
REFERENCE=` (shape capture — note the committed default REFERENCE file
`analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt` no longer
exists; always pass `REFERENCE=` with `--ref-port` or the script exits rc=2),
`docs/design/not_ralph/plan_parity_fix_take2/methodology/shape-delta.sh`,
`scripts/pg-plan-parity-diff.py`, `scripts/tpcds-sf025-regression.sh sweep`.

Finding: **confirmed zero-movement result, root-caused not just measured.**
TPC-H `shape-delta.sh`: `queries=22 text-changed=0 shape-changed=0` — this
held BEFORE discovering the `electOrderedGrouping` sibling gap too, meaning
the mechanism is genuinely inert for this corpus, not merely declining
silently (values gate proves the narrowing logic itself fires correctly —
if `finalSelectOutputNames`/`deriveOrderedSortInputKeep` had a bug that
always declined, that would ALSO show zero movement, so the root-cause
step mattered). Root cause: a GROUP BY aggregate's own published row
(goopg's `Aggregate` node, mirroring PG's `Agg`) already equals the
minimal SELECT-list width by construction — there is no hidden extra width
for this keep-set (sort keys ∪ final SELECT list) to trim away in either
TPC-H's or TPC-DS SF0.25's actual `ORDER BY` shapes. TPC-H Q18 (recon's
named witness) is itself exactly this shape. A REAL currency gap for this
site would need a plain (non-aggregate) `ORDER BY` over a join/scan tree
wider than the SELECT list — neither corpus happens to hit that shape at a
cost-visible scale. Lesson for a future site-narrowing task: check whether
the motivating witness query is a GROUP BY shape BEFORE assuming
`createOrderedPaths` (not `electOrderedGrouping`) is the only site to fix —
this task's own recon missed the sibling because M0141-S2b's
`electOrderedGrouping` loop was filed and landed AFTER the fix1-sweep recon
that named Site A.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` PASS
(full package). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34, gate-stamp
PASS against staged code — required TWO runs: the first stamp was for the
pre-stage tree hash and the commit-msg hook rejected it, so gates must be
re-run AFTER `git add`, not before). `scripts/tpch-acceptance-arm.sh`
before/after digest diff: VERDICT PASS, 24/24 labels (also required a
staged-tree re-run for the commit-hook's PASS-required check — a
`NO-COMPARE` stamp, from running the script without `ACCEPT_BASELINE`,
does NOT satisfy the hook; it needs `ACCEPT_BASELINE=<HEAD-binary-arm-output>`
against the current tree's own build). `scripts/tpcds-sf025-regression.sh
sweep`: PASS (MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan-shape
changed=0), re-run once against staged tree. `pg-plan-parity-diff.py`
(TPC-H, same-PG-reference control): `match=7` identical before/after,
`CATEGORIES-EXCL-MATCH` identical digit-for-digit. Pre-commit hook's
pgbench smoke: PASS x2 (one per commit). `make ralph-state-guard`: same
self-repairing status/progress mismatch pattern as prior loops (prior
loop's clean-exit "completed" marker read as stale); self-repaired to
"in_progress", clean on re-check.

In-flight: none. Two detached worktrees this loop created
(`/tmp/wt-sweepa-baseline` for the initial A/B measurement,
`/tmp/wt-sweepa-gate` for the post-stage gate-stamp re-run — both built the
pre-change baseline binary at HEAD `6f7acae46`) were removed via `git
worktree remove --force`, confirmed via `git worktree list`. All
private-port servers (5582/5583 across 5 separate arm invocations this
loop) were stopped by their own scripts' EXIT traps; verified only the
legitimate shared `:65433` reference server remains running (`systemctl
--user list-units 'goopg-*'`). All temp binaries/output files under `/tmp/`
removed after use. No shared cluster (`:65432`/`:65433`/`:65437`/`:65438`)
was started, stopped, reset, or written beyond read-only
`pg_basebackup`/`SELECT`/`EXPLAIN`.
