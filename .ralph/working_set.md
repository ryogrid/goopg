Task: M0139-0005 — "re-measure Q4's grouping election ratio" (plan-parity
milestone group, recon task). **COMPLETE and committed** this loop, branch
`plan-parity-with-pg-take2-ralph`.

Files: `docs/design/0100-0149/m0139-0005-q4-grouping-ratio-remeasurement.md`
(new, full method + verdict), `docs/design/README.md` (+index row),
`.ralph/fix_plan.md` (M0139-0005 checked off + DONE summary). No production
`.go` file changed — recon task per the plan-parity harness carve-out. Two
production files were temporarily instrumented for live measurement then
fully reverted before commit (`internal/optimizer/upperorderedgrouping.go`,
`internal/optimizer/joinleghook.go` — `git diff --stat` confirmed empty on
both). A throwaway probe test
(`internal/testutil/tpch/zz_probe_m0139_0005_test.go`) was written, run, and
deleted before commit, matching the M0139-S1/S2/S3 precedent.

What was done: built a private, disposable HEAD cluster (20,000 orders via
`internal/testutil/cluster` + `scaleLoader`, never touching the shared
peer-owned `:65433` TPC-H bench server), set `GOOPG_DEBUG_M0139_0005=1` in
the test process env before `cluster.Start()` (server subprocess inherits
it), ran `EXPLAIN (VERBOSE, COSTS ON)` on canonical Q4, and read the
server's log file for temporary debug lines added to `electOrderedGrouping`
(dumps every ORDERED-rel candidate's Startup/Total cost) and `narrowJoinLeg`
(dumps every call + decline reason + which keep-set tier fired). Result:
**`narrowJoinLeg` is never called at all** for Q4 — zero debug lines in the
whole server log, not even a decline. Traced why: Q4's `EXISTS` is
decorrelated by `unnestExistsExpr` (`internal/optimizer/unnest.go:4078`),
which builds `&Join{Type: JoinTypeSemi, Algo: JoinAlgoHash, ...}` DIRECTLY
on the raw parse tree, before any search joinrel exists — never routing
through `createHashJoinPlan`/`joinInputsFor`, the one call site
`narrowJoinLeg` is wired into. This is the same fork METHODOLOGY3 F11/K63
(R73-R77) already named for the costing symptom ("no SEMI path is ever
filed... before any search joinrel exists, so no sjinfo -> no joinrel -> no
path -> no price", later given a display-seam patch by R77 and further
root-caused by the 2026-09-15 `m0137-0011` ledger row's `Filter`-embed gap).
This task establishes the narrowing-specific corollary: the same bypass
that stops a real Path/price from being filed for Q4's semi join also stops
`joinInputsFor`'s narrowing chain from ever being invoked on it.

Key symbols: `electOrderedGrouping`/`groupingEmissionPathkeys`
(`internal/optimizer/upperorderedgrouping.go:148`), `narrowJoinLeg`
(`internal/optimizer/joinleghook.go:69`), `unnestExistsExpr`
(`internal/optimizer/unnest.go:4078`), `createHashJoinPlan`/`joinInputsFor`
(`internal/optimizer/createplanjoin.go:545,361`).

Hypothesis/Findings: **verdict — the ratio has not moved and cannot move
under the current mechanism.** Q4's grouping input width is byte-identical
to before M0139-S1/S2 landed, independent of scale — a bigger-scale
re-measurement of the exact 1.0086-vs-1.01 number would not be informative
until the `unnestExistsExpr` bypass itself is addressed (a materially
larger task: wiring a real Path, or the narrowing hook, into the
EXISTS/IN-decorrelation rewrite — touches the already-tracked
F11/K63/`m0137-0011` lineage, not a narrowing-only patch). Secondary,
not-pursued-further finding: at 20,000-order scale the two ORDERED-rel
candidates aren't even fuzzy-tied (only one survives `addPath` outright) —
scale sensitivity in the underlying cost comparison, a different question
than this task was scoped to answer. Feeds M0139-0006's owner packet
alongside S3's residue finding (Q12: hook fires but retains more than
hoped; Q4: hook cannot fire at all — same family of gap from opposite
ends). Does not decide or advance `minimize_datum` (still NOT APPROVED TO
START).

Gates run: recon task, no production diff (verified via `git diff --stat`
after reverting the two instrumented files). `go build ./...` clean. No
new/changed unit tests (probe deleted before commit, matching precedent).
`make ralph-state-guard`: same recurring stale status/progress.json pattern
as every prior loop, auto-repaired to consistent, then confirmed
consistent. Nightly triage for this loop: `ci/logs/action-items.md`'s
newest run (`20260914-235643`, 14 items) re-verified already fully filed by
a prior loop; no new filing needed.

In-flight: none.

Next step: select the next M0139 slice per the banner order. **M0139-0006**
("put the packed-retention decision to the owner") is now the only open
M0139 recon slice — it should carry BOTH S3's residue measurement (Q12:
128.4 B/row, not K67's assumed 72) AND this task's finding (Q4's ratio
can't move; the join-leg hook structurally cannot reach `unnestExistsExpr`
semi/anti joins) into one owner packet. **Do not implement
`minimize_datum`** — still NOT APPROVED TO START. Alternatively M0140's
still-open M0140-0003 remains valid per the banner ("M0140 does not wait on
M0139"): "land the `GOOPG_GATHER_PATHS` flip on the category metric",
pre-registered "no match flip" per R43 rev 3 / K38. Do not re-open
S1/S2/S3/-0004/-0005 — all five fully done, gated, and pushed.
