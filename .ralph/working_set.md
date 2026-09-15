Task: M0141-S2b-6 — compare goopg's costed Hashed vs. Sort-over-Sorted
`PathAgg` candidate numbers at Q4/Q5/Q12/Q21 against PG's cost formulas.
DONE and committed this loop, as a recon (measurement + design note +
follow-up task, per the group's recon exception). Result: S2b-5's own
numbers do NOT reproduce — the synthetic-dataset probe pattern this whole
S2b sub-thread relied on cannot answer this question.

Files: `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` (new
"S2b-6 result" section — the per-query cost table, the methodology note
explaining why it disagrees with S2b-5 on the same commit, the resume
point). `docs/design/README.md` (index blurb updated). `.ralph/fix_plan.md`
(S2b-6 closed `[x]` with result; new `M0141-S2b-6-resume` filed, gated on
M0142-0003k). `.ralph/deferral_ledger.md` (new row, task-id `m0141-s2b-6`).
Scratch test file `internal/testutil/tpch/zzz_s2b6_probe_test.go` was
created to run the probe and deleted before finishing (same precedent as
S2b-0/S2b-5) — nothing from it is in the tree or history.

Key symbols/paths: `internal/optimizer/groupingpaths.go` (`addGroupingPaths`
— confirmed both Hashed and Sorted `PathAgg` candidates are built here, and
the Sorted candidate always carries a real group-keys Sort over the RAW
input via `sortPathForBounded`, unless `indexOrderedAggInput` matches — it
did not for any of these 4 queries). `internal/optimizer/cost_funcs.go`
(`costAgg`, `costSortRunWithWidth` — hand-verified term-by-term against
`postgres/src/backend/optimizer/path/costsize.c:1898-1985` (`cost_tuplesort`)
and `:2682-2768` (`cost_agg`); no divergence found in the formulas).
`internal/optimizer/pathtrace.go` (`DPPATH` trace format, read not
changed). `internal/optimizer/upperorderedgrouping.go` (`DPGROUP` trace
from S2b-5, read not changed).

Findings this loop: re-ran the S2b-0/S2b-5 probe pattern (`cluster.New` +
`tpch.DDL()` + synthetic load) but with **`ANALYZE` on all 8 tables**
(S2b-0/S2b-5 used `ANALYZE region` only, borrowed from `tpch_run_test.go`'s
convention). At the SAME commit S2b-5 measured (`51a2d176d`): Q5/Q21 still
cleanly elect Hashed+Sort-above (genuinely correct — their ORDER BY doesn't
match GROUP BY columns, so the Sorted candidate needs an extra dominated
Sort). **Q4/Q12 now elect Sorted (bare Aggregate) — the OPPOSITE of
S2b-5's table**, on the identical commit. Traced why: Q4/Q12's join inputs
estimate `rows≈1` on this 5-16-row synthetic dataset, so `numGroups ≈
inputRows` (both clamp to 2 by the "never log(0)" floor both `cost_tuplesort`
and `costSortRunWithWidth` share) — the Sorted candidate's input-Sort term
(nominally the expensive `O(N log N)` term at real scale) and the Hashed
candidate's output-Sort term (nominally the cheap `O(G log G)` term) price
the IDENTICAL two clamped tuples and land on bit-identical totals (0.3525,
2.54 — verified in the `DPPATH`/`DPGROUP` trace, both runs). The election is
an exact tie broken only by a <0.02-cost-unit startup-cost tiebreak that
ANALYZE coverage nudges either way — noise, not a cost-model bug. **This
means the synthetic-dataset probe this whole S2b sub-thread has used cannot
answer S2b-6's specific question**: the input-Sort vs output-Sort terms only
separate into a real signal when `inputRows >> numGroups`, which needs real
SF1-scale cardinalities (tens of thousands of rows vs a handful of groups),
not a hand-built tiny fixture of any size. It does NOT confirm or refute
S1/S2's original 6-query TPC-H "mechanism (B)" finding (that was measured
against the real SF1 HammerDB-loaded `:65433` cluster, not this synthetic
one) — it only shows this specific probe methodology was the wrong
instrument for S2b-6's question, even though it was the right instrument
for S2b-0/S2b-5's binary questions (does a gate decline / does a
translation succeed).

Next step: pick per the `## Current Priority` banner (re-read first in case
it changed). **M0141-S2b-6-resume** (repeat this same term-by-term diff
against the real SF1-loaded `:65433` cluster) is gated on **M0142-0003k**'s
TPC-H cluster reload — a human-authorized shared-resource write, not
something to attempt unattended. Until that reload happens,
**M0141-S2b-1** (DISTINCT loop-fix, cheapest net-new slice, no new
plumbing, independent of the S2b-6 cost-model question) is the next
concretely actionable pick inside M0141-S2b. Do NOT attempt **M0141-S2b-2**
blind (needs its own scoping pass, per K24). M0142-0003i/-0003k(c) (the
shared cluster reload itself) remain BLOCKED on a human decision, as before.

Gates run: `go build ./...` clean (no production code changed this loop —
pure recon/measurement + docs). `make ralph-state-guard` — found a
status/progress inconsistency from the previous loop's clean-exit marker,
auto-repaired, then passed (see status block). Pre-commit hook's pgbench
smoke will run automatically on commit. No `go test` gate run since no
production code changed (the scratch probe itself passed, then was
deleted).

In-flight: none. The scratch probe's throwaway `cluster.New` server shut
down cleanly via its own `defer c.Stop()`; verified via the debug log
(`/tmp/goopg_cluster_debug/s2b6probe.log`, deleted after extracting the
numbers cited above — nothing else references it).
