package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R42/K76 — the qual-pushdown descent must be able to cross a `Project`,
// because the PG-shaped search's boundary republishes binding order through
// one. Three of the four cases are DECLINES: the fail-closed properties are
// the ones worth proving rather than assuming (design §5).

// srcProject wraps child in an identity projection over its own columns.
func srcProject(child Node, isolated bool) *Project {
	out := child.Output()
	targets := make([]Expr, len(out))
	for i, sc := range out {
		targets[i] = &ColumnRef{Index: i, Name: sc.Name, Type: sc.Type, SourceTableIdx: sc.SourceTableIdx}
	}
	return &Project{
		Child:         child,
		Targets:       targets,
		schema:        append(Schema{}, out...),
		IsolatedScope: isolated,
	}
}

func TestPushdownCrossesProjectToTheCorrectLeaf(t *testing.T) {
	// Join{ Project{a}, b } — a conjunct naming a must cross the projection
	// and land on the `a` scan beneath it.
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, srcProject(a, false), b)

	repl, ok := pushConjunctIntoSubtree(j, srcEq(0, "x", 1, 7))
	if !ok {
		t.Fatal("a conjunct naming the projected side must descend through the Project")
	}
	proj, isProj := repl.(*Join).Left.(*Project)
	if !isProj {
		t.Fatalf("Project must be preserved above the pushed filter, got %T", repl.(*Join).Left)
	}
	if _, hasFilter := proj.Child.(*Filter); !hasFilter {
		t.Errorf("filter must land BELOW the Project, on the scan; got %T", proj.Child)
	}
}

func TestPushdownThroughProjectIsCopyOnlyNotAMove(t *testing.T) {
	// R42/K77: `st.proven` is deliberately cleared in the Project arm, so the
	// residual conjunct is KEPT above. Its only consumer deletes the residual
	// when proven && !planted, and the remap still has an unnamed-ref
	// fail-open seam, so this round must never license that deletion.
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, srcProject(a, false), b)

	_, ok, tr := pushConjunctIntoSubtreeTraced(j, srcEq(0, "x", 1, 7))
	if !ok {
		t.Fatal("descent must succeed")
	}
	if tr.proven {
		t.Error("crossing a Project must clear `proven`: R42 is placement-only, " +
			"and a true `proven` here would delete the conjunct from the residual Filter")
	}
}

func TestPushdownDeclinesComputedProjectionTarget(t *testing.T) {
	// A target that is not a bare ColumnRef changes the conjunct's meaning
	// below the projection; remapConjunctThroughProjection must decline.
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	proj := srcProject(a, false)
	proj.Targets[0] = &BinaryOp{
		Op:    parser.OpAdd,
		Left:  &ColumnRef{Index: 0, Name: "x", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		Right: &IntegerConst{Value: 1},
	}
	j := srcJoin(JoinTypeInner, proj, b)

	if _, ok := pushConjunctIntoSubtree(j, srcEq(0, "x", 1, 7)); ok {
		t.Error("a computed projection target must decline the descent")
	}
	if _, hasFilter := proj.Child.(*Filter); hasFilter {
		t.Error("a declined descent must leave the tree untouched")
	}
}

func TestPushdownDeclinesIsolatedScopeProject(t *testing.T) {
	// An IsolatedScope Project's targets are inner-indexed then outer-
	// relabeled, so they must not be remapped against outer coordinates.
	// Today such Projects are contained only by ACCIDENT (a view-rename
	// Project declines merely because the view and body column names
	// differ); this pins the containment as a stated rule.
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	proj := srcProject(a, true)
	j := srcJoin(JoinTypeInner, proj, b)

	if _, ok := pushConjunctIntoSubtree(j, srcEq(0, "x", 1, 7)); ok {
		t.Error("an IsolatedScope Project must decline the descent")
	}
	if _, hasFilter := proj.Child.(*Filter); hasFilter {
		t.Error("a declined descent must leave the tree untouched")
	}
}

func TestPushdownDeclinesProjectLayoutMismatch(t *testing.T) {
	// Targets/Output disagreement would let the remap's index checks pass
	// against the wrong layout.
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	proj := srcProject(a, false)
	proj.Targets = append(proj.Targets, &IntegerConst{Value: 1})
	j := srcJoin(JoinTypeInner, proj, b)

	if _, ok := pushConjunctIntoSubtree(j, srcEq(0, "x", 1, 7)); ok {
		t.Error("a Targets/Output length mismatch must decline the descent")
	}
}
