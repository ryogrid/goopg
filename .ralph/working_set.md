Task: M0142-0008a — scoping recon: size wiring SEMI/ANTI decorrelation into
the DP search's joinrel machinery. DONE (measurement only, no production
diff) and committed this loop.

Files: `docs/design/0100-0149/m0142-0008a-scoping-recon-semi-anti-decorrelation-census.md`
(new — full census + PG-oracle sizing writeup). `docs/design/README.md`
(index entry added, inserted before the existing `m0142-0008b` row).
`.ralph/fix_plan.md` (M0142-0008a closed `[x]`, decomposed into three new
open sub-tasks M0142-0008a-1/-2/-3).

Key symbols read (no edits): `unnestExistsExpr`/`canUnnestExistsExpr`
(`internal/optimizer/unnest.go`) — builds the physical `Join{Semi,Anti}`
node directly on the raw parse tree for a correlated EXISTS/NOT EXISTS.
`whereEligibleForPreDPUnnest`/`runJoinSearchBelowPinned`
(`internal/optimizer/predp.go`) — the S5a pre-DP pull-up (`GOOPG_UNNEST_PREDP`,
default ON) that pins the semi/anti join above a DP search which DOES run on
the subtree below it; its `descend` loop walks past `*Join{Semi,Anti}` nodes
only to find the splice point, never to hand them to `addPath`. PG oracle:
`SpecialJoinInfo` (`postgres/src/include/nodes/pathnodes.h:3027-3042`),
`join_is_legal` (`joinrels.c:350`), `pull_up_sublinks` (`prepjointree.c:468`).

Findings this loop:
- Corrected M0142-0008's own framing: the SEMI/ANTI bypass is NOT "no
  search runs at all" — S5a already runs the DP search on the ordinary
  tables below the pinned spine. The actual residual gap is narrower but
  not easier: the pinned semi/anti join itself never gets a joinrel/
  `SpecialJoinInfo`, so its PLACEMENT (relative join order) and ALGORITHM
  (Hash-semi vs NLI-semi) are never chosen by `addPath` cost comparison —
  `unnestExistsExpr`'s own control flow picks both today.
- A second, worse shape: when a WHERE clause mixes a correlated EXISTS with
  a co-resident SCALAR subquery, `whereEligibleForPreDPUnnest`'s all-or-
  nothing gate disqualifies the WHOLE predicate from pre-DP treatment, so
  it falls to the legacy post-DP path — a TOTAL bypass with no partial win
  at all. TPC-H Q22 is this shape (`> (SELECT avg(...))` alongside
  `NOT EXISTS (SELECT ... WHERE o_custkey = c_custkey)`).
- Census (grep + hand classification of every EXISTS/`IN (SELECT` hit in
  both corpora): 8 real queries carry a correlated EXISTS/NOT EXISTS — TPC-H
  Q4, Q21 (2 stacked: Semi+Anti), Q22 (total-bypass); TPC-DS query10/35
  (3 stacked Semi), query69 (3 stacked Anti), query16/query94 (2 stacked:
  Semi+Anti). TPC-DS's recurring store/web/catalog "channel comparison"
  idiom is entirely this shape — not a niche gap. All corpus `IN (subquery)`
  hits (TPC-H Q16/Q18/Q20's outer IN, TPC-DS query14/23/33/45/56/58/60/95)
  are non-correlated (CTE or self-contained bodies) — a different mechanism
  (`unnestNonCorrelatedInExpr`), not a witness for this gap.
- PG-oracle read confirms M0139-0005's "materially larger task" call was
  right, not just asserted: wiring this needs PG's semi/anti join-order
  LEGALITY constraints (`min_lefthand`/`min_righthand`/`syn_lefthand`/
  `syn_righthand`, consulted by `join_is_legal` at every DP level), which
  goopg's search has never needed before (every other join type it searches
  either has no placement constraint or gets one for free from existing
  outer-join handling). This is comparably hard to S2b-2, not a same-shape
  sibling of S2b-1/S2b-4's simpler translation-seam fixes.
- No new deferral-ledger row (same precedent as M0140-0006's decomposition
  and M0142-0008b's own recon: a scoped decomposition into named, resumable
  sub-tasks is not a silent deferral).

Next step: pick per the `## Current Priority` banner (re-read first, it may
have moved). Newly-available options this loop created: **M0142-0008a-1**
(design-only PG-legality-machinery read + concrete goopg design — this is
the mandatory "further scoping pass" before any of -2/-3 can be attempted,
per K24). Still-open alternatives from before this loop: **M0141-S2b-2**
(base join/scan Pathlist surgery, also needs its own scoping pass first) and
**M0141-S2b-4** (SETOP rel-identity fix, has a witness — TPC-DS Q49 — sized
comparably to S2b-1/S2b-5, no further scoping needed). M0142-0003i/-0003k(c)
and M0141-S2b-6-resume remain BLOCKED on the same human-authorized shared-
cluster-write decision as prior loops (TPC-H `:65433` reload). Given three
"needs its own scoping pass" items now queued (S2b-2, M0142-0008a-1, and
implicitly M0142-0005's own "needs its own scoping/floor-measurement pass"),
a reasonable next pick is whichever the banner or a fresh read ranks first —
M0142-0008a-1 is the most recently motivated and has the freshest context.

Gates run: no `go build`/`go test` gate needed or run — this loop touched
only `docs/` and `.ralph/fix_plan.md` (`git diff --stat -- internal/` empty,
confirmed before writing the design doc's own floor-measurement section).
`make ralph-state-guard` — found the same clean-exit-marker inconsistency as
the prior two loops (status="running"/progress="completed" mismatch),
auto-repaired to `progress="in_progress"`, then passed.

In-flight: none. No server/cluster was started this loop (pure grep +
Serena symbol reads + git log against the working tree and the read-only
`./postgres` oracle).
