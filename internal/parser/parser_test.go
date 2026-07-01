package parser

import "testing"

// TestParseTransactionControl: BEGIN/COMMIT/ROLLBACK and their aliases
// (END/ABORT) plus the optional WORK/TRANSACTION keyword all produce
// the expected statement node.
func TestParseTransactionControl(t *testing.T) {
	cases := []struct {
		in   string
		kind string
	}{
		{"BEGIN", "begin"},
		{"begin work", "begin"},
		{"BEGIN TRANSACTION", "begin"},
		{"COMMIT", "commit"},
		{"end", "commit"},
		{"COMMIT WORK", "commit"},
		{"ROLLBACK", "rollback"},
		{"abort", "rollback"},
		{"ROLLBACK TRANSACTION;", "rollback"},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q): %d stmts, want 1", c.in, len(stmts))
		}
		var got string
		switch stmts[0].(type) {
		case *BeginStmt:
			got = "begin"
		case *CommitStmt:
			got = "commit"
		case *RollbackStmt:
			got = "rollback"
		default:
			t.Fatalf("Parse(%q): unexpected node %T", c.in, stmts[0])
		}
		if got != c.kind {
			t.Errorf("Parse(%q)=%s want %s", c.in, got, c.kind)
		}
	}
}

// TestParseVacuum covers bare VACUUM, options, and target lists.
// VACUUM ANALYZE pgbench_accounts is the exact form pgbench -i emits.
func TestParseVacuum(t *testing.T) {
	tests := []struct {
		in       string
		analyze  bool
		verbose  bool
		nTargets int
		first    string
	}{
		{"VACUUM", false, false, 0, ""},
		{"VACUUM VERBOSE", false, true, 0, ""},
		{"VACUUM ANALYZE", true, false, 0, ""},
		{"VACUUM ANALYZE pgbench_accounts", true, false, 1, "pgbench_accounts"},
		{"VACUUM verbose ANALYSE pgbench_accounts, pgbench_branches", true, true, 2, "pgbench_accounts"},
	}
	for _, c := range tests {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		v, ok := stmts[0].(*VacuumStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T want *VacuumStmt", c.in, stmts[0])
		}
		if v.Analyze != c.analyze || v.Verbose != c.verbose {
			t.Errorf("Parse(%q): Analyze=%v Verbose=%v want Analyze=%v Verbose=%v", c.in, v.Analyze, v.Verbose, c.analyze, c.verbose)
		}
		if len(v.Targets) != c.nTargets {
			t.Errorf("Parse(%q): %d targets want %d", c.in, len(v.Targets), c.nTargets)
		}
		if c.nTargets > 0 && v.Targets[0].Name != c.first {
			t.Errorf("Parse(%q): first target=%q want %q", c.in, v.Targets[0].Name, c.first)
		}
	}
}

// TestParseVacuumTruncateOption covers the parenthesised TRUNCATE option.
// TRUNCATE lexes as the unreserved keyword KwTruncate (it also leads TRUNCATE
// TABLE), so the option list must accept the keyword token — not just an
// identifier — otherwise VACUUM (TRUNCATE false) is wrongly rejected with
// "unrecognised VACUUM option". Regression for the index-only-bitmapscan
// isolation spec, whose s2_vacuum step issues VACUUM (TRUNCATE false).
func TestParseVacuumTruncateOption(t *testing.T) {
	tests := []struct {
		in         string
		noTruncate bool
	}{
		{"VACUUM (TRUNCATE false) t", true},
		{"VACUUM (TRUNCATE FALSE) t", true},
		{"VACUUM (TRUNCATE true) t", false},
		{"VACUUM (TRUNCATE) t", false},
		{"VACUUM (VERBOSE, TRUNCATE false) t", true},
		{"VACUUM (TRUNCATE false, ANALYZE) t", true},
	}
	for _, c := range tests {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		v, ok := stmts[0].(*VacuumStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T want *VacuumStmt", c.in, stmts[0])
		}
		if v.NoTruncate != c.noTruncate {
			t.Errorf("Parse(%q): NoTruncate=%v want %v", c.in, v.NoTruncate, c.noTruncate)
		}
	}
}

