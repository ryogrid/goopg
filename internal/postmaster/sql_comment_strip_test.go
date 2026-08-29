package postmaster

import "testing"

// M0134-0159. PG's lexer folds `--…` and `/*…*/` into {whitespace} (scan.l:213-215)
// and the {whitespace} rule emits no token (scan.l:443), so a comment can appear
// anywhere a space can — including in front of the very first keyword.
//
// goopg classifies the statement classes the parser deliberately does not carry
// (role DDL, database DDL, the CREATE SCHEMA header — the goyacc playbook §12
// hand-written-scanner list) by PREFIX-matching normalizeCompatSQL's output. Before
// this fix the comment survived normalization, so `-- x\nCREATE ROLE r` normalized
// to `-- x create role r`, no `create role ` prefix matched, and the statement fell
// through to the parser, which reports `syntax error … after CREATE (got role)`.
// Every commented SQL script — i.e. essentially every real one — therefore could not
// create, alter or drop a role or a database.
//
// These guards pin the lexical contract stripSQLComments implements: a comment
// becomes one space, and a comment INTRODUCER inside a literal is not a comment.
func TestStripSQLComments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no comment is returned unchanged", "SELECT 1", "SELECT 1"},
		{"leading line comment", "-- hi\nCREATE ROLE r", " \nCREATE ROLE r"},
		{"leading block comment", "/* hi */ CREATE ROLE r", "  CREATE ROLE r"},
		{"trailing line comment before semicolon", "CREATE ROLE r -- c\n;", "CREATE ROLE r  \n;"},
		{"interior block comment", "CREATE/* c */ROLE r", "CREATE ROLE r"},

		// scan.l's <xc> state carries an xcdepth counter (scan.l:455-467): PG
		// block comments NEST, so the FIRST */ does not necessarily end them.
		{"nested block comment", "/* a /* b */ c */ SELECT 1", "  SELECT 1"},

		// A comment introducer inside a literal is literal text.
		{"double dash inside string literal", "SELECT 'a--b'", "SELECT 'a--b'"},
		{"slash star inside string literal", "SELECT 'a/*b'", "SELECT 'a/*b'"},
		{"doubled quote keeps the literal open", "SELECT 'a''--b'", "SELECT 'a''--b'"},
		{"double dash inside quoted identifier", `SELECT "a--b"`, `SELECT "a--b"`},
		{"double dash inside dollar quote", "DO $$ -- x\nBEGIN END $$", "DO $$ -- x\nBEGIN END $$"},
		{"double dash inside tagged dollar quote", "DO $tag$ /* x */ $tag$", "DO $tag$ /* x */ $tag$"},

		// E'' escape strings: a backslash escapes the next byte, so the quote in
		// `\'` does NOT close the literal and the `--` after it is still literal.
		{"escaped quote in E string", `SELECT E'a\'--b'`, `SELECT E'a\'--b'`},
		// …but a bare identifier ending in "e" before a quote is not an E string.
		{"identifier ending in e is not an escape string", `SELECT type'a\'`, `SELECT type'a\'`},

		// Malformed input must stay malformed — never rewrite a broken statement
		// into one the prefix matcher would accept.
		{"unterminated block comment copied through", "SELECT 1 /* x", "SELECT 1 /* x"},
		{"unterminated string copied through", "SELECT 'a-- b", "SELECT 'a-- b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripSQLComments(tc.in); got != tc.want {
				t.Errorf("stripSQLComments(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeCompatSQLSeesCommentedDDL is the regression guard for the bug
// itself: the compat intercepts prefix-match on normalizeCompatSQL, so a
// commented statement must normalize to the same text the bare one does.
func TestNormalizeCompatSQLSeesCommentedDDL(t *testing.T) {
	cases := []struct {
		name      string
		commented string
		want      string
	}{
		// The exact shape regproc.sql opens with.
		{"regproc.sql create role", "/* If objects exist, return oids */\nCREATE ROLE regress_regrole_test;", "create role regress_regrole_test"},
		{"line comment create user", "-- setup\nCREATE USER u1;", "create user u1"},
		{"line comment drop role", "-- teardown\nDROP ROLE regress_regrole_test;", "drop role regress_regrole_test"},
		{"line comment alter role", "-- tweak\nALTER ROLE u1 NOSUPERUSER;", "alter role u1 nosuperuser"},
		{"block comment create database", "/* c */CREATE DATABASE d1;", "create database d1"},
		// A trailing comment used to hide the ';' from the semicolon trim.
		{"trailing comment after semicolon", "CREATE ROLE r; -- done", "create role r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCompatSQL(tc.commented); got != tc.want {
				t.Errorf("normalizeCompatSQL(%q)\n got %q\nwant %q", tc.commented, got, tc.want)
			}
		})
	}
}

// TestSplitLeadingRoleDDLWithComment covers the multi-statement batch seam: a
// batch whose first statement is commented role DDL must still be peeled off and
// handled, not swallowed into the parser with the rest of the batch (M0118-0008's
// splitLeadingRoleDDL, which classifies via normalizeCompatSQL too).
func TestSplitLeadingRoleDDLWithComment(t *testing.T) {
	sql := "-- make the role first\nCREATE ROLE r1;\nCREATE TABLE t (a int);"
	first, rest, ok := splitLeadingRoleDDL(sql)
	if !ok {
		t.Fatalf("splitLeadingRoleDDL did not recognise commented leading role DDL: %q", sql)
	}
	if want := "-- make the role first\nCREATE ROLE r1"; first != want {
		t.Errorf("first = %q, want %q", first, want)
	}
	if want := "CREATE TABLE t (a int);"; rest != want {
		t.Errorf("rest = %q, want %q", rest, want)
	}
}
