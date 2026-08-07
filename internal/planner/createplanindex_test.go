package planner

// M0127-P5.5-c — the `createPlan` index-scan arm (createplanindex.go).
//
// The arm is LIVE in production since M0127-P5.9 (2026-08-06):
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so these
// tests are no longer the only observer of its invariants. What they
// pin, in order: which leaf shapes are rebuildable and what the rewrapper
// reproduces; that the emitted `*IndexScan` carries the LEAF's identity and the
// PATH's index; the executor's `Key`/`Keys` convention; every panic the arm
// promises (each one a wrong answer prevented); and the producer half of the
// shared eligibility gate — a rel whose leaf cannot be rebuilt gains no index
// path at all.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// cpiIndex builds a bare two-column btree index over synthetic column names.
// The catalog is not consulted by the arm — the path carries the index — so no
// table needs to exist behind it.
func cpiIndex(cols ...string) *catalog.Index {
	return &catalog.Index{Name: "cpi_idx", Columns: cols, Method: "btree"}
}

// cpiPath assembles the minimal valid parameterised PathIndexScan over a rel
// whose leaf is `leaf`.
func cpiPath(leaf Node, idx *catalog.Index, clauses []indexPathClause, req RelSet) *Path {
	rel := newRelOptInfo(relsetOf(0), 100, 32)
	rel.baseLeaf = leaf
	return &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		Rows:          1,
		IndexInfo:     idx,
		IndexScanDir:  ForwardScanDirection,
		IndexClauses:  clauses,
		RequiredOuter: req,
	}
}

// TestIndexScanLeafForBareScans: both base-scan shapes resolve, the identity
// fields are the leaf's, and the rewrapper is the identity for a bare leaf.
func TestIndexScanLeafForBareScans(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	seq := &SeqScan{
		Table:                 tbl,
		Alias:                 "o",
		SmallDim:              true,
		PrivilegeCheckRole:    "view_owner",
		PrivilegeCheckRoleSet: true,
	}
	id, rewrap, ok := scanLeafFor(seq)
	if !ok {
		t.Fatal("a bare *SeqScan must be rebuildable")
	}
	if id.table != tbl || id.alias != "o" || !id.smallDim ||
		id.privilegeCheckRole != "view_owner" || !id.privilegeCheckRoleSet {
		t.Fatalf("identity lost fields: %+v", id)
	}
	is := &IndexScan{Table: tbl}
	if got := rewrap(is); got != Node(is) {
		t.Fatalf("bare-leaf rewrap is not the identity: %T", got)
	}

	// An *IndexScan leaf (the pipeline already chose an index) resolves too:
	// replacing its index with the search's choice is the point of the arm.
	if _, _, ok := scanLeafFor(&IndexScan{Table: tbl, Alias: "o2"}); !ok {
		t.Fatal("a bare *IndexScan must be rebuildable")
	}
}

// TestIndexScanLeafForFilterWrappers: the local-qual Filter chain above the
// scan is reproduced over the replacement — as NEW nodes (the originals are
// matched by pointer identity elsewhere), in the same order, with `LeafLocal`
// intact (losing it hands the predicate to a posMap pass that renumbers its
// leaf-local ColumnRefs).
func TestIndexScanLeafForFilterWrappers(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	inner := &Filter{Child: &SeqScan{Table: tbl}, Predicate: &ColumnRef{Name: "p_inner"}, LeafLocal: true}
	outer := &Filter{Child: inner, Predicate: &ColumnRef{Name: "p_outer"}}
	id, rewrap, ok := scanLeafFor(outer)
	if !ok || id.table != tbl {
		t.Fatalf("wrapped leaf did not resolve: ok=%v id=%+v", ok, id)
	}
	is := &IndexScan{Table: tbl}
	got := rewrap(is)

	f1, ok := got.(*Filter)
	if !ok {
		t.Fatalf("rewrap dropped the outer Filter: %T", got)
	}
	if f1 == outer {
		t.Fatal("rewrap reused the original outer Filter node")
	}
	if f1.Predicate != outer.Predicate || f1.LeafLocal {
		t.Fatalf("outer wrapper altered: %+v", f1)
	}
	f2, ok := f1.Child.(*Filter)
	if !ok {
		t.Fatalf("rewrap dropped the inner Filter: %T", f1.Child)
	}
	if f2 == inner {
		t.Fatal("rewrap reused the original inner Filter node")
	}
	if f2.Predicate != inner.Predicate || !f2.LeafLocal {
		t.Fatalf("inner wrapper lost LeafLocal or its predicate: %+v", f2)
	}
	if f2.Child != Node(is) {
		t.Fatalf("replacement scan is not at the bottom: %T", f2.Child)
	}
	// The original leaf is untouched — its child is still the SeqScan.
	if _, still := inner.Child.(*SeqScan); !still {
		t.Fatalf("rewrap mutated the original leaf: %T", inner.Child)
	}
}