// TestParseAnalyze is the analyze-only variant (subset of VACUUM
// options).
func TestParseAnalyze(t *testing.T) {
	stmts, err := Parse("ANALYZE VERBOSE public.pgbench_accounts")
	if err != nil {
		t.Fatal(err)
	}
	a, ok := stmts[0].(*AnalyzeStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if !a.Verbose || len(a.Targets) != 1 {
		t.Fatalf("unexpected analyze: %+v", a)
	}
	if a.Targets[0].Schema != "public" || a.Targets[0].Name != "pgbench_accounts" {
		t.Fatalf("target=%+v", a.Targets[0])
	}
}

// TestParseCheckpoint locks the bare CHECKPOINT verb. Upstream
// only accepts CHECKPOINT (no parenthesised options) — see
// postgres/src/backend/parser/gram.y CheckPointStmt rule.
func TestParseCheckpoint(t *testing.T) {
	stmts, err := Parse("CHECKPOINT")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stmts[0].(*CheckpointStmt); !ok {
		t.Fatalf("got %T", stmts[0])
	}
}

// TestParseShowSetReset locks down the GUC verbs the parser carves out
// from the simple-query path.
func TestParseShowSetReset(t *testing.T) {
	t.Run("show", func(t *testing.T) {
		stmts, err := Parse("SHOW server_version")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ShowStmt)
		if s.All || s.Name != "server_version" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("show all", func(t *testing.T) {
		stmts, err := Parse("SHOW ALL")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ShowStmt)
		if !s.All {
			t.Fatalf("not All: %+v", s)
		}
	})
	t.Run("set =", func(t *testing.T) {
		stmts, err := Parse("SET work_mem = '64MB'")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Local || s.Name != "work_mem" || s.Value != "64MB" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set float value", func(t *testing.T) {
		// Real-typed GUCs (cost params) are SET with fractional literals,
		// e.g. the index-only-scan isolation spec's `SET LOCAL seq_page_cost
		// = 0.1`. parseSetValueAtoms must accept a TokenNumericLit.
		stmts, err := Parse("SET LOCAL seq_page_cost = 0.1")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if !s.Local || s.Name != "seq_page_cost" || s.Value != "0.1" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set negative float value", func(t *testing.T) {
		stmts, err := Parse("SET geqo_seed = -0.5")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Name != "geqo_seed" || s.Value != "-0.5" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set local to default", func(t *testing.T) {
		stmts, err := Parse("SET LOCAL search_path TO DEFAULT")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if !s.Local || !s.Default || s.Name != "search_path" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set list", func(t *testing.T) {
		stmts, err := Parse("SET search_path TO public, pg_catalog")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Value != "public, pg_catalog" {
			t.Fatalf("Value=%q", s.Value)
		}
	})
	t.Run("set role name", func(t *testing.T) {
		// M0119-0004: SET ROLE used to discard the role name entirely
		// (always parsed as Default=true), so the executor/extended-protocol
		// paths had no role name to track for privilege checks.
		stmts, err := Parse("SET ROLE some_role")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Default || s.Name != "role" || s.Value != "some_role" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set role default", func(t *testing.T) {
		stmts, err := Parse("SET ROLE DEFAULT")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if !s.Default || s.Name != "role" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set role none", func(t *testing.T) {
		stmts, err := Parse("SET ROLE NONE")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Default || s.Name != "role" || s.Value != "none" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("reset role", func(t *testing.T) {
		stmts, err := Parse("RESET ROLE")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ResetStmt)
		if s.All || s.Name != "role" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("reset", func(t *testing.T) {
		stmts, err := Parse("RESET work_mem")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ResetStmt)
		if s.All || s.Name != "work_mem" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("reset all", func(t *testing.T) {
		stmts, err := Parse("RESET ALL")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ResetStmt)
		if !s.All {
			t.Fatalf("not All")
		}
	})
}

// TestParseMultiStatement verifies semicolon-separated statements
// produce one Stmt each, including a tolerated empty trailing
// statement.
func TestParseMultiStatement(t *testing.T) {
	stmts, err := Parse("BEGIN; COMMIT;;")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("got %d stmts", len(stmts))
	}
	if _, ok := stmts[0].(*BeginStmt); !ok {
		t.Errorf("[0]=%T", stmts[0])
	}
	if _, ok := stmts[1].(*CommitStmt); !ok {
		t.Errorf("[1]=%T", stmts[1])
	}
}

// TestParseSyntaxError pins the SyntaxError type and message form.
func TestParseSyntaxError(t *testing.T) {
	_, err := Parse("VACUUM ;;ANALYZE ,") // ANALYZE with no target after comma
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*SyntaxError); !ok {
		t.Errorf("err type=%T (%v)", err, err)
	}
}
