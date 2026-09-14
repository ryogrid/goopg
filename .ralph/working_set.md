Task: M0137-0012 — file ledger rows for the four unowned carry-overs (B6, B8,
B10, O15). **COMPLETE and committed this loop** (branch
`plan-parity-with-pg-take2-ralph`). Recon task per the plan-parity harness:
filing only, no production code change.

Files: `.ralph/deferral_ledger.md` (+4 rows, one per finding),
`docs/design/0100-0149/m0137-0012-unowned-carryover-ledger-rows.md` (new),
`docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0137-0012 checked
off with DONE note).

Key symbols cited in the new rows (verified against HEAD, not copied
verbatim from the round doc): `maybeAttachMemoize`
(internal/optimizer/nl_index_join.go:127) + `getMemoizePath`
(internal/optimizer/joinpathsnli.go:313,498) for B6;
`indexProbeCostMultiplier` (internal/optimizer/cost_funcs.go:1052) for B8;
`indexCorrelationFor` (internal/optimizer/costindex.go:479-496) for B10;
`pushConjunctTraced` (internal/optimizer/inner_join_qual_pushdown.go:340-556,
no `*Gather` case in its type switch) for O15.

Findings:
- Nightly triage for run 20260914-235643 (14 items, `ci/logs/action-items.md`)
  was ALREADY fully filed in `.ralph/fix_plan.md` before this loop started
  (filed 2026-09-15, presumably by the loop that landed M0137-0009) — verified
  all 14 AI-ids present, nothing new to file this loop.
- B6 is narrower than its round-doc phrasing suggests: goopg already has a
  general Memoize producer (`joinpathsmemoize.go`, `GOOPG_MEMOIZE` default-on)
  and the NL-index-join path already calls `maybeAttachMemoize`/
  `getMemoizePath` in its generic sweep — the open question is why the
  SPECIFIC probe path R59 repriced doesn't reach a Memoize-wrapped inner, not
  Memoize's absence altogether. Recorded this precisely in the ledger row so a
  future implementer doesn't restart from "build Memoize".
- O15 has no dedicated section in `02-open-problems.md` (only an inline
  mention under C2) — confirmed via code reading that `pushConjunctTraced`'s
  switch truly has no `*Gather` arm, so it falls through to
  `innerJoinPushEligibleInput` and declines. Ledger row cites the exact
  function/line and recommends sequencing the fix after M0137-0010's census.

Next step: per `.ralph/fix_plan.md`'s M0137 section, remaining open tasks are
**M0137-0010** (qual-placement census + duplicate-sensitive-values check,
becomes the M0139 gate — real implementation work, largest remaining M0137
item), **M0137-0011** (root-cause the C3/K63 display/estimator seam), and
**M0137-0013** (close/delete the ten-plus-round ledger carries — explicitly
flagged as campaign-sized, own multi-loop budget). Re-read AGENT.md
§"Plan-parity harness" fresh next loop per the loop-start discipline before
picking. M0137-0010 looks like the natural next pick (it's the prerequisite
gate M0139 needs), but confirm against the banner/fix_plan order first.

Gates run: `go build ./...` clean (repo-wide, doc/ledger-only diff).
`make ralph-state-guard`: found the same recurring running/completed marker
mismatch every recent loop has hit (previous loop's clean-exit marker),
auto-repaired, then OK. No `go test` gate run — this loop touched zero `.go`
files (ledger/doc/fix_plan only), so no package tests were affected; the
pre-commit hook's mandatory pgbench smoke will run automatically at commit
time and gates it as always.

In-flight: none. No bench cluster or throwaway server left running.
