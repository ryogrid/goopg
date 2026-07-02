package server

import "testing"

func TestParseLogStatementLevel(t *testing.T) {
	cases := []struct {
		in    string
		want  logStatementLevel
		valid bool
	}{
		{"", logStmtNone, true},
		{"none", logStmtNone, true},
		{"NONE", logStmtNone, true},
		{" ddl ", logStmtDDL, true},
		{"Mod", logStmtMod, true},
		{"all", logStmtAll, true},
		{"ALL", logStmtAll, true},
		{"verbose", logStmtNone, false},
		{"1", logStmtNone, false},
	}
	for _, c := range cases {
		got, ok := parseLogStatementLevel(c.in)
		if got != c.want || ok != c.valid {
			t.Errorf("parseLogStatementLevel(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.valid)
		}
	}
}

func TestLeadingKeyword(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":                         "SELECT",
		"  insert into t values (1)":        "INSERT",
		"UPDATE\n\tt SET x=1":               "UPDATE",
		"-- a comment\nDELETE FROM t":       "DELETE",
		"/* block */ CREATE TABLE t(x int)": "CREATE",
		"/* multi\nline */\nGRANT ALL":      "GRANT",
		"begin(":                            "BEGIN",
		"":                                  "",
		"-- only a comment":                 "",
	}
	for in, want := range cases {
		if got := leadingKeyword(in); got != want {
			t.Errorf("leadingKeyword(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogStatementLevelShouldLog(t *testing.T) {
	// (level, keyword) -> logged?
	type tc struct {
		lvl logStatementLevel
		kw  string
		exp bool
	}
	cases := []tc{
		{logStmtNone, "SELECT", false},
		{logStmtNone, "INSERT", false},
		{logStmtAll, "SELECT", true},
		{logStmtAll, "INSERT", true},
		{logStmtDDL, "CREATE", true},
		{logStmtDDL, "INSERT", false},
		{logStmtDDL, "SELECT", false},
		{logStmtMod, "INSERT", true},
		{logStmtMod, "UPDATE", true},
		{logStmtMod, "DELETE", true},
		{logStmtMod, "COPY", true},
		{logStmtMod, "CREATE", true}, // mod is a superset of ddl
		{logStmtMod, "SELECT", false},
	}
	for _, c := range cases {
		if got := c.lvl.shouldLog(c.kw); got != c.exp {
			t.Errorf("level %d shouldLog(%q) = %v, want %v", c.lvl, c.kw, got, c.exp)
		}
	}
}
