# Working set

Task: M0145-0005 — single-pass DP over the jointree. Slice 3 (IR-direct
leaf materialisation) LANDED this loop; next is slice 5 (misc
decline-family retirement as corpus admits) or M0145-0006 per the
banner.

Files:
- internal/optimizer/jointreescope.go (NEW) — jtScopeTable: leaf/link
  records emitted at planFromItem construction time; addLeaf/addJoin/
  appendTable/shift; fold arm for non-descendable join types.
- internal/optimizer/jointreescope_test.go (NEW) — parity pins:
  extractScopeLeaves ≡ extractSearchLeaves over a 20-shape matrix +
  decline arms.
- internal/optimizer/joinsearchseam.go — extractScopeLeaves (~280
  lines after extractSearchLeaves); call-site switch in
  tryPGShapedJoinSearch (jointreePipeline && jtScope.root == chain).
- internal/optimizer/planner.go — resolveContext.jtScope field;
  planFromItem returns *jtScopeTable (tab.addLeaf base, tab.addJoin per
  join); planFromClause accumulates jtTab, pins root.
- docs/design/0100-0149/m0145-0005-*.md — slice-3 section + status.
- docs/design/README.md, .ralph/fix_plan.md — index/notes.

Key symbols: jtScopeTable, jtScopeLeaf, jtScopeLink{kind,loLeft,loRight,
hiRight,jn}, extractScopeLeaves, extractSearchLeaves (kept — legacy arm
+ root-pin fallback), rebaseChainQual, rebaseSemiAntiChainQual,
buildLeafSpans, reduceRightLink, decomposeFlatBodyTree.

Findings:
- Extraction is a transcription, not reimplementation: width/realWidth
  = prefix sums over leaf table; base/rightBase sampled at loLeft/
  loRight; belowNullable = range-containment union over processed outer
  links' subtree ranges; preserved = no RIGHT-typed link's range
  encloses the link (all links sit in ancestors' LEFT subtree — right
  subtrees are single leaves).
- Coordinate-translation family (rebase/remap/buildLeafSpans/
  localizeExprToLeaf/pgShapedOffsetChecksOK) does NOT retire — it
  translates between two real coordinate spaces that both still exist;
  the walk's removal removes discovery, not the spaces.
- planFromItem never builds FlattenedRHS or keyed-semi joins (unnest
  synthesizes those; root pin excludes post-unnest chains) — the arms
  are reproduced for parity anyway.
- gofmt: repo baseline is go1.25 — never `gofmt -w` (local go1.26
  rewrites unrelated hunks; reverted once this loop).

Next step: per doc's remaining-slices table — slice 5 (decline-family
retirement: outer-on-qual, inner-on-qual-*, outer-over-derived,
pushdown family) or whatever the banner orders.

Gates run: optimizer suite PASS; units PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); SF0.25 96/96 PASS, 99/99 plan shapes identical;
acceptance arm 24/24 MATCH under JOINTREE+PGSHAPED; plan-gate 16/22
(unchanged stale-pin drift — diffs live Sep-20 :65433 binary, not the
tree; corroborating only). Gate stamps re-run post-staging for the
commit hook.

In-flight: none — all gates consumed foreground this loop.
