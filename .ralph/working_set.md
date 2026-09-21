Task: M0145-0005 — single-pass DP over the jointree. Slice 5-partial
(searched-subtree opacity for the residual pushdown family) LANDED this
loop; next is M0145-0006 per the banner (slice-5 remainder is blocked:
`outer-over-derived` on B-06, `lateral` on parameterized-path legality).

Files: internal/optimizer/inner_join_qual_pushdown.go (noSearched flag,
searched-root/-child prunes, deriveConstAcrossJoinEquality threading),
scan_input_rewrite.go (findUniqueSeqScanByColumn prune),
searched_opacity_test.go (new, 5 pins), design doc + README index,
fix_plan notes.

Key symbols: isSearchedTree, markSearchedTree, pushTrace.noSearched,
pushConjunctIntoSubtreeTracedNoSearched (statement-level, declines at
searched roots), pushConjunctIntoSubtree (stays PERMISSIVE — CTE-inline
R42/Q78 must cross the boundary), pushInnerJoinInputQuals,
findUniqueSeqScanByColumn, deriveConstAcrossJoinEquality.

Hypothesis/Findings: census (tmp/m0144-0011a2-census.log): leaf-count
104 (dominant cause = FULL-join folds, executor-blocked — grouped
j.Right ruled out by joinlist-vs-bindings probe), outer-over-derived
12 (B-06 firewall), lateral 8 (real deps), ON-qual families ZERO fires
(retired by absence). Pushdown family can't die wholesale (declined +
legacy statements need it) — correct shape is searched-boundary
opacity; 3/4 passes already had P5.9-b prunes, 2 holes closed.

Next step: M0145-0006 (upper-rel pathlists) per banner — recon
inputNodePathkeys nil tops + electOrderedGrouping/electOrderedDistinct
residual gates; design doc docs/design/0100-0149/m0145-0006-* to be
reserved.

Gates run: optimizer suite PASS; units PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); SF0.25 96/96 PASS, 99/99 plan shapes identical;
acceptance arm 24/24 MATCH VERDICT PASS under JOINTREE+PGSHAPED
(vs tmp/arm-on-20260920.txt). gate-stamp FAILs are the unstaged-tree
stamp policy, gate verdicts all PASS.

In-flight: none.
