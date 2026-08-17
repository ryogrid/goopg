package nodes

// M0123-S4 sub-slice 25 — EXPLICIT bare-`numeric` cast RelabelType (relabelformat 1).
//
// This is the explicit counterpart of sub-slice 24's implicit bare-numeric relabel. An
// explicit `(inner)::numeric` cast whose operand CARRIES a length typmod — e.g.
// `(5.5::numeric(8,1))::numeric` — is not a no-op: coerce_type_typmod (parse_coerce.c)
// applies a length coercion to the target typmod -1, which collapses to a RelabelType.
// Because the cast is written explicitly, PG uses COERCE_EXPLICIT_CAST (relabelformat 1),
// and pg_get_expr renders the visible `::numeric` syntax (unlike the implicit
// relabelformat-2 form, which pg_get_expr hides). So Rebuild reconstructs a bare
// (no-typmod) `::numeric` CastExpr, and re-resolving `inner::numeric` re-wraps a byte-
// identical relabelformat-1 RelabelType (fixed point).
//
// Every `want` below is captured LIVE from a real PostgreSQL 18.3 server; the
// oracle_pgnodes_adbin_test.go gate re-derives and diffs these byte-for-byte. These
// resolve through the plain ResolveForColumn path (bare numeric column, typmod -1): the
// explicit cast already strips to typmod -1, so ResolveForColumnTypmod adds no further
// wrapper.

import "testing"

var numericRelabelExplicitGolden = []struct {
	name string
	sql  string
	want string
}{
	// A decimal literal explicitly cast to numeric(8,1), then explicitly cast to bare
	// numeric: the funcformat-1 explicit `::numeric(8,1)` cast nests inside the EXPLICIT
	// relabelformat-1 RelabelType.
	{
		name: "explicit_relabel_decimal_8_1",
		sql:  "(5.5::numeric(8,1))::numeric",
		want: `{RELABELTYPE :arg {FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} :resulttype 1700 :resulttypmod -1 :resultcollid 0 :relabelformat 1 :location -1}`,
	},
	// An int4 literal cast to numeric(8,1) then bare numeric: int4_numeric (1740)
	// implicit cast, the funcformat-1 length cast, then the explicit RelabelType.
	{
		name: "explicit_relabel_int4_8_1",
		sql:  "(5::numeric(8,1))::numeric",
		want: `{RELABELTYPE :arg {FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} :resulttype 1700 :resulttypmod -1 :resultcollid 0 :relabelformat 1 :location -1}`,
	},
}

// TestNumericRelabelExplicitMatchesGolden asserts resolveCastExpr emits the EXPLICIT
// relabelformat-1 RelabelType for a bare `::numeric` cast of a typmod'd numeric operand,
// byte-identical to real PG18's adbin.
func TestNumericRelabelExplicitMatchesGolden(t *testing.T) {
	for _, tc := range numericRelabelExplicitGolden {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidNumeric, -1)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod(%q, numeric, -1) degraded, want canonical", tc.sql)
			}
			if got := Out(n); got != tc.want {
				t.Fatalf("Out mismatch for %q:\n got: %s\nwant: %s", tc.sql, got, tc.want)
			}
		})
	}
}

// TestNumericRelabelExplicitCodecRoundTrip closes the text → IR → text loop.
func TestNumericRelabelExplicitCodecRoundTrip(t *testing.T) {
	for _, tc := range numericRelabelExplicitGolden {
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

// TestNumericRelabelExplicitRebuildRoundTrip proves resolve → Rebuild → re-resolve is a
// fixed point: the explicit RelabelType rebuilds to a visible `(inner)::numeric`
// CastExpr (pg_get_expr renders it), and re-resolving that cast re-wraps a byte-identical
// relabelformat-1 tree, so the DEFAULT reload path is loss-free.
func TestNumericRelabelExplicitRebuildRoundTrip(t *testing.T) {
	for _, tc := range numericRelabelExplicitGolden {
		t.Run(tc.name, func(t *testing.T) {
			n1, ok := ResolveForColumnTypmod(mustParse(t, tc.sql), OidNumeric, -1)
			if !ok {
				t.Fatalf("ResolveForColumnTypmod(%q): degraded", tc.sql)
			}
			ast, err := Rebuild(n1)
			if err != nil {
				t.Fatalf("Rebuild(%q): %v", tc.sql, err)
			}
			n2, ok := ResolveForColumnTypmod(ast, OidNumeric, -1)
			if !ok {
				t.Fatalf("re-ResolveForColumnTypmod(%q): degraded", tc.sql)
			}
			if Out(n1) != Out(n2) {
				t.Fatalf("resolve→Rebuild→re-resolve not a fixed point for %q:\n first: %s\nsecond: %s",
					tc.sql, Out(n1), Out(n2))
			}
		})
	}
}

// TestNumericRelabelExplicitNoWrapWhenNoTypmod guards the branch condition: a bare
// `::numeric` cast of a numeric operand that carries NO typmod (`(5.5::numeric)::numeric`)
// is a true no-op — numericNodeTypmod returns -1, so PG (and goopg) emit NO RelabelType,
// just the inner numeric Const.
func TestNumericRelabelExplicitNoWrapWhenNoTypmod(t *testing.T) {
	n, ok := ResolveForColumnTypmod(mustParse(t, "(5.5::numeric)::numeric"), OidNumeric, -1)
	if !ok {
		t.Fatalf("ResolveForColumnTypmod((5.5::numeric)::numeric, numeric, -1): degraded")
	}
	if _, isRelabel := n.(*RelabelType); isRelabel {
		t.Fatalf("a typmod-less `::numeric` cast should NOT wrap in a RelabelType, got %T", n)
	}
	if _, isConst := n.(*Const); !isConst {
		t.Fatalf("a typmod-less `::numeric` cast should resolve to a bare numeric Const, got %T", n)
	}
}
