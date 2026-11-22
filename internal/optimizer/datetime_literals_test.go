package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCanonicalizeDateTimeLiterals: typed literals, casts of a string and a
// string compared with a date/time operand are rewritten; a literal naming the
// current time is not.
func TestCanonicalizeDateTimeLiterals(t *testing.T) {
	saved := CanonicalDateTimeLiteral
	defer func() { CanonicalDateTimeLiteral = saved }()
	CanonicalDateTimeLiteral = func(typ, value, zone, style, _ string) (string, bool) {
		return typ + ":" + value + "@" + zone, true
	}
	typed := &TypedStringLit{Type: "timestamptz", Value: "2020-01-01"}
	cast := &CastExpr{Operand: &StringConst{Value: "2020-01-02"}, TargetType: "date", SourceType: "unknown"}
	now := &TypedStringLit{Type: "timestamptz", Value: "now"}
	cmp := &BinaryOp{Op: parser.OpEq, Left: typed, Right: &StringConst{Value: "x"}}
	plan := &Project{Targets: []Expr{cmp, cast, now}}
	canonicalizeDateTimeLiterals(plan, PlannerSettings{TimeZone: "Z"})
	if typed.Value != "timestamptz:2020-01-01@Z" {
		t.Errorf("typed literal = %q", typed.Value)
	}
	if v := cast.Operand.(*StringConst).Value; v != "date:2020-01-02@Z" {
		t.Errorf("cast operand = %q", v)
	}
	if now.Value != "now" {
		t.Errorf("now was rewritten: %q", now.Value)
	}
	if lit, ok := cmp.Right.(*TypedStringLit); !ok || lit.Type != "timestamptz" || lit.Value != "timestamptz:x@Z" {
		t.Errorf("compared literal = %#v", cmp.Right)
	}
}
