package planner

// M0127-P0.3 — the static hash-key type decision (design leftdeep-joins/05 §4,
// stage E3).
//
// The executor no longer discovers whether build-side keys are
// int64-representable by populating both a string map and an int64 map and
// dropping the loser; it asks the plan up front. That makes these tests the
// coverage of record for a PERFORMANCE cliff rather than a wrong answer: if
// HashKeysAreInt64 quietly starts answering false for ordinary integer joins
// (a ColumnRef whose Type is left zero AND whose schema fallback breaks, say),
// every TPC-H join silently drops back to allocating a canonical key string per
// probe row — which is the exact cost M0043-0003 was fixed to avoid, and no
// row-count or plan-shape gate would notice.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// planHashJoins plans sql against cat and returns every hash Join in the tree.
func planHashJoins(t *testing.T, cat catalog.Catalog, sql string) []*Join {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var joins []*Join
	var walk func(Node)
	walk = func(n Node) {
		switch x := n.(type) {
		case nil:
			return
		case *Join:
			if x.Algo == JoinAlgoHash {
				joins = append(joins, x)
			}
			walk(x.Left)
			walk(x.Right)
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		}
	}
	walk(plan)
	return joins
}

func hashKeyTestCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column) {
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	create("supplier", []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "s_name", Type: catalog.Type{Name: "text"}},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int4"}},
	})
	create("nation", []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int2"}},
		{Name: "n_name", Type: catalog.Type{Name: "text"}},
	})
	create("lineitem", []catalog.Column{
		{Name: "l_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "l_comment", Type: catalog.Type{Name: "text"}},
		{Name: "l_price", Type: catalog.Type{Name: "numeric"}},
	})
	create("orders", []catalog.Column{
		{Name: "o_comment", Type: catalog.Type{Name: "text"}},
		{Name: "o_total", Type: catalog.Type{Name: "numeric"}},
	})
	return cat
}

// TestHashKeysAreInt64OnPlannedIntegerJoin is the one that matters: a join
// planned from real SQL over real catalog columns must reach the int64 lane.
// Mixed integer WIDTHS (int8 = int4, int4 = int2) are included because the
// executor's int64 key is width-agnostic while a type check written as "both
// sides are the same type" would reject them.
func TestHashKeysAreInt64OnPlannedIntegerJoin(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	cat := hashKeyTestCatalog(t)
	joins := planHashJoins(t, cat, `select s_name, n_name
from supplier, nation, lineitem
where s_nationkey = n_nationkey and l_suppkey = s_suppkey`)
	if len(joins) == 0 {
		t.Fatal("no hash join in the plan — the fixture no longer exercises the path")
	}
	for i, j := range joins {
		if !j.HashKeysAreInt64() {
			t.Errorf("join %d (%v = %v): HashKeysAreInt64 = false, want true — "+
				"integer-keyed joins must reach the int64 build", i, j.LeftKey, j.RightKey)
		}
	}
}

// TestHashKeysAreInt64RejectsNonIntegerKeys pins the conservative half. A text
// key must not be promised as int64 (the executor would demote mid-build and
// re-key the whole table), and neither must numeric — for numeric it is the
// VALUES that decide representability, so the promise is not the type's to make.
func TestHashKeysAreInt64RejectsNonIntegerKeys(t *testing.T) {
	cat := hashKeyTestCatalog(t)
	for _, tc := range []struct{ name, sql string }{
		{"text", `select l_comment from lineitem, orders where l_comment = o_comment`},
		{"numeric", `select l_price from lineitem, orders where l_price = o_total`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			joins := planHashJoins(t, cat, tc.sql)
			if len(joins) == 0 {
				t.Skip("planner chose a non-hash algorithm for this shape")
			}
			for i, j := range joins {
				if j.HashKeysAreInt64() {
					t.Errorf("join %d: HashKeysAreInt64 = true for a %s key, want false", i, tc.name)
				}
			}
		})
	}
}

// TestHashKeysAreInt64Guards covers the shapes that must answer false without a
// planner round trip: a non-hash algorithm, and a missing key.
func TestHashKeysAreInt64Guards(t *testing.T) {
	intKey := func(i int) Expr { return &ColumnRef{Index: i, Type: catalog.Type{Name: "int8"}} }
	cases := []struct {
		name string
		join *Join
		want bool
	}{
		{"hash with typed int keys", &Join{Algo: JoinAlgoHash, LeftKey: intKey(0), RightKey: intKey(1)}, true},
		{"nested loop", &Join{Algo: JoinAlgoNestedLoop, LeftKey: intKey(0), RightKey: intKey(1)}, false},
		{"merge", &Join{Algo: JoinAlgoMerge, LeftKey: intKey(0), RightKey: intKey(1)}, false},
		{"missing right key", &Join{Algo: JoinAlgoHash, LeftKey: intKey(0)}, false},
		{"missing left key", &Join{Algo: JoinAlgoHash, RightKey: intKey(1)}, false},
		{"one side text", &Join{
			Algo:     JoinAlgoHash,
			LeftKey:  intKey(0),
			RightKey: &ColumnRef{Index: 1, Type: catalog.Type{Name: "text"}},
		}, false},
		// A bare ColumnRef with no Type and no children to fall back on must
		// answer false rather than panic.
		{"untyped key, no children", &Join{Algo: JoinAlgoHash, LeftKey: &ColumnRef{}, RightKey: &ColumnRef{Index: 1}}, false},
	}
	for _, tc := range cases {
		if got := tc.join.HashKeysAreInt64(); got != tc.want {
			t.Errorf("%s: HashKeysAreInt64 = %v, want %v", tc.name, got, tc.want)
		}
	}
	var nilJoin *Join
	if nilJoin.HashKeysAreInt64() {
		t.Error("nil join: HashKeysAreInt64 = true, want false")
	}
}
