package parser

import (
	"strings"
	"testing"
)

func parseExplainStmt(t *testing.T, sql string) *ExplainStmt {
	t.Helper()
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d stmts", sql, len(stmts))
	}
	es, ok := stmts[0].(*ExplainStmt)
	if !ok {
		t.Fatalf("Parse(%q): got %T, want *ExplainStmt", sql, stmts[0])
	}
	return es
}

// TestParseExplainBareUnchanged: regression guard for byte-for-byte
// invariance of the pre-M0018 bare-EXPLAIN form. ExplainOptions
// must be the zero value so callers that don't yet honour it
// behave exactly as before.
func TestParseExplainBareUnchanged(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN SELECT 1")
	if es.Options != (ExplainOptions{}) {
		t.Errorf("Options = %+v, want zero", es.Options)
	}
	if es.Inner == nil {
		t.Error("Inner is nil")
	}
}

// TestParseExplainAnalyzeKeyword: `EXPLAIN ANALYZE SELECT 1`
// sets Analyze=true and leaves the rest at zero.
func TestParseExplainAnalyzeKeyword(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN ANALYZE SELECT 1")
	if !es.Options.Analyze {
		t.Error("Analyze=false, want true")
	}
	if es.Options.Verbose {
		t.Error("Verbose=true, want false")
	}
}

// TestParseExplainVerboseKeyword: VERBOSE alone.
func TestParseExplainVerboseKeyword(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN VERBOSE SELECT 1")
	if !es.Options.Verbose || es.Options.Analyze {
		t.Errorf("got Analyze=%v Verbose=%v", es.Options.Analyze, es.Options.Verbose)
	}
}

// TestParseExplainAnalyzeVerbose: both keywords in either order.
// Pins upstream's `opt_analyze`/`opt_verbose` flexibility.
func TestParseExplainAnalyzeVerbose(t *testing.T) {
	for _, sql := range []string{
		"EXPLAIN ANALYZE VERBOSE SELECT 1",
		"EXPLAIN VERBOSE ANALYZE SELECT 1",
	} {
		es := parseExplainStmt(t, sql)
		if !es.Options.Analyze || !es.Options.Verbose {
			t.Errorf("%q: got Analyze=%v Verbose=%v, want both true",
				sql, es.Options.Analyze, es.Options.Verbose)
		}
	}
}

// TestParseExplainParenAnalyzeBare: `EXPLAIN (ANALYZE) SELECT 1`
// defaults the bool to true (mirrors upstream's defGetBoolean).
func TestParseExplainParenAnalyzeBare(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN (ANALYZE) SELECT 1")
	if !es.Options.Analyze {
		t.Error("Analyze=false, want true (default-true on bool option)")
	}
}

// TestParseExplainParenAllOptions: every bool flag flips correctly
// via the parenthesised form. Pins the option-name dispatch
// table.
func TestParseExplainParenAllOptions(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN (ANALYZE, VERBOSE, COSTS, BUFFERS, SETTINGS, TIMING, SUMMARY) SELECT 1")
	o := es.Options
	if !o.Analyze || !o.Verbose || !o.Costs || !o.Buffers || !o.Settings || !o.Timing || !o.Summary {
		t.Errorf("not all flags true: %+v", o)
	}
}

// TestParseExplainFormatJSON: FORMAT JSON sets the right enum
// value. FORMAT TEXT is the default; pin the JSON path.
func TestParseExplainFormatJSON(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN (FORMAT JSON) SELECT 1")
	if es.Options.Format != ExplainFormatJSON {
		t.Errorf("Format = %v, want JSON", es.Options.Format)
	}
}

// TestParseExplainFormatText pins that explicit FORMAT TEXT
// gives the default value (in case the dispatch is ever flipped).
func TestParseExplainFormatText(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN (FORMAT TEXT) SELECT 1")
	if es.Options.Format != ExplainFormatText {
		t.Errorf("Format = %v, want TEXT", es.Options.Format)
	}
}

// TestParseExplainRejectsUnknownOption: an unrecognised option
// name errors at the option's position. Without this guard,
// silent acceptance would let a typo go unnoticed.
func TestParseExplainRejectsUnknownOption(t *testing.T) {
	_, err := Parse("EXPLAIN (UNKNOWN_OPT) SELECT 1")
	if err == nil {
		t.Fatal("Parse returned nil for unknown option")
	}
	if !strings.Contains(err.Error(), "unknown EXPLAIN option") {
		t.Errorf("err = %v; want mention of 'unknown EXPLAIN option'", err)
	}
}

// TestParseExplainRejectsEmptyOptionList: `EXPLAIN ()` is a
// syntax error in upstream too.
func TestParseExplainRejectsEmptyOptionList(t *testing.T) {
	_, err := Parse("EXPLAIN () SELECT 1")
	if err == nil {
		t.Fatal("Parse returned nil for empty option list")
	}
}

// TestParseExplainRejectsBadFormat: FORMAT XML is upstream-valid
// but goopg doesn't support it; should error cleanly.
func TestParseExplainRejectsBadFormat(t *testing.T) {
	_, err := Parse("EXPLAIN (FORMAT XML) SELECT 1")
	if err == nil {
		t.Fatal("Parse returned nil for FORMAT XML")
	}
	if !strings.Contains(err.Error(), "unsupported FORMAT") {
		t.Errorf("err = %v; want mention of unsupported FORMAT", err)
	}
}

// TestParseExplainBoolValueForms: ON/OFF/TRUE/FALSE/1/0 all
// parse as the right bool. Without this, an operator using
// upstream-shaped `EXPLAIN (ANALYZE off)` would get incorrect
// behaviour.
func TestParseExplainBoolValueForms(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"EXPLAIN (ANALYZE on) SELECT 1", true},
		{"EXPLAIN (ANALYZE off) SELECT 1", false},
		{"EXPLAIN (ANALYZE true) SELECT 1", true},
		{"EXPLAIN (ANALYZE false) SELECT 1", false},
		{"EXPLAIN (ANALYZE 1) SELECT 1", true},
		{"EXPLAIN (ANALYZE 0) SELECT 1", false},
	}
	for _, c := range cases {
		es := parseExplainStmt(t, c.sql)
		if es.Options.Analyze != c.want {
			t.Errorf("%q: Analyze=%v, want %v", c.sql, es.Options.Analyze, c.want)
		}
	}
}

// TestParseExplainMultipleOptionsMixed: mix bool-with-value and
// bare options in one list.
func TestParseExplainMultipleOptionsMixed(t *testing.T) {
	es := parseExplainStmt(t, "EXPLAIN (ANALYZE, BUFFERS off, FORMAT JSON) SELECT 1")
	if !es.Options.Analyze {
		t.Error("Analyze should be true (bare default)")
	}
	if es.Options.Buffers {
		t.Error("Buffers should be false (explicit off)")
	}
	if es.Options.Format != ExplainFormatJSON {
		t.Error("Format should be JSON")
	}
}
