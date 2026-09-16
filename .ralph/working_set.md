Task: M0142-0008a-3i-plumbing-b2 — item 6b design pass, closing §25.4's
open question (design doc §26). Recon/design-only this loop (no production
diff), continuing the established "prove/scope before coding" pattern.
Item 6b's DIRECTION was already settled last loop (§25.3); this loop
answered the remaining open question that gated starting to code it.
NOT complete — b2 stays open, now unblocked to start coding next loop.

Files this loop:
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §26 added.
- docs/design/README.md: m0142-0008a-1 index row extended with a §26
  summary (also flagged, honestly, that the row's §19.5-25 summaries were
  never written — a pre-existing gap from earlier loops, not touched
  beyond noting it; not this loop's job to backfill).
- .ralph/fix_plan.md: -3i-plumbing-b2's item 6b entry extended with §26's
  answer.

Key symbols (unchanged from last loop): `extractSearchLeaves`
(joinsearchseam.go:1085), `cumOffsets` (joinsearchseam.go, chain-level —
THE ONE WITH THE BUG), `relidsOfExpr`/`tableForCol` (joinrestrict.go:470,575,
GENERIC over whichever cumOffsets is passed in), `unnestExistsExpr`'s
`innerKey.Index` (unnest.go:4680-4705).
NEW this loop: `joinlistProblem.cumOffsets`/`.bindings`
(relfromjoinlist.go:84-99, a SEPARATE array, per-joinlist-ITEM not
per-chain-leaf, built from real `ctx.bindings` only), `RelOptInfo.baseOffset`
(path.go:636-654, set at joinsearch.go:430 from `bindings[i].offset`),
`baseRelLayout`/`translateToLayout` (createplanjoin.go:114-171,205-,
goopg's `set_join_references` analogue — the actual mechanism that rewrites
clause ColumnRefs into the EMITTED plan's coordinates at build time).

Finding this loop (§26): there are TWO DISTINCT `cumOffsets` arrays sharing
a name, not one — (1) joinsearchseam.go's chain-level array (§25's bug,
includes synthetic Semi/Anti leaf width), consumed only by
joinrestrict.go/local_filters.go's legality functions; (2)
relfromjoinlist.go's joinlist-item-level array, built exclusively from real
`ctx.bindings` FROM items (bushy.go's pre-search pipeline), which is what
actually feeds `RelOptInfo.baseOffset` -> `createplanjoin.go`'s
`translateToLayout`, the mechanism that builds the REAL emitted plan's
predicates. `relidsOfExpr`/`tableForCol` are generic functions each layer
calls with its OWN array — same function, non-interacting coordinate
spaces. Cross-checked every actual plan-BUILD site in the optimizer package
(createplannl.go:311, unnest.go:2692/2911/3373/3537/4634, memoize.go:109,
nl_index_join.go:839/1542): ALL re-derive width/offset fresh from the
actually-built Node's real Output() at build time, matching
unnestExistsExpr's established idiom; NONE read cumOffsets (either flavor).
Also confirmed: the bushy/joinlist layer has ZERO Semi/Anti awareness
today — no code threads a synthetic RHS leaf into
`joinlistProblem.bindings`, because a Semi/Anti pair is not yet admissible
as a joinable unit in the bushy DP at all (that gap is
M0142-0008c-3c/-3d/-4's still-unstarted job, not -b2's).

Answer: -3i-plumbing-b2's fix (§25.3's per-leaf (lo,hi) table) is CONFINED
to the chain layer — joinsearchseam.go / joinrestrict.go / local_filters.go
/ unnest.go's unnestExistsExpr. It does NOT reach relfromjoinlist.go,
path.go's baseOffset, or any createplan*.go builder. §25.4's 4-step resume
order is UNCHANGED by this finding; step (1) is now closed with evidence.
Filed a forward note (design doc §26, not a scope change to -b2): when
M0142-0008c-3c/-3d/-4 eventually makes a Semi/Anti pair bushy-DP-admissible,
the bushy layer will face an analogous but SEPARATE "does the RHS get its
own binding slot" question — -b2's fix does not pre-solve it.

Next step: §25.4's steps are now fully unblocked to CODE (not just design):
(1) [DONE by this loop — was step 1, now closed] (2) replace `cumOffsets
[]int` with the per-leaf (lo,hi) table in joinsearchseam.go; (3) update
relidsOfExpr/tableForCol (joinrestrict.go) to scan an explicit per-leaf
table instead of assuming a monotonic prefix sum; (4) point
unnestExistsExpr's innerKey.Index at the new out-of-band "next synthetic
slot" counter; (5) THEN code item 6a's *resolveContext plumbing (understood
to be unnecessary as a ctx.bindings append — the per-leaf table replaces it
outright); (6) write a new unit test exercising `(A SEMI JOIN B) JOIN C`
directly — no existing -b1 test covers a real leaf after a Semi/Anti node.
Do NOT flip `admitSemiAnti=true` in production before this lands.

Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 lands — both
still blocked on a real Semi/Anti SJInfo reaching addPathsToJoinrel
(fix_plan.md lines ~3891, ~3899).

Alternatives if -3i-plumbing-b2 is judged not worth continuing immediately:
M0141-S2b-2 (base join/scan Pathlist-across-search-boundary surgery — still
needs its own scoping pass). M0142-0003i/0003k remain BLOCKED on a
human-authorized shared `:65433` cluster reload — do not attempt.
Unrelated, NOT to be picked up under this banner unless explicitly
re-prioritized: `internal/parser`'s `GroupedJoinUnaliased` AST-drift gap
(pre-existing, traced to `dc91bd6b7`, ~60 failing test functions,
reconfirmed unchanged across 5+ consecutive loops now).

Gates run this loop: `go build ./...` clean (no production code touched,
docs/plan-only loop). `make ralph-state-guard`: found and auto-repaired
the same benign prior-loop clean-exit marker as the last several loops;
consistent after repair.

In-flight: none. Nothing outstanding from this loop — ready to commit
docs/fix_plan/ledger changes.
