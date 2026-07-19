package pgnodes

import (
	"reflect"
	"testing"
)

// string_text_numeric_cast_test.go — M0123-S4 sub-slice 29 gate: an unknown-type
// string literal coerced to text or numeric — whether by an explicit `::type` cast
// (`'foo'::text`, `'5.5'::numeric`) or by a typed column context (`col numeric
// DEFAULT '5.5'`) — folds at parse time to a by-value Const with NO cast node,
// byte-for-byte identical to PG18.3. PG's coerce_type folds the unknown literal via
// stringTypeToConst → the type input function (textin / numeric_in), so the stored
// adbin is a bare Const, not a cast. This extends sub-slice 28 (bool/int folds)
// across textin + numeric_in, sharing foldStringLiteralConst.
//
// text folds a VERBATIM byte copy (textin never trims and never fails); numeric_in
// preserves the display scale (`'5.50'` keeps dscale 2) and folds a leading sign
// into the value, so `'5.5'::numeric` is byte-identical to the bare numeric literal
// `5.5`. The NaN / ±Infinity specials use a distinct varlena not modeled here and
// degrade to SQL text (all-or-nothing).
//
// Each `want` is a LIVE PG18.3 capture (re-derived by the oracle gate
// internal/testport/oracle_pgnodes_adbin_test.go, cases str_cast_text / str_cast_
// numeric*/str_col_numeric).
var stringTextNumericCastGolden = []struct {
	name string
	sql  string // the expression exactly as written
	oid  uint32 // the folded Const's result type OID
	want string
}{
	{
		name: "text_cast",
		sql:  "'foo'::text",
		oid:  OidText,
		want: `{CONST :consttype 25 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 7 [ 28 0 0 0 102 111 111 ]}`,
	},
	{
		name: "numeric_cast",
		sql:  "'5.5'::numeric",
		oid:  OidNumeric,
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]}`,
	},
	{
		name: "numeric_cast_trailing_zero", // dscale 2 preserved: distinct varlena from 5.5
		sql:  "'5.50'::numeric",
		oid:  OidNumeric,
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 0 -127 5 0 -120 19 ]}`,
	},
	{
		name: "numeric_cast_negative", // sign folds into the value (numeric_in), not a unary minus
		sql:  "'-2.5'::numeric",
		oid:  OidNumeric,
		want: `{CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -96 2 0 -120 19 ]}`,
	},
}

// TestStringTextNumericCastResolveMatchesGolden is the forward oracle: an explicit
// `::type` cast of a string literal resolves byte-identical to PG18.3's folded adbin
// — with an UNKNOWN context (expected=0), proving the target type comes from the
// cast, not a column. Also checks the codec round-trip and that ResolveForColumn
// accepts the fold as canonical (no SQL-text degrade).
func TestStringTextNumericCastResolveMatchesGolden(t *testing.T) {
	for _, tc := range stringTextNumericCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, err := ResolveExpr(mustParse(t, tc.sql), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			if _, ok := n.(*Const); !ok {
				t.Fatalf("%q resolved to %T, want a bare *Const (no cast wrapper)", tc.sql, n)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
			back, err := Read(tc.want)
			if err != nil {
				t.Fatalf("Read(%q): %v", tc.want, err)
			}
			if got := Out(back); got != tc.want {
				t.Fatalf("codec round-trip mismatch for %q:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.oid); !ok {
				t.Fatalf("ResolveForColumn(%q, oid=%d) rejected a valid explicit-cast default", tc.sql, tc.oid)
			}
		})
	}
}

// TestStringNumericCastMatchesBareFold proves the explicit `::numeric` cast is
// byte-identical to the bare literal in a numeric column context — the whole premise
// of the fold (the `::type` supplies the target type but adds no wrapper). It also
// confirms `'5.5'::numeric` equals the bare NUMERIC literal `5.5` (both fold via
// set_var_from_str to the identical NumericData varlena).
func TestStringNumericCastMatchesBareFold(t *testing.T) {
	pairs := []struct {
		cast, bare string
		oid        uint32
	}{
		{"'5.5'::numeric", "'5.5'", OidNumeric}, // string cast vs string column context
		{"'5.5'::numeric", "5.5", OidNumeric},   // string cast vs bare numeric literal
		{"'5.50'::numeric", "5.50", OidNumeric}, // dscale 2 both ways
		{"'-2.5'::numeric", "-2.5", OidNumeric}, // negative both ways
		{"'foo'::text", "'foo'", OidText},       // text cast vs text column context
	}
	for _, p := range pairs {
		t.Run(p.cast+"_vs_"+p.bare, func(t *testing.T) {
			castNode, err := ResolveExpr(mustParse(t, p.cast), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", p.cast, err)
			}
			bareNode, err := ResolveExpr(mustParse(t, p.bare), p.oid)
			if err != nil {
				t.Fatalf("ResolveExpr(%q, oid=%d): %v", p.bare, p.oid, err)
			}
			if Out(castNode) != Out(bareNode) {
				t.Fatalf("explicit-cast and column-context fold differ:\n cast: %s\n bare: %s",
					Out(castNode), Out(bareNode))
			}
		})
	}
}

// TestStringTextNumericCastRebuildRoundTrip proves resolve → Rebuild → re-resolve is
// a fixed point for the folded text/numeric Const, so a stored adbin reloads to the
// identical node (the load-bearing decode-side check).
func TestStringTextNumericCastRebuildRoundTrip(t *testing.T) {
	for _, tc := range stringTextNumericCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			// Re-resolve in the folded type's column context (rebuild produces a bare
			// literal; the column type re-selects the fold), matching the reload path.
			n2, ok := ResolveForColumn(ast, tc.oid)
			if !ok {
				t.Fatalf("re-ResolveForColumn(%q, oid=%d) degraded", tc.sql, tc.oid)
			}
			if !reflect.DeepEqual(n1, n2) {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestStringNumericCastSpecialsAndBadDegrade guards the numeric fold boundary: the
// NaN / ±Infinity specials (a distinct varlena PG models but this subset does not)
// and a non-numeric string all degrade to SQL text rather than fold a wrong datum.
// text has no such boundary — textin accepts every string — so it is not listed.
func TestStringNumericCastSpecialsAndBadDegrade(t *testing.T) {
	for _, sql := range []string{
		"'NaN'::numeric",       // special varlena, not modeled
		"'Infinity'::numeric",  // special varlena, not modeled
		"'-Infinity'::numeric", // special varlena, not modeled
		"'abc'::numeric",       // not a number
		"''::numeric",          // empty
	} {
		t.Run(sql, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, sql), OidNumeric); ok {
				t.Fatalf("ResolveForColumn(%q, numeric) accepted a form that must degrade to SQL text", sql)
			}
		})
	}
}
