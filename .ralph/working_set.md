Task: M0142-0008c-3c (hash-join unique-ify substitution) — LANDED, commit
9ecd0b461. Item is DONE in the same "correct, tested, still-unreachable"
sense -3b was. No in-flight work; next loop picks per fix_plan.md's banner.

Files this loop:
- internal/optimizer/pathgen.go: `addHashJoinPath` gained
  `uniq uniqueSide, sjinfo *SpecialJoinInfo`; substitutes the probe
  (uniqueSideOuter) or build (uniqueSideInner) side via `createUniquePath`.
  `generateHashJoinPaths` (test-only, no production caller) updated to pass
  `uniqueSideNone, nil` at its two internal calls.
- internal/optimizer/joinpathsparallel.go: `addPartialHashJoinPath` gained
  the same two params; declines `uniqueSideOuter` outright (PG's own
  `save_jointype != JOIN_UNIQUE_OUTER` gate) and substitutes+ParallelSafe-
  checks for `uniqueSideInner` (always declines today since
  `createUniquePath`'s `PathUnique` never sets `ParallelSafe`).
- internal/optimizer/joinpaths.go: `addPathsToJoinrel` threads `uniq`/`sjinfo`
  into both new call sites.
- internal/optimizer/joinpathsparallel_test.go: 3 pre-existing
  `addPartialHashJoinPath` calls updated with `uniqueSideNone, nil`.
- internal/optimizer/uniqueify_hash_builders_test.go: NEW, 6 direct unit
  tests mirroring `uniqueify_builders_test.go`'s pattern for the hash arm.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §35 —
  full-corpus DPPATH reachability re-check (145,191 lines, ZERO semi/anti),
  the `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK` dead-consumer root
  cause, and the -3c implementation summary.
- docs/design/README.md: m0142-0008a-1 index row extended with §35 summary.
- .ralph/fix_plan.md: -3c marked [x] DONE; -3d's note repointed at §35
  (shares the same blocker, do not re-run the recon); new task
  **M0142-0008a-3i-plumbing-c** filed under the M0142-0008a section as the
  actual next blocker for the whole -0008c family.
- .ralph/deferral_ledger.md: new row (task-id M0142-0008c-3c) recording the
  same finding for the standing ledger.

Key symbols: `addHashJoinPath`/`addPartialHashJoinPath` (now uniq/sjinfo-
aware); `semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK`
(`joinsearchseam.go` — defined, tested, but UNWIRED into production);
`j.SJInfo` in-place renumbering (`joinsearchseam.go:1223-1226`, reach into
`ctx.joinInfoList` still unconfirmed).

Hypothesis/Findings: CONFIRMED via full-100-query-corpus
`GOOPG_PGSHAPED_DP_TRACE=1` sweep — `addPathsToJoinrel` is STILL never
called with a SEMI/ANTI `SpecialJoinInfo` in production, even after
`M0142-0008a-3i-plumbing-b2`'s Phase B went live and changed Q78's plan.
Root cause identified (not just re-observed): `extractSearchLeaves`'s own
two named "consumers" of a Semi/Anti chain link — the SJInfo-legality check
and the qual-placement proof — are called only from test code, never from
`tryPGShapedJoinSearch` itself. Q78's plan change came from Phase B's SPLICE
mechanism re-arranging what surrounds a FIXED, reused Semi/Anti join node,
not from the DP tournament re-costing that node's own placement. This means
`M0142-0008c`'s entire unique-ify family (-3c now built, -3d/-4 not started)
cannot fire regardless of how much of it gets built until the admission
wiring gap is closed.

Next step: pick the next item per fix_plan.md's `## Current Priority`
banner (plan-parity group M0137-M0143 still first). Two natural candidates:
(a) **M0142-0008a-3i-plumbing-c** (filed this loop) — wire
`semiAntiLinksHaveSJInfos`/`semiAntiOnQualsOK` into `tryPGShapedJoinSearch`
and confirm/fix whether `j.SJInfo` reaches `ctx.joinInfoList`; this is the
critical-path item since nothing in `-0008c` can be measured until it lands.
(b) M0142-0008c-3d (merge + partial-nestloop unique-ify) — could still be
built following -3c's exact pattern (same "build, test directly, report
unreachability" precedent), but would face the identical blocker, so (a) is
the higher-value pick.

Gates run this loop: `go build ./...` clean; `go vet ./internal/optimizer/...`
clean; full `go test ./internal/optimizer/...` pass (all pre-existing tests
plus 6 new); TPC-DS SF0.25 sweep against a rebuilt private binary
(`tmp/goopg-sf025-scope-bin`, never touched shared clusters) —
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3,
PLAN-SHAPE same=99 changed=0; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` shows only the pre-existing unrelated
`internal/parser` `GroupedJoinUnaliased` failure (`internal/optimizer`
itself `ok`, cached); commit's own pre-commit hook ran the pgbench smoke
(PASS, TPS ~43/143 across the three transaction types) since a real commit
was made this loop; `make ralph-state-guard` found and auto-repaired the
same benign prior-loop clean-exit marker prior loops have also seen.

In-flight: none. Private SF0.25 server (`:65437`, `tmp/goopg-sf025-scope-bin`)
was stopped after the sweep; shared clusters were never touched (only
read-only EXPLAINs against the git-tracked SF0.25 oracle-comparison cluster).
