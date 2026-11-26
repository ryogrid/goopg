package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestApplyKeyMatchNondeterministicCollation: under a nondeterministic ICU
// collation the apply worker's row lookup treats canonically equivalent
// strings (U+00E4 vs a + U+0308) as the same key, as PG's tuples_equal and
// the index opclass do; a deterministic column still compares bytes, and
// case still differs at the default (tertiary) strength.
func TestApplyKeyMatchNondeterministicCollation(t *testing.T) {
	im := catalog.NewInMemory()
	if _, err := im.CreateCollation(&catalog.UserCollation{Name: "ctest_nondet", Provider: 'i', Locale: "und", Deterministic: false}, "", false); err != nil {
		t.Fatal(err)
	}
	ctx := NewContext()
	ctx.Catalog = im
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "text"}, Collation: "ctest_nondet"},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	}
	colls := nondeterministicCollators(ctx, cols)
	if colls == nil || colls[0] == nil || colls[1] != nil {
		t.Fatalf("collators = %v, want one for a only", colls)
	}
	row := Row{NewStringDatum("äbc"), NewStringDatum("foo")}
	if !rowMatchesKeyCollated(row, Row{NewStringDatum("äbc"), NullDatum}, colls) {
		t.Error("canonically equivalent key must match under the nondeterministic collation")
	}
	if rowMatchesKeyCollated(row, Row{NewStringDatum("ÄBC"), NullDatum}, colls) {
		t.Error("a case difference must not match at tertiary strength")
	}
	if rowMatchesKeyCollated(Row{NewStringDatum("x"), NewStringDatum("ä")}, Row{NewStringDatum("x"), NewStringDatum("ä")}, colls) {
		t.Error("a deterministic column must compare bytes")
	}
}
