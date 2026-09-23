package optimizer

// SELECT DISTINCT ... ORDER BY: since M0141-S2b-4d the ORDER BY is served by
// the Unique candidate's own distinct-clause Sort (PG's shape); the R84
// helper distinctOutputSatisfiesOrder survives as the electOrderedDistinct
// fallback and is pinned directly below.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

func distinctSkipCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	_, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "text"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// hasDistinct walks for a *Distinct node (anti-vacuous guard:
// a tree with no Distinct at all must not pass the skip pins).
func hasDistinct(n Node) bool {
	found := false
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || found {
			return
		}
		if _, ok := cur.(*Distinct); ok {
			found = true
			return
		}
		switch x := cur.(type) {
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		}
	}
	walk(n)
	return found
}

// uniqueSortUnderRoot returns the Sort feeding a root (through Limit)
// *DistinctOn, or nil when the root is not that shape.
func uniqueSortUnderRoot(n Node) *Sort {
	if l, ok := n.(*Limit); ok {
		n = l.Child
	}
	u, ok := n.(*DistinctOn)
	if !ok {
		return nil
	}
	srt, _ := u.Child.(*Sort)
	return srt
}

// TestDistinctOrderByServedByUniqueSort (M0141-S2b-4d): PG plans SELECT
// DISTINCT over the unsorted input with the distinct clause ordered by the
// ORDER BY (transformDistinctClause: ORDER BY items first, direction kept,
// then the other columns), so the Unique's own Sort delivers the ORDER BY
// and nothing is stacked above it — for ascending prefixes, DESC, NULLS
// FIRST and a non-leading column alike. Before 4d goopg sorted by ORDER BY
// below the DISTINCT and hash-deduplicated on top.
func TestDistinctOrderByServedByUniqueSort(t *testing.T) {
	cat := distinctSkipCatalog(t)
	type key struct {
		idx             int
		desc, nullsFrst bool
	}
	for _, tc := range []struct {
		q    string
		want []key // leading keys of the Unique's Sort
	}{
		{"SELECT DISTINCT a FROM t ORDER BY a LIMIT 100", []key{{0, false, false}}},
		{"SELECT DISTINCT a, b FROM t ORDER BY a, b LIMIT 100", []key{{0, false, false}, {1, false, false}}},
		{"SELECT DISTINCT a, b FROM t ORDER BY a LIMIT 100", []key{{0, false, false}, {1, false, false}}},
		{"SELECT DISTINCT a, b FROM t ORDER BY b", []key{{1, false, false}, {0, false, false}}},
		{"SELECT DISTINCT a FROM t ORDER BY a DESC", []key{{0, true, true}}},
		{"SELECT DISTINCT a FROM t ORDER BY a NULLS FIRST", []key{{0, false, true}}},
		{"SELECT DISTINCT a FROM t ORDER BY a DESC NULLS LAST", []key{{0, true, false}}},
	} {
		n, err := Plan(parseOne(t, tc.q), cat)
		if err != nil {
			t.Fatalf("plan %q: %v", tc.q, err)
		}
		srt := uniqueSortUnderRoot(n)
		if srt == nil {
			t.Errorf("%q: root is %T, want Unique (*DistinctOn) over a Sort with no Sort above", tc.q, n)
			continue
		}
		if len(srt.Keys) != len(tc.want) {
			t.Errorf("%q: Sort has %d keys, want %d", tc.q, len(srt.Keys), len(tc.want))
			continue
		}
		for i, w := range tc.want {
			k := srt.Keys[i]
			cr, ok := k.Expr.(*ColumnRef)
			if !ok || cr.Index != w.idx || k.Desc != w.desc || k.NullsFirst != w.nullsFrst {
				t.Errorf("%q: Sort key %d = %+v desc=%v nf=%v, want col %d desc=%v nf=%v",
					tc.q, i, k.Expr, k.Desc, k.NullsFirst, w.idx, w.desc, w.nullsFrst)
			}
		}
	}
}

// TestDistinctOrderByExpressionKeepsOuterSort: an ORDER BY key that is not
// an output column cannot lead the distinct clause, so the ORDER BY stays a
// Sort above the DISTINCT. (PG rejects this statement with 42P10; goopg
// accepts it — a separate divergence.)
func TestDistinctOrderByExpressionKeepsOuterSort(t *testing.T) {
	cat := distinctSkipCatalog(t)
	const q = "SELECT DISTINCT a, b FROM t ORDER BY a || 'x'"
	n, err := Plan(parseOne(t, q), cat)
	if err != nil {
		t.Fatalf("plan %q: %v", q, err)
	}
	if !hasDistinct(n) && uniqueSortUnderRoot(n) == nil {
		t.Fatalf("%q: no DISTINCT node in tree, pin vacuous", q)
	}
	srt, ok := n.(*Sort)
	if !ok {
		t.Fatalf("%q: root is %T, want the ORDER BY Sort above the DISTINCT", q, n)
	}
	switch srt.Child.(type) {
	case *Distinct, *DistinctOn:
	default:
		t.Fatalf("%q: Sort child is %T, want the DISTINCT", q, srt.Child)
	}
}

// TestDistinctOutputSatisfiesOrderHelper pins the R84 helper the
// electOrderedDistinct fallback still uses: goopg's hashed dedup re-sorts
// ascending over every column, so exactly the ascending, nulls-last,
// positional-prefix ORDER BYs are satisfied.
func TestDistinctOutputSatisfiesOrderHelper(t *testing.T) {
	txt := catalog.Type{Name: "text"}
	d := &Distinct{Child: nil, schema: Schema{{Name: "a", Type: txt}, {Name: "b", Type: txt}}}
	col := func(i int, desc, nf bool) SortKey {
		return SortKey{Expr: &ColumnRef{Index: i, Type: txt}, Desc: desc, NullsFirst: nf}
	}
	for _, tc := range []struct {
		name string
		n    Node
		keys []SortKey
		want bool
	}{
		{"asc-prefix", d, []SortKey{col(0, false, false)}, true},
		{"asc-full", d, []SortKey{col(0, false, false), col(1, false, false)}, true},
		{"non-prefix", d, []SortKey{col(1, false, false)}, false},
		{"desc", d, []SortKey{col(0, true, true)}, false},
		{"nulls-first", d, []SortKey{col(0, false, true)}, false},
		{"not-a-distinct", &DistinctOn{schema: d.schema}, []SortKey{col(0, false, false)}, false},
		{"no-keys", d, nil, false},
	} {
		if got := distinctOutputSatisfiesOrder(tc.n, tc.keys); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