// TestIndexScanLeafForDeclines: every non-base-scan shape is a decline, not an
// error — a relation with no index path to build.
func TestIndexScanLeafForDeclines(t *testing.T) {
	for name, leaf := range map[string]Node{
		"nil":              nil,
		"project":          &Project{},
		"join":             &Join{},
		"filter over nil":  &Filter{},
		"filter over join": &Filter{Child: &Join{}},
	} {
		if _, _, ok := scanLeafFor(leaf); ok {
			t.Errorf("%s: not a rebuildable base scan, but accepted", name)
		}
	}
}

// TestCreateIndexScanPlanComposite: the assembled node carries the LEAF's
// identity (table, alias, small-dim, check role) and the PATH's probe (index,
// keys in index-column order), dispatched through createPlan itself.
func TestCreateIndexScanPlanComposite(t *testing.T) {
	tbl := &catalog.Table{Name: "lineitem"}
	idx := cpiIndex("l_partkey", "l_suppkey")
	k0, k1 := &ColumnRef{Name: "p_partkey"}, &ColumnRef{Name: "s_suppkey"}
	leaf := &SeqScan{Table: tbl, Alias: "l", SmallDim: true}
	p := cpiPath(leaf, idx, []indexPathClause{
		{ri: &restrictInfo{}, indexCol: 0, key: k0},
		{ri: &restrictInfo{}, indexCol: 1, key: k1},
	}, relsetOf(1)|relsetOf(2))

	is, ok := createPlan(p).(*IndexScan)
	if !ok {
		t.Fatalf("createPlan(PathIndexScan) = %T, want *IndexScan", createPlan(p))
	}
	if is.Table != tbl || is.Alias != "l" || !is.SmallDim {
		t.Fatalf("leaf identity lost: %+v", is)
	}
	if is.Index != idx {
		t.Fatalf("node carries index %v, path chose %v", is.Index, idx)
	}
	if is.Key != nil || len(is.Keys) != 2 || is.Keys[0] != Expr(k0) || is.Keys[1] != Expr(k1) {
		t.Fatalf("composite probe mis-bound: Key=%v Keys=%v", is.Key, is.Keys)
	}
}

// TestCreateIndexScanPlanSingleColumnUsesKey: the executor's convention — a
// single-column probe sets `Key`, never a one-element `Keys`.
func TestCreateIndexScanPlanSingleColumnUsesKey(t *testing.T) {
	k := &ColumnRef{Name: "o_orderkey"}
	p := cpiPath(&SeqScan{Table: &catalog.Table{Name: "orders"}}, cpiIndex("o_orderkey"),
		[]indexPathClause{{ri: &restrictInfo{}, indexCol: 0, key: k}}, relsetOf(1))
	is := createPlan(p).(*IndexScan)
	if is.Key != Expr(k) || is.Keys != nil {
		t.Fatalf("single-column probe: Key=%v Keys=%v, want Key=k Keys=nil", is.Key, is.Keys)
	}
}

// TestCreateIndexScanPlanFullIndexScan: the unparameterised ordered path — an
// EMPTY clause list means a full index scan (pathnodes.h:1817), which the
// executor expresses as neither Key nor Keys nor bounds set.
func TestCreateIndexScanPlanFullIndexScan(t *testing.T) {
	p := cpiPath(&SeqScan{Table: &catalog.Table{Name: "orders"}}, cpiIndex("o_orderkey"), nil, 0)
	is := createPlan(p).(*IndexScan)
	if is.Key != nil || is.Keys != nil || is.LowKey != nil || is.HighKey != nil {
		t.Fatalf("full index scan must set no probe: %+v", is)
	}
}

// TestCreateIndexScanPlanFilterWrapperSurvives: a leaf with local quals comes
// back as Filter-over-IndexScan, not a bare scan — dropping the wrapper would
// silently widen the relation.
func TestCreateIndexScanPlanFilterWrapperSurvives(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	pred := &ColumnRef{Name: "local_qual"}
	leaf := &Filter{Child: &SeqScan{Table: tbl}, Predicate: pred, LeafLocal: true}
	k := &ColumnRef{Name: "o_orderkey"}
	p := cpiPath(leaf, cpiIndex("o_orderkey"),
		[]indexPathClause{{ri: &restrictInfo{}, indexCol: 0, key: k}}, relsetOf(1))
	f, ok := createPlan(p).(*Filter)
	if !ok {
		t.Fatalf("local-qual wrapper dropped: %T", createPlan(p))
	}
	if f.Predicate != Expr(pred) || !f.LeafLocal {
		t.Fatalf("wrapper altered: %+v", f)
	}
	if _, ok := f.Child.(*IndexScan); !ok {
		t.Fatalf("no IndexScan under the wrapper: %T", f.Child)
	}
}

