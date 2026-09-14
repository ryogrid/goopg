Task: M0139-S2 — "narrow scan output at the new hook" (executor-side
narrowing / projection pushdown milestone). **COMPLETE and committed**
(`62df1ee65`) and pushed this loop, branch `plan-parity-with-pg-take2-ralph`.

Files: `internal/optimizer/joinleghook.go` (narrowJoinLeg now reuses
narrowBuildInput's keep-set chain instead of always declining; gained
`*Path` + `nliInner bool` params), `internal/optimizer/createplanjoin.go`
(call-site update, sets `nliInner` for both NLI kind strings),
`internal/optimizer/joinleghook_test.go` (replaced S1's tests with
`TestNarrowJoinLegCountsAndNarrows` / `TestNarrowJoinLegFiresAndNarrowsOnLiveJoinSearch`),
`internal/optimizer/pathtarget_test.go` + `planner_test.go` +
`internal/executor/owned_build_poison_test.go` (5 pre-existing tests
re-baselined to the new, correct build/Project counts — see design doc for
the derivation of each), `docs/design/0100-0149/m0139-s2-narrow-join-leg-output.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0139-S2
checked off + summary), `analysis/m0139/m0139s2-sf025-hookoff.txt` /
`-hookon.txt` (qual-placement-census capture pair, committed as evidence).

What was done: filled S1's always-decline `narrowJoinLeg` body with the
SAME 3-tier derivation `narrowBuildInput` uses (joinKeepSet -> buildKeepSet
-> neededKeepSet -> narrowPlanOutput). Safety argument for NL legs (where
`deriveJoinKeepsAt` never stamps JoinKeep): the tightest tier correctly
reports "unknown" there and falls through to the two join-kind-agnostic,
over-inclusive fallback tiers (buildKeepSet reads Path.Target; neededKeepSet
reads a raw-AST-walk NAME set) — safe by the SAME argument narrowcostinputs.go
already documents ("can only keep too many columns, never too few").
ONE case is structurally forbidden regardless of keep-set safety: an NLI's
INNER slot is typed concretely (`*IndexScan`/`*BitmapHeapScan` for the two
NLI kind strings `"PathNestLoop(NLI)"`/`"PathNestLoop(NLI-bitmap)"`), so
wrapping it in a `*Project` is a plan-TIME PANIC — caught live by
`TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` and
`TestDerivedTableUnderIndexNLReturnsRows` before the `nliInner` exclusion
was added (2 real bugs found and fixed mid-loop, not hypothetical). Landing
real narrowing legitimately moved plan shape wherever a previously-unwrapped
leg had something to drop, so 5 pre-existing tests needed their hard-coded
build-count oracle RE-DERIVED (via throwaway zz_probe*_test.go files, always
deleted before commit) rather than loosened — each new count was sanity-
checked against the keep-set rules before being hardcoded, never accepted
blind.

Key symbols: `narrowJoinLeg` (joinleghook.go, now ~85 lines with the safety
argument documented inline), `joinKeepSet`/`buildKeepSet`/`neededKeepSet`/
`narrowPlanOutput` (narrowoutput.go, unchanged — reused not duplicated),
`createNestLoopIndexJoinPlan`/`createNestLoopBitmapJoinPlan` (createplannl.go,
the two panic sites that located the NLI-inner exclusion), `findFirstJoin`
(small_dim_buildside_test.go, reused to fix TestPlanJoinPicksHashAlgo rather
than writing a second tree-walker).

Hypothesis/Findings: none open for S2 itself — implementation is complete,
proven safe by construction (the "never fewer than needed" over-inclusion
argument) AND validated live (qual-placement census 99/99 clean, TPC-DS
SF0.25 values gate clean, no timing regression). The two NLI panics were
real, found by running the existing suite (not hypothesised) — TRUST THE
FULL TEST SUITE after any change to a hook this widely reachable; a green
`go build` proves nothing about a type-assertion panic three calls deep.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/... ./internal/testutil/tpch/... ./internal/postmaster/...`
all green. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`:
green except the pre-existing, already-filed `internal/parser`
`GroupedJoinUnaliased` AST-drift (60 test fns, unrelated — confirmed by a
prior loop via git stash). Qual-placement census on full TPC-DS SF0.25
corpus (99 queries, private clone `tmp/goopg-m0139s2-bin`, hook off vs
default-on): `mismatch=0`. `scripts/tpcds-sf025-regression.sh sweep`:
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3, status-delta
verdict-changes=none total-delta=-1.7%. `make ralph-state-guard`: same
recurring stale status/progress.json pattern as every prior loop,
auto-repaired, then consistent.

In-flight: `scripts/tpch-spotcheck.sh` **still could not run** — shared
`:65433` TPC-H bench server was up (peer-owned, per
`goopg_shared_bench_cluster_collisions` memory) for this entire loop too
(now 2 consecutive loops). Not treated as a values-safety gap: the TPC-DS
SF0.25 sweep above is a real, clean, 99-query values gate on real data, and
the narrowing mechanism itself is corpus-agnostic (same 3-tier chain, same
census). Next loop (or whenever the shared server frees up): re-run
`scripts/tpch-spotcheck.sh` opportunistically; expected to reproduce the
canonical Q12=2/Q13=34 anchors unchanged (narrowing changes row WIDTH,
never row COUNT).

Next step: select the next M0139 slice per the banner order — **M0139-S3**
("measure the residue against K67's floor — even narrowed to one column
goopg is 72 B/row -> 103 MB and still spills at work_mem=64MB where PG is
22 B/row -> 31 MB; report the post-pushdown figure as a NUMBER") is the
natural continuation now that S2's real narrowing is live and measurable.
Alternatively M0139-0004 (re-measure the "duplicate hash build map" premise
— independent of the slices, cheap to take if S3 needs a bigger corpus rig)
or M0140's still-open M0140-0003 are both valid per the banner ("M0140 does
not wait on M0139"). Do not re-open S1/S2 — both fully done, gated, and
pushed.
