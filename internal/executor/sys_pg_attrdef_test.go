package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCanonicalAttrdefText covers the writer-side branch that decides whether a
// column DEFAULT is stored as a CANONICAL PG18 pg_node_tree (leading '{', a real
// PG standby can stringToNode it) or degrades to goopg SQL text. M0123-S2
// sub-slice 2. The all-or-nothing / exact-type-match guard lives in
// pgnodes.ResolveForColumn; this pins the executor wrapper's two outputs and the
// leading-'{' discriminator the reload keys on.
func TestCanonicalAttrdefText(t *testing.T) {
	mustExpr := func(sql string) parser.Expr {
		e, err := parser.ParseExpr(sql)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", sql, err)
		}
		return e
	}
	col := func(typeName, defaultSQL string) catalog.Column {
		return catalog.Column{Type: catalog.Type{Name: typeName}, DefaultExpr: mustExpr(defaultSQL)}
	}

	cases := []struct {
		name          string
		col           catalog.Column
		wantCanonical bool     // leading '{' expected
		wantContains  []string // substrings required in the output
	}{
		// Canonical: type matches the column exactly.
		{"int4-op", col("int4", "40 + 2"), true, []string{"OPEXPR", "23"}},
		{"int4-neg", col("int4", "-1"), true, []string{"CONST"}},
		{"text-func", col("text", "upper('x')"), true, []string{"FUNCEXPR", "871"}},
		{"int8-lit", col("int8", "5000000000"), true, []string{"CONST", "20"}},
		// now() on a timestamptz column resolves to a canonical FuncExpr (funcid
		// 1299, result 1184 == column type) — a real standby can evaluate it.
		{"now-tstz", col("timestamptz", "now()"), true, []string{"FUNCEXPR", "1299"}},
		// SQL-text fallback: type mismatch or a node outside the scalar subset.
		{"numeric-lit", col("numeric", "0"), false, []string{"0"}},
		{"smallint-lit", col("int2", "5"), false, []string{"5"}},
		{"case-expr", col("int4", "CASE WHEN true THEN 1 ELSE 2 END"), false, []string{"CASE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalAttrdefText(tc.col)
			if got == "" {
				t.Fatalf("canonicalAttrdefText returned empty")
			}
			isCanonical := got[0] == '{'
			if isCanonical != tc.wantCanonical {
				t.Fatalf("canonical=%v want %v (adbin=%q)", isCanonical, tc.wantCanonical, got)
			}
			for _, sub := range tc.wantContains {
				if !strings.Contains(got, sub) {
					t.Errorf("adbin %q missing %q", got, sub)
				}
			}
		})
	}
}

// TestCanonicalAttrdefTextNilDefault guards the nil-DefaultExpr fast path.
func TestCanonicalAttrdefTextNilDefault(t *testing.T) {
	if got := canonicalAttrdefText(catalog.Column{Type: catalog.Type{Name: "int4"}}); got != "" {
		t.Fatalf("nil DefaultExpr = %q, want empty", got)
	}
}
