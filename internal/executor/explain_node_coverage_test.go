package executor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// Node-type EXPLAIN coverage — planner-refactor-take2 P0-01.
//
// WHY THIS EXISTS. `describePlan` is a type switch whose fallthrough is
// `fmt.Sprintf("%T", n)`, so a plan node with no arm renders its GO TYPE NAME
// into user-visible EXPLAIN output. That is not hypothetical: before this test
// landed, eighteen node types had no arm, and against a live server
//
//	EXPLAIN SELECT * FROM regexp_matches('abc','b','g')   -->  *optimizer.FromRegexpMatches
//	WITH RECURSIVE t(n) AS (...) SELECT * FROM t          -->  *optimizer.RecursiveUnion
//	SELECT DISTINCT ON (k) ...                            -->  *optimizer.DistinctOn
//
// The defect is worse than cosmetic, and the reason is recorded at
// operators_explain.go's BitmapHeapScan arm: goopg once had no arm for that
// node either, so "a plan census grepping for the PG label read zero even once
// bitmap scans were being chosen". A census over EXPLAIN text measures its
// LABELLER, not the planner. Every plan-parity number this project produces is
// read out of this renderer, so the renderer's completeness must be a gate
// before any of those numbers mean anything.
//
// Both sides are derived from source, never restated. A hand-written list of
// node names would be a second copy of the truth checked by nothing — the same
// class of artefact as the flag labels that shipped wrong twice
// (internal/optimizer/flaglabels.go's header comment).
//
// design: docs/design/not_ralph/planner_refactor_take2/impl/P0-A-explain-instrument.md §2

const (
	optimizerPkgDir = "../optimizer"
	explainSrcFile  = "operators_explain.go"
)

// planNodeTypes returns every type in internal/optimizer that satisfies
// optimizer.Node — i.e. declares both `Pos() int` and `Output() Schema` on a
// pointer receiver. That is the interface's literal definition
// (internal/optimizer/plan.go), so the enumeration cannot drift from it.
func planNodeTypes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, optimizerPkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", optimizerPkgDir, err)
	}
	hasPos, hasOutput := map[string]bool{}, map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				ident, ok := star.X.(*ast.Ident)
				if !ok {
					continue
				}
				switch fn.Name.Name {
				case "Pos":
					hasPos[ident.Name] = true
				case "Output":
					hasOutput[ident.Name] = true
				}
			}
		}
	}
	var names []string
	for name := range hasOutput {
		if hasPos[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	// A silently-empty enumeration would make this test pass vacuously, which
	// is the failure a coverage test can least afford.
	if len(names) < 40 {
		t.Fatalf("found only %d plan node types; the AST walk is broken", len(names))
	}
	return names
}

// describePlanArms returns the type names named by `case *optimizer.X` inside
// the given function of operators_explain.go.
func describePlanArms(t *testing.T, funcName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, explainSrcFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", explainSrcFile, err)
	}
	arms := map[string]bool{}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != funcName {
			continue
		}
		found = true
		ast.Inspect(fn, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				star, ok := expr.(*ast.StarExpr)
				if !ok {
					continue
				}
				sel, ok := star.X.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "optimizer" {
					arms[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("function %s not found in %s", funcName, explainSrcFile)
	}
	return arms
}

// TestEveryPlanNodeTypeHasAnExplainArm fails when a plan node type would render
// its Go type name. Add a `case *optimizer.<Type>:` to describePlan carrying the
// label PostgreSQL uses — read it off the reference cluster, do not guess — or,
// if the node genuinely cannot appear in a rendered plan, add it to
// explainCoverageExempt with the reason.
func TestEveryPlanNodeTypeHasAnExplainArm(t *testing.T) {
	arms := describePlanArms(t, "describePlan")
	var uncovered []string
	for _, name := range planNodeTypes(t) {
		if arms[name] {
			continue
		}
		if reason, ok := explainCoverageExempt[name]; ok {
			if reason == "" {
				t.Errorf("%s is exempt with no reason", name)
			}
			continue
		}
		uncovered = append(uncovered, name)
	}
	if len(uncovered) > 0 {
		t.Errorf("%d plan node type(s) have no describePlan arm and would print their Go "+
			"type name into EXPLAIN output:\n  %s\n"+
			"Add `case *optimizer.<Type>:` with PostgreSQL's label for that node, or an "+
			"explainCoverageExempt entry with the reason it cannot be rendered.",
			len(uncovered), strings.Join(uncovered, "\n  "))
	}
}

// TestDescribePlanHasExactlyOneTypeNameFallthrough pins the OTHER half of the
// defect. A `%T` inside a COVERED arm is invisible to the coverage test above —
// the arm exists, so the type looks handled — but it still prints a Go type
// name at runtime. Two arms did exactly that (BitmapHeapScan with a nil Table,
// BitmapIndexScan with a nil Index), which is how a bitmap-scan census could
// read zero while bitmap scans were being chosen. The switch's own fallthrough
// is the only permitted `%T`.
func TestDescribePlanHasExactlyOneTypeNameFallthrough(t *testing.T) {
	src, err := os.ReadFile(explainSrcFile)
	if err != nil {
		t.Fatalf("read %s: %v", explainSrcFile, err)
	}
	start := strings.Index(string(src), "func describePlan(")
	if start < 0 {
		t.Fatal("describePlan not found")
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatal("describePlan end not found")
	}
	body := string(src)[start : start+end]
	if got := strings.Count(body, `fmt.Sprintf("%T"`); got != 1 {
		t.Errorf("describePlan contains %d `fmt.Sprintf(\"%%T\"` sites, want exactly 1 "+
			"(the switch fallthrough). A %%T inside a covered arm prints a Go type name "+
			"at runtime while looking covered to the arm census.", got)
	}
}

// planNodeChildFields returns, for each plan node type, the names of its
// exported fields whose type is `Node` or `[]Node` — i.e. the children a
// complete renderer must walk.
func planNodeChildFields(t *testing.T) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, optimizerPkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", optimizerPkgDir, err)
	}
	isNode := func(e ast.Expr) bool {
		if arr, ok := e.(*ast.ArrayType); ok {
			e = arr.Elt
		}
		ident, ok := e.(*ast.Ident)
		return ok && ident.Name == "Node"
	}
	out := map[string][]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					return true
				}
				for _, f := range st.Fields.List {
					if !isNode(f.Type) {
						continue
					}
					for _, name := range f.Names {
						if name.IsExported() {
							out[ts.Name.Name] = append(out[ts.Name.Name], name.Name)
						}
					}
				}
				return true
			})
		}
	}
	return out
}

