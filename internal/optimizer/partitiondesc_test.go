package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPartitionDescOrder pins partition_bounds_create's canonical order
// (M0148-0001c3a9 slice 1): RANGE by lower bound with MINVALUE first, LIST
// by smallest value with the NULL-only partition next, HASH by (modulus,
// remainder), DEFAULT last; an undecidable bound keeps the catalog order.
// The expected orders are PG 18.3's EXPLAIN member orders for the same DDL.
func TestPartitionDescOrder(t *testing.T) {
	intCol := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}, {Name: "b", Type: catalog.Type{Name: "text"}}}
	rng := func(name, from string, unb, max bool) *catalog.Table {
		return &catalog.Table{Name: name, PartitionBounds: []catalog.PartitionBound{{ChildName: name,
			FromValues: []string{from}, FromUnbounded: []bool{unb}, FromUnboundMax: []bool{max}}}}
	}
	def := func(name string) *catalog.Table {
		return &catalog.Table{Name: name, PartitionBounds: []catalog.PartitionBound{{ChildName: name, IsDefault: true}}}
	}
	list := func(name string, raw, lit []string) *catalog.Table {
		return &catalog.Table{Name: name, PartitionBounds: []catalog.PartitionBound{{ChildName: name, InValues: raw, InValueLiterals: lit}}}
	}
	hash := func(name string, m, r int64) *catalog.Table {
		return &catalog.Table{Name: name, PartitionBounds: []catalog.PartitionBound{{ChildName: name, IsHash: true, Modulus: m, Remainder: r}}}
	}
	names := func(ts []*catalog.Table) string {
		var s []string
		for _, x := range ts {
			s = append(s, x.Name)
		}
		return strings.Join(s, ",")
	}
	for _, c := range []struct {
		name   string
		parent *catalog.Table
		kids   []*catalog.Table
		want   string
	}{
		{"range", &catalog.Table{PartitionKey: []string{"a"}, PartitionMethod: "RANGE", Columns: intCol},
			[]*catalog.Table{rng("p1", "0", false, false), rng("p3", "500", false, false), rng("p2", "250", false, false),
				def("d"), rng("m", "", true, false), rng("p10", "1000", false, false)},
			"m,p1,p2,p3,p10,d"},
		{"list", &catalog.Table{PartitionKey: []string{"b"}, PartitionMethod: "LIST", Columns: intCol},
			[]*catalog.Table{list("n", []string{"NULL"}, []string{"NULL"}), list("z", []string{"z", "a"}, []string{"'z'", "'a'"}),
				def("d"), list("m", []string{"m", "n"}, []string{"'m'", "'n'"}), list("txtnull", []string{"null"}, []string{"'null'"})},
			"z,m,txtnull,n,d"},
		{"hash", &catalog.Table{PartitionKey: []string{"a"}, PartitionMethod: "HASH", Columns: intCol},
			[]*catalog.Table{hash("h2", 4, 2), hash("h1", 4, 1), hash("h0", 2, 0)},
			"h0,h1,h2"},
		{"undecidable", &catalog.Table{PartitionKey: []string{"x"}, PartitionMethod: "RANGE",
			Columns: []catalog.Column{{Name: "x", Type: catalog.Type{Name: "inet"}}}},
			[]*catalog.Table{rng("b", "10.0.0.2", false, false), rng("a", "10.0.0.1", false, false)},
			"b,a"},
	} {
		if got := names(partitionDescOrder(c.parent, c.kids)); got != c.want {
			t.Errorf("%s: order %s, want %s", c.name, got, c.want)
		}
	}
}
