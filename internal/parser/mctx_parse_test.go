package parser_test

import (
	"testing"

	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/parser"
)

// TestParseMctxPath verifies that Parse with a non-nil *mctx.Context
// returns the same AST as the pool path (mc == nil). M0107-0003 Phase C.3.
func TestParseMctxPath(t *testing.T) {
	queries := []string{
		"SELECT 1",
		"SELECT a, b FROM t WHERE a = 1",
		"INSERT INTO t VALUES (1, 'hello')",
		"UPDATE t SET a = 2 WHERE b = 3",
		"DELETE FROM t WHERE id = 42",
	}
	for _, q := range queries {
		q := q
		t.Run(q, func(t *testing.T) {
			// Pool path (mc == nil).
			want, err := parser.Parse(q)
			if err != nil {
				t.Fatalf("pool path parse error: %v", err)
			}
			// mctx path.
			mc := mctx.Acquire(nil, mctx.KindStmt)
			defer mc.Release()
			got, err := parser.Parse(q, mc)
			if err != nil {
				t.Fatalf("mctx path parse error: %v", err)
			}
			if len(want) != len(got) {
				t.Fatalf("stmt count mismatch: want %d got %d", len(want), len(got))
			}
		})
	}
}

// TestParseExprMctxPath verifies ParseExpr with mctx. M0107-0003 Phase C.3.
func TestParseExprMctxPath(t *testing.T) {
	exprs := []string{
		"1 + 2",
		"a AND b",
		"NOT x",
		"CASE WHEN 1=1 THEN 'yes' END",
	}
	for _, e := range exprs {
		e := e
		t.Run(e, func(t *testing.T) {
			// Pool path.
			want, err := parser.ParseExpr(e)
			if err != nil {
				t.Fatalf("pool path: %v", err)
			}
			// mctx path.
			mc := mctx.Acquire(nil, mctx.KindExpr)
			defer mc.Release()
			got, err := parser.ParseExpr(e, mc)
			if err != nil {
				t.Fatalf("mctx path: %v", err)
			}
			if want == nil || got == nil {
				t.Fatal("nil expr returned")
			}
		})
	}
}
