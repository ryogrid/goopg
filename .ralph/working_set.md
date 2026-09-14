Task: M0140-0001 — re-measure the R43-era failing test set under the
`GOOPG_GATHER_PATHS` flip at HEAD. **COMPLETE and committed** this loop
(`e64cab7`), branch `plan-parity-with-pg-take2-ralph`. Recon-only, no
production code touched.

Files: `docs/design/0100-0149/m0140-0001-gather-paths-flip-failing-set-remeasure.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0140-0001 checked off + sub-bullets).

What was done: ran the four R43-named tests
(`TestSplitEqualityForHashMultiKey/searched_enumerator`,
`TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`
in `internal/optimizer`, `TestOwnedBuildPoisonPrebuiltBoundary` in
`internal/executor`) both at the default (`GOOPG_GATHER_PATHS` unset) and
under `GOOPG_GATHER_PATHS=all` (none of the tests set the var internally, so
the env-var flip alone reproduces what a default-on flip would change).

Findings:
  1. **All four PASS at default, all four FAIL under the flip** — same four
     names R43 identified ~87 rounds ago, none independently fixed by the
     intervening work. The M0140 prerequisite ("clear the failing set before
     landing the flip") is exactly as large today as R43 measured it.
  2. Three of the four (the two `Slice3` narrow-build tests +
     `TestOwnedBuildPoisonPrebuiltBoundary`) fail on what reads as **one
     shared mechanism**: under the flip, partial-path admission changes
     which join-tree shape wins the search *before* the narrowing pass runs,
     so their exact-narrow-build-column-set pins no longer match the actual
     (differently-shaped) plan. `TestSplitEqualityForHashMultiKey` fails for
     a distinct, unrelated reason — falls back to Nested Loop instead of
     hash-joining the multi-key equi-join under the flip.
  3. Deliberately did NOT adjudicate any of the four against PG 18.3 — that
     is M0140-0002's job per the recon-task boundary (a production/behavior
     diff in a recon task's commit is a scope violation).
  4. No ledger row filed: the divergence is an interaction between two
     goopg-internal planner features (parallel-path admission vs the
     narrowing pass), not a newly discovered PG-incompatibility.

Key symbols: `gatherPathsMode`/`gatherPathModeFromEnv`
(`internal/optimizer/gatherpaths.go:21,72`) — the flag this task flipped via
env var only, no code touched. Test bodies:
`internal/optimizer/multikey_hash_join_test.go:84`,
`internal/optimizer/pathtarget_test.go:1389,1488`,
`internal/executor/owned_build_poison_test.go:779`.

Gates run: `go build ./...` clean (no source changed — measurement-only
loop, matching the M0138-0001/-0006 precedent). `make ralph-state-guard`:
found and auto-repaired the same recurring stale status/progress.json
pattern as the last several loops (prior loop's clean-exit marker read as
project-completion), then consistent.

In-flight: none.

Next step: Per the banner, re-check `.ralph/fix_plan.md`'s `## Current
Priority` banner fresh. With M0140-0001 now done, the M0138/M0139/M0140 trio
narrows to: **M0140-0002** (adjudicate the four failures against PG 18.3,
with this loop's head start that three of them may be one finding), **M0139-S1**
(hook point inside the join tree, gated on M0137-0010, already unblocked),
or other open M0139/M0140 tasks. M0140-0002 has the most direct continuity
from this loop's findings (same test set, same mechanism hypothesis already
narrowed) — recommend it next unless the banner's ordering or a fresher read
suggests otherwise. M0142-0001 (join-order costing recon) also remains
selectable per M0138's closure last loop. Do not re-run this loop's four-test
measurement again without cause — it is committed and reproducible via the
exact commands in the design doc.
