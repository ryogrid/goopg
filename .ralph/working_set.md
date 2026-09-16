Task: M0142-0008a-3i-plumbing-c16 — implemented c15's full resume point
(SJInfo Path→Join carrier + re-applied `joinInfoList` fix), verified
CORRECT, then REVERTED again because it surfaces a fourth issue (a stale
test fixture, not a production bug). No production code landed this loop.

What happened: applied all 4 pieces from c15's resume point (design doc
§50.3): (1) `Path.SJInfo *SpecialJoinInfo` field (path.go); (2)
`addNestLoopPath` AND `addHashJoinPath` (pathgen.go) both stamp `SJInfo:
sjinfo` — hash needed it too, since `M0142-0008a-3(iii)` (joinpaths.go)
had ALREADY lifted the hash arm's SEMI/ANTI decline before this c-chain
started; c15's "nestloop is the one reachable arm" claim (and
createHashJoinPlan's matching comment) were both STALE, fixed in place;
(3) `createNestLoopPlan` + `createHashJoinPlan` both copy `SJInfo:
p.SJInfo` onto the built `*Join`; (4) `joinInfoList: ctx.joinInfoList`
replacing the dead `semiAntiJoinInfoList` double-append (deleted — was a
genuine, confirmed duplicate-append bug: `ctx.joinInfoList` already gets
every semiAnti SJInfo via a DEDUPING append earlier in the same function,
joinsearchseam.go:594-595, landed by c4).

Result: `go build ./...` clean. c15's own tripwire
(`TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`'s
`j.SJInfo == nil` check) went GREEN — the fix is CONFIRMED correct. But a
LATER assertion in the SAME test then failed: `scans = 2, want 3`.
Root cause (confirmed via a throwaway debug test, removed before commit):
for that test's fixture SQL, `Plan()`'s final tree is now
`InnerJoin(SemiJoin(SeqScan(t1), Project(SeqScan(t2))), SeqScan(t3))` —
the search legally evaluates `t1 SEMI t2` BEFORE joining t3 in (since the
EXISTS correlates only to t1, MinLefthand narrows to {t1} alone), instead
of the test's assumed `SemiJoin(InnerJoin(t1,t3), t2)`. This is CORRECT,
desired behavior — literally the point of all the MinLefthand-narrowing
work this c-chain built — not a regression. The test's fixture is stale:
it implicitly depended on the search DECLINING for Semi/Anti (the
pre-c15 bug), so `Plan()`'s output was always the untouched syntactic
order; now that the search actually runs, it's free to (and does) pick a
different legal order.

Files touched this loop (all reverted, nothing committed except docs):
- internal/optimizer/{path.go, pathgen.go, createplannl.go,
  createplanjoin.go, joinsearchseam.go}: edited, fully reverted via
  `git checkout --` (diff confirmed empty before commit).
- .ralph/fix_plan.md: new item M0142-0008a-3i-plumbing-c16 (unchecked),
  full diagnosis + resume point.
- .ralph/deferral_ledger.md: new row for c16.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §51.
- docs/design/README.md: appended §51 summary to the m0142-0008a-1 row.

Key symbols: `Path.SJInfo` (path.go, new field — NOT landed, described in
fix_plan/design doc for reconstruction), `addNestLoopPath`/
`addHashJoinPath` (pathgen.go), `createNestLoopPlan`/`createHashJoinPlan`
(createplannl.go/createplanjoin.go), `joinInfoList: ctx.joinInfoList`
(joinsearchseam.go, replaces dead `semiAntiJoinInfoList`),
`TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`
(semiantichain_test.go:296-364, the test needing a rewrite).

Next step (concrete, sized like a single loop): (1) re-apply the 4-piece
diff from this loop (~40 lines across 5 files, fully specified in
fix_plan.md's c16 entry / design doc §51.1 — trivial to reconstruct);
(2) fix the ONE failing test by rebuilding its fixture tree by hand
(`*SeqScan`/`*Join` literals, bypassing `Plan()`'s search — follow
m0142_0008a_3i_verify_probe_test.go's schema/position conventions) so it
is a true white-box unit test of `extractSearchLeaves` again, independent
of DP-search tie-breaking — do NOT splice fragments from two separately
`Plan()`-built queries (coordinate spaces are per-statement, silent-wrong-
offset risk); `GOOPG_PGSHAPED_DP=0` does NOT help (package-level `var`,
read once at init, not per-test); (3) `go test ./internal/optimizer/...`
fully green; (4) run `scripts/tpcds-sf025-regression.sh sweep` with a
private `GOOPG_BIN` before landing. Still pending, unrelated, carried
since c11: `predp.go:159-176`'s stale Phase B doc comment.

Gates run this loop: `go build ./internal/optimizer/...` clean (both with
the fix applied and after revert). `go test ./internal/optimizer/...` —
FAILED with the fix applied (1 test, diagnosed above, its failure is
evidence FOR the fix), PASS after revert. `make ralph-state-guard`:
self-repaired the same stale "completed" progress marker noted the last
several loops, then reported OK.

In-flight: none. No private binaries/data copies were created this loop
(diagnosis used only the existing unit test suite + a throwaway
`TestZZDebugSemiTreeShape`, removed before commit) — nothing to clean up.
