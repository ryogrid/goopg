Task: M0140-0002 — adjudicate the `GOOPG_GATHER_PATHS` flip's failing tests
against PG 18.3. **COMPLETE and committed** this loop (`c4d8013e5`), branch
`plan-parity-with-pg-take2-ralph`. One test-harness fix landed; no planner
production code touched.

Files: `docs/design/0100-0149/m0140-0002-gather-paths-flip-failing-set-adjudication.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0140-0002 checked off + sub-bullets), `internal/optimizer/q21_live_test.go`
(fixed `visit()`'s Gather-blindness).

What was done:
  1. **Scope-corrected M0140-0001**: a full `go test
     ./internal/optimizer/... ./internal/executor/...` sweep under the flip
     finds **seven** failures, not the four M0140-0001 got by re-running only
     the R43-era named list. Three new: `TestPartialPathIsNeverTheFinalPath`,
     `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`,
     `TestSlice3CorrelatedBodyDeclinesParentAware`.
  2. **Fixed one real false-failure**: `TestSplitEqualityForHashMultiKey/
     searched_enumerator`. Probed the actual plan (throwaway
     zz_probe_multikey_test.go, deleted after use) — under the flip it
     correctly chooses `JoinAlgoHash` with both `LeftKey`/`RightKey` set; the
     shared `visit()` test helper just had no `case *Gather` and silently
     stopped descending once the flip wrapped the tree in one (production
     code's `boundaryWalkChildren` was already fixed for the identical
     reason under R11). Added `case *Gather`/`*GatherMerge` to `visit()`;
     confirmed PASS under the flip and no regression at default
     (`go test ./internal/optimizer/...` fully green).
  3. **Adjudicated the 3 narrow-build shape tests + TestPartialPathIsNeverTheFinalPath**
     as stale pins, not correctness bugs: ran a live PG 18.3 `EXPLAIN` of the
     real TPC-H Q9 query (SF=1, `bench/tpch` port 65432) — PG's actual plan
     matches **neither** goopg arm (orders joined last via Parallel Hash under
     Gather; lineitem via Nested-Loop+Index-Scan, never hashed), so
     "closer to PG" can't decide between default/flipped shapes here — Q9's
     join-order gap predates this flip (K26/R51-53/R68, gated to M0142).
     Concrete effect under the flip: a filter-column-drop optimization
     declines to fire under a Gather-wrapped prebuilt leaf (extra column
     carried, no data loss); a partial-path-production timing assumption
     breaks because the flip also activates K80's `addPartialHashJoinPath`.
     Action: re-pin all 4 when M0140-0003 lands the flip, not before.
  4. **Flagged the real remaining finding**: `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`
     / `TestSlice3CorrelatedBodyDeclinesParentAware` — TPC-H Q2's
     scalar-aggregate decorrelation declines ENTIRELY under the flip,
     falling back to a per-outer-row `SubPlan`. Not proven incorrect, but the
     strongest candidate for a real root-cause fix before M0140-0003 lands
     the flip default-on. Root cause NOT isolated (adjudication only).
  5. **Cache hazard recorded**: a `go test` run for `TestOwnedBuildPoisonPrebuiltBoundary`
     under the flip without `-count=1` returned a **stale PASS** from the
     result cache even though the env var changed. Every number in the
     design doc was reconfirmed with `-count=1`.

Key symbols: `visit()` (`internal/optimizer/q21_live_test.go:198`, now
Gather-aware). `boundaryWalkChildren` (`internal/optimizer/createplanroot.go:427`,
the production-code precedent for the same fix). `jsgDecorrelatedAgg`
(`internal/optimizer/joinsearchunnestgroupkey_test.go`) — where the Q2
decorrelation-decline symptom surfaces; root cause not yet located.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` and
`./internal/executor/...` (default) both green. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: the only failures are the ALREADY-FILED,
pre-existing `internal/parser` `GroupedJoinUnaliased` AST-drift (60 test
functions, fix_plan.md lines ~181-206, explicitly unrelated to
optimizer/executor diffs) and the pre-existing untracked `bak/` build
failure — both confirmed unrelated to this loop's diff via `git stash`.
pgbench smoke pre-commit hook PASS. `make ralph-state-guard`: same recurring
stale status/progress.json pattern as prior loops, auto-repaired, then
consistent.

In-flight: none.

Next step: Re-check `.ralph/fix_plan.md`'s `## Current Priority` banner
fresh. With M0140-0001 and M0140-0002 both done, the strongest-continuity
options are: **M0140-0003** (land the `GOOPG_GATHER_PATHS` flip on the
category metric) — but it is NOT yet unblocked by this loop's own findings:
the Q2 decorrelation-decline (item 4 above) should be root-caused first, or
at minimum explicitly accepted as a known regression, before flipping the
default; alternatively select **M0139-S1** (hook point inside the join tree,
gated on M0137-0010, already unblocked) or another open M0139/M0140/M0142
task per the banner. Do NOT re-run this loop's seven-test measurement again
without cause — it is committed and reproducible via the exact commands in
the design doc. If M0140-0003 is selected next, its first move should be
isolating the Q2 decorrelation-decline root cause (leading hypothesis, NOT
yet verified: another Gather-blind tree-walk in the decorrelation-eligibility
check, by analogy with this loop's `visit()` finding — check that
hypothesis with instrumentation before assuming it).
