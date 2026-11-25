package plpgsql

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPLExprWithFromClause: PL/pgSQL evaluates an expression as
// `SELECT <expr>`, so a declaration initializer or IF condition may carry its
// own FROM/WHERE (026_overwrite_contrecord's DO block). Such a source is read
// as the scalar subquery `(SELECT <expr>)`; a plain expression stays as is.
// M0149-0017.
func TestPLExprWithFromClause(t *testing.T) {
	e, err := parsePLExpr("setting::int FROM pg_settings WHERE name = 'wal_segment_size'")
	if err != nil {
		t.Fatalf("FROM-clause expression: %v", err)
	}
	if _, ok := e.(*parser.SubqueryExpr); !ok {
		t.Fatalf("FROM-clause expression parsed as %T, want a scalar subquery", e)
	}
	if e, err := parsePLExpr("remain < 2 * 8192"); err != nil {
		t.Fatalf("plain expression: %v", err)
	} else if _, ok := e.(*parser.SubqueryExpr); ok {
		t.Fatalf("plain expression wrapped in a subquery")
	}
	if _, err := parsePLExpr("1 +"); err == nil {
		t.Fatalf("broken expression accepted")
	}
	body := `
DECLARE
    wal_segsize int := setting::int FROM pg_settings WHERE name = 'wal_segment_size';
BEGIN
    IF wal_segsize < 2 * setting::int from pg_settings where name = 'block_size' THEN
        RETURN;
    END IF;
END`
	if _, err := Parse(body); err != nil {
		t.Fatalf("DO body with SELECT-shaped expressions: %v", err)
	}
}
