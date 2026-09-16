Task: M0142-0008a-3i-plumbing-c1 — LANDED and committed. Merged
`semiAnti[*].pred`'s split conjuncts into `conjuncts`, mirroring `outerLinks`'
`onOuter` treatment (design doc §36 gap 1 / §37 landing note).

Files this loop:
- internal/optimizer/joinsearchseam.go: 14-line addition right after the
  `outerLinks` block (was line 554-555), before `partitionConjunctsForJoinPlanning`
  consumes `conjuncts`. `for _, lk := range semiAnti { conjuncts =
  append(conjuncts, splitAnd(lk.pred)...) }`.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §37 —
  full inertness proof (traced through `semiAntiOnQualsOK`'s both-sides
  contract, `tableForCol`'s -2 multi-table path, `buildRestrictInfos`'
  relid computation, and why the DP search's real-leaves-only
  `prob.bindings` can never form the join needed to consume it) plus the
  empirical confirmation numbers.
- docs/design/README.md: m0142-0008a-1 row appended with §37 summary.
- .ralph/fix_plan.md: `-3i-plumbing-c1` flipped `[x]`, landing note with
  gate results, "Next" pointer to c2.

Key symbols: same as before — `extractSearchLeaves` (`joinsearchseam.go:1109`);
`semiAntiChainLink` (`:1543`); the new loop sits at `joinsearchseam.go:555-565`;
`partitionConjunctsForJoinPlanning`/`tableForCol` (`local_filters.go:62`,
`joinrestrict.go:605`); `buildRestrictInfos` (`joinrestrict.go:167`);
`searchConsumes` (`joinsearchseam.go:1067`).

Findings: confirmed via code trace AND live measurement (not just argued)
that c1 is behaviour-neutral until c2-c4 land — every semiAnti conjunct's
relids necessarily include the not-yet-real synthetic leaf's bit (per
`semiAntiOnQualsOK`'s both-sides contract), so no join formed from the
DP search's real-leaves-only `prob.bindings` can ever consume it. One
pre-existing (not newly introduced) gap surfaced by the trace: because
`buildRestrictInfos` still records the clause, `searchConsumes` reports it
"seen" in the final residual computation, so the semiAnti ON qual is not
re-added to the residual `Filter` either — the predicate is silently
unenforced with or without c1, same observable outcome either way. This is
exactly the gap c4/c5 (SJInfo/decline-gate wiring) are meant to close, not a
new problem c1 created.

Next step: pick up **M0142-0008a-3i-plumbing-c2** — give the synthetic
Semi/Anti RHS leaf a real `rangeBinding`/`baseRelInfo` and extend
`prob.bindings`/`scans`/`relInfos` to `nprefix+len(semiAnti)` (design doc
§36, gaps 2-3; `joinsearchseam.go:556-581`'s `for i, b := range
ctx.bindings[:nprefix]` loop is the site to extend). This is the piece that
actually starts to move plan shapes (unlike c1), so per §36/§37 it needs its
own dedicated loop with a live Q78-shaped fixture check on the resulting row
estimate via `estimateBaseRelInfo`/`applyRelSizeFallback` — do not assume
the fallback produces a sane (non-zero) estimate for a `rangeBinding{table:
nil}` leaf; verify it.

Gates run this loop: `go build ./...` clean; `go test
./internal/optimizer/...` PASS; `scripts/tpcds-sf025-regression.sh sweep`
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0, PLAN-SHAPE same=99/99 vs prior
commit; `scripts/tpch-spotcheck.sh` SKIPPED (pre-existing M0142-0003k
data-dir blocker, confirmed unrelated — TPC-H `tpch` DB still needs the
reload a human must run, see CLAUDE.md); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — ONE failure, `internal/parser`
`yacc_locking_test.go` AST-drift (`GroupedJoinUnaliased` field), confirmed
via `git stash` to be PRE-EXISTING on HEAD before this loop's change (a
concurrent loop's grammar WIP already landed on this branch, not touched by
this loop — internal/parser was never in this loop's diff). `make
ralph-state-guard` PASS (auto-repaired the same benign prior-loop clean-exit
marker seen every recent loop). Commit's own pre-commit hook runs the
pgbench smoke.

In-flight: none.
