package parser

import (
	"testing"

)

// TestSplitStatements pins the top-level ';' splitter: dollar-quoted bodies
// and quoted strings are opaque single tokens by the time we see them, so a
// ';' inside them can never split; trailing semicolons don't produce empty
// fragments.
func TestSplitStatements(t *testing.T) {
	toks, err := Lex(`SELECT 1; SELECT 2;`)
	if err != nil {
		t.Fatal(err)
	}
	frags := SplitStatements(toks)
	if len(frags) != 2 {
		t.Fatalf("fragments = %d, want 2 (trailing ; produces no empty fragment)", len(frags))
	}

	toks, err = Lex(`SELECT $$a;b$$; SELECT 'x;y'`)
	if err != nil {
		t.Fatal(err)
	}
	frags = SplitStatements(toks)
	if len(frags) != 2 {
		t.Fatalf("dollar-quote/semicolon fragments = %d, want 2", len(frags))
	}
}


// TestRouteBatchMixesPerFragment pins PER-FRAGMENT routing: each statement in
// a batch goes to the parser that owns it.
//
// This used to pin the opposite — one unrouted fragment declined the ENTIRE
// batch — with the note "until per-fragment mixing lands". It landed, and it
// had to: while the legacy parser still handled every class, dragging a whole
// batch onto it was survivable. Once P7.2 deleted the routed classes' legacy
// arms it became a hard 42601, because the legacy path no longer knows what
// `SET` is. `CREATE COLLATION c (…); SET x = on;` — real DDL from
// wal_pg_waldump_test.go — failed on the SET.
func TestRouteBatchMixesPerFragment(t *testing.T) {
	// Restore, never delete: SELECT is routed by default now, and deleting
	// the entry here un-routed it for every test that ran after this one.
	prev, had := routedStmts["select"]
	defer func() {
		if had {
			routedStmts["select"] = prev
		} else {
			delete(routedStmts, "select")
		}
	}()
	routedStmts["select"] = true

	// Both fragments routed → batch routes to the new parser (since P1.1,
	// SELECT core parses, so this is a full success path).
	toks, _ := Lex("SELECT 1; SELECT 2;")
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

	// Mixed batch → BOTH halves parse, each on its own path. The unrouted
	// half has to be a class the grammar genuinely does not cover AND the
	// retained compat scanners still accept: CREATE COLLATION is one (DISCARD
	// used to serve here and stopped working as a probe the moment P5.3
	// routed it; CREATE ROLE is no good either — postmaster intercepts role
	// DDL above Parse, so the parser rejects it outright).
	toks, _ = Lex("SELECT 1; CREATE COLLATION c (locale = 'C'); SET x = on;")
	stmts, handled, rerr := routeBatch("SELECT 1; CREATE COLLATION c (locale = 'C'); SET x = on;", toks)
	if !handled {
		t.Fatal("mixed batch declined; per-fragment routing should handle it")
	}
	if rerr != nil {
		t.Fatalf("mixed batch: %v", rerr)
	}
	if len(stmts) != 3 {
		t.Fatalf("mixed batch produced %d statements, want 3", len(stmts))
	}
	// The SET is the one that regressed: it is ROUTED, and before per-fragment
	// mixing the unrouted CREATE COLLATION in front of it sent it to a legacy
	// path that no longer has a SET arm.
	if _, ok := stmts[2].(*SetStmt); !ok {
		t.Fatalf("third statement is %T, want *SetStmt", stmts[2])
	}
}

// TestQuotedIdentIsNotKeyword pins the ident-routing guard: a QUOTED
// "select" is an identifier, never the keyword.
func TestQuotedIdentIsNotKeyword(t *testing.T) {
	// Restore, never delete: SELECT is routed by default now, and deleting
	// the entry here un-routed it for every test that ran after this one.
	prev, had := routedStmts["select"]
	defer func() {
		if had {
			routedStmts["select"] = prev
		} else {
			delete(routedStmts, "select")
		}
	}()
	routedStmts["select"] = true
	toks, _ := Lex(`"select" 1`) // nonsense SQL; only routing matters
	if fragmentRouted(SplitStatements(toks)[0]) {
		t.Fatal(`quoted "select" routed as keyword`)
	}
}
