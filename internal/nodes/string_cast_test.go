package nodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// string_cast_test.go — M0123-S4 sub-slice 28 gate: an unknown-type string literal
// coerced to bool / int2 / int4 / int8 — whether by an explicit `::type` cast
// (`'123'::int4`) or by a typed column context (`col int4 DEFAULT '123'`) — folds
// at parse time to a by-value Const with NO cast node, byte-for-byte identical to
// PG18.3. PG's coerce_type folds the unknown literal via stringTypeToConst → the
// type input function (int4in / int8in / int2in / boolin), so the stored adbin is a
// bare Const, not a cast. This extends sub-slice 27 (date/timestamptz string folds)
// across the boolean + exact-integer input functions, sharing foldStringLiteralConst.
//
// Scope boundary this slice depends on: a BARE integer literal `5` is already
// int4-typed, so `int2 DEFAULT 5` is an int4→int2 cast FuncExpr (funcid 314), NOT a
// folded int2 Const — only an *unknown-type string* literal folds. That boundary is
// asserted by TestStringCastNonStringOperandNotFolded.
//
// Each `want` is a LIVE PG18.3 capture (re-derived by the oracle gate
// internal/testport/oracle_pgnodes_adbin_test.go, which stores these DEFAULTs on a
// real PG18 and diffs the adbin).
var stringCastGolden = []struct {
	name string
	sql  string // the explicit-cast expression exactly as written
	oid  uint32 // the folded Const's result type OID
	want string
}{
	{
		name: "int4_cast",
		sql:  "'123'::int4",
		oid:  OidInt4,
		want: `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 123 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "int4_cast_negative", // sign-extends into the 8-byte datum word
		sql:  "'-5'::int4",
		oid:  OidInt4,
		want: `{CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -5 -1 -1 -1 -1 -1 -1 -1 ]}`,
	},
	{
		name: "int8_cast",
		sql:  "'123'::int8",
		oid:  OidInt8,
		want: `{CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 123 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "int2_cast",
		sql:  "'12'::int2",
		oid:  OidInt2,
		want: `{CONST :consttype 21 :consttypmod -1 :constcollid 0 :constlen 2 :constbyval true :constisnull false :location -1 :constvalue 2 [ 12 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "bool_cast_true",
		sql:  "'t'::bool",
		oid:  OidBool,
		want: `{CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 1 0 0 0 0 0 0 0 ]}`,
	},
	{
		name: "bool_cast_false_word", // 'false' spelling → false
		sql:  "'false'::bool",
		oid:  OidBool,
		want: `{CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 :constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]}`,
	},
}

// TestStringCastResolveMatchesGolden is the forward oracle: an explicit `::type`
// cast of a string literal resolves byte-identical to PG18.3's folded adbin — with
// an UNKNOWN context (expected=0), proving the target type comes from the cast, not
// a column.
func TestStringCastResolveMatchesGolden(t *testing.T) {
	for _, tc := range stringCastGolden {
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
			// Codec round-trip: Out → Read → Out reproduces the same bytes.
			back, err := Read(tc.want)
			if err != nil {
				t.Fatalf("Read(%q): %v", tc.want, err)
			}
			if got := Out(back); got != tc.want {
				t.Fatalf("codec round-trip mismatch for %q:\n got: %s\nwant: %s", tc.name, got, tc.want)
			}
			// The cast folds to the column type, so ResolveForColumn must accept it as
			// canonical (top type == the cast target), not degrade to SQL text.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.oid); !ok {
				t.Fatalf("ResolveForColumn(%q, oid=%d) rejected a valid explicit-cast default", tc.sql, tc.oid)
			}
		})
	}
}

// TestStringCastMatchesBareFold proves the explicit-cast form is byte-identical to
// the bare literal folded in the equivalent column context — the whole premise of
// the sub-slice (the `::type` supplies the target type but adds no wrapper, and both
// sibling paths route through foldStringLiteralConst).
func TestStringCastMatchesBareFold(t *testing.T) {
	pairs := []struct {
		cast, bare string
		oid        uint32
	}{
		{"'123'::int4", "'123'", OidInt4},
		{"'-5'::int4", "'-5'", OidInt4},
		{"'123'::int8", "'123'", OidInt8},
		{"'12'::int2", "'12'", OidInt2},
		{"'t'::bool", "'t'", OidBool},
		{"'false'::bool", "'false'", OidBool},
		{"'yes'::bool", "'yes'", OidBool},
		{"'off'::bool", "'off'", OidBool},
	}
	for _, p := range pairs {
		t.Run(p.cast, func(t *testing.T) {
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

// TestStringCastBoolSpellings pins parse_bool_with_len's accepted unique-prefix
// spellings (true/yes/on/1 and false/no/off/0 plus their prefixes, case-insensitive
// with surrounding whitespace) against the bool datum they fold to.
func TestStringCastBoolSpellings(t *testing.T) {
	trueForms := []string{"t", "tr", "true", "TRUE", "y", "yes", "on", "1", "  true  "}
	falseForms := []string{"f", "fa", "false", "FALSE", "n", "no", "off", "0", " off "}
	for _, s := range trueForms {
		if v, ok := parseBoolLiteral(s); !ok || !v {
			t.Fatalf("parseBoolLiteral(%q) = (%v,%v), want (true,true)", s, v, ok)
		}
	}
	for _, s := range falseForms {
		if v, ok := parseBoolLiteral(s); !ok || v {
			t.Fatalf("parseBoolLiteral(%q) = (%v,%v), want (false,true)", s, v, ok)
		}
	}
	// Rejected: not a unique prefix, or an ambiguous/too-short "o".
	for _, s := range []string{"", "o", "maybe", "tru3", "yep", "10", "truex", "onn"} {
		if _, ok := parseBoolLiteral(s); ok {
			t.Fatalf("parseBoolLiteral(%q) accepted, want rejected", s)
		}
	}
}

// TestStringCastNonStringOperandNotFolded is the scope boundary: a BARE integer
// literal is int4-typed, so a cast to int2 keeps a visible int4→int2 cast FuncExpr
// (funcid 314) — it must NOT collapse to a bare int2 Const the way an unknown-type
// string literal does. This is why foldStringLiteralConst only fires on a
// *parser.StringConst operand.
func TestStringCastNonStringOperandNotFolded(t *testing.T) {
	n, err := ResolveExpr(mustParse(t, "5::int2"), 0)
	if err != nil {
		t.Fatalf("ResolveExpr(5::int2): %v", err)
	}
	fe, ok := n.(*FuncExpr)
	if !ok {
		t.Fatalf("5::int2 resolved to %T, want *FuncExpr (int4->int2 cast, not a folded int2 Const)", n)
	}
	if fe.Funcid != 314 || fe.Funcformat != 1 {
		t.Fatalf("5::int2 = FuncExpr{funcid %d, funcformat %d}, want funcid 314 / funcformat 1", fe.Funcid, fe.Funcformat)
	}
	// A bare integer literal in an int2 COLUMN context now resolves canonically
	// via the implicit int4→int2 cast FuncExpr (M0123-S4 sub-slice 31).
	if _, ok := ResolveForColumn(mustParse(t, "5"), OidInt2); !ok {
		t.Fatalf("int2 DEFAULT 5 (bare integer literal) degraded to SQL text; want canonical int4→int2 implicit cast FuncExpr")
	}
}

// TestStringCastGracefulDegradation guards the fold boundary: a literal the input
// function would reject (non-numeric, fractional, overflow) and a deliberately
// excluded non-decimal spelling (0x…, which PG *would* fold but this subset does
// not, to guarantee byte-identity) all degrade to SQL text rather than fold a wrong
// or unverified datum. ResolveForColumn(..., ok=false) is the SQL-text fallback.
func TestStringCastGracefulDegradation(t *testing.T) {
	cases := []struct {
		sql string
		oid uint32
	}{
		{"'abc'::int4", OidInt4},   // not a number
		{"'12.5'::int8", OidInt8},  // fractional — int input rejects
		{"'0x10'::int4", OidInt4},  // hex spelling deliberately excluded (all-or-nothing)
		{"'99999'::int2", OidInt2}, // out of int2 range
		{"'maybe'::bool", OidBool}, // not a bool spelling
		{"''::int4", OidInt4},      // empty
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.oid); ok {
				t.Fatalf("ResolveForColumn(%q, oid=%d) accepted a form that must degrade to SQL text", tc.sql, tc.oid)
			}
		})
	}
}

// TestStringCastReloadFixedPoint proves the reload path holds: the folded Const
// rebuilds to a literal which, re-resolved in the COLUMN context PG's
// build_column_default restores, reproduces the identical datum. The fixed point is
// column-scoped (matching sub-slices 26/27): an int2 Const rebuilds to a STRING
// literal so it re-folds via foldStringLiteralConst (a bare IntegerConst would
// resolve to int4 and break it); int4/int8 rebuild to an integer literal that
// resolveIntLiteral re-types from the column context; bool rebuilds to a boolean
// literal.
func TestStringCastReloadFixedPoint(t *testing.T) {
	for _, tc := range stringCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), 0)
			if err != nil {
				t.Fatalf("ResolveExpr(%q): %v", tc.sql, err)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			// int2 must rebuild to a string literal (not an IntegerConst) so the
			// re-resolve routes back through the string fold.
			if tc.oid == OidInt2 {
				if _, ok := ast.(*parser.StringConst); !ok {
					t.Fatalf("rebuilt int2 %q = %T, want *parser.StringConst", tc.sql, ast)
				}
			}
			n2, err := ResolveExpr(ast, tc.oid)
			if err != nil {
				t.Fatalf("re-ResolveExpr in column context: %v", err)
			}
			if got := Out(n2); got != tc.want {
				t.Fatalf("resolve->Rebuild->re-resolve not a fixed point for %q:\n got: %s\nwant: %s",
					tc.sql, got, tc.want)
			}
		})
	}
}
