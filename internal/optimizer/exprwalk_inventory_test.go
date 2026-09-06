package optimizer

// Walker-inventory census gate (M0125-0002 STEP 0).
//
// M0125-0001 built the child-slot primitive (exprwalk.go) and a gate that
// keeps it exhaustive as new Expr types appear. That gate answers "does the
// primitive know all 32 types?". It cannot answer the question M0125-0002 is
// scoped by: "how many hand-written type switches over Expr are still out
// there, not built on the primitive?"
//
// Three different figures were carried in the plan for that count — §0 of the
// round-2 doc said fourteen, §13.4 said seven, and M0124-0003 made it nine —
// and none had been re-derived from source. This file derives it mechanically
// and pins the answer, so the number can never silently drift again.
//
// THE CENSUS. A *site* is a function in package planner (non-test files) whose
// body contains a type switch with at least one `case *T:` where T is an Expr
// (i.e. T has an `exprNode()` method — the same source of truth
// exprwalk_exhaustive_test.go uses). As derived on 2026-07-30 at 64 sites:
//
//	 2  exprwalkPrimitive       — exprChildSlots / shallowCloneExpr, complete
//	50  walkerPending           — recursive traversals, 2..25 of 32 arms
//	12  nonRecursiveClassifier  — decide-and-return, no descent
//
// Amended 2026-07-30 by M0125-0024: 63 sites — 3 primitives (exprSelfKey
// joined), 48 walkerPending (exprEqual and planExprContentKey converted), 12
// classifiers. Those two were the only census members whose fail-open
// CONFLATES instead of no-opping, because both compute an identity.
// M0125-0002 commits 1-2 then DEMOTED remapByPosMap and cloneExprShiftIdx
// (dispatch switches survive inside their Rewrite closures), and commits 3
// and 4 (2026-08-03) DELETED bushy.go:visitColumnRefs and
// bushy.go:visitColumnRefsForTable outright — their switches vanished; the
// *ColumnRef filter in each new body is a type assertion, not a switch.
// Commit 5 (2026-08-03) then DEMOTED planner.go:exprSide — its body is
// walkExprRefs now, but a two-arm dispatch survives in the Visit closure —
// leaving the pinned walkerPending population at 46. Commit 6 (2026-08-03)
// handled the producer/consumer pair in local_filters.go both ways at once:
// conjunctIsLocalEligible DEMOTED (its veto dispatch survives in the Visit
// closure) and localizeExprToLeaf DELETED (cloneExprRefs left it with a
// *ColumnRef type assertion, no switch), leaving 45. (New pinned sites had
// joined since the 2026-07-30 census; the map below, not this comment, is
// the authoritative count.)
//
// So the live figure for the RC-1a class is **50, not seven**. The seven named
// in M0125-0002 are a hand-picked *conversion* scope chosen for their MHJ and
// local-filter blast radius; they were never the population. The per-site arm
// counts and the severity split (which fail-opens can corrupt rows, which only
// degrade an estimate) are recorded in
// docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md.
// (That doc's MHJ composition risk was retired by construction at M0127-P6.2,
// when the node was deleted; the walker-conversion scope it set is unchanged.)
//
// WHAT THIS GATE ENFORCES, and why it is set equality rather than a count:
//
//   - An UNPINNED site fails the test. A new hand-written Expr type switch is
//     a new instance of the defect class M0125-0001 exists to end, and it must
//     either be built on exprChildSlots or be admitted deliberately, with a
//     deferral-ledger row.
//   - A MISSING pinned site fails the test. When M0125-0002 converts a walker
//     onto a driver, its type switch disappears and the pin must be deleted in
//     the same commit — that is how the milestone's progress stays auditable
//     instead of being asserted in prose.
//
// Arm counts are deliberately NOT asserted. Adding an arm to a partial walker
// is the band-aid M0125-0001 was written to replace; pinning the counts would
// turn every such band-aid into a test edit that looks like progress. The
// counts live in the design doc as a dated snapshot instead.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// walkerRole is what the census found a site to be. It is documentation
// attached to each pin; the gate checks only that the value is one of the
// three known roles, so a typo cannot create a silently unclassified pin.
type walkerRole string

