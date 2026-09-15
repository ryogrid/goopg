Task: M0141-S2a — "mechanism (A) re-measure post-M0139" for the plan-parity
M0141 group. **DONE, committed this loop** (branch
`plan-parity-with-pg-take2-ralph`, commit `e40658f64`). No production
behavior change (pure recon, live re-capture only).

Files: `docs/design/0100-0149/m0141-s2a-mechanism-a-remeasure-post-m0139.md`
(new design doc), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(S2a checked off with findings; filed **M0141-S2a-fix** as the real resume
point), `.ralph/deferral_ledger.md` (+1 row, task-id `m0141-s2a`).

What was found: M0139 (executor-side narrowing) landed in full (all six
slices DONE) between S2's capture and this task, satisfying S2's stated gate
("re-measure once M0139 lands"). Live re-capture against the same bench
clusters S1/S2 used (`:65433`/`:65432`, both up throughout, neither
restarted) shows Q3/Q13/Q18 **byte-for-byte unchanged** from S1/S2's
pre-M0139 capture — identical costs to two decimal places, identical
Sorted-vs-Hashed choice. Confirmed the serving `:65433` binary genuinely
carries M0139's narrowing (behavioral check on a known two-table join, not a
binary-hash check — hashes differ on build-id noise even from identical
source). **Root cause pinned, not just re-observed**: `applyUpperNarrowing`
(M0139's narrowing entry point) runs at `Plan()`'s tail (`planner.go:189`),
strictly *after* `planStmtWithSettings` (`:141`) has already built, costed
and strategy-decided the whole tree via `createGroupingPaths`/
`addGroupingPaths` (`:1760` -> `groupingpaths.go:340`) — live
`EXPLAIN (VERBOSE)` shows the Hash Join's `Output:` list genuinely narrowed
to 7 columns while its cost/width figures (`aggInputWidth`,
`groupingpaths.go:327`, reading the PRE-narrowing child) stay identical to
pre-M0139. **This refutes S2's gating assumption**: M0139, exactly as landed
and sequenced, structurally cannot feed back into `costAgg`'s spill-arm
currency (`cost_funcs.go:516-540`) for ANY query — not "not yet true",
impossible under the current pipeline order. Secondary finding: R124 §7
(pre-M0139, in `TODO.md`) already tested an adjacent "corrected currency +
ncols narrowing" pairing and found it measured identical to the currency fix
alone (refuted, flag deleted at M0137-0009) — a second, independent prior
that "narrow, then re-measure" is not sufficient by itself. Also: Q10 (named
by `groupingpaths_test.go`'s `TestCostAggHashedNeverChargesSpill` doc
comment, but NOT a member of S1's own TPC-H aggregation-strategy list)
already matches PG at SF=1 live scale — the unit test's flip is a
small-scale artefact (same pattern as M0139-0005's Q4 finding), not a live
mismatch; not pursued further.

Key symbols: `internal/optimizer/planner.go:141` (`planStmtWithSettings`,
builds+costs+decides the whole tree) vs `:189` (`applyUpperNarrowing`, runs
strictly after, on the "finished Node"); `groupingpaths.go:327`
(`aggInputWidth`, reads the pre-narrowing child regardless);
`groupingpaths.go:340` (`addGroupingPaths`, the Hashed-vs-Sorted contest,
called from `createGroupingPaths:92`/`planner.go:1760`); `narrowoutput.go:708`
(`narrowPlanOutput`, always wraps in a NEW `*Project`, never mutates in
place); `cost_funcs.go:500-509` (comment naming the K65/K66 "ncols-narrowing
family" and R124's already-refuted pairing attempt).

Gates run: `go build ./...` clean (no `.go` file touched — pure recon, per
harness precedent for S0/S1/S2). No `go test` run for the same reason. No
values/plan-gate sweep (no behavior changed). Pre-commit hook's pgbench
smoke: ran and PASSED as part of `git commit` (TPC-B/simple-update/
select-only, 0 failed, ~142 TPS select-only — see commit `e40658f64` output).
`make ralph-state-guard`: found a real INCONSISTENT (status="running" vs a
stale progress.json "completed" marker left by a prior loop's clean exit),
self-repaired to "in_progress", re-ran and confirmed OK.

In-flight: none. (Temp artefacts `/tmp/estimate-audit-s2a`,
`/tmp/goopg-head-check`, `/tmp/m0141-s2a/` were all cleaned up before commit
— nothing under `analysis/` was added, since the live re-capture reproduced
S1's already-committed numbers byte-for-byte and added no new plan-shape
information worth freezing as a new artefact.)

Next step: Per the M0141 fix_plan wording, the two live M0141 resume points
are now **M0141-S2a-fix** (needs its OWN scoping pass before attempting —
two coupled changes: move/preview narrowing before `createGroupingPaths`'s
cost time, AND correct the `inAvgVarBytes` currency, since R124 already
falsified "currency alone") and **M0141-S2b** (mechanism B, K24's
Pathlist-not-Node upper-planner surgery — also needs its own scoping pass,
per S2's own filing). Neither is a same-loop pickup without that scoping
work first. Re-read the `## Current Priority` banner in `.ralph/fix_plan.md`
fresh next loop: M0137/M0138/M0139/M0140 are now ALL fully checked off (no
open items in any of them), so the banner's order collapses to
M0141/M0142(gated on M0138, which is now done, worth checking)/M0143. Given
M0141's own two live slices both need a dedicated scoping task before
they're selectable-sized, the next loop should check: (a) whether M0142
(Join-order costing, gated on M0138 having landed AND been measured — M0138
IS now fully done) has a selectable entry-recon task the way M0141-S0 was,
or (b) run the M0141-S2a-fix / M0141-S2b scoping pass itself as this loop's
one task, or (c) fall through to M0143 (gated on nothing) if both are
genuinely not same-loop-sized. Do not re-attempt S2a or re-verify M0139's
completeness — both are now closed facts.
