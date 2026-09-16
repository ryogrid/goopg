Task: M0142-0008a-3i-plumbing-b2 — §25.4 step (3) scoping pass (the
`unnest.go` `innerKey.Index`/`j.Predicate` divergence question §27.4 left
open). LANDED and committed (87ef300f6), design-only, no production code
touched. NOT complete overall — this refutes step (3)'s original framing
and surfaces a bigger, previously-unstated gap; see Next step.

Files this loop (all committed):
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §28 added
  (§28.1/§28.2 refute the index-rebase premise with live traces, §28.3
  finds the real production-reachability gap, §28.4 finds a correctness
  gap for the future plan-build arm, §28.5 resume point).
- .ralph/fix_plan.md: -3i-plumbing-b2's entry extended with this loop's
  finding (mirrors §28, condensed).
- docs/design/README.md: m0142-0008a-1 index row extended with a §28
  summary.

Key symbols traced (no new symbols added):
- `unnest.go:4681-4726` (`unnestExistsExpr`'s `outerKey`/`innerKey`/
  `joinPredicate` construction) — read again, this time cross-checked
  against the walk's own conventions rather than in isolation.
- `joinsearchseam.go:1093-1281` (`extractSearchLeaves`, esp. the Semi/Anti
  arm 1132-1193 and the generic `base`/`rebaseChainQual` treatment every
  join type shares).
- `joinlayout.go:623-745` (`reresolveJoinByName`) and `:543-550`
  (`reconcileNLILayoutBody`'s `*Join` case, which already special-cases
  Semi/Anti by skipping the `.Right` recursion) — the codebase's OWN
  existing by-name key/predicate reconciliation idiom, previously
  unnoticed by this design doc's prior sections.
- `createplanjoin.go:492-504` (`joinInputs.joinPredicate`) and `:545-599`
  (`createHashJoinPlan`, esp. the "SEMI/ANTI are nestloop-only" comment at
  C-03c) — the ordinary-join convention of folding every hash-key pair
  into `Predicate` too, which `unnestExistsExpr` does NOT follow for its
  own primary key.
- `planner.go:1524-1536` — traced `origChain`'s capture point relative to
  `unnestSubqueriesInPlan`, the actual fact that answers §28.3.
- `semiAntiChainLink` (joinsearchseam.go:1449-1453, `jointype, lhs, rhs,
  pred` — confirmed via Serena `find_symbol`, no key fields at all).

