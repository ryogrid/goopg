package planner

// M0127-P5.5-d — the seq-scan and sort `createPlan` arms (createplansimple.go).
//
// The arms are inert in production (`GOOPG_PGSHAPED_DP` OFF, no `planSelect`
// caller), so these tests are the only observer of their invariants. What they
// pin: the seq-scan rebuild is a FRESH node that loses no `*SeqScan` field
// (including the four fields only this arm copies — a field added to the node
// but not to `scanIdentity` fails here, which is the struct's stated purpose);
// the demotion of an `*IndexScan` leaf to the seq scan the search priced; the
// local-qual `*Filter` chain re-created as new nodes; the sort arm's recursion
// into its child arm and the SortAsc→Desc negation; and every panic each arm
// promises.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// cpsSeqPath assembles the minimal valid PathSeqScan over a rel whose leaf is
// `leaf`.
func cpsSeqPath(leaf Node) *Path {
	rel := newRelOptInfo(relsetOf(0), 100, 32)
	rel.baseLeaf = leaf
	return &Path{Kind: PathSeqScan, Rel: rel, Rows: 100}
}

// TestCreateSeqScanPlanLosslessRebuild: a leaf whose base scan is already a
// `*SeqScan` comes back as a FRESH node — never the leaf itself, which the
// pre-search pipeline still owns — with every field equal, including the four
// fields only this arm carries (EstRelRows, LockParentOID, SkipIfVanished,
// InheritParentOID).
func TestCreateSeqScanPlanLosslessRebuild(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	leaf := &SeqScan{
		Table:                 tbl,
		Alias:                 "o",
		EstRelRows:            12345,
		SmallDim:              true,
		LockParentOID:         77,
		SkipIfVanished:        true,
		InheritParentOID:      78,
		PrivilegeCheckRole:    "view_owner",
		PrivilegeCheckRoleSet: true,
	}
	got := createPlan(cpsSeqPath(leaf))
	ss, ok := got.(*SeqScan)
	if !ok {
		t.Fatalf("createPlan(PathSeqScan) = %T, want *SeqScan", got)
	}
	if ss == leaf {
		t.Fatal("arm returned the pipeline's own leaf node; it must rebuild a fresh one")
	}
	if ss.Table != tbl || ss.Alias != "o" || ss.EstRelRows != 12345 || !ss.SmallDim ||
		ss.LockParentOID != 77 || !ss.SkipIfVanished || ss.InheritParentOID != 78 ||
		ss.PrivilegeCheckRole != "view_owner" || !ss.PrivilegeCheckRoleSet {
		t.Fatalf("rebuild lost fields: %+v", ss)
	}
}

// TestCreateSeqScanPlanDemotesIndexLeaf: the interesting case — the pipeline
// chose an index probe for this leaf and the search costed a sequential scan
// cheaper. The arm demotes, carrying the identity across node types.
func TestCreateSeqScanPlanDemotesIndexLeaf(t *testing.T) {
	tbl := &catalog.Table{Name: "lineitem"}
	leaf := &IndexScan{
		Table:                 tbl,
		Alias:                 "l",
		Index:                 cpiIndex("l_orderkey"),
		Key:                   &ColumnRef{Name: "probe"},
		SmallDim:              true,
		PrivilegeCheckRole:    "view_owner",
		PrivilegeCheckRoleSet: true,
	}
	ss, ok := createPlan(cpsSeqPath(leaf)).(*SeqScan)
	if !ok {
		t.Fatalf("createPlan over an *IndexScan leaf = %T, want *SeqScan", createPlan(cpsSeqPath(leaf)))
	}
	if ss.Table != tbl || ss.Alias != "l" || !ss.SmallDim ||
		ss.PrivilegeCheckRole != "view_owner" || !ss.PrivilegeCheckRoleSet {
		t.Fatalf("demotion lost identity: %+v", ss)
	}
}

// TestCreateSeqScanPlanFilterWrapperSurvives: local quals live above the scan
// in `*Filter` wrappers; the arm re-creates them as NEW nodes with `LeafLocal`
// intact, over the fresh scan.
func TestCreateSeqScanPlanFilterWrapperSurvives(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	pred := &ColumnRef{Name: "local_qual"}
	inner := &SeqScan{Table: tbl}
	wrap := &Filter{Child: inner, Predicate: pred, LeafLocal: true}
	f, ok := createPlan(cpsSeqPath(wrap)).(*Filter)
	if !ok {
		t.Fatalf("local-qual wrapper dropped: %T", createPlan(cpsSeqPath(wrap)))
	}
	if f == wrap {
		t.Fatal("arm reused the original Filter node")
	}
	if f.Predicate != Expr(pred) || !f.LeafLocal {
		t.Fatalf("wrapper altered: %+v", f)
	}
	child, ok := f.Child.(*SeqScan)
	if !ok {
		t.Fatalf("no SeqScan under the wrapper: %T", f.Child)
	}
	if child == inner {
		t.Fatal("arm reused the original scan node under the fresh wrapper")
	}
}

