package planner

// Exhaustiveness gate for exprwalk.go (M0125-0001).
//
// The RC-1a defect class is "a type switch over Expr that silently
// no-ops on a type it was never taught". Adding the missing arms does
// not prevent the NEXT type from reproducing it, so the real deliverable
// is this gate: it reads plan.go's `func (*T) exprNode() {}` receivers
// with go/ast and asserts SET EQUALITY, in both directions, against the
// case lists of exprChildSlots and shallowCloneExpr.
//
// Both directions matter and catch different mistakes:
//   - plan.go ⊄ exprwalk.go  ⇒ a 33rd Expr type was added and the
//     traversal layer does not know it. This is the RC-1a hole.
//   - exprwalk.go ⊄ plan.go  ⇒ an arm names a type that is no longer an
//     Expr (renamed or deleted). It compiles — a type switch arm for a
//     type that still exists but no longer implements Expr is only
//     unreachable, not illegal — and would quietly stop matching.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// exprNodeReceivers returns the set of concrete type names T for which
// `func (*T) exprNode()` is declared anywhere in package planner.
//
// It scans EVERY non-test .go file in the package, not plan.go alone
// (design D5 requirement 1). The unexported exprNode() marker closes the
// Expr set to other *packages*, not to other *files* in this one, so a
// type declared in, say, unnest.go would evade a single-file parse
// silently — which is the exact failure mode this gate exists to end.
// All 32 live in plan.go today; the gate must not depend on that
// staying true.
//
// _test.go files are excluded deliberately: the gate governs production
// Expr types, and this file declares an unenumerated `unknownExpr`
// fixture on purpose to pin the fail-closed contract.
func exprNodeReceivers(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	got := map[string]bool{}
	scanned := 0
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "exprNode" || fn.Recv == nil {
				continue
			}
			if len(fn.Recv.List) != 1 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				// A value receiver would still satisfy Expr; flag it
				// rather than skipping, since exprwalk.go uses *T.
				t.Fatalf("%s: exprNode has a non-pointer receiver: %T", path, fn.Recv.List[0].Type)
			}
			id, ok := star.X.(*ast.Ident)
			if !ok {
				t.Fatalf("%s: exprNode receiver is not a plain identifier: %T", path, star.X)
			}
			got[id.Name] = true
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no package files — the gate would vacuously pass")
	}
	if len(got) == 0 {
		t.Fatalf("found no exprNode() receivers across %d package files — the gate would vacuously pass", scanned)
	}
	return got
}

