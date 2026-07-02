package server

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/config"
)

// capturingHandler is a minimal slog.Handler that records every emitted
// record's message and attrs, so logDuration/logStatement tests can assert
// on actual log output instead of only the pure decision helpers.
type capturingHandler struct {
	records *[]capturedRecord
}

type capturedRecord struct {
	msg   string
	attrs map[string]any
}

func newCapturingLogger() (*slog.Logger, *[]capturedRecord) {
	records := &[]capturedRecord{}
	return slog.New(&capturingHandler{records: records}), records
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	*h.records = append(*h.records, capturedRecord{msg: r.Message, attrs: attrs})
	return nil
}
func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(name string) slog.Handler       { return h }

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

// TestSessionLogMinDurationStatement pins the -1 disabled sentinel
// (matching the `log_min_duration_statement` GUC's own BootVal) versus 0
// ("log every statement") and a positive threshold, plus the nil-session and
// unparseable-value fallbacks.
func TestSessionLogMinDurationStatement(t *testing.T) {
	if got := sessionLogMinDurationStatement(nil); got != -1 {
		t.Errorf("nil session: got %d, want -1", got)
	}
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if got := sessionLogMinDurationStatement(sess); got != -1 {
		t.Errorf("boot default: got %d, want -1", got)
	}
	if err := sess.Set("log_min_duration_statement", "0", false); err != nil {
		t.Fatalf("Set(0): %v", err)
	}
	if got := sessionLogMinDurationStatement(sess); got != 0 {
		t.Errorf("after SET ...=0: got %d, want 0", got)
	}
	if err := sess.Set("log_min_duration_statement", "500", false); err != nil {
		t.Fatalf("Set(500): %v", err)
	}
	if got := sessionLogMinDurationStatement(sess); got != 500 {
		t.Errorf("after SET ...=500: got %d, want 500", got)
	}
	if err := sess.Set("log_min_duration_statement", "-1", false); err != nil {
		t.Fatalf("Set(-1): %v", err)
	}
	if got := sessionLogMinDurationStatement(sess); got != -1 {
		t.Errorf("after SET ...=-1: got %d, want -1", got)
	}
}

// TestExceedsLogMinDuration pins check_log_duration's threshold comparison:
// negative disables, zero always logs, positive is a >= threshold.
func TestExceedsLogMinDuration(t *testing.T) {
	cases := []struct {
		elapsedMs float64
		threshold int64
		want      bool
	}{
		{elapsedMs: 0, threshold: -1, want: false},
		{elapsedMs: 9999, threshold: -1, want: false},
		{elapsedMs: 0, threshold: 0, want: true},
		{elapsedMs: 12345, threshold: 0, want: true},
		{elapsedMs: 499, threshold: 500, want: false},
		{elapsedMs: 500, threshold: 500, want: true},
		{elapsedMs: 501, threshold: 500, want: true},
	}
	for _, c := range cases {
		if got := exceedsLogMinDuration(c.elapsedMs, c.threshold); got != c.want {
			t.Errorf("exceedsLogMinDuration(%v, %d) = %v, want %v", c.elapsedMs, c.threshold, got, c.want)
		}
	}
}

