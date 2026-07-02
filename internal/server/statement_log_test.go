package server

import (
	"testing"

	"github.com/goopg/goopg/internal/config"
)

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

// TestEffectiveLogStatementLevel pins root-0023's deferred follow-up: a
// client `SET log_statement = 'all'` must take effect even when the server's
// GOOPG_LOG_STATEMENT env-var toggle is quieter (or unset), and the env var
// must still work when no session GUC override is present — the effective
// level is whichever of the two is louder, never quieter than either.
func TestEffectiveLogStatementLevel(t *testing.T) {
	cases := []struct {
		name    string
		envLvl  logStatementLevel
		sessSet string // "" ⇒ leave the session GUC at its boot default ("none")
		want    logStatementLevel
	}{
		{name: "env-none-sess-unset", envLvl: logStmtNone, sessSet: "", want: logStmtNone},
		{name: "env-none-sess-all", envLvl: logStmtNone, sessSet: "all", want: logStmtAll},
		{name: "env-all-sess-unset", envLvl: logStmtAll, sessSet: "", want: logStmtAll},
		{name: "env-all-sess-none", envLvl: logStmtAll, sessSet: "none", want: logStmtAll},
		{name: "env-ddl-sess-mod", envLvl: logStmtDDL, sessSet: "mod", want: logStmtMod},
		{name: "env-mod-sess-ddl", envLvl: logStmtMod, sessSet: "ddl", want: logStmtMod},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
			if tc.sessSet != "" {
				if err := sess.Set("log_statement", tc.sessSet, false); err != nil {
					t.Fatalf("Set(log_statement=%q): %v", tc.sessSet, err)
				}
			}
			s := &Server{logStmtLevel: tc.envLvl}
			if got := s.effectiveLogStatementLevel(sess); got != tc.want {
				t.Errorf("effectiveLogStatementLevel(env=%d, sess=%q) = %d, want %d",
					tc.envLvl, tc.sessSet, got, tc.want)
			}
		})
	}
	// nil session: env level alone applies.
	s := &Server{logStmtLevel: logStmtDDL}
	if got := s.effectiveLogStatementLevel(nil); got != logStmtDDL {
		t.Errorf("effectiveLogStatementLevel(nil session) = %d, want %d", got, logStmtDDL)
	}
}
