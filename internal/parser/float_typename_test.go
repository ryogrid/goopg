package parser

import "testing"

// float_typename_test.go — root-0034.
//
// PostgreSQL resolves the SQL-standard `FLOAT [ ( precision ) ]` spelling
// entirely inside the grammar (postgres/src/backend/parser/gram.y,
// opt_float): no precision reduces to SystemTypeName("float8"), 1..24 to
// "float4", 25..53 to "float8", and anything outside [1,53] is an
// ERRCODE_INVALID_PARAMETER_VALUE error. The reduction is a *rename*, so the
// precision never survives as a typmod.
//
// goopg had no such reduction: "float" reached catalog.TypeNameToOID, whose
// `default: return OIDText` fallback silently made a `c3 float` column a text
// column, while the executor's own type tables (internal/executor/codec.go,
// expr.go) still read the name as float8. That encode/decode sibling split
// made `INSERT INTO nametbl VALUES(1,'two',3.0)` report "INSERT 0 1" and then
// return zero rows — the regress `index_including` §10 divergence tracked as
// M-NIGHTLY item (b).

// TestParseColumnTypeFloatAlias pins the opt_float reduction for CREATE TABLE
// column types, including that the precision is consumed (no typmod args).
func TestParseColumnTypeFloatAlias(t *testing.T) {
	cases := []struct {
		sql      string
		wantType string
	}{
		{`CREATE TABLE t (a float)`, "float8"},
		{`CREATE TABLE t (a FLOAT)`, "float8"},
		{`CREATE TABLE t (a float(1))`, "float4"},
		{`CREATE TABLE t (a float(24))`, "float4"},
		{`CREATE TABLE t (a float(25))`, "float8"},
		{`CREATE TABLE t (a float(53))`, "float8"},
		// Unaffected spellings keep resolving as before.
		{`CREATE TABLE t (a float4)`, "float4"},
		{`CREATE TABLE t (a float8)`, "float8"},
		{`CREATE TABLE t (a double precision)`, "float8"},
		{`CREATE TABLE t (a real)`, "real"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		ct := stmts[0].(*CreateTableStmt).Columns[0].Type
		if ct.Name != tc.wantType {
			t.Errorf("%s: type=%q want %q", tc.sql, ct.Name, tc.wantType)
		}
		// opt_float builds a bare SystemTypeName, so the precision must not
		// linger as a typmod (a stray typmod would render as float8(25) in
		// format_type and break pg_dump round-trips).
		if len(ct.Args) != 0 {
			t.Errorf("%s: args=%v want none", tc.sql, ct.Args)
		}
	}
}

// TestParseColumnTypeFloatPrecisionErrors pins PG's two opt_float rejections,
// message text and SQLSTATE included (22023, not the parser's default 42601).
func TestParseColumnTypeFloatPrecisionErrors(t *testing.T) {
	cases := []struct {
		sql     string
		wantMsg string
	}{
		// A negative precision is a plain syntax error in PG too (opt_float's
		// Iconst cannot be signed), so only 0 reaches the "at least 1 bit" arm.
		{`CREATE TABLE t (a float(0))`, "precision for type float must be at least 1 bit"},
		{`CREATE TABLE t (a float(54))`, "precision for type float must be less than 54 bits"},
		{`SELECT '3'::float(54)`, "precision for type float must be less than 54 bits"},
		{`SELECT CAST(3 AS float(0))`, "precision for type float must be at least 1 bit"},
		{`CREATE DOMAIN d AS float(54)`, "precision for type float must be less than 54 bits"},
	}
	for _, tc := range cases {
		_, err := Parse(tc.sql)
		if err == nil {
			t.Errorf("%s: parsed without error", tc.sql)
			continue
		}
		se, ok := err.(*SyntaxError)
		if !ok {
			t.Errorf("%s: err=%T want *SyntaxError", tc.sql, err)
			continue
		}
		if se.Error() != tc.wantMsg {
			t.Errorf("%s: msg=%q want %q", tc.sql, se.Error(), tc.wantMsg)
		}
		if se.Code != "22023" {
			t.Errorf("%s: code=%q want 22023", tc.sql, se.Code)
		}
	}
}

// TestParseCastFloatAlias covers both cast spellings (`::` and CAST(… AS …)).
// The bare `::float` form already resolved to double precision downstream via
// internal/executor's own name table; the precision-bearing form did not, and
// `::float(24)` produced float8 where PG produces real.
func TestParseCastFloatAlias(t *testing.T) {
	cases := []struct {
		sql      string
		wantType string
	}{
		{`SELECT '3'::float`, "float8"},
		{`SELECT '3'::float(24)`, "float4"},
		{`SELECT '3'::float(25)`, "float8"},
		{`SELECT CAST(3 AS float)`, "float8"},
		{`SELECT CAST(3 AS float(20))`, "float4"},
		{`SELECT CAST(3 AS float(53))`, "float8"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		cast, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
		if !ok {
			t.Fatalf("%s: target=%T want *CastExpr", tc.sql, stmts[0].(*SelectStmt).Targets[0].Expr)
		}
		if cast.Type.Name != tc.wantType {
			t.Errorf("%s: type=%q want %q", tc.sql, cast.Type.Name, tc.wantType)
		}
		if len(cast.Typmods) != 0 {
			t.Errorf("%s: typmods=%v want none", tc.sql, cast.Typmods)
		}
	}
}

// TestParseCreateDomainFloatAlias covers CREATE DOMAIN's AS clause, which has
// its own type-name grammar copy.
func TestParseCreateDomainFloatAlias(t *testing.T) {
	cases := []struct {
		sql      string
		wantBase string
	}{
		{`CREATE DOMAIN d AS float`, "float8"},
		{`CREATE DOMAIN d AS float(10)`, "float4"},
		{`CREATE DOMAIN d AS float(40)`, "float8"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.sql, err)
		}
		d := stmts[0].(*CreateDomainStmt)
		if d.BaseType != tc.wantBase {
			t.Errorf("%s: base=%q want %q", tc.sql, d.BaseType, tc.wantBase)
		}
		if len(d.BaseTypeArgs) != 0 {
			t.Errorf("%s: args=%v want none", tc.sql, d.BaseTypeArgs)
		}
	}
}

// TestParseFloatAliasQuotedIsUserType guards the one case that must NOT be
// rewritten: a quoted "float" names a user-defined type, so the standard
// alias must not swallow it. PG's grammar reaches opt_float only from the
// FLOAT_P keyword, never from an IDENT.
func TestParseFloatAliasQuotedIsUserType(t *testing.T) {
	stmts, err := Parse(`CREATE TABLE t (a "float")`)
	if err != nil {
		t.Fatal(err)
	}
	if got := stmts[0].(*CreateTableStmt).Columns[0].Type.Name; got != "float" {
		t.Errorf(`"float" column type=%q want float (unrewritten)`, got)
	}
}

// TestParseFloatArrayAlias covers the array suffix on both the column-type
// path (IsArray flag) and the cast path (name suffix), since the cast paths
// append "[]" to the type name before typmods are parsed.
func TestParseFloatArrayAlias(t *testing.T) {
	stmts, err := Parse(`CREATE TABLE t (a float[])`)
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt).Columns[0].Type
	if ct.Name != "float8" || !ct.IsArray {
		t.Errorf("column type=%q isArray=%v want float8/true", ct.Name, ct.IsArray)
	}

	stmts, err = Parse(`SELECT '{1,2}'::float[]`)
	if err != nil {
		t.Fatal(err)
	}
	cast := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
	if cast.Type.Name != "float8[]" {
		t.Errorf("cast type=%q want float8[]", cast.Type.Name)
	}
}
