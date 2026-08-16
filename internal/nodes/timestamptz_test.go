package nodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// timestamptz_test.go — M0123-S4 sub-slice 4(b) gate: canonical timestamptz
// (OID 1184) Const datums. A timestamptz DEFAULT literal is folded to a
// by-value int64 Const at parse time (coerce_type's stringTypeToConst calls
// timestamptz_in immediately), so PG's stored pg_attrdef.adbin holds a Const of
// microseconds-since-2000, NOT a cast FuncExpr — the same shape as an integer
// Const but constlen 8 / consttype 1184.
//
// Every `want` was captured VERBATIM from PostgreSQL 18.3:
//
//	CREATE TABLE ts_test(
//	  a timestamptz DEFAULT '2024-01-15 10:30:00+00',
//	  b timestamptz DEFAULT '2000-01-01 00:00:00+00',
//	  c timestamptz DEFAULT 'epoch',
//	  d timestamptz DEFAULT '1999-12-31 23:59:59.5+00');
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='ts_test'::regclass ORDER BY a.attnum;
var timestamptzGolden = []struct {
	name string
	sql  string // the quoted literal exactly as written in the DEFAULT clause
	want string
}{
	{
		name: "utc_no_fraction",
		sql:  "'2024-01-15 10:30:00+00'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -70 -66 67 -8 -79 2 0 ]}`,
	},
	{
		name: "epoch_zero_datum", // the PG epoch itself → all-zero datum word
		sql:  "'2000-01-01 00:00:00+00'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "epoch_keyword_negative", // 'epoch' == 1970, pre-2000 → sign-extended
		sql:  "'epoch'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 32 -56 -60 -2 -94 -4 -1 ]}`,
	},
	{
		name: "sub_second_negative", // 1999-12-31 23:59:59.5 → -500000 μs
		sql:  "'1999-12-31 23:59:59.5+00'",
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ -32 94 -8 -1 -1 -1 -1 -1 ]}`,
	},
}

// TestTimestamptzResolveMatchesGolden is the forward oracle: a timestamptz
// DEFAULT literal must resolve byte-identical to PG18.3's adbin.
func TestTimestamptzResolveMatchesGolden(t *testing.T) {
	for _, tc := range timestamptzGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidTimestamptz)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			// ResolveForColumn must ACCEPT the default as canonical (top type ==
			// timestamptz), not degrade to SQL text.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidTimestamptz); !ok {
				t.Fatalf("ResolveForColumn(%q, timestamptz) rejected a valid default", tc.sql)
			}
		})
	}
}

// TestTimestamptzCodecRoundTrip closes text -> IR -> text (Read then re-Out).
func TestTimestamptzCodecRoundTrip(t *testing.T) {
	for _, tc := range timestamptzGolden {
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

// TestTimestamptzResolveRebuildRoundTrip proves resolve -> Rebuild -> re-resolve
// is a fixed point: the Const rebuilds to a canonical UTC timestamp literal
// which re-resolves to the identical datum.
func TestTimestamptzResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range timestamptzGolden {
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

// TestTimestamptzGracefulDegradation guards the determinism boundary: a literal
// whose UTC instant depends on the session TimeZone (a bare date, or a
// date+time with NO explicit offset) must NOT resolve to a Const — the writer
// degrades to SQL text rather than emit a TimeZone-dependent datum a standby
// would disagree with. A non-timestamptz context is likewise rejected.
func TestTimestamptzGracefulDegradation(t *testing.T) {
	reject := []string{
		"'2024-01-15 10:30:00'", // no offset — TimeZone dependent
		"'2024-01-15'",          // bare date — no time, no offset
		"'not a timestamp'",
	}
	for _, sql := range reject {
		t.Run(sql, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, sql), OidTimestamptz); ok {
				t.Fatalf("ResolveForColumn(%q, timestamptz) accepted a non-deterministic literal", sql)
			}
		})
	}
	// A timestamptz literal in a text column context stays text, not a Const.
	if _, ok := ResolveForColumn(mustParse(t, "'2024-01-15 10:30:00+00'"), OidText); !ok {
		t.Fatalf("timestamptz-shaped string in a text column should still resolve as text")
	}
}

// TestParseTimestamptzMicros locks the PG-faithful parser math directly (offset
// forms, fractional rounding, the epoch keyword) independent of the codec.
func TestParseTimestamptzMicros(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"2000-01-01 00:00:00+00", 0, true},
		{"2024-01-15 10:30:00+00", 758629800000000, true},
		{"epoch", -946684800000000, true},
		{"1999-12-31 23:59:59.5+00", -500000, true},
		{"2000-01-01T00:00:00Z", 0, true},           // 'T' separator + Z
		{"2000-01-01 00:00:00+05:30", -19800000000, true}, // +5:30 → UTC is earlier
		{"2000-01-01 00:00:00-0530", 19800000000, true},   // packed negative offset
		{"2000-01-01 05:30:00+0530", 0, true},             // wall 05:30 at +5:30 == UTC epoch
		{"2000-01-01 00:00:00", 0, false},                 // no offset
		{"garbage", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseTimestamptzMicros(tc.in)
			if ok != tc.ok {
				t.Fatalf("parseTimestamptzMicros(%q) ok=%v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("parseTimestamptzMicros(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
