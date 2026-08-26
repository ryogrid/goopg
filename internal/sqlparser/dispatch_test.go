package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestSplitStatements pins the top-level ';' splitter: dollar-quoted bodies
// and quoted strings are opaque single tokens by the time we see them, so a
// ';' inside them can never split; trailing semicolons don't produce empty
// fragments.
func TestSplitStatements(t *testing.T) {
	toks, err := parser.Lex(`SELECT 1; SELECT 2;`)
	if err != nil {
		t.Fatal(err)
	}
	frags := SplitStatements(toks)
	if len(frags) != 2 {
		t.Fatalf("fragments = %d, want 2 (trailing ; produces no empty fragment)", len(frags))
	}

	toks, err = parser.Lex(`SELECT $$a;b$$; SELECT 'x;y'`)
	if err != nil {
		t.Fatal(err)
	}
	frags = SplitStatements(toks)
	if len(frags) != 2 {
		t.Fatalf("dollar-quote/semicolon fragments = %d, want 2", len(frags))
	}
}


// TestRouteBatchWholeBatchOrLegacy pins the whole-batch rule: one routed +
// one unrouted statement must decline the ENTIRE batch (mixed batches stay
// legacy until per-fragment mixing lands).
func TestRouteBatchWholeBatchOrLegacy(t *testing.T) {
	defer delete(routedStmts, "select")
	routedStmts["select"] = true

	// Both fragments routed → batch routes to the new parser (since P1.1,
	// SELECT core parses, so this is a full success path).
	toks, _ := parser.Lex("SELECT 1; SELECT 2;")
	stmts, handled, err := routeBatch("", toks)
	if !handled {
		t.Fatal("all-routed batch declined")
	}
	if err != nil {
		t.Fatalf("all-routed batch: %v", err)
	}
	if len(stmts) != 2 {
		t.Fatalf("all-routed batch produced %d statements, want 2", len(stmts))
	}

	// Mixed batch → declines wholesale.
	toks, _ = parser.Lex("SELECT 1; DISCARD ALL;")
	_, handled, rerr := routeBatch("", toks)
	if handled || rerr != nil {
		t.Fatalf("mixed batch: handled=%v err=%v, want false/nil", handled, rerr)
	}
}

// TestQuotedIdentIsNotKeyword pins the ident-routing guard: a QUOTED
// "select" is an identifier, never the keyword.
func TestQuotedIdentIsNotKeyword(t *testing.T) {
	defer delete(routedStmts, "select")
	routedStmts["select"] = true
	toks, _ := parser.Lex(`"select" 1`) // nonsense SQL; only routing matters
	if fragmentRouted(SplitStatements(toks)[0]) {
		t.Fatal(`quoted "select" routed as keyword`)
	}
}
