Task: M0142-0008a-3i-plumbing-c11 — root cause CONFIRMED live, exact
one-line fix identified, but REVERTED before commit (not landed) after it
surfaced two further pre-existing regressions. Re-opened with corrected,
narrower scope in fix_plan.md. Production code diff for this loop is ZERO
(joinsearchseam.go / joinsearchlevel.go both `git diff`-clean); only docs
(design doc §46, fix_plan.md, deferral ledger) changed.

Files this loop (all reverted except docs):
- internal/optimizer/joinsearchseam.go: tried
  `joinInfoList: ctx.joinInfoList` (deleting `semiAntiJoinInfoList`) to fix
  the duplicate-append bug c10 found. Verified live it fixes Q69's
  reachability. REVERTED (`git checkout --`) after the SF0.25 sweep showed
  it regresses Q10 PASS→ERROR. Also tried a `recover()`-guarded wrapper
  (`planJoinlistSearchRecovered`) around `planJoinlistSearch` for the
  `len(semiAnti)>0` path specifically, to neutralise a SEPARATE panic in
  `createPlanAtSearchRootRange` — this worked for Q69 (no more crash, 100
  rows, matches oracle) but does NOT cover Q10's failure (a different,
  non-panic error at a different pipeline stage), so it alone isn't
  sufficient either. Both attempts reverted; NEITHER is in the tree now.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §46 —
  full root-cause writeup (46.2), the fix + why it was reverted (46.3),
  and the two prerequisite tasks for c11 to actually land (46.4).
- .ralph/fix_plan.md: c11 entry rewritten with the confirmed root cause,
  the exact fix, and the corrected re-opened scope (fix boundaryMap's
  semiAnti exemption AND Q10's OR-EXISTS admission BEFORE re-attempting).
- .ralph/deferral_ledger.md: new row for this loop's landed-diagnosis/
  reverted-fix outcome.

Key symbols: `tryPGShapedJoinSearch` (joinsearchseam.go:215-823) — the
`joinInfoList: semiAntiJoinInfoList(ctx.joinInfoList, semiAnti)` call
(~line 779, UNCHANGED in the committed tree) is the actual bug: `base`
(== `ctx.joinInfoList`) already contains every `semiAnti[*].sjinfo`
pointer thanks to the population loop 20 lines above (joinsearchseam.go:
593-597, c6), so appending `semiAnti` a second time double-counts;
`joinIsLegal` (joinsearchlevel.go:239-259) then declines a pairing that
matches the duplicate twice as "matches multiple SpecialJoinInfos".
`createPlanAtSearchRootRange`/`boundaryMap` (createplanroot.go:108-269) —
the NEW panic site once the duplicate-fix unblocks the search;
`whereEligibleForPreDPUnnest` (predp.go:34-43) — does NOT check for
OR-combined EXISTS, likely the real gap behind Q10's regression.

Findings: (1) c10's "two independently-built pointer-distinct clones"
hypothesis was WRONG in mechanism (though right that duplication happens):
live tracing found ONE call site's own local list-construction
(`semiAntiJoinInfoList`) re-appending pointers its OWN function's earlier
population loop had already added — a simple double-count, not a clone
provenance mystery. (2) Fixing it is a proven, verified one-liner
(`joinInfoList: ctx.joinInfoList`) that DOES unblock Q69's DP search for
the first time in the c5-c11 series (`jointype=semi`/`anti` DPPATH lines,
full 6-relation `status=ok`). (3) But "unblocks the search" immediately
walks into TWO more pre-existing, independent, never-before-reachable
bugs: a boundary-totality panic (semiAnti leaves' internal columns
wrongly required to be "published") and a Q10-specific outer-ref depth
error (likely an admission-scope bug: OR-combined EXISTS should probably
never enter the semiAnti chain at all). (4) Confirmed via the MANDATORY
`scripts/tpcds-sf025-regression.sh sweep` gate (run with a private
`GOOPG_BIN=tmp/goopg-sf025-c11-bin` to avoid the shared nightly binary)
that landing the fix alone is a real regression: `PASS -Q10` / `ERROR
+Q10` in the status-delta, even though Q69 correctly flips to PASS/100
rows in the same sweep. (5) Q69's row count matching the oracle is NOT
strong evidence the plan is fully correct — its checksum is `ck=n/a`
(LIMIT-saturated), so a content-level check never ran; the boundary
panic's underlying column-miscount is exactly the kind of bug that could
produce a correct row count with wrong values on some OTHER query.

Next step: pick up **M0142-0008a-3i-plumbing-c11** (`.ralph/fix_plan.md`,
design doc §46.4) at prerequisite (a): fix
`createPlanAtSearchRootRange`/`boundaryMap`'s totality contract
(createplanroot.go) to exempt a semiAnti synthetic leaf's own coordinate
range — a SEMI/ANTI join never projects RHS columns above itself, so
nothing should ever require those columns "published" at the search
root; the existing `fill`-licensed narrowed-index-only-leaf mechanism is
the template to extend. Re-verify with the same private-binary +
`GOOPG_PGSHAPED_DP_TRACE=1` method (log truncated before each restart)
that Q69 no longer panics AND that its `EXPLAIN` plan shape actually
changes (not just "search succeeded, still fell back silently"). THEN (b)
root-cause Q10's OR-combined-EXISTS admission before re-attempting the
`joinInfoList` fix; re-run the FULL SF0.25 sweep (not just Q69/Q10) before
landing anything.

Gates run this loop: `go build ./...` clean (production code is
identical to HEAD — the fix was reverted). `go test ./internal/optimizer/...`
green (unchanged from HEAD, cached-equivalent). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` — PASS except the same pre-existing
`internal/parser` `GroupedJoinUnaliased` AST-drift failure every recent
loop has hit (unrelated, unchanged, tracked under M-NIGHTLY).
`scripts/tpcds-sf025-regression.sh sweep` run TWICE this loop (private
`GOOPG_BIN`): once against the (reverted) fix — showed Q10 PASS→ERROR,
Q69 ERROR(crash)→PASS depending on which of the two fix attempts was
active — this is the evidence that led to reverting; a THIRD run against
the final, fully-reverted tree was not repeated since `git diff` already
proves byte-identity with HEAD's known-good state (no need to re-measure
what didn't change). `make ralph-state-guard` auto-repaired the same
benign prior-loop clean-exit marker seen every recent loop, then PASS.
No commit's pre-commit pgbench hook run yet this loop (see below — will
run when this working-set/doc-only commit is made).

In-flight: none. Private trace binary (`tmp/goopg-c11-bin`,
`tmp/goopg-sf025-c11-bin`) and private SF0.25 data copy
(`tmp/c11-sf025-data`) all stopped/removed before this write-up. All
throwaway `C11TRACE` instrumentation in `joinsearchseam.go` and
`joinsearchlevel.go` reverted (`git checkout --`), confirmed via
`git diff --stat` showing zero changes to both files.