// TestCreateSeqScanPlanPanics: each precondition's violation panics with a
// message naming the defect.
func TestCreateSeqScanPlanPanics(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	cases := map[string]struct {
		path *Path
		want string
	}{
		"no rel": {
			path: &Path{Kind: PathSeqScan},
			want: "no RelOptInfo",
		},
		"unrebuildable leaf": {
			path: cpsSeqPath(&Project{}),
			want: "not a rebuildable base scan",
		},
		"parameterised": {
			path: func() *Path {
				p := cpsSeqPath(&SeqScan{Table: tbl})
				p.RequiredOuter = relsetOf(1)
				return p
			}(),
			want: "nothing can discharge it",
		},
		"claims ordering": {
			path: func() *Path {
				p := cpsSeqPath(&SeqScan{Table: tbl})
				p.Pathkeys = []PathKey{{Expr: &ColumnRef{Name: "k"}, SortAsc: true}}
				return p
			}(),
			want: "a heap scan delivers none",
		},
		"carries index detail": {
			path: func() *Path {
				p := cpsSeqPath(&SeqScan{Table: tbl})
				p.IndexInfo = cpiIndex("o_orderkey")
				return p
			}(),
			want: "carries index detail",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("no panic")
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, tc.want) {
					t.Fatalf("panic %q does not name the defect %q", msg, tc.want)
				}
			}()
			createPlan(tc.path)
		})
	}
}

// TestCreateSortPlanOverPrebuilt: the arm recurses into the child arm, wraps
// the built node, and translates each pathkey with the SortAsc→Desc negation
// (PG's ascending-true against the executor's descending-true) and NullsFirst
// carried through unchanged.
func TestCreateSortPlanOverPrebuilt(t *testing.T) {
	sub := &SeqScan{Table: &catalog.Table{Name: "orders"}}
	k1, k2 := &ColumnRef{Name: "a"}, &ColumnRef{Name: "b"}
	p := &Path{
		Kind: PathSort,
		Pathkeys: []PathKey{
			{Expr: k1, SortAsc: true, NullsFirst: false},
			{Expr: k2, SortAsc: false, NullsFirst: true},
		},
		Children: []*Path{newPrebuiltPath(&RelOptInfo{}, sub)},
	}
	s, ok := createPlan(p).(*Sort)
	if !ok {
		t.Fatalf("createPlan(PathSort) = %T, want *Sort", createPlan(p))
	}
	if s.Child != Node(sub) {
		t.Fatalf("sort wraps %T, want the child arm's node", s.Child)
	}
	if len(s.Keys) != 2 {
		t.Fatalf("sort carries %d keys, want 2", len(s.Keys))
	}
	if s.Keys[0].Expr != Expr(k1) || s.Keys[0].Desc || s.Keys[0].NullsFirst {
		t.Fatalf("ascending key mistranslated: %+v", s.Keys[0])
	}
	if s.Keys[1].Expr != Expr(k2) || !s.Keys[1].Desc || !s.Keys[1].NullsFirst {
		t.Fatalf("descending key mistranslated: %+v", s.Keys[1])
	}
}

// TestCreateSortPlanOverSeqScanPath: the recursion reaches a REAL child arm,
// not just the prebuilt unwrap — the shape `sortPathFor` produces once the
// child is a searched path rather than a wrapped node.
func TestCreateSortPlanOverSeqScanPath(t *testing.T) {
	leaf := &SeqScan{Table: &catalog.Table{Name: "orders"}, Alias: "o"}
	p := &Path{
		Kind:     PathSort,
		Pathkeys: []PathKey{{Expr: &ColumnRef{Name: "o_orderdate"}, SortAsc: true}},
		Children: []*Path{cpsSeqPath(leaf)},
	}
	s, ok := createPlan(p).(*Sort)
	if !ok {
		t.Fatalf("createPlan(PathSort) = %T, want *Sort", createPlan(p))
	}
	child, ok := s.Child.(*SeqScan)
	if !ok || child == leaf || child.Alias != "o" {
		t.Fatalf("child arm not applied: %T (reused=%v)", s.Child, child == leaf)
	}
}

// TestCreateSortPlanPanics: the producer-bug preconditions.
func TestCreateSortPlanPanics(t *testing.T) {
	sub := newPrebuiltPath(&RelOptInfo{}, &SeqScan{Table: &catalog.Table{Name: "t"}})
	oneKey := []PathKey{{Expr: &ColumnRef{Name: "k"}, SortAsc: true}}
	cases := map[string]struct {
		path *Path
		want string
	}{
		"no children": {
			path: &Path{Kind: PathSort, Pathkeys: oneKey},
			want: "0 children",
		},
		"two children": {
			path: &Path{Kind: PathSort, Pathkeys: oneKey, Children: []*Path{sub, sub}},
			want: "2 children",
		},
		"no pathkeys": {
			path: &Path{Kind: PathSort, Children: []*Path{sub}},
			want: "orders by nothing",
		},
		"nil key expression": {
			path: &Path{Kind: PathSort, Pathkeys: []PathKey{{SortAsc: true}}, Children: []*Path{sub}},
			want: "no expression",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("no panic")
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, tc.want) {
					t.Fatalf("panic %q does not name the defect %q", msg, tc.want)
				}
			}()
			createPlan(tc.path)
		})
	}
}
