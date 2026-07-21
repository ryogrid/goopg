package pgnodes

import (
	"reflect"
	"testing"
)

// string_float_oid_cast_test.go — M0123-S4 sub-slice 29c gate: an unknown-type
// string literal coerced to oid / float4 / float8 — whether by an explicit `::type`
// cast (`'5'::oid`, `'5.5'::float8`) or by a typed column context (`col float8
// DEFAULT '5.5'`) — folds at parse time to a by-value Const with NO cast node,
// byte-for-byte identical to PG18.3. PG's coerce_type folds the unknown literal via
// stringTypeToConst → the type input function (oidin / float4in / float8in), so the
// stored adbin is a bare Const, not a cast. This closes sub-slice 28/29's oid/float
// string-fold deferral, extending the shared foldStringLiteralConst across those three
// input functions.
//
// Datum traps (all confirmed byte-identical by the LIVE oracle
// internal/testport/oracle_pgnodes_adbin_test.go cases str_cast_oid/str_cast_float*):
//   - oid is 32-bit UNSIGNED and ZERO-extends into the 8-byte datum word (a `:constlen
//     4` type whose outDatum prints sizeof(Datum)=8 bytes with a `:constvalue 4`
//     length prefix).
//   - float8 reinterprets the IEEE-754 double's 64 bits as an int64 (the raw
//     little-endian bit pattern in the word).
//   - float4 reinterprets the 32-bit float's bits as an int32 and Int32GetDatum
//     SIGN-extends them, so a NEGATIVE float (`-2.5`, bit pattern 0xC0200000) fills the
//     high four bytes with 0xFF — exactly like a negative int4.
//
// Both PG's strtod/strtof and Go's strconv.ParseFloat are correctly rounded, so the
// folded float bits are identical for every finite decimal spelling.
var stringFloatOidCastGolden = []struct {
	name string
	sql  string // the expression exactly as written
	oid  uint32 // the folded Const's result type OID
	want string
}{
	{
		name: "oid_cast",
		sql:  "'5'::oid",
		oid:  OidOid,
		want: `{CONST :consttype 26 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "float8_cast_int",
		sql:  "'5'::float8",
		oid:  OidFloat8,
		want: `{CONST :consttype 701 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 0 0 0 20 64 ]}`,
	},
	{
		name: "float8_cast_decimal",
		sql:  "'5.5'::float8",
		oid:  OidFloat8,
		want: `{CONST :consttype 701 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 0 0 0 22 64 ]}`,
	},
	{
		name: "float8_cast_negative", // sign lives in the bit pattern, not a unary minus
		sql:  "'-2.5'::float8",
		oid:  OidFloat8,
		want: `{CONST :consttype 701 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 0 0 0 4 -64 ]}`,
	},
	{
		name: "float8_cast_scientific",
		sql:  "'1.5e10'::float8",
		oid:  OidFloat8,
		want: `{CONST :consttype 701 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 0 0 -80 -114 -16 11 66 ]}`,
	},
	{
		name: "float4_cast_int",
		sql:  "'5'::float4",
		oid:  OidFloat4,
		want: `{CONST :consttype 700 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 -96 64 0 0 0 0 ]}`,
	},
	{
		name: "float4_cast_decimal",
		sql:  "'5.5'::float4",
		oid:  OidFloat4,
		want: `{CONST :consttype 700 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 -80 64 0 0 0 0 ]}`,
	},
	{
		name: "float4_cast_negative", // negative float4 bits sign-extend the high word to 0xFF
		sql:  "'-2.5'::float4",
		oid:  OidFloat4,
		want: `{CONST :consttype 700 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 32 -64 -1 -1 -1 -1 ]}`,
	},
}

// TestStringFloatOidCastResolveMatchesGolden is the forward oracle: an explicit
// `::type` cast of a string literal resolves byte-identical to PG18.3's folded adbin —
// with an UNKNOWN context (expected=0), proving the target type comes from the cast,
// not a column. Also checks the codec round-trip and that ResolveForColumn accepts the
// fold as canonical (no SQL-text degrade).
func TestStringFloatOidCastResolveMatchesGolden(t *testing.T) {
	for _, tc := range stringFloatOidCastGolden {
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

// TestStringFloatOidCastMatchesBareCol proves the explicit `::type` cast is
// byte-identical to the same string literal in that type's column context — the whole
// premise of the fold (the `::type` supplies the target type but adds no wrapper).
func TestStringFloatOidCastMatchesBareCol(t *testing.T) {
	pairs := []struct {
		cast, col string
		oid       uint32
	}{
		{"'5'::oid", "'5'", OidOid},
		{"'5.5'::float8", "'5.5'", OidFloat8},
		{"'-2.5'::float8", "'-2.5'", OidFloat8},
		{"'5.5'::float4", "'5.5'", OidFloat4},
		{"'-2.5'::float4", "'-2.5'", OidFloat4},
	}
	for _, p := range pairs {
		t.Run(p.cast+"_vs_col_"+p.col, func(t *testing.T) {
			castNode, err := ResolveExpr(mustParse(t, p.cast), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", p.cast, err)
			}
			colNode, err := ResolveExpr(mustParse(t, p.col), p.oid)
			if err != nil {
				t.Fatalf("ResolveExpr(%q, oid=%d): %v", p.col, p.oid, err)
			}
			if Out(castNode) != Out(colNode) {
				t.Fatalf("explicit-cast and column-context fold differ:\n cast: %s\n col:  %s",
					Out(castNode), Out(colNode))
			}
		})
	}
}

// TestStringFloatOidCastRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point for the folded oid/float Const, so a stored adbin reloads to the
// identical node. Rebuild emits the shortest round-tripping decimal spelling (a plain
// STRING literal), which the folded type's column context re-folds to the identical
// bits (the load-bearing decode-side check).
func TestStringFloatOidCastRebuildRoundTrip(t *testing.T) {
	for _, tc := range stringFloatOidCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
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

// TestStringFloatOidCastBadDegrade guards the fold boundary: a non-numeric string, an
// empty string, a signed / out-of-range oid, and the non-finite float spellings
// (Inf/NaN — a distinct datum this subset does not fold) all degrade to SQL text
// rather than fold a wrong or non-finite datum.
func TestStringFloatOidCastBadDegrade(t *testing.T) {
	for _, sql := range []string{
		"'abc'::oid",         // not a number
		"''::oid",            // empty
		"'-1'::oid",          // strtoul wrap-around form excluded
		"'99999999999'::oid", // out of 32-bit range
		"'5.5'::oid",         // oidin rejects a decimal point
		"'abc'::float8",      // not a number
		"''::float8",         // empty
		"'Infinity'::float8", // non-finite (distinct datum, not folded)
		"'NaN'::float8",      // non-finite
		"'inf'::float4",      // non-finite
		"'5 x'::float8",      // trailing junk
	} {
		t.Run(sql, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, sql), typeOIDForDegrade(sql)); ok {
				t.Fatalf("ResolveForColumn(%q) accepted a form that must degrade to SQL text", sql)
			}
		})
	}
}

// typeOIDForDegrade picks the column OID matching the `::type` suffix of a degrade
// case so ResolveForColumn is exercised in the same type context PG would use.
func typeOIDForDegrade(sql string) uint32 {
	switch {
	case hasSuffixFold(sql, "::oid"):
		return OidOid
	case hasSuffixFold(sql, "::float4"):
		return OidFloat4
	default:
		return OidFloat8
	}
}

func hasSuffixFold(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
