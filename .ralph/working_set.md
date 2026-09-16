Task: M0142-0008a-3i-plumbing-b2 — item 6b design pass (design doc §25).
Recon/design-only this loop (no production diff), continuing the
established "prove/scope before coding" pattern. Item 6b now has a
settled DIRECTION but is not yet coded. NOT complete — b2 stays open.

Files this loop:
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §25 added.
- docs/design/README.md: m0142-0008a-1 index row extended with §25 summary.
- .ralph/fix_plan.md: -3i-plumbing-b2's item 6b entry extended with §25's
  finding + settled direction + 4-step resume order.
- .ralph/deferral_ledger.md: second M0142-0008a-3i-plumbing-b2 row appended
  (the §24 row from last loop stays; this is an additional, deeper finding).

Key symbols: `extractSearchLeaves` (joinsearchseam.go:1085, the walk that
appends the Semi/Anti RHS leaf and accumulates `width`), the shared
`cumOffsets []int` built at joinsearchseam.go:322-328 and threaded into
`relidsOfExpr`/`tableForCol` (joinrestrict.go:470,575) from EVERY
legality/restriction-list consumer (`buildRestrictInfos`,
`partitionConjunctsForJoinPlanning`, `deriveOuterLinkConstants`,
`outerOnQualsOK`, `innerOnQualsBelowNullableOK`, `searchConsumes`,
`semiAntiOnQualsOK`), `unnestExistsExpr`'s `innerKey.Index = outerWidth +
params[0].SubCol.Index` construction (unnest.go:4680-4705).

Findings (read before picking up item 6b again):
1. Live-traced `(A SEMI JOIN B) JOIN C ON qualAC` through the walk: the
   join's own `base := width` (joinsearchseam.go:1199) is captured BEFORE
   recursing into its children, so it is 0 at the root regardless of what
   lives inside `j.Left` — `qualAC`'s `ColumnRef` for `C` (resolved
   pre-unnest against real `ctx.bindings` at absolute index `wA+k`) is
   stored into `onQuals` completely unrebased.
2. But `cumOffsets` (built from `widths[]` in WALK order, includes B's
   real width unconditionally) places `C` at column offset `wA+wB`, not
   `wA` — so `relidsOfExpr(qualAC, cumOffsets)` misattributes `C`'s
   reference to synthetic leaf `B`'s RelSet bit whenever `wB>0` (NOT
   guaranteed zero for the M14/R3-4 shapes item 6 targets). This is a
   silent wrong-legality/wrong-rows risk, independent of `ctx.bindings`,
   and strictly worse than §24.2's `FOR UPDATE` crash finding.
3. Ruled out two tempting fixes with concrete code evidence (not just
   reasoning): (a) zeroing the synthetic leaf's `cumOffsets` contribution
   breaks `semiAntiOnQualsOK`'s own already-tested requirement that the
   RHS occupy a real-width slice (`unnestExistsExpr` already encodes
   `innerKey.Index` that way); (b) rebasing nearby quals via the existing
   `rebaseChainQual`/`base` mechanism cannot work because the needed shift
   is a function of WHICH leaf a qual references, not of which join node
   holds it — a single scalar `base` per node can't express a piecewise
   shift.
4. Settled direction (§25.3): decouple leaf/RelSet-bit space (walk order,
   untouched) from column-index space. Real leaves keep their exact
   `ctx.bindings` ranges. Synthetic (Semi/Anti RHS) leaves get an
   out-of-band range appended AFTER the total real width instead of
   inline at their walk position. `relidsOfExpr`/`tableForCol`
   (joinrestrict.go:470-493,:575-584) generalize from "scan a monotonic
   prefix-sum array" to "scan an explicit per-leaf (lo,hi) table" (still
   one linear pass, just not required to be sorted). `unnestExistsExpr`'s
   `innerKey.Index` sources its base from the new out-of-band counter
   instead of `outerWidth`.
5. This SAME mechanism is also §24.2's undesigned "side-channel" option —
   no `ctx.bindings` entry is ever created, so none of the ~24 unaudited
   consumer sites (including the `FOR UPDATE` crash site) are touched.
   Item 6b is therefore ONE design decision, not two independent ones.

Next step: before coding, verify §25.3's remaining open question — does
the FINAL winning-plan build step (wherever -b1's item-4 SJInfo-rebuild
currently re-derives real leaf-index bits post-search) already re-derive
`outerWidth` fresh from the winning tree's actual LHS output at build
time (mirroring how `unnestExistsExpr` builds it today), or does it also
need the out-of-band scheme? This determines whether the fix is confined
to the search's internal bookkeeping only, or reaches the emitted plan
too. Then: (1) replace `cumOffsets []int` with the per-leaf (lo,hi) table
in joinsearchseam.go; (2) update relidsOfExpr/tableForCol; (3) point
unnestExistsExpr's innerKey.Index at the new counter; (4) THEN code item
6a's *resolveContext plumbing (now understood to be unnecessary as a
ctx.bindings append — the per-leaf table replaces it outright); (5) write
a new unit test exercising `(A SEMI JOIN B) JOIN C`-shaped input directly
— no existing `-b1` test covers a real leaf positioned after a Semi/Anti
node. Do NOT flip `admitSemiAnti=true` in production before this lands.

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
