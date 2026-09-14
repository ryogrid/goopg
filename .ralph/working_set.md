Task: M0140-0003 — "land the `GOOPG_GATHER_PATHS` flip on the category metric"
(plan-parity milestone group, M0140 TPC-DS parallelism). **COMPLETE and
committed/pushed** this loop, branch `plan-parity-with-pg-take2-ralph`.

Files: `internal/optimizer/unnest.go` (the fix: `clonePlanReplacingOuter`
`*Gather`/`*GatherMerge` arms), `internal/optimizer/gatherpaths.go` (default
flip + comments), `internal/optimizer/flaglabels.go` (2 provenance comments),
`scripts/planner-flags.env` (regenerated), `internal/optimizer/pathtarget_test.go`
+ `internal/optimizer/considerparallel_test.go` + `internal/executor/owned_build_poison_test.go`
(4 stale-pin re-baselines), `docs/design/planner-c19d-gather-paths/DESIGN.md`
(§5 landed-status addendum), `docs/milestones/0140-tpcds-parallelism.md` (K80
status), `docs/design/0100-0149/m0140-0003-gather-paths-flip-lands-default-on.md`
(new design doc), `docs/design/README.md` (+index row), `analysis/m0140/*`
(12 capture artefacts, evidence), `.ralph/fix_plan.md` (M0140-0003 checked off).

What was done: root-caused M0140-0002's flagged Q2-decorrelation decline —
instrumented every bail point in `canUnnestSubquery`/`unnestSubquery`
(throwaway `println` probes, removed) and found `clonePlanReplacingOuter` had
no `*Gather`/`*GatherMerge` case, so a partial path winning inside a scalar
subquery's own inner plan made the correlation-substitution clone bail with
"unsupported plan node", silently keeping the subquery a per-outer-row
`SubPlan`. Two wrong hypotheses tried and refuted first (test walker
Gather-blindness; `canUnnestSubquery` type-assertion bail) — see the design
doc's numbered list. Fixed with a single-child recursion arm (same class as
R11's `boundaryWalkChildren` and M0140-0002's `visit()` fixes). Flipped
`gatherPathModeFromEnv`'s unset/empty default to `all` (was `off`). Re-pinned
the four tests M0140-0002 pre-adjudicated as safe. Measured both arms (off vs
all), same commit, same stats epoch: TPC-DS SF0.25 vs live PG (match held at
canonical 2, `parallelism` 86->85, 50/99 shape-delta all tagged parallelism,
values PASS=96/0/0/0/0) and TPC-H both the canonical serial protocol
(confirmed inert, `parallelism=0`/`match=6` unmoved) and the non-serial
diagnostic protocol matching R43/K38's own measurement (`parallelism 17->16,
no new match`, independently reproducing R43 rev 3's historical `18->16`).
`tpch-spotcheck.sh` PASS under the new default.

Key symbols: `clonePlanReplacingOuter` (unnest.go:1503, the fix site),
`gatherPathModeFromEnv` (gatherpaths.go, the flip), `canUnnestSubquery`/
`unnestSubquery` (unnest.go, the bail chain instrumented), `addPartialHashJoinPath`
(joinpathsparallel.go, K80's now-live producer).

Hypothesis/Findings: root cause fully isolated and fixed, not just adjudicated
around. `internal/parser`'s ~60 golden-fixture "AST drift" failures + an
untracked `bak/` build failure are PRE-EXISTING and unrelated (verified: zero
diff under either path; the parser failure is already filed as nightly
`AI-20260914-235643-001` under M-NIGHTLY, dated 2026-09-14, before this loop
started) — do not re-investigate these as caused by this task.

Gates run: `go test ./internal/optimizer/... ./internal/executor/...` green
in default/off/all arms. `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34) —
required stopping/restarting the shared `:65433` cluster via the sanctioned
lifecycle scripts (verified zero `pg_stat_activity` connections + >24h-stale
WAL first; never `pkill`) since its private-clone snapshot mechanism needs
the port fully quiet, not merely idle — restored to the correct final state
(unset env, default `all`) afterward. `scripts/tpcds-sf025-regression.sh
sweep` PASS=96/0/0/0/0. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: only the two known-unrelated failures above.
`make ralph-state-guard`: same recurring stale status="running"/
progress="completed" pattern as every prior loop; auto-repaired, confirmed
consistent.

In-flight: none.

Next step: M0140-0003 is DONE. Per the banner order, re-check M0138 (fully
done, all six [x]) and M0139 (fully done, all six [x]) — both closed. Select
the next open **M0140** item: **M0140-0004** ("partial-Append producer, K43"
— PG uses Parallel Append in six TPC-DS queries, goopg has zero partial paths
on join rels via that route) if unblocked, else **M0140-0005** ("file the two
out-of-reach items as ledger rows" — Q14's third category and the K14/K15/K41
non-planner floor). If both are blocked, fall through per the banner to
**M0141-S0** (scoping recon, the only selectable M0141 item) or **M0143**
(gated on nothing). Do not re-open M0140-0001/-0002/-0003.
