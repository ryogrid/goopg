package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestHashJoinBuildOnSmallDim (M0054-0010) confirms a hash join
// between a SmallDimension-flagged table and a much larger table
// pins the small side as the build side, regardless of which
// side is the join's Left vs Right child.
//
// Setup mirrors the TPC-H supplier × nation join: nation is the
// SmallDimension side; supplier is the larger fact-ish table. The
// planner is invoked with `nation` first in FROM (LEFT child) in
// one variant and `supplier` first in another. In both cases the
// resulting *Join must have `BuildLeft` set to the side where
// nation lives.
func TestHashJoinBuildOnSmallDim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fromOrder string
		// Side where the small-dim table lives in the planned Join.
		// `j.Left` is `from-list[0]`'s scan (post any planner
		// rewrites that don't reorder this binary case).
		wantBuildOn string // "left" or "right"
	}{
		{name: "nation-then-supplier", fromOrder: "nation, supplier", wantBuildOn: "left"},
		{name: "supplier-then-nation", fromOrder: "supplier, nation", wantBuildOn: "right"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := catalog.NewInMemory()
			nat, err := cat.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
				{Name: "n_nationkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
				{Name: "n_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			nat.SmallDimension = true
			if _, err := cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
				{Name: "s_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
				{Name: "s_nationkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
				{Name: "s_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
			}); err != nil {
				t.Fatal(err)
			}
			stmt := parseOne(t, `SELECT s_name, n_name FROM `+tc.fromOrder+` WHERE s_nationkey = n_nationkey`)
			node, err := Plan(stmt, cat)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			j := findFirstJoin(node)
			if j == nil {
				t.Fatalf("no Join in plan; tree: %s", describePlanTree(node))
			}
			if j.Algo != JoinAlgoHash {
				t.Fatalf("expected hash join algo; got %v", j.Algo)
			}
			// Identify which side has nation.
			leftHasSmall := IsSmallDimensionSide(j.Left)
			rightHasSmall := IsSmallDimensionSide(j.Right)
			switch tc.wantBuildOn {
			case "left":
				if !leftHasSmall {
					t.Fatalf("nation expected on Join.Left; tree: %s", describePlanTree(node))
				}
				if !j.BuildLeft {
					t.Errorf("BuildLeft = %v, want true (nation on Left = small side)", j.BuildLeft)
				}
			case "right":
				if !rightHasSmall {
					t.Fatalf("nation expected on Join.Right; tree: %s", describePlanTree(node))
				}
				if j.BuildLeft {
					t.Errorf("BuildLeft = %v, want false (nation on Right = small side, build-on-right)", j.BuildLeft)
				}
			}
		})
	}
}

// findFirstJoin returns the first *Join in the plan tree.
func findFirstJoin(n Node) *Join {
	if n == nil {
		return nil
	}
	switch x := n.(type) {
	case *Join:
		return x
	case *Project:
		return findFirstJoin(x.Child)
	case *Filter:
		return findFirstJoin(x.Child)
	case *Sort:
		return findFirstJoin(x.Child)
	case *Limit:
		return findFirstJoin(x.Child)
	case *Aggregate:
		return findFirstJoin(x.Child)
	}
	return nil
}
