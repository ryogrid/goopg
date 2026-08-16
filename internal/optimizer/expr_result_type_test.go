package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// exprTypeFixture is a table whose columns cover the operand types the
// resolution arms below exercise. ResolveIndexPredicate binds a bare column
// reference against it, which is exactly how CREATE INDEX resolves an index
// expression, so these tests run the production resolution path rather than
// hand-built plan nodes.
func exprTypeFixture() *catalog.Table {
	return &catalog.Table{
		Schema: "public",
		Name:   "t",
		Columns: []catalog.Column{
			{Name: "i", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
			{Name: "b", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
			{Name: "n", Type: catalog.Type{Name: "numeric"}, Ordinal: 2},
			{Name: "s", Type: catalog.Type{Name: "text"}, Ordinal: 3},
			{Name: "ts", Type: catalog.Type{Name: "timestamp"}, Ordinal: 4},
			{Name: "flag", Type: catalog.Type{Name: "bool"}, Ordinal: 5},
			{Name: "m", Type: catalog.Type{Name: "mood"}, Ordinal: 6}, // user enum
		},
	}
}

func resolveForTest(t *testing.T, sql string) Expr {
	t.Helper()
	pe, err := parser.ParseExpr(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	e, err := ResolveIndexPredicate(pe, exprTypeFixture())
	if err != nil || e == nil {
		t.Fatalf("resolve %q: %v", sql, err)
	}
	return e
}

// TestExprResultTypeResolves pins the types an index-expression key column can
// be given. The function-call and operator arms are read out of the PG 18
// pg_proc / pg_operator seed rather than a local name table, so a wrong answer
// here means goopg disagrees with what a real backend's parse analysis would
// record for the same expression.
func TestExprResultTypeResolves(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want string
	}{
		{"i", "int4"},
		{"s", "text"},
		{"lower(s)", "text"},
		{"upper(s)", "text"},
		{"length(s)", "int4"},
		{"md5(s)", "text"},
		{"abs(i)", "int4"},
		{"abs(n)", "numeric"},
		{"i::text", "text"},
		{"n::int8", "int8"},
		{"s || 'x'", "text"},
		{"i + 1", "int4"},
		{"b * 2", "int8"},
		{"date_trunc('day', ts)", "timestamp"},
		{"-i", "int4"},
		{"NOT flag", "bool"},
		{"i IS NULL", "bool"},
		{"'lit'", "text"},
		{"1", "int4"},
		{"1.5", "numeric"},
		{"true", "bool"},
		{"CASE WHEN flag THEN s ELSE 'x' END", "text"},
		{"CASE WHEN flag THEN i ELSE 0 END", "int4"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got, ok := ExprResultType(resolveForTest(t, tc.sql))
			if !ok {
				t.Fatalf("ExprResultType(%q) declined; want %s", tc.sql, tc.want)
			}
			if got.Name != tc.want {
				t.Errorf("ExprResultType(%q) = %q, want %q", tc.sql, got.Name, tc.want)
			}
		})
	}
}

// TestExprTypeArrayConcat pins the planner exprType OpConcat arm's array
// result: `||` over array operands is array_cat / array_append /
// array_prepend and advertises the array side's type, so the wire layer sends
// a text[] TypeOID (psql \d+ reloptions). Twin of analyzer analyzeExpr's
// OpConcat arm — both must detect arrays the same way and return the same
// result type. M0134-0002 C1.
func TestExprTypeArrayConcat(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		want string
	}{
		{"'{a,b}'::text[] || '{c,d}'::text[]", "text[]"}, // array_cat
		{"'{a,b}'::text[] || 'c'::text", "text[]"},       // array_append
		{"'c'::text || '{a,b}'::text[]", "text[]"},       // array_prepend
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got := exprType(resolveForTest(t, tc.sql))
			if got.Name != tc.want {
				t.Errorf("exprType(%q) = %q, want %q", tc.sql, got.Name, tc.want)
			}
		})
	}
}

// TestExprResultTypeDeclinesUnknown is the half that protects callers: a key
// decoder handed a confidently wrong type misreads key bytes, so anything not
// resolvable through the seed must come back ok=false instead of defaulting to
// text the way inferExprType does. The `m` column is a user-defined enum, whose
// name catalog.TypeNameToOID silently maps to text(25) — the exact fallback
// exactTypeOID exists to distrust.
func TestExprResultTypeDeclinesUnknown(t *testing.T) {
	for _, sql := range []string{
		"lower(m)",  // enum arg → the text(25) fallback must not be trusted
		"no_such_f(i)",
	} {
		t.Run(sql, func(t *testing.T) {
			if got, ok := ExprResultType(resolveForTest(t, sql)); ok {
				t.Errorf("ExprResultType(%q) resolved to %q; want a decline — a "+
					"wrong type here misreads index key bytes", sql, got.Name)
			}
		})
	}
}

// TestExprResultTypeEnumColumnPassesThrough guards the one case where a
// user-defined type IS trustworthy: a bare column reference carries its
// declared catalog type, no OID round-trip involved.
func TestExprResultTypeEnumColumnPassesThrough(t *testing.T) {
	got, ok := ExprResultType(resolveForTest(t, "m"))
	if !ok || got.Name != "mood" {
		t.Fatalf("ExprResultType(m) = (%q, %v), want (mood, true)", got.Name, ok)
	}
}
