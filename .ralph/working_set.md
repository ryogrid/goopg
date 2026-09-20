Task: M0142-0008a-3i-route-a — BOTH STEPS LANDED 2026-09-20. Step 2 (the
  flattening splice) landed in plan-level form: `Join.FlattenedRHS` +
  seam-side decomposition of a simple planned body into real-costed
  synthetic leaves. Task marked [x]; residuals deferred (below).
Files: internal/optimizer/plan.go (ExistsExpr.Subquery, InExpr.Subquery,
  Join.FlattenedRHS), planner.go (:15306 IN, :15338 EXISTS assignments;
  :16834/:17072 copy sites), foldconst.go:69, exists_to_any.go:384
  (deliberately nil), sublinkpullup.go (sublinkBodyIsSimple,
  decomposeFlatBodyTree, flatBodyScopeProject, schemaIsLeafConcat,
  exprHasSublinkPlan via ExprSubplans), unnest.go (three flatten arms +
  IsolatedScope identity re-wrap), joinsearchseam.go (admitSemiAnti walk,
  bodyQuals, multi-bit rhs, realWidth, leafSpans, semianti-not-tail gate),
  relfromjoinlist.go (joinlistProblem.leafSpans, leafSpanWindow),
  joinrestrict.go (comment), flattened_rhs_test.go (NEW),
  sublinkpullup_test.go, relfromjoinlist_test.go, considerparallel_test.go,
  gatherpaths_test.go, narrowcostinputs_test.go (leafSpans consumers),
  docs/design/0100-0149/m0142-0008a-3i-route-a-retain-sublink-parse-tree.md,
  analysis/m0142/m0142-0008a-3i-route-a-step2-census.txt
Key symbols: sublinkBodyIsSimple, decomposeFlatBodyTree, Join.FlattenedRHS,
  extractSearchLeaves(admitSemiAnti), semiAntiChainLink.bodyQuals/flattened,
  leafSpan, leafSpanWindow, flatBodyScopeProject, projectIsPositionalIdentity,
  ExprSubplans, leafRangeRelSet.
Hypothesis/Findings:
  - The filed "splice FROM items before planning" framing is unreachable
    without the M0145 IR: goopg plans bodies eagerly at expression
    resolution. What landed is the plan-level equivalent — the SEAM
    decomposes a marked simple body back into base-relation leaves, so
    the search sees real relations instead of one opaque synthetic leaf.
  - decomposeFlatBodyTree accepts SeqScan/Filter/Inner|Cross/identity
    Project; LeafLocal filters only directly over a bare *SeqScan (there
    leaf-local == subtree concat space, so the same +base shift lifts).
    Output must equal leaf-concat exactly or coordinates can't be trusted.
  - IN arms re-wrap the stripped body in a positional-identity
    IsolatedScope Project — restores M0063/M0071 NLI/pushdown protection
    STRUCTURALLY (pickInnerSide needs a bare *SeqScan). EXISTS bodies
    keep bare-body NLI eligibility (M0063-0004) — do NOT gate NLI on
    FlattenedRHS.
  - P0-H11 closed: cumOffsets []int could not carry out-of-band synthetic
    spans; leafSpans []leafSpan does. leafSpanWindow admits a boundary
    only over a contiguous span union.
  - NEW DECLINE semianti-not-tail: c2's construction needs every synthetic
    leaf in a TAIL slot; a mid-chain demoted ANTI (Q78: web_sales ANTI
    web_returns JOIN date_dim) walks [real,synthetic,real] and panicked
    translateToLayout (binding col 75 vs 7-col layout). Now an explicit
    decline → fallback marker join; Q78 green (15 rows, oracle ck). Unit
    regression: TestSeamDeclinesRealLeafAfterSyntheticLeaf.
  - exprHasSublinkPlan must flag ANY planned sublink (OuterColumnRef coords
    die under flattening); implemented on ExprSubplans/exprChildSlots so
    plain IN (a,b,c) lists stay flattenable.
  - Movement measured NONE on SF0.25: leaf-count still 26 (remaining
    declines all have a SECOND undecomposable member — *Project
    composites/NLI/Gather/CTEScan, the M0144-0003a census set);
    semianti-not-tail×3 is a new conversion class on Q78's chain; plan
    diffs on Q33/Q56/Q60/Q83 are qualifier-only (item_1.i_manufact_id).
  - TRAP (again): gate stamps hash the STAGED tree. bench/tpcds/server.sh
    stop is guard-blocked; stop the private sf025 goopg with
    `goopg stop -D bench/tpcds/runtime_goopg/data-sf025`.
Next step: none for this task. Deferred: arbitrary synthetic-leaf
  placement (real leaf after synthetic — needs leaf reorder + relset
  remap + create-plan coordinate translation); hasTargetSRFs narrowing
  (shared built-in SRF registry with isNestedSRFName). Both in the
  deferral ledger. M0145-0003 jointree-first is the residual's real shape.
  Do NOT touch M0144-0011 ([!]), M0137-0019a ([!]), M0142-0005 ([!]).
Gates run: units PASS (exit 0, 0 FAIL); internal/optimizer PASS;
  tpch-spotcheck PASS (Q12=2 Q13=33); tpcds-sf025 sweep PASS (PASS=96
  MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3; Q78 15 rows
  ck=c06cf981a7819a37 oracle-verified; plan channel vs baseline
  changed=4 qualifier-only); DP_TRACE census captured (evidence file
  above); go vet clean; pgbench smoke via the commit hook.
In-flight: none.
