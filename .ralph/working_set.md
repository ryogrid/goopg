Task: M0137-0011 — root-cause C3/K63 (the second display/estimator seam).
**COMPLETE and committed this loop** (commit `1f860878f`, branch
`plan-parity-with-pg-take2-ralph`). Recon task per the plan-parity harness:
diagnosis + design note + ledger row, no production diff (instrumentation
added, used against a live server, then fully reverted).

Files: `.ralph/deferral_ledger.md` (+1 row
`m0137-0011-filter-node-missing-plancost-embed`),
`docs/design/0100-0149/m0137-0011-c3-k63-display-seam-root-cause.md` (new),
`docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0137-0011
checked off with DONE note), `analysis/m0137-0011/*.txt` (raw
`estimate-audit -plan-only` Q12 captures, kept as evidence per "Way of
working").

Root cause (confirmed by live trace, not inferred): `optimizer.SeqScan`
embeds `PlanCost` (plan.go:641); `optimizer.Filter` does NOT (plan.go:1531,
only `searchedTree`). Any base relation with a local qualifier
(`attachRelationLocalFilters`) reaches `buildInitialRels` wrapped as
`Filter{Child: SeqScan}`; `stampPlanCost`'s `n.(planCostSetter)` assertion
silently fails on that Filter, so the search's correct cost (271,421.24 for
Q12's `lineitem`) is discarded and EXPLAIN falls back to
`DeriveLegacyDisplayCost`'s childless-leaf formula (page cost missing) —
exactly R37's stale "legacy model" number (60,299.79). `orders` (no local
qualifier, bare `SeqScan`) renders correctly. K62 stands (display-only,
cannot move a plan-choice category); blast radius is wider than R37 knew —
every base-local-filtered scan in the corpus, not just Q12's `lineitem`.
Fix shape (embed `PlanCost` on `Filter`) is named in the ledger row but NOT
implemented — left for a future task since landing it implies a
corpus-wide before/after measurement, out of scope for a recon loop.

Key symbols: `stampPlanCost`/`explainCostFields`/`DeriveLegacyDisplayCost`/
`legacyDisplayChildren` (internal/optimizer/plancost.go,
internal/executor/operators_explain.go), `buildInitialRels`
(internal/optimizer/joinsearch.go:433), `SeqScan`/`Filter` struct defs
(internal/optimizer/plan.go:639-758, 1531-1569).

Next step: per `.ralph/fix_plan.md`'s M0137 section, remaining open tasks
are **M0137-0010** (qual-placement census + duplicate-sensitive-values
check — becomes the M0139 gate, largest remaining M0137 item, real
implementation work) and **M0137-0013** (close/delete the ten-plus-round
ledger carries — explicitly flagged as campaign-sized, own multi-loop
budget, split out of M0137-0009). Re-read AGENT.md §"Plan-parity harness"
fresh next loop before picking (loop-start discipline). M0137-0010 looks
like the natural next pick since it's the M0139 prerequisite and is listed
as real (non-recon) implementation work — confirm against the
banner/fix_plan order first, same as this loop did.

Gates run: `go build ./...` clean at HEAD (both temporarily-traced files
confirmed byte-identical to origin via `git diff --stat` before commit —
empty). No `go test` gate needed (zero test-visible `.go` diff in the final
commit). Pre-commit hook's pgbench smoke PASSED (both simple-update and
select-only arms, 0 failed transactions) — see commit `1f860878f`.
`make ralph-state-guard`: hit the same recurring running/completed marker
mismatch every recent loop has seen (previous loop's clean-exit marker),
auto-repaired, then OK.

In-flight: none. The `bench/tpch` 65433 goopg lane was started fresh this
loop (it was down at loop start), used twice — once on the ordinary binary
for a baseline capture, once restarted on a private traced binary
(`/tmp/goopg-m0137-0011-trace`, since deleted) for the live-pointer trace —
and then stopped again via `stop_goopg.sh`, restoring it to the down state
it was found in. No throwaway server, no lingering PID, no private clone
left running. `/tmp/estimate-audit-m0137-0011` and
`/tmp/goopg-m0137-0011-trace` both deleted.