const (
	// exprwalkPrimitive is exprwalk.go's own switch: complete over all 32
	// types and kept that way by exprwalk_exhaustive_test.go.
	exprwalkPrimitive walkerRole = "primitive"

	// walkerPending is a recursive traversal that enumerates a SUBSET of the
	// Expr types, so an unenumerated type is a silent no-op. This is the
	// RC-1a class. M0125-0002 converts these; each conversion deletes a pin.
	walkerPending walkerRole = "walker-pending"

	// nonRecursiveClassifier decides something about the node in front of it
	// and returns without descending. Its unenumerated types fall through to
	// a deliberate "not applicable" answer, so completing it is not required
	// for correctness — but it is still counted here, because "does not
	// recurse" is a property that a later edit can quietly take away.
	nonRecursiveClassifier walkerRole = "classifier"

	// boundedQualSpine recurses, but over a CLOSED set of node types chosen
	// for a semantic reason rather than for coverage: completing it would
	// make it WRONG. Added by M0125-0036, whose EXISTS→ANY conversion walks
	// only the AND/OR spine of a qual, because every other position — NOT,
	// CASE, a function argument — is one where the ANY form's NULL is
	// distinguishable from the EXISTS form's FALSE. An exprwalk driver is
	// therefore not the fix for these; the closed set IS the invariant, and
	// pinning it here is how a later edit that widens the set gets audited.
	boundedQualSpine walkerRole = "bounded-qual-spine"

	// failClosedTypeResolver recurses only into TYPE-DETERMINING positions —
	// a cast's operand, a CASE branch, an operator's operands, a function's
	// arguments — and returns ok=false for every node type it does not
	// enumerate. Added by M0119-0006 for ExprResultType, goopg's mirror of
	// PG's exprType() (nodes/nodeFuncs.c), which upstream is likewise a
	// per-node-type switch and not a traversal: there is no "descend into all
	// children" answer to "what type does this node produce". It is a distinct
	// role because the RC-1a hazard does not apply — an unenumerated type is
	// not silently skipped, it makes the whole resolution DECLINE, and every
	// caller's decline path is its pre-existing conservative behaviour. An
	// edit that gave it a default answer instead of a decline would be the
	// real defect, which is what pinning it here audits.
	failClosedTypeResolver walkerRole = "fail-closed-type-resolver"
)

