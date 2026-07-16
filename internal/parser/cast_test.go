package parser

import "testing"

// TestParseCastSimpleSchemaQualified pins the pgbench-i probe shape
// `SELECT relkind FROM pg_catalog.pg_class WHERE oid =
// $1::pg_catalog.regclass`. The cast is the only piece this loop
// adds; the rest of the SELECT is already supported.
func TestParseCastSimpleSchemaQualified(t *testing.T) {
	stmts, err := Parse(`SELECT 1 WHERE oid = $1::pg_catalog.regclass`)
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if s.Where == nil {
		t.Fatal("missing WHERE")
	}
	bin, ok := s.Where.(*BinaryOp)
	if !ok {
		t.Fatalf("WHERE=%T want *BinaryOp", s.Where)
	}
	cast, ok := bin.Right.(*CastExpr)
	if !ok {
		t.Fatalf("right=%T want *CastExpr", bin.Right)
	}
	if _, ok := cast.Operand.(*ParamRef); !ok {
		t.Errorf("cast.Operand=%T want *ParamRef", cast.Operand)
	}
	if cast.Type.Schema != "pg_catalog" || cast.Type.Name != "regclass" {
		t.Errorf("cast.Type=%+v want pg_catalog.regclass", cast.Type)
	}
}

// TestParseCastTypmod covers the `(N)` and `(N,M)` typmod tail.
func TestParseCastTypmod(t *testing.T) {
	stmts, err := Parse(`SELECT 'x'::varchar(10)`)
	if err != nil {
		t.Fatal(err)
	}
	cast, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
	if !ok {
		t.Fatalf("target=%T", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if cast.Type.Name != "varchar" {
		t.Errorf("type=%q", cast.Type.Name)
	}
	if len(cast.Typmods) != 1 || cast.Typmods[0] != 10 {
		t.Errorf("typmods=%v", cast.Typmods)
	}
}

// TestParseCastMultiWordTypeName covers DU-002's `'na'::character varying`
// gap: pg_dump emits multi-word built-in type names (character varying,
// double precision, timestamp with/without time zone, bit varying) as CAST
// targets in DEFAULT-expression dumps, e.g. `CREATE DOMAIN ... DEFAULT
// 'na'::character varying`. parseTypeNameAfterCast previously consumed only
// a single identifier, so the trailing keyword ("varying"/"precision"/…) was
// left dangling and produced a syntax error; it now shares
// parseMultiWordTypeName with parseColumnType/parseCreateDomain's AS-clause
// (CREATE TABLE/CREATE DOMAIN column-type) paths.
func TestParseCastMultiWordTypeName(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantType string
	}{
		{"character varying bare", `SELECT 'x'::character varying`, "varchar"},
		{"character varying typmod", `SELECT 'x'::character varying(20)`, "varchar"},
		{"double precision", `SELECT 1::double precision`, "float8"},
		{"bit varying", `SELECT '1'::bit varying`, "varbit"},
		{"timestamp with time zone", `SELECT now()::timestamp with time zone`, "timestamptz"},
		{"timestamp without time zone", `SELECT now()::timestamp without time zone`, "timestamp"},
		{"CAST double precision", `SELECT CAST(1 AS double precision)`, "float8"},
		{"CAST character varying", `SELECT CAST('x' AS character varying)`, "varchar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			cast, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
			if !ok {
				t.Fatalf("target=%T want *CastExpr", stmts[0].(*SelectStmt).Targets[0].Expr)
			}
			if cast.Type.Name != tc.wantType {
				t.Errorf("type=%q want %q", cast.Type.Name, tc.wantType)
			}
		})
	}
}

// TestParseCastSchemaQualifiedNotMultiWord ensures the multi-word lookahead
// added for TestParseCastMultiWordTypeName does not misfire on a
// schema-qualified single-word type name (the `.`-branch returns before
// parseMultiWordTypeName is consulted).
func TestParseCastSchemaQualifiedNotMultiWord(t *testing.T) {
	stmts, err := Parse(`SELECT 1::pg_catalog.int4`)
	if err != nil {
		t.Fatal(err)
	}
	cast, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
	if !ok {
		t.Fatalf("target=%T want *CastExpr", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if cast.Type.Schema != "pg_catalog" || cast.Type.Name != "int4" {
		t.Errorf("cast.Type=%+v want pg_catalog.int4", cast.Type)
	}
}

// TestParseCastChained: `expr::int8::text` should produce a
// CastExpr whose Operand is itself a CastExpr.
func TestParseCastChained(t *testing.T) {
	stmts, err := Parse(`SELECT 1::int8::text`)
	if err != nil {
		t.Fatal(err)
	}
	outer, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
	if !ok {
		t.Fatalf("got %T", stmts[0].(*SelectStmt).Targets[0].Expr)
	}
	if outer.Type.Name != "text" {
		t.Errorf("outer type=%q want text", outer.Type.Name)
	}
	inner, ok := outer.Operand.(*CastExpr)
	if !ok {
		t.Fatalf("inner=%T want *CastExpr", outer.Operand)
	}
	if inner.Type.Name != "int8" {
		t.Errorf("inner type=%q want int8", inner.Type.Name)
	}
}

// TestParseCastCharDisambiguation covers M0122-0005's "1-byte char (OID 18)
// disambiguation" gap: the parser must distinguish the bare CHAR keyword
// (PostgreSQL's grammar maps it to bpchar with an implicit length of 1) from
// the quoted identifier "char" (pg_type OID 18, a distinct 1-byte internal
// type that never carries a typmod). Since both produce Type.Name=="char",
// an explicit typmod is the only signal later stages (exprType, typeOIDFor,
// pgTypeofDisplayName) have to tell them apart — bare char must synthesize
// typmod 1 (like the existing ddl.go:parseColumnType path already does for
// column declarations), while quoted "char" must have none.
func TestParseCastCharDisambiguation(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		typmods []int64
	}{
		{"bare unquoted char", `SELECT 'x'::char`, []int64{1}},
		{"bare char with explicit length", `SELECT 'x'::char(3)`, []int64{3}},
		{"quoted char", `SELECT 'x'::"char"`, nil},
		{"CAST(...AS char)", `SELECT CAST('x' AS char)`, []int64{1}},
		{`CAST(...AS "char")`, `SELECT CAST('x' AS "char")`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			cast, ok := stmts[0].(*SelectStmt).Targets[0].Expr.(*CastExpr)
			if !ok {
				t.Fatalf("target=%T want *CastExpr", stmts[0].(*SelectStmt).Targets[0].Expr)
			}
			if cast.Type.Name != "char" {
				t.Errorf("type=%q want char", cast.Type.Name)
			}
			if len(cast.Typmods) != len(tc.typmods) {
				t.Fatalf("typmods=%v want %v", cast.Typmods, tc.typmods)
			}
			for i := range tc.typmods {
				if cast.Typmods[i] != tc.typmods[i] {
					t.Errorf("typmods=%v want %v", cast.Typmods, tc.typmods)
				}
			}
		})
	}
}
