package pgnodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// date_test.go — M0123-S4 sub-slice 26 gate: canonical date (OID 1082) Const
// datums. A date DEFAULT literal is folded to a by-value DateADT (int32
// days-since-2000) Const at parse time — date_in is TimeZone-independent, so
// (unlike timestamptz) PG folds ANY plain ISO date literal, and goopg reproduces
// the identical adbin. The wire form is the int4 Const shape but consttype 1082:
// outDatum prints constlen 4 yet emits all sizeof(Datum)==8 bytes, and a pre-2000
// (negative) day count sign-extends into the high bytes.
//
// Every `want` was captured VERBATIM from PostgreSQL 18.3:
//
//	CREATE TABLE dt(
//	  a date DEFAULT '2024-03-15',
//	  b date DEFAULT '2000-01-01',
//	  c date DEFAULT '1999-12-31',
//	  d date DEFAULT '0001-01-01',
//	  e date DEFAULT '5874897-12-31');
//	SELECT a.attname, ad.adbin::text FROM pg_attrdef ad
//	  JOIN pg_attribute a ON a.attrelid=ad.adrelid AND a.attnum=ad.adnum
//	  WHERE ad.adrelid='dt'::regclass ORDER BY a.attnum;
var dateGolden = []struct {
	name string
	sql  string // the quoted literal exactly as written in the DEFAULT clause
	want string
}{
	{
		name: "post_2000", // 2024-03-15 → day 8840 (0x2288), zero-extended
		sql:  "'2024-03-15'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -120 34 0 0 0 0 0 0 ]}`,
	},
	{
		name: "epoch_zero_datum", // the date epoch itself (2000-01-01) → day 0
		sql:  "'2000-01-01'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "pre_epoch_negative_one", // 1999-12-31 → day -1, sign-extended
		sql:  "'1999-12-31'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 -1 -1 -1 -1 -1 -1 -1 ]}`,
	},
	{
		name: "year_one", // 0001-01-01 → large negative day count (sign-extended)
		sql:  "'0001-01-01'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -7 -37 -12 -1 -1 -1 -1 -1 ]}`,
	},
	{
		name: "max_date", // 5874897-12-31 → 0x7FDA970C, high bit of the int32 set
		sql:  "'5874897-12-31'",
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 12 -105 -38 127 0 0 0 0 ]}`,
	},
}

// TestDateResolveMatchesGolden is the forward oracle: a date DEFAULT literal must
// resolve byte-identical to PG18.3's adbin.
func TestDateResolveMatchesGolden(t *testing.T) {
	for _, tc := range dateGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), OidDate)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			// ResolveForColumn must ACCEPT the default as canonical (top type ==
			// date), not degrade to SQL text.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), OidDate); !ok {
				t.Fatalf("ResolveForColumn(%q, date) rejected a valid default", tc.sql)
			}
		})
	}
}

// TestDateCodecRoundTrip closes text -> IR -> text (Read then re-Out).
func TestDateCodecRoundTrip(t *testing.T) {
	for _, tc := range dateGolden {
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

// TestDateResolveRebuildRoundTrip proves resolve -> Rebuild -> re-resolve is a
// fixed point: the Const rebuilds to a canonical "YYYY-MM-DD" literal which
// re-resolves to the identical datum.
func TestDateResolveRebuildRoundTrip(t *testing.T) {
	for _, tc := range dateGolden {
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

// TestDateGracefulDegradation guards the type/validity boundary: a date literal
// in a non-date column context stays whatever that context makes it (text), and a
// non-canonical calendar triple (month 13, day 32) is rejected by the round-trip
// guard so it degrades to SQL text rather than fold a garbage datum.
func TestDateGracefulDegradation(t *testing.T) {
	reject := []string{
		"'2024-13-01'", // month out of range
		"'2024-02-30'", // day out of range for February
		"'not a date'",
	}
	for _, sql := range reject {
		t.Run(sql, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, sql), OidDate); ok {
				t.Fatalf("ResolveForColumn(%q, date) accepted an invalid date literal", sql)
			}
		})
	}
	// A date-shaped string in a text column context stays text, not a date Const.
	if _, ok := ResolveForColumn(mustParse(t, "'2024-03-15'"), OidText); !ok {
		t.Fatalf("date-shaped string in a text column should still resolve as text")
	}
}

// TestParseDateDays locks the PG-faithful DateADT math directly, independent of
// the codec: the day count is date2j(literal) - postgresEpochJDate, and the
// round-trip guard rejects an out-of-range calendar field.
func TestParseDateDays(t *testing.T) {
	cases := []struct {
		in   string
		want int32
		ok   bool
	}{
		{"2000-01-01", 0, true},
		{"2024-03-15", 8840, true},
		{"1999-12-31", -1, true},
		{"2000-01-02", 1, true},
		{"2024-13-01", 0, false}, // month 13
		{"2024-02-30", 0, false}, // Feb 30
		{"garbage", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseDateDays(tc.in)
			if ok != tc.ok {
				t.Fatalf("parseDateDays(%q) ok=%v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("parseDateDays(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
