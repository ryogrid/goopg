package pgnodes

// M0123-S4 sub-slice 24 — bare-`numeric`-column typmod'd-DEFAULT RelabelType.
//
// This closes sub-slice 23's remaining degrade: a BARE `numeric` column (atttypmod
// -1) whose DEFAULT carries a length typmod, e.g. `col numeric DEFAULT 5.5::numeric(8,1)`.
// coerce_type_typmod (parse_coerce.c) reaches its no-op branch here — the target typmod
// is -1, so the numeric() length coercion of sub-slice 23 would do nothing — and PG
// instead emits an IMPLICIT RelabelType (relabelformat 2, COERCE_IMPLICIT_CAST) that
// re-labels the funcformat-1 `::numeric(8,1)` cast's exposed typmod back to the column's
// -1. The RelabelType is invisible to pg_get_expr (an implicit relabel has no `::type`
// syntax), so Rebuild unwraps it (rebuildRelabelType) and re-resolving the inner cast in
// the same bare-column context re-wraps a byte-identical tree (fixed point).
//
// Every `want` below is captured LIVE from a real PostgreSQL 18.3 server; the
// oracle_pgnodes_adbin_test.go gate re-derives and diffs these byte-for-byte. The bare
// column's packed typmod is -1 (numericColSQLTypmod("numeric")), which drives the
// RelabelType branch of ResolveForColumnTypmod.

import "testing"

var numericRelabelGolden = []struct {
	name string
	sql  string
	want string
}{
	// A decimal literal explicitly cast to numeric(8,1), into a bare numeric column:
	// the funcformat-1 explicit cast (sub-slice 22) nests inside the implicit RelabelType.
	{
		name: "bare_numeric_default_explicit_8_1",
		sql:  "5.5::numeric(8,1)",
		want: `{RELABELTYPE :arg {FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -128 5 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} :resulttype 1700 :resulttypmod -1 :resultcollid 0 :relabelformat 2 :location -1}`,
	},
	// An int4 literal cast to numeric(8,1): int4_numeric (1740) implicit cast, then the
	// funcformat-1 length cast, then the RelabelType (a triple nest).
	{
		name: "bare_numeric_default_int4_explicit_8_1",
		sql:  "5::numeric(8,1)",
		want: `{RELABELTYPE :arg {FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1740 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 0 0 0 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} :resulttype 1700 :resulttypmod -1 :resultcollid 0 :relabelformat 2 :location -1}`,
	},
	// An int8-magnitude literal cast to numeric(8,1): int8_numeric (1781) then the wrap.
	{
		name: "bare_numeric_default_int8_explicit_8_1",
		sql:  "5000000000::numeric(8,1)",
		want: `{RELABELTYPE :arg {FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({FUNCEXPR :funcid 1781 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 2 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 20 :consttypmod -1 :constcollid 0 :constlen 8 :constbyval true :constisnull false :location -1 :constvalue 8 [ 0 -14 5 42 1 0 0 0 ]}) :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} :resulttype 1700 :resulttypmod -1 :resultcollid 0 :relabelformat 2 :location -1}`,
	},
	// A negative decimal (doNegate fold) cast to numeric(8,1), then the wrap.
	{
		name: "bare_numeric_default_negative_explicit_8_1",
		sql:  "(-2.5)::numeric(8,1)",
		want: `{RELABELTYPE :arg {FUNCEXPR :funcid 1703 :funcresulttype 1700 :funcretset false :funcvariadic false :funcformat 1 :funccollid 0 :inputcollid 0 :args ({CONST :consttype 1700 :consttypmod -1 :constcollid 0 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 10 [ 40 0 0 0 -128 -96 2 0 -120 19 ]} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 5 0 8 0 0 0 0 0 ]}) :location -1} :resulttype 1700 :resulttypmod -1 :resultcollid 0 :relabelformat 2 :location -1}`,
	},
}

// TestNumericRelabelMatchesGolden asserts ResolveForColumnTypmod wraps a typmod'd
// numeric default in the implicit bare-column RelabelType so the emitted adbin is
// byte-identical to real PG18's for a bare `numeric` column DEFAULT.
func TestNumericRelabelMatchesGolden(t *testing.T) {
	for _, tc := range numericRelabelGolden {
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

// TestNumericRelabelCodecRoundTrip closes the text → IR → text loop for the RelabelType.
func TestNumericRelabelCodecRoundTrip(t *testing.T) {
	for _, tc := range numericRelabelGolden {
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

// TestNumericRelabelRebuildRoundTrip proves resolve → Rebuild → re-resolve is a fixed
// point: the implicit RelabelType unwraps in Rebuild (pg_get_expr renders it invisibly),
// and re-resolving the inner `::numeric(p,s)` cast for the same bare column re-wraps a
// byte-identical tree, so the DEFAULT reload path is loss-free.
func TestNumericRelabelRebuildRoundTrip(t *testing.T) {
	for _, tc := range numericRelabelGolden {
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

// TestNumericRelabelNoWrapWhenNoTypmod guards the branch condition: a bare numeric
// column DEFAULT whose value carries NO typmod (a bare decimal / int literal) resolves
// to the plain node with NO RelabelType — PG stores just the Const. Only a default
// carrying a length typmod (an explicit `::numeric(p,s)` cast) triggers the relabel.
func TestNumericRelabelNoWrapWhenNoTypmod(t *testing.T) {
	// Bare decimal literal: PG stores a plain numeric Const, no relabel.
	n, ok := ResolveForColumnTypmod(mustParse(t, "5.5"), OidNumeric, -1)
	if !ok {
		t.Fatalf("ResolveForColumnTypmod(5.5, numeric, -1): degraded")
	}
	if _, isRelabel := n.(*RelabelType); isRelabel {
		t.Fatalf("bare decimal default should NOT wrap in a RelabelType, got %T", n)
	}
	if _, isConst := n.(*Const); !isConst {
		t.Fatalf("bare decimal default should resolve to a bare numeric Const, got %T", n)
	}

	// Rebuild rejects an unmodeled relabelformat (0 = COERCE_EXPLICIT_CALL, never
	// emitted for these numeric strips) rather than silently dropping it. The
	// EXPLICIT relabelformat 1 form is now modeled (sub-slice 25) and has its own
	// round-trip coverage in numeric_relabel_explicit_test.go.
	if _, err := rebuildRelabelType(&RelabelType{Relabelformat: 0}, Rebuild); err == nil {
		t.Fatalf("rebuildRelabelType should reject an unmodeled (relabelformat 0) node")
	}
}
