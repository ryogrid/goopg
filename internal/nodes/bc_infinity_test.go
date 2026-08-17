package nodes

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// bc_infinity_test.go — M0123-S4 sub-slice 40 gate: broader date input forms
// (infinity, BC years). PG folds these deterministically to Const values in
// pg_attrdef.adbin; every "want" was captured verbatim from PostgreSQL 18.3.

var bcInfinityGoldenDate = []struct {
	name string
	sql  string // the quoted literal exactly as written in the DEFAULT clause
	want string
}{
	{
		name: "date_infinity",
		sql:  "'infinity'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 -1 -1 127 0 0 0 0 ]}`,
	},
	{
		name: "date_neg_infinity",
		sql:  "'-infinity'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 -128 -1 -1 -1 -1 ]}`,
	},
	{
		name: "date_0001_01_01_bc",
		sql:  "'0001-01-01 BC'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -117 -38 -12 -1 -1 -1 -1 -1 ]}`,
	},
	{
		name: "date_0044_03_15_bc",
		sql:  "'0044-03-15 BC'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 123 -99 -12 -1 -1 -1 -1 -1 ]}`,
	},
}

var bcInfinityGoldenTimestamp = []struct {
	name string
	sql  string // the quoted literal exactly as written in the DEFAULT clause
	want string
}{
	{
		name: "ts_infinity",
		sql:  "'infinity'",
		want: `{CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -1 -1 -1 -1 -1 -1 127 ]}`,
	},
	{
		name: "ts_neg_infinity",
		sql:  "'-infinity'",
		want: `{CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 0 0 0 0 -128 ]}`,
	},
	{
		name: "ts_0001_01_01_bc",
		sql:  "'0001-01-01 00:00:00 BC'",
		want: `{CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 -79 27 61 -58 31 -1 ]}`,
	},
	{
		name: "ts_0044_03_15_bc",
		sql:  "'0044-03-15 00:00:00 BC'",
		want: `{CONST :consttype 1114 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 91 -20 -34 -7 26 -1 ]}`,
	},
}

var bcInfinityGoldenTimestamptz = []struct {
	name string
	sql  string // the quoted literal exactly as written in the DEFAULT clause
	want string
}{
	{
		name: "tstz_infinity",
		sql:  "'infinity'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -1 -1 -1 -1 -1 -1 -1 127 ]}`,
	},
	{
		name: "tstz_neg_infinity",
		sql:  "'-infinity'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 0 0 0 0 -128 ]}`,
	},
	{
		name: "tstz_0001_01_01_bc",
		sql:  "'0001-01-01 00:00:00+00 BC'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 -79 27 61 -58 31 -1 ]}`,
	},
	{
		name: "tstz_0044_03_15_bc",
		sql:  "'0044-03-15 00:00:00+00 BC'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 91 -20 -34 -7 26 -1 ]}`,
	},
}

func TestBCInfinityDateResolveMatchesGolden(t *testing.T) {
	for _, tc := range bcInfinityGoldenDate {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidDate)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			// ResolveForColumn must accept the default as canonical.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidDate); !ok {
				t.Fatalf("ResolveForColumn(%q, date) rejected a valid default", tc.sql)
			}
		})
	}
}

func TestBCInfinityTimestampResolveMatchesGolden(t *testing.T) {
	for _, tc := range bcInfinityGoldenTimestamp {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidTimestamp)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidTimestamp); !ok {
				t.Fatalf("ResolveForColumn(%q, timestamp) rejected a valid default", tc.sql)
			}
		})
	}
}

func TestBCInfinityTimestamptzResolveMatchesGolden(t *testing.T) {
	for _, tc := range bcInfinityGoldenTimestamptz {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidTimestamptz)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidTimestamptz); !ok {
				t.Fatalf("ResolveForColumn(%q, timestamptz) rejected a valid default", tc.sql)
			}
		})
	}
}