// TestEveryPlanNodeWithChildrenIsWalked fails when a plan node carries child
// nodes that planChildren does not visit, which makes that node's ENTIRE
// SUBTREE vanish from EXPLAIN.
//
// This is the sharper half of P0-01. A mislabelled node is visible and
// suspicious; a truncated plan reads as agreement on everything it does not
// show, so a parity census scores it as a match. The repository has paid for
// this once already — M0125-0037(i)'s missing SetOp arm truncated TPC-DS
// Q5/Q18/Q67 to four lines — and four more node types were found in the same
// state here: DistinctOn (its whole scan tree), RecursiveUnion (both terms),
// RowsFrom (every entry) and Copy (the source query).
func TestEveryPlanNodeWithChildrenIsWalked(t *testing.T) {
	walked := describePlanArms(t, "planChildren")
	nodes := map[string]bool{}
	for _, n := range planNodeTypes(t) {
		nodes[n] = true
	}
	var missing []string
	for name, fields := range planNodeChildFields(t) {
		if !nodes[name] || walked[name] {
			continue
		}
		if reason, ok := childWalkExempt[name]; ok {
			if reason == "" {
				t.Errorf("%s is exempt from the child walk with no reason", name)
			}
			continue
		}
		missing = append(missing, name+" (fields: "+strings.Join(fields, ", ")+")")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d plan node type(s) carry child nodes that planChildren never walks, so "+
			"their subtrees are invisible in EXPLAIN:\n  %s\n"+
			"Add a `case *optimizer.<Type>:` to planChildren, or a childWalkExempt entry "+
			"with the reason the children must not render.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// childWalkExempt names plan node types whose child fields deliberately do not
// render, with the reason.
var childWalkExempt = map[string]string{
	"Explain": "the EXPLAIN wrapper itself: its Child IS the plan being rendered, " +
		"walked by the explain operator rather than as a child of a rendered node",
}

// explainCoverageExempt names plan node types that deliberately have no
// describePlan arm, with the reason. Empty: every type the optimizer can build
// is renderable, and a node that cannot reach EXPLAIN should be argued for here
// rather than left to the fallthrough.
var explainCoverageExempt = map[string]string{}
