package optimizer

import (
	"fmt"
	"testing"
)

// widthFixture builds an n-relation comma FROM list through the same fixture
// the rest of the seam tests use, with one leaf-local restriction so the
// problem is not a bare cross product.
func widthFixture(n int) ([]string, Node, *resolveContext) {
	names := make([]string, n)
	rows := make([]int64, n)
	for i := range names {
		names[i] = fmt.Sprintf("t%02d", i)
		rows[i] = int64(1000 * (i + 1))
	}
	node, ctx := seamFixture(names, rows)
	return names, node, ctx
}

// TestSeamDeclinesAtTheRelSetWidth pins M0145-0005's `leaf-count-overflow`
// decision: goopg's join search refuses a problem wider than `maxSearchRels`
// and falls back to the syntactic tree, where PostgreSQL has no such ceiling
// at all (`Relids` is a `Bitmapset`, bitmapset.c, grown on demand).
//
// The divergence is a MISSED OPTIMISATION, not a wrong answer — that is what
// the identity assertion below states: the seam hands back the caller's own
// node and residual untouched, so the statement still plans and still
// enforces every qual, just without a costed join order.
//
// The 32-relation arm is the non-vacuity control and it is load-bearing: at
// exactly `maxSearchRels` the identical fixture IS searched, so the refusal is
// the relset width and not some other property of a wide comma FROM list
// (which is what a one-armed test would leave open).
func TestSeamDeclinesAtTheRelSetWidth(t *testing.T) {

	t.Run("at the width the problem is searched", func(t *testing.T) {
		names, node, ctx := widthFixture(maxSearchRels)
		out, _, used := tryPGShapedJoinSearch(node, seamLocal(names, 0), ctx, nil)
		if !used {
			t.Fatalf("the seam declined a %d-relation problem; maxSearchRels is %d, so this "+
				"must be searched or the test below proves nothing about the width",
				maxSearchRels, maxSearchRels)
		}
		if out == nil {
			t.Fatal("searched problem returned a nil tree")
		}
	})

	t.Run("one past the width falls back to the syntactic tree", func(t *testing.T) {
		names, node, ctx := widthFixture(maxSearchRels + 1)
		pred := seamLocal(names, 0)
		out, residual, used := tryPGShapedJoinSearch(node, pred, ctx, nil)
		if used {
			t.Fatalf("the seam searched a %d-relation problem, but `RelSet` is %d bits wide — "+
				"leaf indexes at or above that bit are unrepresentable in joinIsLegal's masks",
				maxSearchRels+1, maxSearchRels)
		}
		// Fail-closed means "hand the caller back exactly what it gave us".
		// Anything else here would be a half-planned tree, which is the
		// failure mode this decline exists to avoid.
		if out != node {
			t.Fatal("the decline returned a different tree; a refused search must be the identity")
		}
		if residual != pred {
			t.Fatal("the decline dropped or rewrote the residual; a refused search must not touch it")
		}
	})
}

// TestNewSearchCtxRefusesPastTheRelSetWidth pins the second, independent
// refusal on the same ceiling. The seam above declines before ever building a
// context; this is the constructor's own guard, which is what protects a
// caller that reaches the search by another route (a flattened body can push
// the leaf count past the width even when the statement's FROM list fits —
// the `leaf-count-overflow` site in joinsearchseam.go).
func TestNewSearchCtxRefusesPastTheRelSetWidth(t *testing.T) {
	if _, err := newSearchCtx(maxSearchRels, defaultCostParams(), nil); err != nil {
		t.Fatalf("newSearchCtx(%d) = %v, want a context: the ceiling is inclusive", maxSearchRels, err)
	}
	if _, err := newSearchCtx(maxSearchRels+1, defaultCostParams(), nil); err == nil {
		t.Fatalf("newSearchCtx(%d) built a context; RelSet is %d bits wide",
			maxSearchRels+1, maxSearchRels)
	}
}