// typeSwitchCaseTypes parses exprwalk.go and returns the set of type
// names appearing as `case *T:` inside the named function's type switch.
func typeSwitchCaseTypes(t *testing.T, fnName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exprwalk.go", nil, 0)
	if err != nil {
		t.Fatalf("parse exprwalk.go: %v", err)
	}
	var target *ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == fnName && fn.Recv == nil {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatalf("function %s not found in exprwalk.go", fnName)
	}

	got := map[string]bool{}
	nSwitch := 0
	ast.Inspect(target.Body, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSwitchStmt)
		if !ok {
			return true
		}
		nSwitch++
		for _, stmt := range ts.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok || len(cc.List) == 0 {
				continue // `default:` — carries no type names
			}
			for _, texpr := range cc.List {
				star, ok := texpr.(*ast.StarExpr)
				if !ok {
					continue // e.g. `case nil:`
				}
				if id, ok := star.X.(*ast.Ident); ok {
					got[id.Name] = true
				}
			}
		}
		return true
	})
	// More than one type switch would mean the case list is split and
	// this gate is only reading part of it.
	if nSwitch != 1 {
		t.Fatalf("%s has %d type switches, want exactly 1 — the gate reads them all but the invariant assumes one", fnName, nSwitch)
	}
	return got
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffSets returns the elements of a that are absent from b.
func diffSets(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func assertSetEquality(t *testing.T, fnName string, declared, covered map[string]bool) {
	t.Helper()
	if missing := diffSets(declared, covered); len(missing) > 0 {
		t.Errorf("%s does not handle %d Expr type(s): %v\n"+
			"Every concrete Expr must appear in %s — a type it has never been taught is a SILENT no-op, "+
			"which is the RC-1a defect (TPC-DS Q76 returned 0 rows instead of 100 because remapByPosMap "+
			"had no *IsNullExpr arm). If the new type has no Expr/Node children, add it to the "+
			"childless-leaf case list; that records the decision instead of leaving it to fall through.",
			fnName, len(missing), missing, fnName)
	}
	if stale := diffSets(covered, declared); len(stale) > 0 {
		t.Errorf("%s handles %d type(s) that no longer implement Expr: %v\n"+
			"Such an arm still compiles but can never match, so it is dead code that reads as coverage.",
			fnName, len(stale), stale)
	}
}

func TestExprChildSlotsCoversEveryExprType(t *testing.T) {
	declared := exprNodeReceivers(t)
	covered := typeSwitchCaseTypes(t, "exprChildSlots")
	assertSetEquality(t, "exprChildSlots", declared, covered)
	t.Logf("package planner declares %d concrete Expr types: %v", len(declared), sortedKeys(declared))
}

func TestShallowCloneExprCoversEveryExprType(t *testing.T) {
	declared := exprNodeReceivers(t)
	covered := typeSwitchCaseTypes(t, "shallowCloneExpr")
	assertSetEquality(t, "shallowCloneExpr", declared, covered)
}

// The two switches must also agree with EACH OTHER. If they drift, a
// type could be walkable but not cloneable, so cloneExprRefs
// would abort fail-closed on a perfectly ordinary expression.
func TestExprWalkSwitchesAgreeWithEachOther(t *testing.T) {
	slots := typeSwitchCaseTypes(t, "exprChildSlots")
	clone := typeSwitchCaseTypes(t, "shallowCloneExpr")
	if missing := diffSets(slots, clone); len(missing) > 0 {
		t.Errorf("exprChildSlots handles types shallowCloneExpr does not: %v", missing)
	}
	if missing := diffSets(clone, slots); len(missing) > 0 {
		t.Errorf("shallowCloneExpr handles types exprChildSlots does not: %v", missing)
	}
}

// ---------------------------------------------------------------------
// Driver behaviour
// ---------------------------------------------------------------------

// unknownExpr implements Expr but is deliberately absent from both
// switches, to pin the FAIL-CLOSED contract. It lives in a _test file so
// the go/ast gate above (which reads only plan.go) does not see it.
type unknownExpr struct{ pos int }

func (e *unknownExpr) Pos() int { return e.pos }
func (*unknownExpr) exprNode()  {}

func colRef(idx int, name string) *ColumnRef {
	return &ColumnRef{Index: idx, Name: name}
}

// collectIndices walks e and returns every ColumnRef index in visit order.
func collectIndices(e Expr, pol scopePolicy) ([]int, bool) {
	var got []int
	ok := walkExprRefs(e, pol, exprVisitor{
		Visit: func(n Expr) bool {
			if c, isCol := n.(*ColumnRef); isCol {
				got = append(got, c.Index)
			}
			return true
		},
	})
	return got, ok
}

func TestWalkPlanExprVisitsEverySameScopeChild(t *testing.T) {
	// CASE WHEN a=1 THEN b ELSE c END  — exercises the []CaseWhen
	// struct-slice slots, which are the easiest to get wrong.
	e := &CaseExpr{
		Whens: []CaseWhen{{
			When: &BinaryOp{Left: colRef(0, "a"), Right: &IntegerConst{Value: 1}},
			Then: colRef(1, "b"),
		}},
		Else: colRef(2, "c"),
	}
	got, ok := collectIndices(e, scopeIgnore)
	if !ok {
		t.Fatal("walk aborted unexpectedly")
	}
	want := []int{0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("visited indices %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visited indices %v, want %v", got, want)
		}
	}
}

func TestWalkPlanExprFailsClosedOnUnknownType(t *testing.T) {
	var seen Expr
	ok := walkExprRefs(&unknownExpr{}, scopeIgnore, exprVisitor{
		OnUnknown: func(e Expr) { seen = e },
	})
	if ok {
		t.Fatal("walk returned true for an unenumerated Expr type; the contract is fail-closed")
	}
	if seen == nil {
		t.Fatal("OnUnknown was not called")
	}
}

