package analyzer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCastAssignmentError pins M0149-0001k5a: an explicit cast written as an
// INSERT / UPDATE value is judged against PG's assignment coercions (the
// analyzer types casts as unknown, so isAssignable alone let every cast
// through). Messages are PG 18.3's, measured on the same statements.
func TestCastAssignmentError(t *testing.T) {
	cast := func(typ string) parser.Expr {
		return parser.NewCastExpr(0, parser.NewIntegerConst(0, 1), parser.ObjectName{Name: typ}, nil)
	}
	for _, tc := range []struct {
		src, dst, msg string
	}{
		{"float8", "money", `column "c" is of type money but expression is of type double precision`},
		{"int4", "bool", `column "c" is of type boolean but expression is of type integer`},
		{"text", "int4", `column "c" is of type integer but expression is of type text`},
		{"float8", "date", `column "c" is of type date but expression is of type double precision`},
		{"date", "time", `column "c" is of type time without time zone but expression is of type date`},
		{"int8", "money", ""},
		{"numeric", "money", ""},
		{"int4", "text", ""},
		{"timestamp", "time", ""},
		{"date", "timestamptz", ""},
		{"float8", "mydomain", ""},
	} {
		err := castAssignmentError(cast(tc.src), "c", catalog.Type{Name: tc.dst})
		got := ""
		if err != nil {
			got = err.(*AnalyzeError).Message
		}
		if got != tc.msg {
			t.Errorf("%s -> %s: %q, want %q", tc.src, tc.dst, got, tc.msg)
		}
	}
	if !isAssignable(catalog.Type{Name: "date"}, catalog.Type{Name: "timestamp"}) {
		t.Error("date must be assignable to timestamp")
	}
}
