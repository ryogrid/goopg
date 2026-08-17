package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/nodes"
)

// TestRebuildAttrdefExpr covers the pg_attrdef reload discriminator (M0123-S2
// sub-slice 2): a canonical PG18 pg_node_tree adbin (leading '{') is decoded via
// pgnodes.Read → Rebuild, while goopg's legacy SQL text keeps going through
// parser.ParseExpr. Both must reconstruct an AST that formats back to the same
// SQL the writer started from — i.e. the writer/reload sibling pair round-trips.
func TestRebuildAttrdefExpr(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		targetType uint32
		canonical  bool // whether the writer would store canonical bytes
	}{
		{"int4-op", "40 + 2", nodes.OidInt4, true},
		{"int4-neg", "-1", nodes.OidInt4, true},
		{"text-func", "upper('x')", nodes.OidText, true},
		{"int8-lit", "5000000000", nodes.OidInt8, true},
		// Integer literal in a numeric column: canonical via the implicit
		// int4_numeric cast FuncExpr; reload rebuilds it back to the bare "0"
		// literal (M0123-S4 sub-slice 4a).
		{"numeric-int-cast", "0", 1700, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := parser.ParseExpr(tc.sql)
			if err != nil {
				t.Fatalf("ParseExpr(%q): %v", tc.sql, err)
			}
			wantSQL := catalog.FormatExprForAttrdef(src)

			// Reproduce exactly what the writer stores.
			var adbin string
			if node, ok := nodes.ResolveForColumn(src, tc.targetType); ok {
				adbin = nodes.Out(node)
			} else {
				adbin = wantSQL
			}
			if (adbin[0] == '{') != tc.canonical {
				t.Fatalf("adbin %q canonical=%v want %v", adbin, adbin[0] == '{', tc.canonical)
			}

			got, err := rebuildAttrdefExpr(adbin)
			if err != nil {
				t.Fatalf("rebuildAttrdefExpr(%q): %v", adbin, err)
			}
			if gotSQL := catalog.FormatExprForAttrdef(got); gotSQL != wantSQL {
				t.Fatalf("round-trip SQL = %q, want %q (adbin=%q)", gotSQL, wantSQL, adbin)
			}
		})
	}
}

// TestRebuildAttrdefExprMalformed asserts a malformed canonical value surfaces an
// error (so the reload degrades that one column to a NULL default rather than
// panicking) and a bad SQL text likewise errors.
func TestRebuildAttrdefExprMalformed(t *testing.T) {
	if _, err := rebuildAttrdefExpr("{NOTANODE :x 1}"); err == nil {
		t.Fatal("malformed canonical adbin: want error, got nil")
	}
	if _, err := rebuildAttrdefExpr("40 +"); err == nil {
		t.Fatal("malformed SQL adbin: want error, got nil")
	}
}
