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

// TestParseVacuumTargetCols locks down the optional per-relation column list
// on VACUUM/ANALYZE targets (gram.y vacuum_relation: relation_expr
// opt_name_list): ANALYZE t(a, b), VACUUM ANALYZE t(a) and a bare ANALYZE t
// (nil column list) all produce parallel Targets + TargetCols.
func TestParseVacuumTargetCols(t *testing.T) {
	cases := []struct {
		in   string
		stmt string // "analyze" or "vacuum"
		name string
		cols []string // nil for no column list
	}{
		{"analyze t(a, b)", "analyze", "t", []string{"a", "b"}},
		{"vacuum analyze t(a)", "vacuum", "t", []string{"a"}},
		{"ANALYZE t", "analyze", "t", nil},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q): %d stmts, want 1", c.in, len(stmts))
		}
		var targets []ObjectName
		var cols [][]string
		switch s := stmts[0].(type) {
		case *AnalyzeStmt:
			targets, cols = s.Targets, s.TargetCols
		case *VacuumStmt:
			targets, cols = s.Targets, s.TargetCols
		default:
			t.Fatalf("Parse(%q): unexpected node %T", c.in, stmts[0])
		}
		if len(targets) != 1 || targets[0].Name != c.name {
			t.Errorf("Parse(%q): targets=%v want [%q]", c.in, targets, c.name)
		}
		if c.cols == nil {
			if len(cols) != 1 || cols[0] != nil {
				t.Errorf("Parse(%q): TargetCols=%v want [nil]", c.in, cols)
			}
		} else {
			if len(cols) != 1 || len(cols[0]) != len(c.cols) {
				t.Errorf("Parse(%q): TargetCols=%v want %v", c.in, cols, c.cols)
			}
			for i := range c.cols {
				if cols[0][i] != c.cols[i] {
					t.Errorf("Parse(%q): TargetCols[0][%d]=%q want %q", c.in, i, cols[0][i], c.cols[i])
				}
			}
		}
	}
}