// An unknown type nested inside a known container must also abort — the
// historical bug is precisely that the outer walk reported success while
// having skipped a subtree.
func TestWalkPlanExprFailsClosedOnNestedUnknownType(t *testing.T) {
	e := &BinaryOp{Left: colRef(0, "a"), Right: &unknownExpr{}}
	if ok := walkExprRefs(e, scopeIgnore, exprVisitor{}); ok {
		t.Fatal("walk returned true despite an unenumerated node nested under a BinaryOp")
	}
}

func TestWalkPlanExprScopePolicies(t *testing.T) {
	inner := &SeqScan{}
	// EXISTS(...) with one PARAM_EXEC arg in the OUTER coordinate space.
	e := &ExistsExpr{Plan: inner, Args: []Expr{colRef(7, "outer_key")}}

	// scopeIgnore: never signals, never aborts, still visits Args.
	got, ok := collectIndices(e, scopeIgnore)
	if !ok || len(got) != 1 || got[0] != 7 {
		t.Fatalf("scopeIgnore: got %v ok=%v, want [7] ok=true", got, ok)
	}

	// scopeSignal: reports the crossing but keeps going.
	signals := 0
	ok = walkExprRefs(e, scopeSignal, exprVisitor{OnScope: func(Node) { signals++ }})
	if !ok || signals != 1 {
		t.Fatalf("scopeSignal: ok=%v signals=%d, want ok=true signals=1", ok, signals)
	}

	// scopeVeto: reports and aborts. This is walkColumnRefsImpl's
	// onOuter() behaviour for a subquery.
	signals = 0
	ok = walkExprRefs(e, scopeVeto, exprVisitor{OnScope: func(Node) { signals++ }})
	if ok {
		t.Fatal("scopeVeto: walk should abort on an inner-scope child")
	}
	if signals != 1 {
		t.Fatalf("scopeVeto: signals=%d, want 1", signals)
	}
}

// A scope-opening node with no Args reports ZERO same-scope slots. It
// must still be recognised as scope-opening — this is the "classify by
// slot kind, never by len(kids)" trap.
func TestScopeOpeningNodeWithZeroExprSlots(t *testing.T) {
	e := &ArraySubqueryExpr{Plan: &SeqScan{}}
	slots, ok := exprChildSlots(e)
	if !ok {
		t.Fatal("ArraySubqueryExpr not enumerated")
	}
	if len(slots) != 1 || slots[0].kind != slotInnerPlan {
		t.Fatalf("got %d slot(s) kind=%v, want exactly one slotInnerPlan", len(slots), slots[0].kind)
	}
	if ok := walkExprRefs(e, scopeVeto, exprVisitor{}); ok {
		t.Fatal("ArraySubqueryExpr must veto under scopeVeto despite having no Expr children")
	}
}

// MultiAssignSubqElem.Row is statically *MultiAssignSubqRow, so a type
// switch over Expr cannot reach it as a child.
func TestMultiAssignSubqElemReachesItsRowAsASlot(t *testing.T) {
	row := &MultiAssignSubqRow{Plan: &SeqScan{}, NCols: 2}
	e := &MultiAssignSubqElem{Row: row, ColIdx: 1}
	slots, ok := exprChildSlots(e)
	if !ok {
		t.Fatal("MultiAssignSubqElem not enumerated")
	}
	if len(slots) != 1 || slots[0].kind != slotSubqRow {
		t.Fatalf("got %d slot(s), want exactly one slotSubqRow", len(slots))
	}
	if *slots[0].row != row {
		t.Fatal("slot does not address the shared MultiAssignSubqRow")
	}
}

func TestRewritePlanExprInPlaceMutates(t *testing.T) {
	left := colRef(0, "a")
	e := Expr(&BinaryOp{Left: left, Right: colRef(1, "b")})
	ok := rewriteExprRefsInPlace(&e, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if c, isCol := n.(*ColumnRef); isCol {
				c.Index += 10
			}
			return n
		},
	})
	if !ok {
		t.Fatal("rewrite aborted")
	}
	bin := e.(*BinaryOp)
	if bin.Left.(*ColumnRef).Index != 10 || bin.Right.(*ColumnRef).Index != 11 {
		t.Fatalf("indices not shifted in place: %d, %d",
			bin.Left.(*ColumnRef).Index, bin.Right.(*ColumnRef).Index)
	}
	// In-place means the ORIGINAL node object was mutated.
	if left.Index != 10 {
		t.Fatalf("original ColumnRef not mutated (index %d) — this driver is the in-place one", left.Index)
	}
}

