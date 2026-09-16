Task: M0142-0008a-3i-plumbing-b — SCOPING PASS ONLY (design doc §22). No
production code changed this loop; this was the "needs its own dedicated
scoping pass before coding" deliverable §21.4 explicitly asked for.

Files this loop: docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md
(§22 added — both §21.4 open questions answered + a new §22.3 finding),
docs/design/README.md (index row extended with §22 summary),
.ralph/fix_plan.md (`-3i-plumbing-b` flipped to `[x]` scoping-only, replaced
by two new sub-items `-3i-plumbing-b1`/`-b2`; the two `M0142-0008c-3c`/`-3d`
"blocked on" references retargeted from `-3i-plumbing-b` to
`-3i-plumbing-b2` specifically), .ralph/deferral_ledger.md
(`M0142-0008a-3i-plumbing-b` row appended).

Key symbols (unchanged locations, just re-confirmed by trace):
`origChain` (planner.go:1533, predp.go:73/87/107 — ONLY 2 use sites,
grep-confirmed), `extractSearchLeaves`'s walk (joinsearchseam.go:1104-1191),
`chainOnQual.belowNullable` (joinsearchseam.go:1217-1220),
`tryPGShapedJoinSearch` (joinsearchseam.go:216-), `splitOuterSpine`
(joinsearchseam.go:710-725, a DIFFERENT pre-existing spine mechanism for
ORIGINAL-FROM-clause OUTER joins — NOT reusable for the EXISTS-derived
Semi/Anti spine, confirmed by reading it), `existsUnnestSJInfo`
(unnest.go:4397-4458, the throwaway synL=1/synR=2 numbering item 4 needs to
replace), `unnestSubqueriesInPlan`/`unnestExistsExpr` (unnest.go:424,4461 —
confirmed NEITHER takes a `*resolveContext`).

Hypothesis/Findings (IMPORTANT — read before picking up -3i-plumbing-b1):
1. §21.4 Q1 (does origChain's capture point need to move?) — NO. origChain
   has exactly one use site; "move the capture post-unnest" was never the
   right fix because origChain structurally never contains a Semi/Anti node
   NO MATTER WHEN captured (unnesting wraps it from above, never replaces
   it). The real content of item 1 is "feed tryJoinSearch a tree that
   includes the spine," a predp.go-level tree-shape change, not a
   planner.go-level timing change.
2. §21.4 Q2 (does belowNullable need a Semi/Anti-aware arm?) — NO. A
   Semi/Anti walk arm should recurse j.Left and return `below` UNCHANGED,
   exactly like the existing INNER-link arm (joinsearchseam.go:1173),
   because Semi/Anti null-extends NEITHER side. This REMOVES an item from
   scope, doesn't add one.
3. NEW finding, not in §21.4's original 5-item list: `ctx.bindings`/
   `ctx.joinlist` are fixed at FROM-clause-resolution time (planner.go:3096-
   3103, the ONLY assignment site, grep-confirmed), strictly BEFORE
   unnestSubqueriesInPlan runs — and neither unnestSubqueriesInPlan nor
   unnestExistsExpr take a *resolveContext at all. So a Semi/Anti RHS leaf
   the walk would admit has NO ctx.bindings entry, and
   tryPGShapedJoinSearch's existing leaf-count/offset checks
   (joinsearchseam.go:309 `len(scans)!=nprefix`, :324 offset-agreement) would
   DECLINE the search outright — silently inert, not a crash, but a real
   6th coupled item (call it item 6) that must be solved before items 2-4
   can ever actually fire in production. Filed as the whole of
   -3i-plumbing-b2's scope.

Next step: per banner order (M0137-M0143 group), pick up
**M0142-0008a-3i-plumbing-b1** (fix_plan.md, design doc §22.4) — the INERT
scaffold: (1) extend predp.go's descend loop (predp.go:96-101) to pass
through non-Semi/Anti *Join nodes instead of hard-bailing; (2) extend
extractSearchLeaves's *Join type-switch to admit Semi/Anti per §22.2's now-
SETTLED semantics (recurse j.Left, append j.Right as ONE opaque leaf via the
same `scans = append(scans, n)` path the existing "not an admitted type"
branch already uses at :1111-1114, return `below` unchanged, build a
semiAntiChainLink); (3) rebuild the matched SpecialJoinInfo's
Syn/MinLefthand/Righthand from real leaf-index bits using
`semiAntiLinksHaveSJInfos` (landed by -3i-plumbing-a) to find the match,
replacing existsUnnestSJInfo's synL=1/synR=2 placeholder. MUST land gated
behind an eligibility check that PROVABLY cannot fire in production yet
(item 6 is not done) — same "prove inert before wiring" verification shape
as -3i-plumbing-a/-0008c-1/-2/-3a: new unit tests on the extended walk
directly, plus a TPC-DS SF0.25 sweep proving zero plan-shape change.
Do NOT attempt item 6 (ctx.bindings/joinlist extension into
unnestSubqueriesInPlan's signature) or the actual cutover in the same loop —
that's -3i-plumbing-b2, which needs its OWN scoping pass into
unnestSubqueriesInPlan's other internal call sites (IN-family unnesting
etc.) first, not investigated yet.
Do NOT pick up M0142-0008c-3c/-3d before -3i-plumbing-b2 lands (retargeted
this loop from "-b" to "-b2" specifically) — both still blocked on a real
Semi/Anti SJInfo reaching addPathsToJoinrel.

Alternatives if -3i-plumbing-b1 is judged not worth continuing immediately:
M0141-S2b-2 (base join/scan Pathlist-across-search-boundary surgery — still
needs its own scoping pass). M0142-0003i/0003k remain BLOCKED on a
human-authorized shared `:65433` cluster reload — do not attempt.
Unrelated, NOT to be picked up under this banner unless explicitly
re-prioritized: `internal/parser`'s `GroupedJoinUnaliased` AST-drift gap
(pre-existing, traced to `dc91bd6b7`, ~60 failing test functions,
reconfirmed unchanged across 4 consecutive loops as of the prior loop — not
re-run this loop since no code changed).

Gates run: `go build ./...` (clean — expected, no production code touched
this loop, doc/plan-file changes only). `make ralph-state-guard` — clean,
no repair needed. Full test suite / TPC-DS sweep NOT re-run this loop
(nothing that could affect them changed — docs and fix_plan/ledger only).

In-flight: none.