func TestBCInfinityDateCodecRoundTrip(t *testing.T) {
	for _, tc := range bcInfinityGoldenDate {
		t.Run(tc.name, func(t *testing.T) {
			n, err := Read(tc.want)
			if err != nil {
				t.Fatalf("Read(%q): %v", tc.name, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("re-Out mismatch:\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestBCInfinityDateResolveRebuildRoundTrip proves resolve→Rebuild→re-resolve
// is a fixed point: the Const rebuilds to a canonical literal which re-resolves
// to the identical datum.
func TestBCInfinityDateResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range bcInfinityGoldenDate {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidDate)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			if _, ok := ast.(*parser.StringConst); !ok {
				t.Fatalf("rebuilt %q = %T, want *parser.StringConst", tc.sql, ast)
			}
			n2, err := ResolveExpr(ast, OidDate)
			if err != nil {
				t.Fatalf("re-ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n2); got != tc.want {
				t.Fatalf("resolve->Rebuild->re-resolve not a fixed point for %q:\n got: %s\nwant: %s",
					tc.sql, got, tc.want)
			}
		})
	}
}

// TestBCInfinityTimestampResolveRebuildRoundTrip mirrors the date test above
// for timestamp (OID 1114) values.
func TestBCInfinityTimestampResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range bcInfinityGoldenTimestamp {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidTimestamp)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			if _, ok := ast.(*parser.StringConst); !ok {
				t.Fatalf("rebuilt %q = %T, want *parser.StringConst", tc.sql, ast)
			}
			n2, err := ResolveExpr(ast, OidTimestamp)
			if err != nil {
				t.Fatalf("re-ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n2); got != tc.want {
				t.Fatalf("resolve->Rebuild->re-resolve not a fixed point for %q:\n got: %s\nwant: %s",
					tc.sql, got, tc.want)
			}
		})
	}
}

// TestBCInfinityTimestamptzResolveRebuildRoundTrip mirrors the same
// round-trip for timestamptz (OID 1184).
func TestBCInfinityTimestamptzResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range bcInfinityGoldenTimestamptz {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), OidTimestamptz)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			if _, ok := ast.(*parser.StringConst); !ok {
				t.Fatalf("rebuilt %q = %T, want *parser.StringConst", tc.sql, ast)
			}
			n2, err := ResolveExpr(ast, OidTimestamptz)
			if err != nil {
				t.Fatalf("re-ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n2); got != tc.want {
				t.Fatalf("resolve->Rebuild->re-resolve not a fixed point for %q:\n got: %s\nwant: %s",
					tc.sql, got, tc.want)
			}
		})
	}
}

// TestBCInfinityParseEdgeCases covers edge-case inputs.
func TestBCInfinityParseEdgeCases(t *testing.T) {
	// parseDateDays — case-insensitive
	if _, ok := parseDateDays("INFINITY"); !ok {
		t.Error("INFINITY should be accepted for date")
	}
	// AD suffix is a no-op.
	if d, ok := parseDateDays("2024-01-15 AD"); !ok {
		t.Error("2024-01-15 AD should be accepted")
	} else if d == 0 {
		t.Error("2024-01-15 AD should produce a non-zero value")
	}
	// parseTimestampMicros
	if _, ok := parseTimestampMicros("Infinity"); !ok {
		t.Error("Infinity (mixed case) should be accepted")
	}
	// parseTimestamptzMicros
	if _, ok := parseTimestamptzMicros("-Infinity"); !ok {
		t.Error("-Infinity (mixed case) should be accepted")
	}
	// BC requires explicit offset for timestamptz
	if _, ok := parseTimestamptzMicros("0001-01-01 00:00:00 BC"); ok {
		t.Error("BC timestamptz without explicit offset should be rejected")
	}
}

// TestBCInfinityEnsureNoRegressions verifies that existing date/timestamp
// golden tests are unaffected by the BC/infinity changes.
func TestBCInfinityEnsureNoRegressions(t *testing.T) {
	// Existing timestamptz golden tests should still pass.
	for _, tc := range timestamptzGolden {
		t.Run("tstz_"+tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidTimestamptz)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
		})
	}
}

// TestFormatInfinitySpecial verifies format functions return the exact expected
// strings for infinity sentinels.
func TestFormatInfinitySpecial(t *testing.T) {
	if got := formatDate(math.MaxInt32); got != "infinity" {
		t.Errorf("formatDate(MaxInt32) = %q, want %q", got, "infinity")
	}
	if got := formatDate(math.MinInt32); got != "-infinity" {
		t.Errorf("formatDate(MinInt32) = %q, want %q", got, "-infinity")
	}
	if got := formatTimestamp(math.MaxInt64); got != "infinity" {
		t.Errorf("formatTimestamp(MaxInt64) = %q, want %q", got, "infinity")
	}
	if got := formatTimestamp(math.MinInt64); got != "-infinity" {
		t.Errorf("formatTimestamp(MinInt64) = %q, want %q", got, "-infinity")
	}
	if got := formatTimestamptzUTC(math.MaxInt64); got != "infinity" {
		t.Errorf("formatTimestamptzUTC(MaxInt64) = %q, want %q", got, "infinity")
	}
	if got := formatTimestamptzUTC(math.MinInt64); got != "-infinity" {
		t.Errorf("formatTimestamptzUTC(MinInt64) = %q, want %q", got, "-infinity")
	}
}