// exprSwitchInventory pins every Expr type switch in package planner, keyed
// "<file>:<func>". Census of 2026-07-30 (HEAD ac9bf911).
//
// Ordering is by file then function, matching the census output so the two can
// be diffed directly.
var exprSwitchInventory = map[string]walkerRole{
	// CONVERTED by M0125-0002 commit 1: the recursion and the
	// exhaustiveness moved to exprChildSlots (the walker now drives
	// rewriteExprRefsInPlace). What is left is a bottom-up dispatch over the
	// six types needing work beyond child descent, inside the Rewrite
	// closure — the census attributes a closure's switch to its enclosing
	// function, so the pin DEMOTES instead of disappearing. A conversion is
	// audited by this role change; only a walker whose switch vanishes
	// entirely loses its line.
	"joinlayout.go:remapByPosMap":           nonRecursiveClassifier, // moved from bushy.go at M0127-P6.3 (rename only)
	"joinlayout.go:remapOuterRefsInSubplan": walkerPending, // 5 of 32 arms; moved from bushy.go at M0127-P6.3
	"joinlayout.go:remapPosMapAfterRewrite": walkerPending, // 8 of 32 arms; moved from bushy.go at M0127-P6.3
	// CONVERTED by M0125-0002 commit 7, the LAST of the series. The
	// recursion is walkExprRefs (scopeSignal); the surviving switch is the
	// three-arm "reads row data but names no column" veto inside the Visit
	// closure (*OuterColumnRef / *CTIDExpr / *MergeWholeRowRef, plus the
	// empty-Name *ColumnRef), attributed by the census to its enclosing
	// function. Same demoted shape as commits 1, 2, 5 and 6's producer half.
	// RC-1a class 45 -> 44.
	"joinlayout.go:visitColumnRefsByName": nonRecursiveClassifier, // moved from bushy.go at M0127-P6.3 (rename only)
	// Added by M0127-P5.5-e-i. Built on cloneExprRefs (which carries both the
	// recursion and the exhaustiveness); what the census sees is the
	// three-arm dispatch inside the Rewrite closure — renumber *ColumnRef,
	// refuse *OuterColumnRef / *CTIDExpr — attributed to the enclosing
	// function. Same demoted shape as nl_index_join.go:cloneExprShiftIdx,
	// whose two-arm veto set this one's is a superset of. Fail-open is not a
	// hazard here: an unenumerated type aborts cloneExprRefs itself, and the
	// arm panics on the `!ok`.
	// C-19a (2026-09-06): `is_parallel_safe` built on walkExprRefs under
	// scopeVeto; the three-arm dispatch (*FuncCall proparallel lookup,
	// *OuterColumnRef / *ExecParamRef as PARAM_EXEC) lives inside the Visit
	// closure and is attributed to the enclosing function — the demoted
	// shape of local_filters.go:conjunctIsLocalEligible. Fail-closed by
	// construction: an unenumerated type aborts the walk (OnUnknown) and the
	// rel is read as parallel-unsafe.
	"considerparallel.go:isParallelSafeExpr": nonRecursiveClassifier,
	"createplanjoin.go:translateToLayout": nonRecursiveClassifier,
	// Added by the M0125-0035 CTE-body arm. Both are built on the
	// exprwalk primitives — walkExprRefs carries the recursion and
	// cloneExprRefs the rewrite — and what the census sees is the
	// bottom-up dispatch inside their Visit closures (veto
	// *OuterColumnRef / *FuncCall, validate *ColumnRef), attributed to
	// the enclosing function. Same demoted shape as
	// nl_index_join.go:cloneExprShiftIdx. Fail-open is a DECLINE here:
	// an unenumerated type passes the visitor and the conjunct is still
	// only pushed if every ColumnRef validates, and property 2 keeps the
	// residual copy either way.
	"cte_inline_pushdown.go:remapConjunctThroughCTEOutput":  nonRecursiveClassifier,
	"cte_inline_pushdown.go:remapConjunctThroughProjection": nonRecursiveClassifier,
	// Added by B-06 synthesis. A two-arm classifier (StringConst /
	// TypedStringLit target at one output position), not a traversal:
	// no descent, no scope handling, miss (anything else) returns
	// false and the position stays unknown. Same demoted shape as the
	// CTE entries above.
	"cte_stats_synthesis.go:branchLiteralAt": nonRecursiveClassifier,
	// Added by M0125-0036. See boundedQualSpine's comment: the arm set is
	// the transformation's NULL-semantics invariant, not an omission.
	"exists_to_any.go:rewriteExistsToAnyQual": boundedQualSpine,
	"exprwalk.go:exprChildSlots":              exprwalkPrimitive,
	// Added by M0125-0024: the per-node half of the identity key, complete
	// over all 32 types and gated with the other two by
	// exprwalk_exhaustive_test.go.
	"exprwalk.go:exprSelfKey":                                   exprwalkPrimitive,
	"exprwalk.go:shallowCloneExpr":                              exprwalkPrimitive,
	"foldconst.go:FoldConstants":                                walkerPending, // 15 of 32 arms
	"foldconst.go:toLiteralValue":                               nonRecursiveClassifier,
	"inner_join_qual_pushdown.go:innerJoinPushTarget":           nonRecursiveClassifier,
	// M0125-0046 pinned `inner_join_qual_pushdown.go:mhjResidualConjunctTable`
	// here — the MHJ analog of innerJoinPushTarget. M0127-P6.2 deleted it with
	// the node, so the row is deleted rather than demoted.
	// M0125-0002 commit 6 DEMOTED this one: the body is walkExprRefs
	// (scopeVeto) now, but a two-arm veto dispatch survives in the Visit
	// closure, so the census still keys a site to this function.
	// localizeExprToLeaf, its consumer, was DELETED from this map in the
	// same commit — cloneExprRefs left it with a *ColumnRef type
	// assertion and no switch at all.
	"local_filters.go:conjunctIsLocalEligible": nonRecursiveClassifier,
	// Added by C-02b. Built on walkExprRefs (scopeVeto carries the
	// recursion and the exhaustiveness — sublinks and unenumerated
	// kinds abort the walk, fail-closed); what the census sees is the
	// three-arm dispatch inside the Visit closure (veto
	// *OuterColumnRef / *FuncCall, collect *ColumnRef identities),
	// attributed to the enclosing function. Same demoted shape as
	// innerJoinPushTarget above. Fail-open is a DECLINE here: ok=false
	// keeps the legacy copy-pass verdict and consults no delay proof.
	"outerjoin_delay.go:qualSrcRelSet": nonRecursiveClassifier,
	// `mhj_input_rewrite.go` was renamed `scan_input_rewrite.go` by M0127-P6.2,
	// which deleted its MultiHashJoin half: `cloneExprForShift` and
	// `pushSingleSourceFiltersIntoMHJTables` went with the node (rows deleted,
	// not demoted); `matchSingleTableConstantPredicate` survives under the new
	// path and keeps its classification.
	"scan_input_rewrite.go:matchSingleTableConstantPredicate": nonRecursiveClassifier,
	// CONVERTED by M0125-0002 commit 2, and DEMOTED for the same reason
	// commit 1's remapByPosMap was: the recursion and the exhaustiveness
	// moved to exprChildSlots (via cloneExprRefs), but a two-arm bottom-up
	// dispatch survives inside the Rewrite closure — shift a *ColumnRef,
	// veto *OuterColumnRef / *CTIDExpr — and the census attributes a
	// closure's switch to its enclosing function. RC-1a class 48 → 47.
	"nl_index_join.go:cloneExprShiftIdx":                        nonRecursiveClassifier,
	"nl_index_join.go:residualCostMultiplier":                   nonRecursiveClassifier,
	// planner.go:exprEqual and planner.go:planExprContentKey were CONVERTED by
	// M0125-0024 and their pins are DELETED, not demoted: unlike commit 1's
	// remapByPosMap, no per-type dispatch survives — both are now
	// exprIdentityKey plus one fail-closed direction each, with no type switch
	// of their own. They were the two sites whose fail-open CONFLATES rather
	// than no-ops (an identity function's unenumerated type is not skipped, it
	// is asserted equal to every other node of its Go type), so the RC-1a
	// class shrinks 50 → 48 and the census 64 → 63.
	// CONVERTED by M0125-0002 commit 5, and DEMOTED for the same reason
	// commits 1-2 demoted remapByPosMap and cloneExprShiftIdx: the
	// recursion and the exhaustiveness moved to exprChildSlots (via
	// walkExprRefs), but a two-arm bottom-up dispatch survives inside the
	// Visit closure — classify a *ColumnRef by leftWidth, veto
	// *OuterColumnRef / *CTIDExpr — and the census attributes a closure's
	// switch to its enclosing function. RC-1a class 47 → 46.
	// Added by M0119-0006. See failClosedTypeResolver's comment: an
	// unenumerated Expr type makes ExprResultType decline, never guess.
	"expr_result_type.go:ExprResultType": failClosedTypeResolver,

	"planner.go:exprSide":                        nonRecursiveClassifier,
	"planner.go:exprType":                        walkerPending, // 22 of 32 arms
	"planner.go:findFirstNestedSRF":              walkerPending, // 6 of 32 arms
	"planner.go:inferExprType":                   nonRecursiveClassifier,
	"planner.go:isConstantExpr":                  walkerPending, // 25 of 32 arms
	"planner.go:isConstantPlanExpr":              walkerPending, // 12 of 32 arms
	// Added by M0134-0001 S4 (class 8): top-level "plain literal/param?"
	// classifier for the single-conjunct Filter drop — decide-and-return, no
	// descent, deliberate default=false (keep Filter). Same shape as
	// selectivity.go:isConstExpr.
	"planner.go:isPlainConstantBound":            nonRecursiveClassifier,
	"planner.go:planHasEscapingOuterRef":         walkerPending, // 6 of 32 arms
	// Renamed by review/260831-2 X-8: the hand-written switch moved into
	// planIndexScanFromWhereShape; planIndexScanFromWhere is now the thin
	// wrapper that applies the enable_indexscan/indexonlyscan toggles.
	"planner.go:planIndexScanFromWhereShape":     nonRecursiveClassifier,
	"planner.go:remapColumnRefsToSchema":         walkerPending, // 13 of 32 arms
	"planner.go:replaceExprNode":                 walkerPending, // 6 of 32 arms
	"planner.go:shiftColumnRefsBy":               walkerPending, // 13 of 32 arms
	"planner.go:withinGroupDirectArgColumnName":  walkerPending, // 2 of 32 arms
	"predp.go:remapSublinkOuterRefs":             walkerPending, // 3 of 32 arms
	"predp.go:whereEligibleForPreDPUnnest":       nonRecursiveClassifier,
	// Added by take2 P1-14b (patternsel slice): a 2-arm shape recogniser
	// (StringConst / LikeEscapePattern) with decline-by-default — an
	// unenumerated type falls through to the match default, the
	// pre-existing conservative behaviour, so completing it is not
	// required for correctness.
	"patternsel.go:patternConstString":          nonRecursiveClassifier,
	"pushdown.go:walkColumnRefsImpl":             walkerPending, // 18 of 32 arms
	"selectivity.go:clauseSelectivity":           walkerPending, // 4 of 32 arms
	"selectivity.go:clauseSelectivityWithSource": walkerPending, // 4 of 32 arms
	"selectivity.go:formatExprConstant":          nonRecursiveClassifier,
	"selectivity.go:isConstExpr":                 nonRecursiveClassifier,
	"subplan_lower.go:analyzeSublink":            walkerPending, // 7 of 32 arms
	"subplan_lower.go:excludedRefsWithin":        walkerPending, // 6 of 32 arms
	"subplan_lower.go:handleFor":                 nonRecursiveClassifier,
	"subplan_lower.go:rewriteSublinkPlan":        walkerPending, // 6 of 32 arms
	"subplan_lower_walk.go:lowerTraverseExpr":    walkerPending, // 24 of 32 arms
	"unnest.go:canUnnestExistsExpr":              walkerPending, // 5 of 32 arms
	"unnest.go:cloneExprLeaf":                    walkerPending, // 14 of 32 arms
	"unnest.go:cloneExprReplacingOuter":          walkerPending, // 11 of 32 arms
	"unnest.go:cloneExprSubstituteAggIdx0":       walkerPending, // 7 of 32 arms
	"unnest.go:containsExpr":                     walkerPending, // 5 of 32 arms
	"unnest.go:countSublinksInExpr":              nonRecursiveClassifier,
	// deepVisitSublinkChildren descends through walkExprTreeDeep, which is a
	// PLAN walker with no Expr switch of its own, so the census's mutual-
	// recursion pass reports it non-recursive. It is a walker.
	"unnest.go:deepVisitSublinkChildren":   walkerPending, // 5 of 32 arms
	"unnest.go:findExistsExprInExpr":       walkerPending, // 5 of 32 arms
	"unnest.go:findExprInExpr":             walkerPending, // 3 of 32 arms
	"unnest.go:findInExprInExpr":           walkerPending, // 5 of 32 arms
	"unnest.go:findSubqueryInExpr":         walkerPending, // 5 of 32 arms
	"unnest.go:liftResidualConjuncts":      walkerPending, // 5 of 32 arms
	"unnest.go:nullPreservingScalarTarget": walkerPending, // 9 of 32 arms
	"unnest.go:planCloneSupported":         walkerPending, // 5 of 32 arms
	"unnest.go:replaceExprInConjunct":      walkerPending, // 5 of 32 arms
	"unnest.go:residualExprLiftable":       walkerPending, // 12 of 32 arms
	"unnest.go:shiftExprColumnIdx":         walkerPending, // 5 of 32 arms
	"unnest.go:subqueryANDReachable":       walkerPending, // 2 of 32 arms
	"unnest.go:walkExprTree":               walkerPending, // 8 of 32 arms
	"unnest.go:walkSubqueryPlansInExpr":    walkerPending, // 9 of 32 arms
}

