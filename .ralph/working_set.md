Task: M0142-0008a-3i-plumbing-c15 — re-attempted this loop, REVERTED and
refiled with a concrete resume point (design doc §50, fix_plan.md, deferral
ledger). No production code landed this loop.

What happened: c14 (previous loop) fixed Q69's crash via `chainCarriesLateral`.
That unblocked c11 item (c): re-apply `joinInfoList: ctx.joinInfoList`
(delete the duplicate-appending `semiAntiJoinInfoList` helper,
joinsearchseam.go:767). The fix itself is CONFIRMED CORRECT for its own
mechanism — it built clean and go-tested clean except for exactly one
failure:
`TestExtractSearchLeaves_AdmitSemiAnti_NarrowsMinLefthandToCorrelatedRelation`
(semiantichain_test.go:316) now finds a `JoinTypeSemi` node in `Plan()`'s
returned tree with `.SJInfo == nil`.

Root cause (confirmed by reading, not yet re-instrumented after the
revert): `.SJInfo` is set exactly ONCE in production
(`unnest.go:4837`, `existsUnnestSJInfo`, at AST-unnesting time, long before
the DP search runs). NO `createPlan` join constructor
(`createHashJoinPlan` createplanjoin.go:565, `createMergeJoinPlan`
createplanjoin.go:724, the plain-nestloop constructor createplannl.go:143
— "the one arm a searched SEMI or ANTI join can reach" per its own comment
— or the NLI constructors createplannl.go:360+) ever copies `.SJInfo` onto
the fresh `*Join` node it builds. Before this fix the duplicate-list bug
always declined the search for any Semi/Anti statement, so the ORIGINAL
AST-built `*Join` node (carrying `.SJInfo` directly from unnesting) always
reached the final tree unchanged. With the search now succeeding, the
final tree's Semi join is a NEW node `createPlan` built from a `Path`,
which had nowhere to carry `.SJInfo` forward — a THIRD latent,
previously-untested defect in the same "unwinnable path is untested path"
family as c12/c13 (`goopg_unwinnable_path_is_untested` memory).

Scope: `grep -rln '\.SJInfo\b' internal/` returns exactly 3 files
(joinsearchseam.go — search-time only, strictly before createPlan — and
two `_test.go` files). Nothing downstream of `createPlan` reads
`Join.SJInfo` today, so this is not (yet) a live wrong-query-result bug,
but it breaks a real protected unit-test invariant and is filed as a
genuine defect rather than waved off — landing the joinInfoList fix with a
known-broken invariant would repeat the exact "ship a partial fix that
silently exposes an untested path" mistake this whole c-chain has been
careful to avoid.

Files touched this loop (all committed, commit 10ceb8e7c):
- .ralph/fix_plan.md: new item M0142-0008a-3i-plumbing-c15 (unchecked),
  full diagnosis + resume point.
- .ralph/deferral_ledger.md: new row for c15.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §50.
- docs/design/README.md: appended §50 summary to the m0142-0008a-1 index
  row (same "row's true physical end" convention as §49).
- internal/optimizer/joinsearchseam.go, semiantichain_test.go: edited
  then FULLY REVERTED (`git checkout --`) — not part of the commit,
  `git diff --stat` empty, confirmed before committing.

Key symbols: `addNestLoopPath` (pathgen.go:175 — already receives
`sjinfo` as a parameter, currently drops it), `Path` struct (path.go —
needs a new `SJInfo *SpecialJoinInfo` field), the plain-nestloop
constructor (createplannl.go:143, the `j := &Join{...}` literal —
needs `SJInfo: p.SJInfo`), `existsUnnestSJInfo` (unnest.go:4837, the sole
producer of `.SJInfo` today).

Next step (concrete, sized like a single loop): (1) add `SJInfo
*SpecialJoinInfo` to `Path`; (2) `addNestLoopPath` (pathgen.go:192-206)
sets `SJInfo: sjinfo` on the `&Path{...}` it builds — also thread the
`sjinfo` param through the hash/merge path constructors for uniformity
even though only nestloop is reachable for Semi/Anti today; (3)
createplannl.go's plain-nestloop constructor adds `SJInfo: p.SJInfo` to
its `&Join{...}` literal (nil is correct/harmless for every non-Semi/Anti
join); (4) re-run `go test ./internal/optimizer/...` (the narrowing test
above is the tripwire — must go green); (5) THEN re-apply the
`joinInfoList: ctx.joinInfoList` one-liner (c11 item (c) / this loop's
reverted diff) and run the FULL `scripts/tpcds-sf025-regression.sh sweep`
with a private `GOOPG_BIN` before landing both pieces together. Still
pending, unrelated, carried since c11 (not this loop's job):
`predp.go:159-176`'s stale Phase B doc comment.

Gates run this loop: `go build ./...` clean (both with the temporary fix
applied and after revert). `go test ./internal/optimizer/...` — FAILED
with the fix applied (1 test, diagnosed above), PASS after revert. `go
test ./internal/executor/...` PASS (sibling-path/practice-card gate, run
after revert). `make ralph-state-guard`: self-repaired the same stale
"completed" progress marker from the prior loop's clean exit (same
pattern noted the last two loops), then reported OK. Pre-commit pgbench
smoke: PASS (TPC-B ~43 tps, select-only ~146 tps, 0 failed — the
"no connection to server" lines during warmup are the documented flaky
vacuum-phase probe, not a real failure; all three phases completed with
0 failed transactions).

In-flight: none. No private binaries/data copies were created this loop
(the diagnosis was done via the existing unit test + temporary `t.Logf`,
not a live SF0.25 repro) — nothing to clean up.
