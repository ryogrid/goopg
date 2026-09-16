Task: M0142-0008a-3i-plumbing-b2 — step (i) reachability-wiring scoping pass
(design doc §30). Design/docs-only loop, no production code touched.

Files this loop:
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §30 added.
- .ralph/fix_plan.md: -3i-plumbing-b2's entry extended with §30's two
  findings + corrected next-step.
- docs/design/README.md: m0142-0008a-1 index row extended with a §30
  summary sentence.

Key symbols:
- `tryJoinSearch` (joinsearchseam.go:202) — thin wrapper, 4 call sites
  (predp.go:139, planner.go:1544/1569/1606); only predp.go:139 could ever
  meaningfully pass admitSemiAnti=true.
- `tryPGShapedJoinSearch` (joinsearchseam.go:216) — the function actually
  gating reachability. Its preamble (`:226` nrels, `:261` nprefix, `:314`
  leaf-count decline, `:333`/`:347` offset-disagreement declines) is keyed
  off `ctx.bindings`/`ctx.joinlist`, BOTH frozen before
  `unnestSubqueriesInPlan` runs.
- `runJoinSearchBelowPinned` (predp.go:73) — the descend loop originally
  targeted for the reachability fix; read in full this loop (its
  spineJoins/spineFilters bookkeeping + bottom-up reresolveJoinByName
  splice-back, lines 73-202).

Findings this loop (both from live tracing, not re-derivation):
1. §26.3's "-3i-plumbing-b2 depends on M0142-0008c-3c/-3d/-4" note
   (carried forward verbatim through §28.5 and §29) is BACKWARDS.
   fix_plan.md's own -0008c-3c/-3d entries state the opposite: they are
   blocked ON -3i-plumbing-b2 landing first ("no TPC-DS measurement can
   distinguish 'correct but unreachable' from 'wrong' while
   addPathsToJoinrel never receives a real SEMI/ANTI sjinfo"). -0008c-3a/
   -3b (their prerequisite) are already [x] DONE. So -3i-plumbing-b2 does
   NOT need -0008c-3c/-3d/-4 first — that false dependency is now removed
   from its critical path.
2. The obvious next-step design (thread admitSemiAnti through
   tryJoinSearch; have predp.go's descend loop stop pinning the outermost
   spine Semi/Anti join so the tree handed to tryJoinSearch actually
   contains one) is NOT sufficient by itself. `tryPGShapedJoinSearch`'s
   own preamble computes nrels/nprefix from ctx.bindings/ctx.joinlist
   (frozen pre-unnest); a chain rooted above a Semi/Anti join returns one
   more leaf (the -b1 synthetic RHS leaf) than nprefix expects, so
   joinsearchseam.go:314's `len(scans) != nprefix` "leaf-count" decline
   fires unconditionally. This is a DIFFERENT, deeper blocker than §28's
   origChain finding, and is NOT fixed by §25-§27's cumOffsets->
   []leafSpan work (that fixed chain-internal attribution via
   extractSearchLeaves's own widths/scans returns, not this outer
   ctx.bindings-keyed size gate — confirmed by reading :331/:408, which
   derive fresh from extractSearchLeaves's return values and are fine
   with a widened chain).

Two candidate designs recorded in §30, NEITHER coded:
(a) Widen ctx.joinlist/ctx.bindings's counts to include the synthetic
    Semi/Anti RHS leaf before the preamble runs — reopens §24.2's
    ~24-consumer-site audit question, but narrower (only 3 preamble reads
    need the count, not the whole chain walk / not a live rangeBinding
    visible to all ~24 sites).
(b) A parallel entry point that bypasses tryPGShapedJoinSearch's
    ctx.bindings/ctx.joinlist-keyed preamble entirely and reuses only its
    post-preamble DP-core logic (conjunct partitioning onward, :396+,
    which is already cumOffsets/widths-driven and therefore already
    unnest-agnostic).

Next step: decide between (a) and (b) — a design decision, not yet sized
as code — before touching predp.go or joinsearchseam.go again. Coding the
predp.go descend-loop extension first, as originally planned by the prior
loop's baton, would have produced a change that still hits the :314
"leaf-count" decline and stays just as inert, for a reason the
descend-loop change alone cannot fix. Whoever picks this up next should
weigh (a) vs (b) by re-reading §24.2's exact ~24-consumer list (found by
grep in that section) and checking how many of those 24 sites are
actually reachable with a Semi/Anti-RHS-bearing statement in scope, since
that number is the real cost of option (a).

M0142-0008c-3c/-3d remain independently available to pick up once
-3i-plumbing-b2 lands (per finding 1) but are not blocking this item's own
design decision.

Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 fully lands —
both still blocked on a real Semi/Anti SJInfo reaching addPathsToJoinrel
per their own fix_plan text (unaffected by finding 1's correction — that
correction only reverses which item blocks which, not whether the block
exists).

Alternatives if -3i-plumbing-b2 is judged not worth continuing
immediately: M0141-S2b-2 (base join/scan Pathlist-across-search-boundary
surgery — still needs its own scoping pass). M0142-0003i/0003k remain
BLOCKED on a human-authorized shared `:65433` cluster reload — do not
attempt. Unrelated, NOT to be picked up under this banner unless
explicitly re-prioritized: `internal/parser`'s `GroupedJoinUnaliased`
AST-drift gap (pre-existing, ~60 failing test functions — still the ONLY
package failing in the units precommit gate, unchanged by this loop).

Gates run this loop: `go build ./...` clean (no .go files touched this
loop; ran as a sanity check anyway). `make ralph-state-guard`: found and
auto-repaired the same benign prior-loop clean-exit marker as several
prior loops; consistent after repair. No optimizer/executor code changed,
so `go test ./internal/optimizer/...` and the TPC-DS SF0.25 sweep were not
re-run (nothing to regress — design-doc/fix_plan/README edits only).
Nightly triage: checked ci/logs/action-items.md's latest run
(20260916-035206, 13 items) — all 13 AI-ids already filed under existing
or new fix_plan tasks by a prior loop; nothing to file this loop.

In-flight: none. Nothing outstanding from this loop once committed.
