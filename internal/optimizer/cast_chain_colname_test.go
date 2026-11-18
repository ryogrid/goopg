package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCastChainColumnName pins FigureColnameInternal's T_TypeCast rule: a
// column (or function) name survives any number of casts, and only a cast
// over a nameless argument falls back to the type name.
func TestCastChainColumnName(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{{Name: "member", Type: catalog.Type{Name: "oid"}}}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"select member::regrole::text from t": "member",
		"select member::text::varchar from t": "member",
		"select member::text from t":          "member",
		"select 'x'::text::varchar from t":    "varchar",
		"select now()::date::text from t":     "now",
		// Quoted "char" is the one-byte type (column char); bare CHAR is
		// bpchar (regress char.sql).
		`select 'a'::"char" from t`:         "char",
		`select cast('a' as "char") from t`: "char",
		"select 'a'::char from t":           "bpchar",
		"select 'a'::char(3) from t":        "bpchar",
	}
	for q, want := range cases {
		stmts, err := parser.Parse(q)
		if err != nil {
			t.Fatal(err)
		}
		node, err := Plan(stmts[0], cat)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got := node.Output()[0].Name; got != want {
			t.Errorf("%s: column %q, want %q", q, got, want)
		}
	}
}