// TestParseVacuumTargetColsMissingParen locks the missing-closing-paren error
// on a column list (p.errAtCur("expected ')'")).
func TestParseVacuumTargetColsMissingParen(t *testing.T) {
	if _, err := Parse("analyze t(a, b"); err == nil {
		t.Fatal("Parse(\"analyze t(a, b\") succeeded, want syntax error")
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
	// M0134-0155: SET ROLE is dispatched via PG's generic_set grammar
	// (gram.y:1656-1693 `var_name TO var_list | var_name '=' var_list`), so
	// the TO/= separator spellings must parse identically to the bare form.
	t.Run("set role to name", func(t *testing.T) {
		stmts, err := Parse("SET ROLE TO some_role")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Default || s.Name != "role" || s.Value != "some_role" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set role equals name", func(t *testing.T) {
		stmts, err := Parse("SET ROLE = some_role")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Default || s.Name != "role" || s.Value != "some_role" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set role to default", func(t *testing.T) {
		stmts, err := Parse("SET ROLE TO DEFAULT")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if !s.Default || s.Name != "role" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set session authorization name", func(t *testing.T) {
		stmts, err := Parse("SET SESSION AUTHORIZATION alice")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Default || s.Local || s.Name != "session_authorization" || s.Value != "alice" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	// M0134-0155: SESSION AUTHORIZATION deliberately has NO generic_set
	// grammar upstream (gram.y:1764/:1774 dedicated productions accept only a
	// bare rolename or DEFAULT), so the TO/= spellings are 42601 syntax
	// errors in PG 18.3 (oracle-verified) and must stay rejected here.
	t.Run("set session authorization rejects TO", func(t *testing.T) {
		if _, err := Parse("SET SESSION AUTHORIZATION TO alice"); err == nil {
			t.Fatal("SET SESSION AUTHORIZATION TO alice: want parse error, got none")
		}
	})
	t.Run("set local session authorization name", func(t *testing.T) {
		stmts, err := Parse("SET LOCAL SESSION AUTHORIZATION bob")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Default || !s.Local || s.Name != "session_authorization" || s.Value != "bob" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set local session authorization rejects equals", func(t *testing.T) {
		if _, err := Parse("SET LOCAL SESSION AUTHORIZATION = bob"); err == nil {
			t.Fatal("SET LOCAL SESSION AUTHORIZATION = bob: want parse error, got none")
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
	// M0134-0028a: SET/SHOW/RESET TIME ZONE is PG's dedicated two-word
	// alias for the "timezone" GUC (gram.y:1709,1904,1974), a separate
	// grammar production rather than a generic SET name=value lookup.
	t.Run("set time zone string", func(t *testing.T) {
		stmts, err := Parse("SET TIME ZONE 'America/New_York'")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Name != "timezone" || s.Value != "America/New_York" || s.Default {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set time zone numeric", func(t *testing.T) {
		stmts, err := Parse("SET TIME ZONE -7")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Name != "timezone" || s.Value != "-7" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set time zone default", func(t *testing.T) {
		stmts, err := Parse("SET TIME ZONE DEFAULT")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Name != "timezone" || !s.Default {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set time zone local", func(t *testing.T) {
		stmts, err := Parse("SET TIME ZONE LOCAL")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Name != "timezone" || s.Value != "local" || s.Default {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("show time zone", func(t *testing.T) {
		stmts, err := Parse("SHOW TIME ZONE")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ShowStmt)
		if s.All || s.Name != "timezone" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("reset time zone", func(t *testing.T) {
		stmts, err := Parse("RESET TIME ZONE")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*ResetStmt)
		if s.All || s.Name != "timezone" {
			t.Fatalf("unexpected: %+v", s)
		}
	})
	t.Run("set timezone generic spelling still works", func(t *testing.T) {
		// Regression guard: the ordinary `SET name = value` spelling of the
		// same GUC must keep parsing exactly as before the TIME ZONE
		// intercept was added.
		stmts, err := Parse("SET timezone = 'UTC'")
		if err != nil {
			t.Fatal(err)
		}
		s := stmts[0].(*SetStmt)
		if s.Name != "timezone" || s.Value != "UTC" || s.Default {
			t.Fatalf("unexpected: %+v", s)
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


// TestParseSyntaxErrorAtOrNearWording verifies M0134-0070: the trailing-token
// -after-statement error now goes through errSyntaxAtCur() and echoes PG's
// raw-source "near" text (postgres/src/backend/parser/scan.c
// scanner_yyerror), not goopg's old "expected ';' or end of input (got …)"
// phrasing. Covers the strings.sql illegal-comment-in-continuation fixture
// (TokenStringLit, quoted with embedded '' doubling), a plain trailing
// identifier (TokenIdent, no regression), and a trailing double-quoted
// identifier (TokenQuotedIdent, quoted with embedded "" doubling).
func TestParseSyntaxErrorAtOrNearWording(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "string-lit continuation (strings.sql)",
			in:   "SELECT 'first line'\n' - next line' /* this comment is not allowed here */\n' - third line'\n\tAS \"Illegal comment within continuation\";",
			want: `' - third line'`,
		},
		{
			name: "trailing identifier",
			in:   "SELECT 1 foo bar",
			want: `bar`,
		},
		{
			name: "trailing quoted identifier",
			in:   `SELECT 1 "foo" "bar"`,
			want: `"bar"`,
		},
	}
	for _, c := range cases {
		_, err := Parse(c.in)
		se, ok := err.(*SyntaxError)
		if !ok {
			t.Fatalf("%s: err type=%T (%v), want *SyntaxError", c.name, err, err)
		}
		if se.Message != c.want {
			t.Errorf("%s: Message=%q, want %q", c.name, se.Message, c.want)
		}
	}
}

// TestParseDeclareCursorWithHold verifies M0134-0056: `WITH` is a reserved
// keyword (lexed as TokenKeyword/KwWith, not TokenIdent), so the WITH/WITHOUT
// HOLD clause on DECLARE CURSOR must be matched via acceptKeyword, not
// acceptIdentKeyword — previously the `with` arm could never match and
// `DECLARE ... WITH HOLD ... FOR ...` raised a spurious "expected FOR" error.
func TestParseDeclareCursorWithHold(t *testing.T) {
	cases := []string{
		"DECLARE c1 CURSOR WITH HOLD FOR SELECT 1",
		"DECLARE c2 CURSOR WITHOUT HOLD FOR SELECT 1",
		"DECLARE c3 CURSOR FOR SELECT 1", // no HOLD clause at all, still fine
	}
	for _, in := range cases {
		stmts, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q): %d stmts, want 1", in, len(stmts))
		}
		if _, ok := stmts[0].(*DeclareCursorStmt); !ok {
			t.Fatalf("Parse(%q): %T, want *DeclareCursorStmt", in, stmts[0])
		}
	}
}

// TestParseFetchAbsoluteNegative verifies M0134-0056: `FETCH ABSOLUTE -1` and
// `FETCH RELATIVE -1` must parse (the leading `-` is a separate unary-minus
// token, never fused into the integer literal by the lexer). True
// ABSOLUTE/RELATIVE positioning semantics are out of scope for this brief —
// only the parse-acceptance is verified here.
func TestParseFetchAbsoluteNegative(t *testing.T) {
	cases := []struct {
		in            string
		wantCount     int64
		wantForward   bool
		wantCursorFOO string
	}{
		{"FETCH ABSOLUTE -1 FROM c", -1, true, "c"},
		{"FETCH ABSOLUTE 5 FROM c", 5, true, "c"},
		{"FETCH RELATIVE -2 FROM c", -2, true, "c"},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("Parse(%q): %d stmts, want 1", c.in, len(stmts))
		}
		fs, ok := stmts[0].(*FetchStmt)
		if !ok {
			t.Fatalf("Parse(%q): %T, want *FetchStmt", c.in, stmts[0])
		}
		if fs.Count != c.wantCount {
			t.Errorf("Parse(%q): Count=%d, want %d", c.in, fs.Count, c.wantCount)
		}
		if fs.CursorName != c.wantCursorFOO {
			t.Errorf("Parse(%q): CursorName=%q, want %q", c.in, fs.CursorName, c.wantCursorFOO)
		}
	}
}