// exprSwitchSites runs the census: every package function whose body holds a
// type switch with at least one Expr-typed case, keyed "<file>:<func>".
//
// Methods are keyed "<file>:(T).<name>" so a method can never collide with a
// free function of the same name. There are none today; the scheme exists so
// that adding one does not produce a confusing duplicate-key failure.
func exprSwitchSites(t *testing.T) map[string]int {
	t.Helper()
	exprTypes := exprNodeReceivers(t)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	fset := token.NewFileSet()
	sites := map[string]int{}
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		base := filepath.Base(path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				name = "(" + recvTypeName(fn.Recv.List[0].Type) + ")." + name
			}
			arms := 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSwitchStmt)
				if !ok {
					return true
				}
				for _, stmt := range ts.Body.List {
					cc, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, ct := range cc.List {
						star, ok := ct.(*ast.StarExpr)
						if !ok {
							continue
						}
						id, ok := star.X.(*ast.Ident)
						if !ok {
							continue
						}
						if exprTypes[id.Name] {
							arms++
						}
					}
				}
				return true
			})
			if arms > 0 {
				sites[base+":"+name] += arms
			}
		}
	}
	if scanned == 0 || len(sites) == 0 {
		t.Fatalf("census found %d sites across %d files — the gate would vacuously pass", len(sites), scanned)
	}
	return sites
}

