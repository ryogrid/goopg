package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The spellings asserted here are read off upstream's parse_bool_with_len
// (postgres/src/backend/utils/adt/bool.c): the unambiguous prefixes of
// "true"/"false"/"yes"/"no", "on"/"off" (which need at least two characters,
// so a lone "o" is rejected), and "1"/"0" only at length one. boolin wraps it
// with leading/trailing whitespace trimming and raises 22P02
// (ERRCODE_INVALID_TEXT_REPRESENTATION) quoting the ORIGINAL, untrimmed input.
func TestPgBoolInMatchesParseBoolWithLen(t *testing.T) {
	trueSpellings := []string{"t", "tr", "tru", "true", "y", "ye", "yes", "on", "1",
		"T", "TRUE", "Yes", "ON", "  true  ", "\ttrue\n"}
	falseSpellings := []string{"f", "fa", "fal", "fals", "false", "n", "no", "of", "off", "0",
		"F", "FALSE", "No", "OFF", "  false  "}
	// "o" is the load-bearing rejection: it is a prefix of both "on" and
	// "off", which is exactly why upstream forces a minimum length of 2.
	invalid := []string{"o", "O", "", " ", "2", "-1", "10", "01", "tru e", "yess",
		"nope", "onn", "offf", "truex"}

	for _, s := range trueSpellings {
		if b, ok := pgBoolIn(s); !ok || !b {
			t.Errorf("pgBoolIn(%q) = (%v, %v), want (true, true)", s, b, ok)
		}
	}
	for _, s := range falseSpellings {
		if b, ok := pgBoolIn(s); !ok || b {
			t.Errorf("pgBoolIn(%q) = (%v, %v), want (false, true)", s, b, ok)
		}
	}
	for _, s := range invalid {
		if _, ok := pgBoolIn(s); ok {
			t.Errorf("pgBoolIn(%q) accepted, want rejected", s)
		}
	}
}

// TestEncodeValuePGBoolAcceptsUnknownLiteral is the storage-encode half of the
// gap: a bare quoted literal is typed `unknown` upstream and reaches the column
// through boolin, so `INSERT INTO t(b) VALUES ('true')` loads on PG. goopg's
// codec used to demand KindBool strictly and raised
// `XX000 expected bool, got kind 3` — which is why every pg_dump / COPY-style
// script that quotes its booleans failed here.
func TestEncodeValuePGBoolAcceptsUnknownLiteral(t *testing.T) {
	bt := catalog.Type{Name: "bool"}
	cases := []struct {
		lit  string
		want byte
	}{
		{"true", 1}, {"t", 1}, {"yes", 1}, {"on", 1}, {"1", 1}, {" TRUE ", 1},
		{"false", 0}, {"f", 0}, {"no", 0}, {"off", 0}, {"0", 0}, {" FALSE ", 0},
	}
	for _, tc := range cases {
		got, err := encodeValuePG(bt, NewStringDatum(tc.lit))
		if err != nil {
			t.Fatalf("encodeValuePG(bool, %q): %v", tc.lit, err)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("encodeValuePG(bool, %q) = %v, want [%d]", tc.lit, got, tc.want)
		}
	}

	// The KindBool path must be untouched.
	for _, tc := range []struct {
		d    Datum
		want byte
	}{{NewBoolDatum(true), 1}, {NewBoolDatum(false), 0}} {
		got, err := encodeValuePG(bt, tc.d)
		if err != nil || len(got) != 1 || got[0] != tc.want {
			t.Errorf("encodeValuePG(bool, %v) = %v, %v; want [%d]", tc.d, got, err, tc.want)
		}
	}

	// Unrecognised text must raise boolin's 22P02 quoting the original input,
	// not the strict-Kind XX000 and not a silent false.
	_, err := encodeValuePG(bt, NewStringDatum(" maybe "))
	if err == nil {
		t.Fatalf("encodeValuePG(bool, \" maybe \") succeeded, want 22P02")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "22P02" {
		t.Fatalf("encodeValuePG(bool, \" maybe \") err = %v, want *ExecError 22P02", err)
	}
	if !strings.Contains(ee.Message, `" maybe "`) {
		t.Errorf("message %q should quote the original untrimmed input, as boolin does", ee.Message)
	}
}

// TestBoolColumnAcceptsQuotedLiteralEndToEnd drives the real INSERT path, which
// is where the defect was observed. The array case rides the same arm:
// encodeArrayValuePG recurses into encodeValuePG per element, so a `bool[]`
// column carried element text (KindString) into the strict-Kind arm too.
func TestBoolColumnAcceptsQuotedLiteralEndToEnd(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		"CREATE TABLE bool_quoted (id int4, b boolean, ba boolean[])",
		"INSERT INTO bool_quoted VALUES (1, 'true', '{t,yes,on,1}')",
		"INSERT INTO bool_quoted VALUES (2, 'off', '{f,no,off,0}')",
		"INSERT INTO bool_quoted VALUES (3, 'YES', '{TRUE,FALSE}')",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	rows := runQuery(t, ctx, "SELECT id, b, ba FROM bool_quoted ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	want := []struct {
		b  bool
		ba string
	}{
		{true, "{t,t,t,t}"},
		{false, "{f,f,f,f}"},
		{true, "{t,f}"},
	}
	for i, w := range want {
		if got := rows[i][1].BoolValue(); got != w.b {
			t.Errorf("row %d: b = %v, want %v", i+1, got, w.b)
		}
		if got := rows[i][2].StringValue(); got != w.ba {
			t.Errorf("row %d: ba = %q, want %q", i+1, got, w.ba)
		}
	}

	// An unrecognised spelling must still be refused, or the acceptance above
	// would just be "anything goes".
	if err := runDDL(t, ctx, "INSERT INTO bool_quoted VALUES (4, 'maybe', '{t}')"); err == nil {
		t.Fatalf("INSERT of 'maybe' into a bool column succeeded, want 22P02")
	}
}
