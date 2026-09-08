package optimizer

// The resolver family's arm lists, pinned against each other mechanically.
//
// WHY THIS IS A SOURCE TEST AND NOT A BEHAVIOUR TEST. Three times now, a plan
// node type has been taught to ONE walker over the plan tree and not the
// other, and each time the divergence was a live estimate defect found by a
// benchmark census months later:
//
//   - M0125-0038: the `*Project` remap went into the ndistinct walker only;
//     every Project-wrapped equi-join fell back to defaultEqSelectivity.
//   - M0127-P5.6-e-ii/-e-iii: the `*Join` arm went into one walker only.
//   - the `*NestedLoopIndexJoin` arm (docs/design/planner-rowest-collapse/
//     DESIGN.md §3): `relFilteredRowsWalk` had it, `resolveBaseColumn` never
//     did, and every column above an NLI resolved to no statistics at all.
//     TPC-DS Q99's aggregate read 720657 against an actual 90.
//
// The first two were answered by DELEGATION — `columnNDistinctForChild`,
// `columnStatsForChild` and `columnRawRowsForChild` all call
// `resolveBaseColumn` now, so those cannot drift. `relFilteredRowsWalk` cannot
// be folded in the same way: it asks a different question ("is this subtree
// still about one relation, and how many of its rows survive"), returns a
// different type, and SEALS at a join instead of descending into one. It is a
// separate walker over the SAME node vocabulary, which is precisely the shape
// that drifts, and a comment saying "keep these in step" is what failed three
// times.
//
// So this test reads the two type switches out of the source and requires
// their DESCENDING arms to be the same set. It is the same technique as the
// D-01 descriptor test, which pins two independent transcriptions of
// `pg_type.dat` against each other rather than trusting them to be re-read
// together.
//
// If it fails: you added (or removed) a plan node type in one walker. Make the
// same edit in the other, or — if the node genuinely has no meaning for one of
// them — add it to `resolverArmExemptions` WITH the reason, so the next reader
// sees an argument instead of a hole.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// resolverArmExemptions lists node types that legitimately appear in one
// walker and not the other, each with the reason it is not drift.
//
// Empty is the goal state and is currently the truth: every descending arm is
// shared. LEAF arms are not exemptions and are not listed — they are excluded
// structurally (see descendingSwitchArms).
var resolverArmExemptions = map[string]string{}

// descendingSwitchArms returns the case types of the type switch in `funcName`
// whose bodies DESCEND — i.e. recurse, directly or through a function-local
// closure. Leaf arms (`*SeqScan`, `*IndexScan` in `resolveBaseColumn`, which
// answer from the node itself rather than walking on) are excluded by that
// rule and need no exemption entry: `relFilteredRowsWalk` reaches the same
// leaves through its `n == rel` identity check, which is not a switch arm at
// all.
func descendingSwitchArms(t *testing.T, file, funcName string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == funcName {
			fn = d
			return false
		}
		return true
	})
	if fn == nil || fn.Body == nil {
		t.Fatalf("%s: function %s not found", file, funcName)
	}

	// Names that mean "recurse": the function itself, plus any function-local
	// closure (`passthrough`, `joinSide`), since those are how the second
	// walker spells its recursion.
	recurses := map[string]bool{funcName: true}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			if _, isLit := rhs.(*ast.FuncLit); !isLit || i >= len(as.Lhs) {
				continue
			}
			if id, ok := as.Lhs[i].(*ast.Ident); ok {
				recurses[id.Name] = true
			}
		}
		return true
	})

	var sw *ast.TypeSwitchStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if s, ok := n.(*ast.TypeSwitchStmt); ok && sw == nil {
			sw = s
			return false
		}
		return true
	})
	if sw == nil {
		t.Fatalf("%s: %s has no type switch", file, funcName)
	}

	arms := map[string]bool{}
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok || len(cc.List) == 0 { // default: not a node type
			continue
		}
		descends := false
		for _, s := range cc.Body {
			ast.Inspect(s, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && recurses[id.Name] {
					descends = true
				}
				return !descends
			})
			if descends {
				break
			}
		}
		if !descends {
			continue
		}
		for _, texpr := range cc.List {
			star, ok := texpr.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); ok {
				arms["*"+id.Name] = true
			}
		}
	}
	if len(arms) == 0 {
		t.Fatalf("%s: %s yielded no descending arms — the extractor is broken, "+
			"not the code under test", file, funcName)
	}
	return arms
}

func TestResolverFamilyArmListsAgree(t *testing.T) {
	resolve := descendingSwitchArms(t, "joinkeyproof.go", "resolveBaseColumn")
	filtered := descendingSwitchArms(t, "cardinality.go", "relFilteredRowsWalk")

	missing := func(from, in map[string]bool, fromName, inName string) []string {
		var out []string
		for arm := range from {
			if in[arm] {
				continue
			}
			if _, exempt := resolverArmExemptions[arm]; exempt {
				continue
			}
			out = append(out, arm)
		}
		sort.Strings(out)
		return out
	}

	if gone := missing(resolve, filtered, "resolveBaseColumn", "relFilteredRowsWalk"); len(gone) > 0 {
		t.Errorf("relFilteredRowsWalk (cardinality.go) is missing arms that "+
			"resolveBaseColumn (joinkeyproof.go) has: %s\n"+
			"Sibling walkers over one node vocabulary must change together — "+
			"add the arm, or add an exemption with its reason to "+
			"resolverArmExemptions.", strings.Join(gone, ", "))
	}
	if gone := missing(filtered, resolve, "relFilteredRowsWalk", "resolveBaseColumn"); len(gone) > 0 {
		t.Errorf("resolveBaseColumn (joinkeyproof.go) is missing arms that "+
			"relFilteredRowsWalk (cardinality.go) has: %s\n"+
			"This is the exact shape of the NLI defect (Q99: 720657 estimated, "+
			"90 actual) — add the arm, or add an exemption with its reason to "+
			"resolverArmExemptions.", strings.Join(gone, ", "))
	}

	// The arm that caused it, named explicitly: an extractor that silently
	// stopped finding arms would make the set comparison above vacuous, and
	// this is the one node type whose absence is already known to cost five
	// orders of magnitude.
	for _, arm := range []string{"*Join", "*NestedLoopIndexJoin", "*Project"} {
		if !resolve[arm] {
			t.Errorf("resolveBaseColumn has no descending %s arm", arm)
		}
	}
}
