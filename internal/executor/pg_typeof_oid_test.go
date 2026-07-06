package executor

import "testing"

// TestPgTypeofOIDCast pins the M0122-0005 pg_typeof()::oid follow-up: real
// PostgreSQL declares pg_typeof()'s SQL return type as regtype, whose wire/
// binary representation IS the type's OID (like regclass/regproc) — so
// `pg_typeof(x)::oid` is a binary-compatible reinterpretation of the same
// OID pg_typeof already resolved, not a text-parse of the display name
// through oidin(). Before this fix, pg_typeof returned plain display text
// (e.g. "integer"), so `::oid` tried (and failed) to parse "integer" as a
// number. OID values verified against postgres/src/include/catalog/pg_type_d.h.
func TestPgTypeofOIDCast(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		query   string
		wantOID int64
	}{
		{`SELECT pg_typeof(1::int4)::oid`, 23},   // INT4OID
		{`SELECT pg_typeof(1::int8)::oid`, 20},   // INT8OID
		{`SELECT pg_typeof(1.5)::oid`, 1700},     // NUMERICOID
		{`SELECT pg_typeof('x'::text)::oid`, 25}, // TEXTOID
		{`SELECT pg_typeof(true)::oid`, 16},      // BOOLOID
		{`SELECT pg_typeof(1::"char")::oid`, 18}, // OID-18 "char"
		{`SELECT pg_typeof(NULL)::oid`, 705},     // UNKNOWNOID
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.query)
		if len(rows) != 1 || rows[0][0].Kind != KindInt || rows[0][0].Int != c.wantOID {
			t.Errorf("%s = %v, want oid %d", c.query, rows, c.wantOID)
		}
	}
}

// TestPgTypeofPlainDisplayUnaffected verifies the OID-backed representation
// still renders as the display name (not a raw number) for a plain
// `SELECT pg_typeof(...)` with no `::oid` cast — the wire-output layer
// (planner.exprType's FuncCall case + dispatch.go's regtype formatting)
// must map the underlying OID back to text.
func TestPgTypeofPlainDisplayUnaffected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		query string
		want  string
	}{
		{`SELECT pg_typeof(1::int4)`, "integer"},
		{`SELECT pg_typeof(1.5)`, "numeric"},
		{`SELECT pg_typeof('x'::text)`, "text"},
		{`SELECT pg_typeof(true)`, "boolean"},
		{`SELECT pg_typeof(1::"char")`, `"char"`},
		{`SELECT pg_typeof(NULL)`, "unknown"},
	}
	for _, c := range cases {
		rows := runQuery(t, ctx, c.query)
		if len(rows) != 1 || rows[0][0].Kind != KindInt {
			t.Fatalf("%s = %v, want a single KindInt (OID-backed regtype) row", c.query, rows)
		}
		got := RegtypeName(nil, uint32(rows[0][0].Int))
		if got != c.want {
			t.Errorf("%s render = %q, want %q", c.query, got, c.want)
		}
	}
}
