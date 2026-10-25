package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestIndexProbeFormMatchesIndexProbeKey pins M0122-0012 slice 80: a
// parameterised index scan forms its probe key through indexProbeForm, which
// resolves the key columns once and, for an all-fixed-width probe, fills a
// template formed by the first indexProbeKey call. Every probe must be
// indexProbeKey's bytes: one- and two-attribute prefixes of an int index
// (the template path), a probe whose prefix holds a varchar (never
// templated), and a form whose attribute count changes between calls.
func TestIndexProbeFormMatchesIndexProbeKey(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE INDEX items_acb ON items (a, c, b)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "items"})
	ints := []int64{0, 1, -1, 7, 2147483647, -2147483648, 123456, 42}
	for _, tc := range []struct {
		index     string
		widths    []int // attributes per probe, in call order
		templated bool
	}{
		{"items_ab", []int{1, 1, 1, 1, 1, 1, 1, 1}, true},
		{"items_ab", []int{2, 2, 2, 2, 2, 2, 2, 2}, true},
		{"items_ab", []int{1, 2, 1, 2, 2, 1, 1, 2}, true}, // the template covers the first width only
		{"items_acb", []int{2, 2, 2, 1, 2, 1, 2, 2}, false},
	} {
		idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: tc.index})
		if !ok {
			t.Fatalf("lookup %s", tc.index)
		}
		var f indexProbeForm
		for call, n := range tc.widths {
			parts := make([]indexProbeKeyPart, n)
			for i := range n {
				col, found := f.colAt(ctx, tbl, idx.Columns[i], i)
				if !found {
					t.Fatalf("%s: column %s not found", tc.index, idx.Columns[i])
				}
				v := NewIntDatum(ints[(call+i)%len(ints)])
				if col.Type.Name == "varchar" {
					v = NewStringDatum([]string{"x", "a longer varchar value", ""}[call%3])
				}
				parts[i] = indexProbeKeyPart{col: col, val: v}
			}
			got, gerr := f.form(ctx, idx, parts)
			want, werr := ctx.indexProbeKey(idx, parts)
			if gerr != nil || werr != nil {
				t.Fatalf("%s call %d: form err %v, indexProbeKey err %v", tc.index, call, gerr, werr)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s call %d (%d attrs): form %x, indexProbeKey %x", tc.index, call, n, got, want)
			}
		}
		if (f.tmpl != nil) != tc.templated {
			t.Fatalf("%s %v: templated=%v, want %v", tc.index, tc.widths, f.tmpl != nil, tc.templated)
		}
	}
}
