package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/parser/analyzer"
)

// TestTypedLiteralOfUserType pins M0149-0001h1a85 against PG 18.3: gram.y's
// `func_name Sconst` reads a typed literal of any type — a user enum
// (`mood 'happy'`), a schema-qualified one, nchar and national character —
// and an unknown type name is 42704 `type "..." does not exist`, raised by
// typenameType before anything runs.
func TestTypedLiteralOfUserType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TYPE mood AS ENUM ('sad', 'happy')"); err != nil {
		t.Fatal(err)
	}
	for sql, want := range map[string]string{
		"SELECT mood 'happy'":            "happy",
		"SELECT public.mood 'sad'":       "sad",
		"SELECT nchar 'ab'":              "ab",
		"SELECT national character 'cd'": "cd",
	} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		if err := analyzer.Analyze(stmts[0], ctx.Catalog); err != nil {
			t.Fatalf("Analyze(%q): %v", sql, err)
		}
		rows := runQuery(t, ctx, sql)
		if got := rows[0][0].Format(); got != want {
			t.Errorf("%s = %s, want %s", sql, got, want)
		}
	}
	for _, sql := range []string{"SELECT nosuchtype 'x'", "SELECT public.nosuch 'x'"} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		err = analyzer.Analyze(stmts[0], ctx.Catalog)
		ae, ok := err.(*analyzer.AnalyzeError)
		if !ok || ae.Code != "42704" || ae.Pos != 7 {
			t.Errorf("Analyze(%q) = %v, want 42704 at the type name", sql, err)
		}
	}
	// A type in a schema reached through search_path is not refused: the
	// analyzer has no session, so an unqualified name is accepted when a type
	// of that name exists in any schema (M0149-0001h1a85a); arrays check
	// their element type.
	if err := runDDL(t, ctx, "CREATE SCHEMA s2"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE TYPE s2.t2 AS ENUM ('x')"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"SELECT t2 'x'", "SELECT s2.t2 'x'"} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		if err := analyzer.Analyze(stmts[0], ctx.Catalog); err != nil {
			t.Errorf("Analyze(%q): %v", sql, err)
		}
	}
	if !analyzer.TypeNameExists(ctx.Catalog, "", "mood[]") || !analyzer.TypeNameExists(ctx.Catalog, "", "float") {
		t.Errorf("mood[] / float should resolve")
	}
	// Option lists are not typed literals.
	if _, err := parser.Parse("CREATE SERVER s FOREIGN DATA WRAPPER w OPTIONS (host 'h', port '1')"); err != nil {
		t.Errorf("OPTIONS list: %v", err)
	}
}