// TestCreateIndexScanPlanPanics: each precondition's violation panics with a
// message naming the defect — a producer bug, not a recoverable condition.
func TestCreateIndexScanPlanPanics(t *testing.T) {
	tbl := &catalog.Table{Name: "orders"}
	idx2 := cpiIndex("a", "b")
	ka, kb := &ColumnRef{Name: "ka"}, &ColumnRef{Name: "kb"}
	two := []indexPathClause{
		{ri: &restrictInfo{}, indexCol: 0, key: ka},
		{ri: &restrictInfo{}, indexCol: 1, key: kb},
	}
	cases := map[string]struct {
		path *Path
		want string
	}{
		"no rel": {
			path: &Path{Kind: PathIndexScan, IndexInfo: idx2},
			want: "no RelOptInfo",
		},
		"unrebuildable leaf": {
			path: cpiPath(&Project{}, idx2, two, relsetOf(1)),
			want: "not a rebuildable base scan",
		},
		"no index named": {
			path: func() *Path {
				p := cpiPath(&SeqScan{Table: tbl}, idx2, two, relsetOf(1))
				p.IndexInfo = nil
				return p
			}(),
			want: "names no index",
		},
		"backward direction": {
			path: func() *Path {
				p := cpiPath(&SeqScan{Table: tbl}, idx2, two, relsetOf(1))
				p.IndexScanDir = BackwardScanDirection
				return p
			}(),
			want: "no direction",
		},
		// The dangerous one: empty means FULL SCAN, so building a
		// parameterised path from it would turn a costed point probe into a
		// whole-relation scan with an undischarged parameterisation.
		"parameterised but empty": {
			path: cpiPath(&SeqScan{Table: tbl}, idx2, nil, relsetOf(1)),
			want: "carries no index clauses",
		},
		"prefix probe": {
			path: cpiPath(&SeqScan{Table: tbl}, idx2, two[:1], relsetOf(1)),
			want: "binds 1 of 2",
		},
		"order lost": {
			path: cpiPath(&SeqScan{Table: tbl}, idx2, []indexPathClause{
				{ri: &restrictInfo{}, indexCol: 1, key: kb},
				{ri: &restrictInfo{}, indexCol: 0, key: ka},
			}, relsetOf(1)),
			want: "order was lost",
		},
		"no probe expression": {
			path: cpiPath(&SeqScan{Table: tbl}, idx2, []indexPathClause{
				{ri: &restrictInfo{}, indexCol: 0, key: ka},
				{ri: &restrictInfo{}, indexCol: 1, key: nil},
			}, relsetOf(1)),
			want: "no probe expression",
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

// TestIndexPathProducersDeclineUnrebuildableLeaf: the shared gate's producer
// half. A rel whose leaf createPlan cannot rebuild gains NO index path — else
// the DP could choose a path the builder then panics on.
func TestIndexPathProducersDeclineUnrebuildableLeaf(t *testing.T) {
	cat, orders, _ := ppiCatalog(t)
	ppiSetStats(orders, 1_500_000,
		catalog.ColumnStats{NDistinct: 1_500_000},
		catalog.ColumnStats{NDistinct: 150_000},
		catalog.ColumnStats{NDistinct: 5})
	outer, inner := relsetOf(0), relsetOf(1)
	s := orderedCtx(t, orders, 1_500_000, ppiEquiClause(outer, "l_orderkey", inner, "o_orderkey"))
	// A leaf shape the arm cannot rebuild, standing where ppiCtx put a SeqScan.
	s.levelRels(1)[1].baseLeaf = &Project{}
	before := len(s.levelRels(1)[1].Pathlist)

	s.addBaseRelIndexPaths(cat)

	rel := s.levelRels(1)[1]
	if len(rel.Pathlist) != before {
		t.Fatalf("producers added %d path(s) over an unrebuildable leaf",
			len(rel.Pathlist)-before)
	}
	for _, p := range rel.Pathlist {
		if p.Kind == PathIndexScan {
			t.Fatalf("index path costed over a leaf createPlan cannot rebuild: %+v", p)
		}
	}
}

// TestBuildInitialRelsRecordsBaseLeaf: the production writer of the field the
// arm and the gate both read — every level-1 rel carries the leaf it was
// handed, by identity.
func TestBuildInitialRelsRecordsBaseLeaf(t *testing.T) {
	leaves := []Node{
		&SeqScan{Table: &catalog.Table{Name: "a"}},
		&Filter{Child: &SeqScan{Table: &catalog.Table{Name: "b"}}, Predicate: &ColumnRef{Name: "q"}},
	}
	s, err := buildInitialRels(
		[]rangeBinding{{}, {}},
		leaves,
		[]baseRelInfo{{filteredRows: 10}, {filteredRows: 10}},
		defaultCostParams(),
		0,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for i, rel := range s.levelRels(1) {
		if rel.baseLeaf != leaves[i] {
			t.Fatalf("rel %d carries leaf %T, want the handed leaf by identity", i, rel.baseLeaf)
		}
	}
}
