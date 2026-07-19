package pgnodes

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// datetime_cast_test.go — M0123-S4 sub-slice 27 gate: an explicit
// `::date` / `::timestamptz` cast of a BARE string literal folds to the by-value
// Const at parse time, byte-for-byte identical to the same literal in a
// date/timestamptz COLUMN context. PG's coerce_type folds an unknown-type literal
// via stringTypeToConst→the type input function, so `'2024-03-15'::date` stores a
// bare DateADT Const with NO cast node — the `::type` supplies the target type but
// adds no wrapper. This closes the asymmetry where the bare `DEFAULT '2024-03-15'`
// form was canonical (sub-slice 26 / 4b) but the explicit-cast form degraded.
//
// Each `want` is the SAME golden byte string a sub-slice-26/4b column-context fold
// produces (and is re-derived LIVE by the oracle gate
// internal/testport/oracle_pgnodes_adbin_test.go, which now stores these
// explicit-cast DEFAULTs on a real PG18.3 and diffs the adbin).
var datetimeCastGolden = []struct {
	name string
	sql  string // the explicit-cast expression exactly as written
	oid  uint32 // the folded Const's result type OID
	want string
}{
	{
		name: "date_cast_post_2000", // '2024-03-15'::date → day 8840, identical to the bare fold
		sql:  "'2024-03-15'::date",
		oid:  OidDate,
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -120 34 0 0 0 0 0 0 ]}`,
	},
	{
		name: "date_cast_pre_epoch", // '1999-12-31'::date → day -1, sign-extended
		sql:  "'1999-12-31'::date",
		oid:  OidDate,
		want: `{CONST :consttype 1082 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ -1 -1 -1 -1 -1 -1 -1 -1 ]}`,
	},
	{
		name: "timestamptz_cast_offset", // '2024-01-15 10:30:00+00'::timestamptz, deterministic (explicit offset)
		sql:  "'2024-01-15 10:30:00+00'::timestamptz",
		oid:  OidTimestamptz,
		want: `{CONST :consttype 1184 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -70 -66 67 -8 -79 2 0 ]}`,
	},
}

// TestDatetimeCastResolveMatchesGolden is the forward oracle: an explicit
// `::date`/`::timestamptz` cast of a string literal resolves byte-identical to
// PG18.3's folded adbin — with an UNKNOWN context (expected=0), proving the target
// type comes from the cast, not the column.
func TestDatetimeCastResolveMatchesGolden(t *testing.T) {
	for _, tc := range datetimeCastGolden {
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
			// The cast folds to the column type, so ResolveForColumn must accept it as
			// canonical (top type == the cast target), not degrade to SQL text.
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.oid); !ok {
				t.Fatalf("ResolveForColumn(%q, oid=%d) rejected a valid explicit-cast default", tc.sql, tc.oid)
			}
		})
	}
}

// TestDatetimeCastMatchesBareFold proves the explicit-cast form is byte-identical
// to the bare literal folded in the equivalent column context — the whole premise
// of the sub-slice (the `::type` supplies the target type but adds no wrapper).
func TestDatetimeCastMatchesBareFold(t *testing.T) {
	pairs := []struct {
		cast, bare string
		oid        uint32
	}{
		{"'2024-03-15'::date", "'2024-03-15'", OidDate},
		{"'1999-12-31'::date", "'1999-12-31'", OidDate},
		{"'2024-01-15 10:30:00+00'::timestamptz", "'2024-01-15 10:30:00+00'", OidTimestamptz},
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

// TestDatetimeCastGracefulDegradation guards the fold boundary: a non-string
// operand (a genuine conversion, not a literal fold), an unparseable/invalid
// literal, a TimeZone-dependent timestamptz form, a typmod-qualified target, and
// an unmodeled date/time type all degrade to SQL text rather than fold a wrong
// datum. ResolveForColumn(..., ok=false) is the SQL-text fallback signal.
func TestDatetimeCastGracefulDegradation(t *testing.T) {
	cases := []struct {
		sql string
		oid uint32
	}{
		{"now()::date", OidDate},                               // non-string operand = real conversion node
		{"'garbage'::date", OidDate},                           // unparseable literal
		{"'2024-13-01'::date", OidDate},                        // month out of range
		{"'2024-02-30'::date", OidDate},                        // Feb 30
		{"'2024-01-15 10:30:00'::timestamptz", OidTimestamptz}, // no offset → TimeZone-dependent
		{"'garbage'::timestamptz", OidTimestamptz},             // unparseable literal
		{"'2024-03-15'::timestamp", 1114},                      // timestamp (no tz) has no modeled datum
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			if _, ok := ResolveForColumn(mustParse(t, tc.sql), tc.oid); ok {
				t.Fatalf("ResolveForColumn(%q, oid=%d) accepted a form that must degrade to SQL text", tc.sql, tc.oid)
			}
		})
	}
}

// TestDatetimeCastReloadFixedPoint proves the reload path holds: the folded Const
// rebuilds to a bare "YYYY-MM-DD" / UTC-timestamp literal which, re-resolved in the
// COLUMN context PG's build_column_default restores, reproduces the identical
// datum. (Rebuild drops the redundant cast exactly as pg_get_expr's column-default
// deparse relies on the attribute type — the fixed point is column-scoped, matching
// sub-slice 26's rebuild contract.)
func TestDatetimeCastReloadFixedPoint(t *testing.T) {
	for _, tc := range datetimeCastGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, err := ResolveExpr(mustParse(t, tc.sql), 0)
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
