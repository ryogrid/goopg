Task: M0139-S1 — "a hook point inside the join tree" (executor-side
narrowing / projection pushdown milestone). **COMPLETE and committed** this
loop, branch `plan-parity-with-pg-take2-ralph`.

Files: `internal/optimizer/joinleghook.go` (new — the hook + flag),
`internal/optimizer/joinleghook_test.go` (new — 3 tests),
`internal/optimizer/createplanjoin.go` (call site in `joinInputsFor`),
`internal/optimizer/flaglabels.go` (+`GOOPG_NARROW_LEG_HOOK` provenance row),
`scripts/planner-flags.env` (regenerated via `go run
./cmd/gen-planner-flag-labels`), `docs/design/0100-0149/m0139-s1-join-leg-hook.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0139-S1
checked off + summary).

What was done: recon found the real gap narrower than the milestone doc's
"no `*Project` above the scan at all" — `joinInputsFor` already narrows a
hash join's inner/build side (`narrowBuildInput`) and both merge-join sides
(`narrowMergeInput`); only a hash join's OUTER/probe side and BOTH nested-loop
sides (plain + NLI) were never reached. `narrowPlanOutput` (the function that
actually builds a `*Project`) already declines to wrap a no-op cut
(`len(keep) >= len(lay)`), so an "identity-wrapping hook" built by calling it
directly would be silently absorbed and prove nothing about reachability —
the hook had to be a genuinely new call site, not a reuse of that function.
Landed `narrowJoinLeg(n, lay)`, gated by new default-ON flag
`GOOPG_NARROW_LEG_HOOK`, called from `joinInputsFor` on both legs of every
join kind (safe uniformly because the function is proven to never mutate
its input). For S1 it is UNCONDITIONALLY A DECLINE: counts every
currently-unhooked eligible leg (`isNarrowableLeaf`) but always returns the
pair byte-identical to input — "no parity movement" is guaranteed by
construction, not merely predicted.

Key symbols: `narrowJoinLeg`/`isNarrowableLeaf`/`legHookFireCount*`
(`internal/optimizer/joinleghook.go`), the two new call-site lines in
`joinInputsFor` (`internal/optimizer/createplanjoin.go`, right after the
existing `narrowBuildInput`/`narrowMergeInput` calls), `narrowPlanOutput`
(`narrowoutput.go:708`, the function M0139-S2 will reuse at this hook).

Hypothesis/Findings: none open — this was plumbing with a mechanically
proven zero-behaviour-change property, not a diagnosis task. M0139-S2's job
(next slice, still unchecked in fix_plan.md) is to replace `narrowJoinLeg`'s
always-decline body with the real keep-set derivation `narrowBuildInput`
already uses (`buildKeepSet`/`joinKeepSet`/`neededKeepSet`), reusing it
rather than duplicating it — that is where real narrowing (and the actual
row-width win) happens, and where a corpus-wide "N queries narrowed" count
becomes meaningful.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/...` fully green (including
`TestFlagProvenanceEnvIsGenerated`, which required regenerating
`scripts/planner-flags.env`). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`: green except the pre-existing,
already-filed `internal/parser` `GroupedJoinUnaliased` AST-drift (60 test
fns) and untracked `bak/` build failure — both confirmed unrelated (no
optimizer/executor packages in the failure list). `scripts/tpcds-sf025-regression.sh
sweep`: PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3 (clean
values gate on real data). `make ralph-state-guard`: same recurring stale
status/progress.json pattern as prior loops, auto-repaired, then consistent.

In-flight: `scripts/tpch-spotcheck.sh` **could not complete** — retried 3x
over ~15 min, every attempt failed at
`tpch-private-clone: 127.0.0.1:65433 still busy after 60s` because the
SHARED TPC-H bench server was up the entire loop (peer-owned per the
`goopg_shared_bench_cluster_collisions` memory — must not be stopped by this
loop) and the private-clone snapshot step (`scripts/lib/tpch-private-clone.sh`)
requires the shared server to be fully DOWN, not merely idle, before it will
copy the data directory. Not treated as a values-safety gap: this specific
change is proven a hard byte-identical no-op by `TestNarrowJoinLegDeclinesButCounts`
(every return path pointer/content-identical to input, for every
flag/shape combination), independent of which server answers the question,
and the TPC-DS SF0.25 sweep above is a real, clean values gate on real data.
Next loop (or a retry later in THIS loop before it ends, if the shared
server frees up): re-run `scripts/tpch-spotcheck.sh` opportunistically;
expected to reproduce the canonical Q12=2/Q13=34 anchors unchanged.

Next step: select **M0139-S2** ("narrow scan output at the new hook —
reuse the existing narrowing rather than duplicating it") per the banner
order (M0138 done, M0139 in progress, M0140 has M0140-0003 still open too —
either is a valid pick; M0139-S2 has direct continuity with this loop's
work). S2's job: replace `narrowJoinLeg`'s always-decline body with a real
keep-set derivation reusing `buildKeepSet`/`joinKeepSet`/`neededKeepSet`
(the same functions `narrowBuildInput` already calls in `narrowoutput.go`),
call `narrowPlanOutput` with that real keep set instead of returning
unchanged, and measure the actual row-width win corpus-wide (values gates +
qual-placement census + per-query timing, per the milestone's own
Definition-of-Done items still unchecked). Do not re-derive `attr_needed` as
the blocker (ruled out twice already).
