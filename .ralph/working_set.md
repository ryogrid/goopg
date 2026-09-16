Task: M0142-0008a-3i-plumbing-c7 — LANDED and committed.
Split `rebaseChainQual`'s single additive `base` shift into a piecewise
rebase for chained semiAnti links (c6's filed follow-on). Verified with a
real regression test (not throwaway instrumentation this time), confirmed
it changes corpus behavior, and root-caused + filed the NEXT blocker (c8).

Files this loop:
- internal/optimizer/joinsearchseam.go: semiAnti arm of `extractSearchLeaves`'s
  `walk` (~line 1290) now captures `outerWidth := len(j.Left.Output())` and
  `rightBase := width` (right after `walk(j.Left)` returns, before
  appending j.Right); new `rebaseSemiAntiChainQual(pred, outerWidth, base,
  rightBase)` function (next to `rebaseChainQual`) replaces the old
  `if pred != nil && base != 0 { rebaseChainQual(pred, base) }` call —
  shifts an operand by `base` if `cr.Index < outerWidth` (outer), else to
  `rightBase + (cr.Index - outerWidth)` (inner).
- internal/optimizer/semiantichain_test.go: new
  `TestExtractSearchLeaves_AdmitSemiAnti_ChainedLinksRebaseInnerKeyCorrectly`
  — two-link chained fixture (t1 outer, two independent single-table EXISTS
  over t2/t3), asserts both links' folded eq resolves within their own
  {lhs,rhs} via `relidsOfExpr`/`relsOverlap`, then `semiAntiOnQualsOK`.
  Verified this FAILS on the old uniform-`base` code (temporarily reverted,
  reproduced `relidsOfExpr=0x3, want overlap with rhs=0x4` — bits{0,1} vs
  needing bit 2 — then restored the real fix before commit).
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §42 —
  the c7 fix, verification (incl. the deliberate revert-to-confirm-test-is-
  real step), and §42.3's full root-cause of the NEXT blocker
  (`problemPairsOuterWithDerived` misclassifying a multi-relation semiAnti
  RHS leaf as "derived"/no-stats).
- docs/design/README.md: m0142-0008a-1 row tail extended (Python exact-
  string-replace on the c6-row anchor — do NOT full-file-rewrite this row,
  it has embedded raw newlines).
- .ralph/fix_plan.md: `-3i-plumbing-c7` flipped `[x]`; new
  `-3i-plumbing-c8` filed (narrow `problemPairsOuterWithDerived`'s
  "derived" classification, or exempt a semiAnti link's own RHS hand).
- .ralph/deferral_ledger.md: new row, task-id `m0142-0008a-3i-plumbing-c7`.

Key symbols: `rebaseSemiAntiChainQual` (new, joinsearchseam.go, next to
`rebaseChainQual`); the semiAnti arm of `walk` inside `extractSearchLeaves`
(joinsearchseam.go ~line 1276-1360); `unnest.go:4694`'s
`innerKey.Index = outerWidth + params[0].SubCol.Index` (the convention the
new rebase now correctly inverts); `leafIsDerivedInput`
(relfromjoinlist.go:523) and `problemPairsOuterWithDerived` (:564) — the
NEXT blocker, already deliberately covers `JoinSemi`/`JoinAnti`
(`TestProblemPairsOuterWithDerivedSemiOverDerived`/`...AntiOverDerived`,
semiantichain_test.go:140/154) but over-broad for a semiAnti's OWN RHS leaf.

Findings: (1) c7's fix is real and changes corpus behavior — full-corpus
sweep: `semianti-on-qual` declines 1→0 (was pinned to Q69 by c6's
isolation); `semianti-link-no-sjinfo` unchanged at 3. `jointype=semi`/`anti`
DPPATH reachability is STILL zero. (2) Per-query isolation on Q69 alone: NO
semiAnti-related decline reason appears at all anymore — Q69 clears BOTH
semiAnti gates. Instead ONE new seam-decline appears,
`reason=outer-over-derived nrels=6`, from `searchOneProblem` (a LATER
pipeline stage than the semiAnti gates, reached only once they pass — why
this was invisible before c7). (3) Root-caused it by reading
`problemPairsOuterWithDerived`/`leafIsDerivedInput` directly (no
instrumentation needed this time): `leafIsDerivedInput` returns true
whenever a leaf's underlying scan has `info.table == nil` — true for a
genuine CTE/subquery (correct, the C-04a/Q78 hazard) AND for ANY semiAnti
RHS leaf whose EXISTS body joins ≥2 relations (always `nil` `.table` since
it's a multi-table subtree), even though that leaf's row estimate is a
REAL cost-estimated value, not a defaulted guess. Q78 (single-table EXISTS
body) is unaffected by this specific guard since its RHS descends to an
actual base-table scan with a real `.table` — its continued
byte-identical plan traces to some OTHER, still-unfound gate, not this
one. (4) Confirmed via a controlled revert-and-rerun that the new unit
test is a REAL pin, not a tautology, before landing it permanently
(replacing the throwaway-instrumentation technique c6 used).

Next step: pick up **M0142-0008a-3i-plumbing-c8** — narrow
`problemPairsOuterWithDerived`'s (relfromjoinlist.go:564) "derived"
classification so it does not veto a semiAnti link's own multi-relation
RHS leaf while still declining on a genuine CTE/subquery elsewhere in the
problem. Two candidate directions in design doc §42.3 / fix_plan c8: (a)
give `leafIsDerivedInput` a signal for "real per-child stats despite no
single base table" (e.g. keyed off a non-default `EstimateRows()`) instead
of `.table == nil`; (b) exempt a semiAnti link's OWN `rhs` hand
specifically in `problemPairsOuterWithDerived`'s loop (likely closer to
the guard's original intent, avoids a new stats-confidence signal). MUST
NOT regress `TestProblemPairsOuterWithDerivedSemiOverDerived`/
`...AntiOverDerived`'s CTE case — add a third unit test for "multi-relation
EXISTS body must NOT decline via this guard" before landing. Re-run both
the TPC-DS SF0.25 sweep and the full-corpus `GOOPG_PGSHAPED_DP_TRACE=1`
sweep after landing (a `jointype=semi`/`anti` DPPATH line finally
appearing is still the only real reachability signal).

Gates run this loop: `go build ./...` clean; `go test
./internal/optimizer/...` green (includes the new regression test, plus a
controlled fail-then-pass check against the old code before committing);
full-corpus `GOOPG_PGSHAPED_DP_TRACE=1` sweep (private binary
`tmp/goopg-m0142-c7-bin`, built+removed this loop) — 96/100 EXPLAINs
succeeded (4 pre-existing unrelated parse gaps, unchanged), 150,137 DPPATH
lines, 0 `jointype=semi`/`anti`, `seam-decline` reasons:
`semianti-link-no-sjinfo`=3 (unchanged), `semianti-on-qual`=0 (was 1);
per-query isolation on Q69 alone confirms zero semiAnti-related declines
and one new `outer-over-derived nrels=6`; `scripts/tpcds-sf025-regression.sh
sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0, PLAN-SHAPE same=99
changed=0 vs the c6 commit (Q78 byte-identical, checksum unchanged since
c2). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — same
pre-existing `internal/parser` `GroupedJoinUnaliased` AST-drift failure as
every recent loop (`internal/optimizer` itself green). `make
ralph-state-guard` auto-repaired the same benign prior-loop clean-exit
marker seen every recent loop, then PASS. Commit's own pre-commit hook
runs the pgbench smoke.

In-flight: none. Private trace binary and sf025 server both stopped/removed
(`GOOPG_BIN=tmp/goopg-m0142-c7-bin bench/tpcds/server.sh stop sf025`, then
`rm tmp/goopg-m0142-c7-bin`).