Findings this loop (supersede §25.3/§27.2's framing):
1. `j.Predicate`'s index convention was never at risk — it already matches
   every other join type's "local, 0-based at this join's own leftmost
   leaf" convention, and `extractSearchLeaves`'s existing generic
   `rebaseChainQual` call (not Semi/Anti-specific) already handles it,
   pinned by the prior loop's own `TestBuildLeafSpansAttributesRealLeafAfterSyntheticCorrectly`.
2. `j.LeftKey`/`j.RightKey` are read by NO search-time function (grepped
   `extractSearchLeaves`/`buildLeafSpans`/`relidsOfExpr`/`tableForCol` —
   none touch them). Their only non-execution reader,
   `joinlayout.go`'s `reresolveJoinByName`, already handles Semi/Anti
   correctly wherever IT runs (but is explicitly skipped for
   PG-shaped-search-produced trees via `isSearchedTree`/
   `assertSearchedTreeNeedsNoReconcile`, so it doesn't apply to anything
   `tryPGShapedJoinSearch` builds today). **Step (3), as originally
   framed ("rebase innerKey.Index"), is moot — not deferred, not risky,
   just not a real problem.**
3. THE REAL GAP: `extractSearchLeaves`'s ONE production call
   (`joinsearchseam.go:309`) receives `chain = origChain`, and
   `planner.go:1533` captures `origChain := f.Child` BEFORE
   `unnestSubqueriesInPlan(node)` runs on line 1534 — so `origChain`
   structurally CANNOT contain a Semi/Anti join, ever. Flipping
   `admitSemiAnti=true` at today's one call site is STILL a guaranteed
   no-op, independent of everything §25-§27 already fixed. A NEW call
   over the POST-unnest tree — some form of the `predp.go` descend-loop
   extension already named in this item's existing fix_plan.md scope
   ("extend predp.go's descend loop to pass through non-Semi/Anti *Join
   nodes") — is the reachability PRECONDITION for the rest of item 6b's
   scope, not a parallel/optional piece of it. This was implicit before
   but not previously stated as the gate.
4. A genuine, previously undocumented correctness gap for whoever DOES
   wire that call: `semiAntiChainLink{pred: j.Predicate}` silently drops
   the join's own equijoin condition in the common single-equi-key case
   (params[0]'s equality lives ONLY in `LeftKey`/`RightKey`, deliberately
   excluded from `Predicate` since the hash match already enforces it —
   contrast `createplanjoin.go`'s `joinInputs.joinPredicate`, which DOES
   fold every hash-key pair into the emitted node's `Predicate`, the
   discipline the Q9 multi-equality bug was fixed by). Any future
   `-0008c-3c/-3d/-4` plan-build arm that reconstructs a node from `pred`
   alone would build an unconditional (Cartesian-like) Semi/Anti instead
   of the correct equi-semi-join. Cheap, localized fix when that work
   starts: fold `LeftKey`/`RightKey` into an explicit `OpEq` conjunct at
   `extractSearchLeaves`'s capture site, mirroring `joinPredicate`'s own
   idiom — NOT a change to `unnestExistsExpr` itself.

Next step: wire the predp.go descend-loop extension (already named in
fix_plan.md's -3i-plumbing-b2 scope: "extend predp.go's descend loop to
pass through non-Semi/Anti *Join nodes instead of hard-bailing,
predp.go:96-101") so SOME call reaches the post-unnest tree with
`admitSemiAnti=true` — this is now understood to be the reachability
precondition for everything else, not an independent sub-item. Land
finding 4's dropped-equijoin fix (fold LeftKey/RightKey into `pred` as an
explicit conjunct in `extractSearchLeaves`'s semi/anti capture) in the SAME
loop that lands the descend-loop extension — do NOT wire reachability
without it, or `admitSemiAnti=true` becomes a live wrong-rows bug the
moment it does anything. Only after both land does flipping
`admitSemiAnti=true` behind a real end-to-end fixture (§27.4's existing
gate) become a meaningful test. Also still likely depends on enough of
`M0142-0008c-3c`/`-3d`/`-4` (bushy-DP admission of a Semi/Anti pair as a
joinable unit) existing — this loop did not re-examine that dependency,
per §26.3's forward note.

Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 fully lands —
both still blocked on a real Semi/Anti SJInfo reaching addPathsToJoinrel
(fix_plan.md, grep `M0142-0008c-3c`/`-3d` — re-grep line numbers, they
shift every loop this doc is touched).

Alternatives if -3i-plumbing-b2 is judged not worth continuing
immediately: M0141-S2b-2 (base join/scan Pathlist-across-search-boundary
surgery — still needs its own scoping pass). M0142-0003i/0003k remain
BLOCKED on a human-authorized shared `:65433` cluster reload — do not
attempt. Unrelated, NOT to be picked up under this banner unless
explicitly re-prioritized: `internal/parser`'s `GroupedJoinUnaliased`
AST-drift gap (pre-existing, traced to `dc91bd6b7`, ~60 failing test
functions — still the ONLY package failing in the units precommit gate,
unchanged by this loop, confirmed unrelated). Nightly triage
(ci/logs/action-items.md, run 20260916-035206, 13 items): already fully
filed under M-NIGHTLY in fix_plan.md by a prior loop — verified this loop,
all 13 AI-ids already appear (no new filing needed).

Gates run this loop: `go build ./...` clean (no .go files touched this
loop — design-doc-only change, verified build is unaffected).
`make ralph-state-guard`: found and auto-repaired the same benign
prior-loop clean-exit marker as several prior loops; consistent after
repair. Pre-commit hook's mandatory CI-parity pgbench smoke: PASS (TPC-B
42 tps, simple-update 42 tps, select-only 147 tps, 0 failed). No
`go test`/TPC-DS sweep run this loop — no production code changed, so
nothing to regression-gate beyond the trace-against-live-source already
embedded in §28's citations.

In-flight: none. Committed as 87ef300f6. Nothing outstanding from this
loop.