// recvTypeName renders a receiver type as a bare name, dropping the pointer.
func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver
		return recvTypeName(t.X)
	}
	return "?"
}

// TestExprSwitchInventoryIsPinned is the census gate. See the file header for
// why it asserts set equality and not arm counts.
func TestExprSwitchInventoryIsPinned(t *testing.T) {
	got := exprSwitchSites(t)

	var unpinned, missing []string
	for key := range got {
		if _, ok := exprSwitchInventory[key]; !ok {
			unpinned = append(unpinned, key)
		}
	}
	for key := range exprSwitchInventory {
		if _, ok := got[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(unpinned)
	sort.Strings(missing)

	for _, key := range unpinned {
		t.Errorf("unpinned Expr type switch %s (%d Expr arms): a NEW hand-written "+
			"walker is a new instance of the RC-1a defect class. Build it on "+
			"exprChildSlots / walkExprRefs / rewriteExprRefsInPlace / cloneExprRefs "+
			"(exprwalk.go), or — if it must stay hand-written — add it to "+
			"exprSwitchInventory here AND append a row to .ralph/deferral_ledger.md.",
			key, got[key])
	}
	for _, key := range missing {
		t.Errorf("pinned Expr type switch %s no longer exists: if M0125-0002 converted "+
			"it onto an exprwalk driver, delete its line from exprSwitchInventory in "+
			"the same commit (that deletion is how the milestone's progress is "+
			"audited); if it was renamed, update the key.", key)
	}

	for key, role := range exprSwitchInventory {
		switch role {
		case exprwalkPrimitive, walkerPending, nonRecursiveClassifier, boundedQualSpine,
			failClosedTypeResolver:
		default:
			t.Errorf("%s: unknown walkerRole %q", key, role)
		}
	}
}

// TestExprSwitchCensusIsNotVacuous pins the census machinery itself. Both
// halves of the gate above are set comparisons, so a census that silently
// stopped finding sites — a parse mode change, a glob that misses files, a
// renamed exprNode marker — would let the inventory rot while still reporting
// PASS. exprwalk.go's two primitives are the fixture: they are the sites this
// package can least afford to lose track of, and they are guaranteed to be
// found by any working census.
func TestExprSwitchCensusIsNotVacuous(t *testing.T) {
	got := exprSwitchSites(t)

	for _, key := range []string{"exprwalk.go:exprChildSlots", "exprwalk.go:shallowCloneExpr", "exprwalk.go:exprSelfKey"} {
		arms, ok := got[key]
		if !ok {
			t.Fatalf("census did not find %s — the machinery is broken, not the inventory", key)
		}
		if arms != len(exprNodeReceivers(t)) {
			t.Errorf("%s: census counted %d Expr arms, want all %d Expr types "+
				"(exprwalk_exhaustive_test.go asserts completeness; a mismatch here "+
				"means the census is miscounting arms)", key, arms, len(exprNodeReceivers(t)))
		}
	}

	// The class is large today and shrinks only through M0125-0002 commits. A
	// census that reports a handful of sites is a broken census, not a
	// finished milestone; when the walkers really are gone, this floor is
	// lowered in the same commit that removes the last pin.
	if len(got) < 20 {
		t.Errorf("census found only %d Expr type switches; 64 were pinned on "+
			"2026-07-30 and they are removed one commit at a time. Verify the "+
			"census still parses the package before lowering this floor.", len(got))
	}
}
