package optimizer

import "testing"

// TestAddPathCanonicalPathkeysEvictFuzzyTiedMerge pins M0146-0185 against PG
// 18.3's add_path over canonical pathkeys.
//
// TPC-DS Q77's `wr` joinrel holds a merge join sorted by wr_web_page_sk and a
// nested loop (index scan on web_page_pkey outer, Material inner) sorted by
// wp_web_page_sk. The join equates the two columns, so PG's pathkeys for both
// are one canonical pathkey: the totals are equal within STD_FUZZ_FACTOR, the
// nested loop starts first, and add_path keeps only the nested loop. goopg
// compared the keys by expression, called them PATHKEYS_DIFFERENT, kept both,
// and the merge won on total.
func TestAddPathCanonicalPathkeysEvictFuzzyTiedMerge(t *testing.T) {
	wr := &ColumnRef{Name: "wr_web_page_sk", SourceTableIdx: 1, Index: 0}
	wp := &ColumnRef{Name: "wp_web_page_sk", SourceTableIdx: 3, Index: 5}
	mk := func(rel *RelOptInfo, key Expr, startup, total float64) *Path {
		return &Path{Kind: PathMergeJoin, Rel: rel, Rows: 7, Cost: Cost{Startup: startup, Total: total},
			Pathkeys: []PathKey{{Expr: key, SortAsc: true}}}
	}
	for _, tc := range []struct {
		name      string
		class     func(a, b Expr) bool
		wantPaths int
	}{
		{"canonical pathkeys", func(a, b Expr) bool { return true }, 1},
		{"no class: syntactic pathkeys", nil, 2},
	} {
		rel := newRelOptInfo(RelSet(0b111), 7, 16)
		rel.pathkeyClass = tc.class
		addPath(rel, mk(rel, wr, 2318.82, 2319.23), "mergejoin")
		nl := mk(rel, wp, 0.44, 2330.71)
		nl.Kind = PathNestLoop
		addPath(rel, nl, "join.nestloop")
		if len(rel.Pathlist) != tc.wantPaths {
			t.Fatalf("%s: %d paths kept, want %d", tc.name, len(rel.Pathlist), tc.wantPaths)
		}
		if tc.wantPaths == 1 && rel.Pathlist[0] != nl {
			t.Fatalf("%s: kept %v, want the fast-start nested loop", tc.name, rel.Pathlist[0].Kind)
		}
	}
	// Direction still matters: a DESC key on the class's other member is a
	// different pathkey.
	if got := comparePathkeysDimRel(&RelOptInfo{pathkeyClass: func(a, b Expr) bool { return true }},
		[]PathKey{{Expr: wr, SortAsc: true}}, []PathKey{{Expr: wp, SortAsc: false}}); got != dimIncomparable {
		t.Fatalf("opposite directions compare %v, want incomparable", got)
	}
}