// TestLogDurationEmitsCombinedOrBareLine pins check_log_duration's
// `was_logged` split: when logStatement already emitted the statement text
// (wasLogged=true), logDuration must NOT repeat it (bare "duration" line);
// when it did not (wasLogged=false, e.g. log_statement=none but
// log_min_duration_statement caught it), logDuration must include the
// statement text so it isn't lost entirely.
func TestLogDurationEmitsCombinedOrBareLine(t *testing.T) {
	logger, records := newCapturingLogger()
	s := &Server{cfg: Config{Logger: logger}}
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if err := sess.Set("log_min_duration_statement", "0", false); err != nil {
		t.Fatalf("Set: %v", err)
	}

	*records = nil
	s.logDuration(time.Now(), true, "simple", "SELECT 1", sess, nil)
	if len(*records) != 1 {
		t.Fatalf("wasLogged=true: got %d records, want 1", len(*records))
	}
	if _, ok := (*records)[0].attrs["statement"]; ok {
		t.Errorf("wasLogged=true: duration line unexpectedly repeats the statement text: %+v", (*records)[0])
	}

	*records = nil
	s.logDuration(time.Now(), false, "simple", "SELECT 1", sess, nil)
	if len(*records) != 1 {
		t.Fatalf("wasLogged=false: got %d records, want 1", len(*records))
	}
	if got := (*records)[0].attrs["statement"]; got != "SELECT 1" {
		t.Errorf("wasLogged=false: statement attr = %v, want %q", got, "SELECT 1")
	}

	// Disabled (boot default -1): no record at all.
	disabledSess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	*records = nil
	s.logDuration(time.Now(), false, "simple", "SELECT 1", disabledSess, nil)
	if len(*records) != 0 {
		t.Errorf("disabled threshold: got %d records, want 0", len(*records))
	}

	// Below threshold: no record.
	thresholdSess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if err := sess.Set("log_min_duration_statement", "0", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := thresholdSess.Set("log_min_duration_statement", "60000", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	*records = nil
	s.logDuration(time.Now(), false, "simple", "SELECT 1", thresholdSess, nil)
	if len(*records) != 0 {
		t.Errorf("below threshold: got %d records, want 0", len(*records))
	}
}

// TestLogStatementReturnsWasLogged pins logStatement's bool return, which
// logDuration relies on to decide the combined-vs-bare line split.
func TestLogStatementReturnsWasLogged(t *testing.T) {
	logger, _ := newCapturingLogger()
	s := &Server{cfg: Config{Logger: logger}, logStmtLevel: logStmtNone}
	sess := config.NewSessionRegistry(config.BuildDefaultRegistry())
	if got := s.logStatement("simple", "SELECT 1", sess, nil); got != false {
		t.Errorf("log_statement=none: wasLogged = %v, want false", got)
	}
	if err := sess.Set("log_statement", "all", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.logStatement("simple", "SELECT 1", sess, nil); got != true {
		t.Errorf("log_statement=all: wasLogged = %v, want true", got)
	}
}

func TestFormatLogLinePrefix(t *testing.T) {
	fixedTime := time.Date(2026, 7, 3, 8, 19, 45, 123_000_000, time.UTC)
	base := logLineFields{Time: fixedTime, PID: 4242, User: "wp_user", Database: "wordpress", AppName: "wp4pg", Xid: 99}

	tests := []struct {
		name   string
		format string
		f      logLineFields
		want   string
	}{
		{"empty format", "", base, ""},
		{"default PG bootval", "%m [%p] ", base, "2026-07-03 08:19:45.123 UTC [4242] "},
		{"literal percent", "100%% done", base, "100% done"},
		{"user and database", "%u@%d> ", base, "wp_user@wordpress> "},
		{"application name", "[%a]", base, "[wp4pg]"},
		{"xid", "tx=%x", base, "tx=99"},
		{"xid zero", "tx=%x", logLineFields{}, "tx=0"},
		{"unknown user/database/appname", "%u %d %a", logLineFields{}, "[unknown] [unknown] [unknown]"},
		{"unrecognised escape dropped", "a%zb", base, "ab"},
		{"trailing percent dropped", "abc%", base, "abc"},
		{"positive padding right-justifies", "[%10p]", base, "[      4242]"},
		{"negative padding left-justifies", "[%-10p]", base, "[4242      ]"},
		{"literal text only", "no escapes here", base, "no escapes here"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatLogLinePrefix(tt.format, tt.f); got != tt.want {
				t.Errorf("formatLogLinePrefix(%q) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

// TestServerLogLinePrefix verifies logStatement attaches a "prefix"
// attribute expanded from the registry's log_line_prefix value against the
// call's connTx/session context, that a custom prefix format is honoured,
// and that it is omitted entirely when the GUC is set to empty
// (PostgreSQL's own "no prefix" behaviour). The GUC's own BootVal ("%m
// [%p] ", mirroring upstream) means a fresh registry already attaches one.
func TestServerLogLinePrefix(t *testing.T) {
	logger, records := newCapturingLogger()
	reg := config.BuildDefaultRegistry()
	s := &Server{cfg: Config{Logger: logger, Registry: reg}, logStmtLevel: logStmtAll}
	sess := config.NewSessionRegistry(reg)
	connTx := &connTxState{DBName: "wordpress", BackendPID: 777}

	if err := reg.Set("log_line_prefix", "[%p %d] ", config.SourceConfigFile); err != nil {
		t.Fatalf("Set: %v", err)
	}
	*records = nil
	s.logStatement("simple", "SELECT 1", sess, connTx)
	if len(*records) != 1 {
		t.Fatalf("got %d records, want 1", len(*records))
	}
	if got, want := (*records)[0].attrs["prefix"], "[777 wordpress] "; got != want {
		t.Errorf("prefix attr = %v, want %q", got, want)
	}

	// log_line_prefix is not a ContextSigHup default a client can SET, but a
	// config-file value of "" (upstream's own "no prefix" spelling) must
	// suppress the attr entirely rather than attaching an empty string.
	if err := reg.Set("log_line_prefix", "", config.SourceConfigFile); err != nil {
		t.Fatalf("Set: %v", err)
	}
	*records = nil
	s.logStatement("simple", "SELECT 1", sess, connTx)
	if len(*records) != 1 {
		t.Fatalf("got %d records, want 1", len(*records))
	}
	if _, ok := (*records)[0].attrs["prefix"]; ok {
		t.Errorf("empty log_line_prefix: unexpectedly attached a prefix attr: %+v", (*records)[0])
	}
}
