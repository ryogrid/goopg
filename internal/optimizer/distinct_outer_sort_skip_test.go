package optimizer

// R84: skip the M0097-0046 outer Sort over *Distinct when the
// Distinct output already delivers ORDER BY (all keys ASC +
// nulls-last + positional prefix). distinctOp re-sorts
// ascending/nulls-last over all columns, destroying input
// order — so the skip is exact exactly there, and the
// decline arms below pin every other shape keeps its Sort.

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

// topSortAboveDistinct reports whether the plan root chain
// contains a Sort directly above a *Distinct (the shape the
// skip removes).
func topSortAboveDistinct(n Node) bool {
	if srt, ok := n.(*Sort); ok {
		if _, ok := srt.Child.(*Distinct); ok {
			return true
		}
	}
	if l, ok := n.(*Limit); ok {
		return topSortAboveDistinct(l.Child)
	}
	return false
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

func TestDistinctOuterSortSkippedForAscPrefix(t *testing.T) {
	cat := distinctSkipCatalog(t)
	for _, q := range []string{
		"SELECT DISTINCT a FROM t ORDER BY a LIMIT 100",
		"SELECT DISTINCT a, b FROM t ORDER BY a, b LIMIT 100",
		"SELECT DISTINCT a FROM t ORDER BY a",
		// Partial prefix: (a,b) ordering satisfies ORDER BY a.
		"SELECT DISTINCT a, b FROM t ORDER BY a LIMIT 100",
	} {
		n, err := Plan(parseOne(t, q), cat)
		if err != nil {
			t.Fatalf("plan %q: %v", q, err)
		}
		if !hasDistinct(n) {
			t.Fatalf("%q: no *Distinct in tree, pin vacuous", q)
		}
		if topSortAboveDistinct(n) {
			t.Errorf("%q: outer Sort kept, want skipped (ASC prefix over *Distinct)", q)
		}
	}
}

func TestDistinctOuterSortKeptOtherwise(t *testing.T) {
	cat := distinctSkipCatalog(t)
	for _, q := range []string{
		// DESC: distinctOp cannot produce it.
		"SELECT DISTINCT a FROM t ORDER BY a DESC",
		// Non-prefix: (a,b) ordering does not satisfy ORDER BY b.
		"SELECT DISTINCT a, b FROM t ORDER BY b",
		// Explicit NULLS FIRST: distinctOp puts nulls last.
		"SELECT DISTINCT a FROM t ORDER BY a NULLS FIRST",
		// DESC NULLS LAST: direction still unserved.
		"SELECT DISTINCT a FROM t ORDER BY a DESC NULLS LAST",
		// Non-ColumnRef key: expression ORDER BY keeps the Sort.
		"SELECT DISTINCT a, b FROM t ORDER BY a || 'x'",
	} {
		n, err := Plan(parseOne(t, q), cat)
		if err != nil {
			t.Fatalf("plan %q: %v", q, err)
		}
		if !hasDistinct(n) {
			t.Fatalf("%q: no *Distinct in tree, pin vacuous", q)
		}
		if !topSortAboveDistinct(n) {
			t.Errorf("%q: outer Sort skipped, want kept", q)
		}
	}
}