func TestCloneRewritePlanExprLeavesOriginalUntouched(t *testing.T) {
	orig := &BinaryOp{Left: colRef(0, "a"), Right: colRef(1, "b")}
	got, ok := cloneExprRefs(orig, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if c, isCol := n.(*ColumnRef); isCol {
				c.Index += 10
			}
			return n
		},
	})
	if !ok {
		t.Fatal("clone-rewrite aborted")
	}
	if orig.Left.(*ColumnRef).Index != 0 || orig.Right.(*ColumnRef).Index != 1 {
		t.Fatalf("ORIGINAL was mutated: %d, %d",
			orig.Left.(*ColumnRef).Index, orig.Right.(*ColumnRef).Index)
	}
	cl := got.(*BinaryOp)
	if cl.Left.(*ColumnRef).Index != 10 || cl.Right.(*ColumnRef).Index != 11 {
		t.Fatalf("clone not rewritten: %d, %d",
			cl.Left.(*ColumnRef).Index, cl.Right.(*ColumnRef).Index)
	}
	if cl == orig {
		t.Fatal("clone-rewrite returned the original node")
	}
}

// A plain struct copy aliases the backing array of a []Expr field, so a
// rewrite through the clone would be visible through the original. This
// pins that shallowCloneExpr duplicates the slice.
func TestCloneRewritePlanExprDoesNotAliasSliceFields(t *testing.T) {
	orig := &FuncCall{Name: "abs", Args: []Expr{colRef(3, "x")}}
	if _, ok := cloneExprRefs(orig, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if c, isCol := n.(*ColumnRef); isCol {
				c.Index += 100
			}
			return n
		},
	}); !ok {
		t.Fatal("clone-rewrite aborted")
	}
	if got := orig.Args[0].(*ColumnRef).Index; got != 3 {
		t.Fatalf("original FuncCall.Args was aliased and mutated: index %d, want 3", got)
	}
}

// Same hazard, one level subtler: CaseExpr.Whens is a slice of structs.
func TestCloneRewritePlanExprDoesNotAliasCaseWhens(t *testing.T) {
	orig := &CaseExpr{Whens: []CaseWhen{{When: colRef(1, "w"), Then: colRef(2, "t")}}}
	if _, ok := cloneExprRefs(orig, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if c, isCol := n.(*ColumnRef); isCol {
				c.Index += 100
			}
			return n
		},
	}); !ok {
		t.Fatal("clone-rewrite aborted")
	}
	if orig.Whens[0].When.(*ColumnRef).Index != 1 || orig.Whens[0].Then.(*ColumnRef).Index != 2 {
		t.Fatalf("original CaseExpr.Whens was aliased and mutated: %d, %d",
			orig.Whens[0].When.(*ColumnRef).Index, orig.Whens[0].Then.(*ColumnRef).Index)
	}
}

func TestCloneRewritePlanExprFailsClosedOnUnknownType(t *testing.T) {
	e := &BinaryOp{Left: colRef(0, "a"), Right: &unknownExpr{}}
	got, ok := cloneExprRefs(e, scopeIgnore, exprRewriter{})
	if ok || got != nil {
		t.Fatalf("clone-rewrite must fail closed on an unenumerated type; got %v ok=%v", got, ok)
	}
}

// The clone must not deep-copy an inner plan: no existing driver in this
// package does, and doing so would silently duplicate a subplan.
func TestCloneRewritePlanExprAliasesInnerPlan(t *testing.T) {
	inner := &SeqScan{}
	orig := &ExistsExpr{Plan: inner}
	got, ok := cloneExprRefs(orig, scopeIgnore, exprRewriter{})
	if !ok {
		t.Fatal("clone-rewrite aborted")
	}
	if got.(*ExistsExpr).Plan != Node(inner) {
		t.Fatal("inner plan was copied; it must be aliased")
	}
}
