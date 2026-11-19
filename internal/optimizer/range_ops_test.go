package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestRangeOpCall(t *testing.T) {
	rng := &CastExpr{Operand: &StringConst{Value: "[1,5)"}, TargetType: "int4range", SourceType: "unknown"}
	e, ok := rangeOpCall(1, parser.OpContains, rng, &IntegerConst{Value: 2})
	if fc, isCall := e.(*FuncCall); !ok || !isCall || fc.Name != "range_contains_elem"+RangeOpTypeSep+"int4range" || fc.ReturnType != "bool" {
		t.Errorf("@> elem = %#v", e)
	}
	e, ok = rangeOpCall(1, parser.OpOverlap, rng, &StringConst{Value: "[4,8)"})
	fc, _ := e.(*FuncCall)
	if !ok || fc == nil || !strings.HasPrefix(fc.Name, "range_overlaps") {
		t.Fatalf("&& literal = %#v", e)
	}
	if c, isCast := fc.Args[1].(*CastExpr); !isCast || c.TargetType != "int4range" {
		t.Errorf("literal operand = %#v", fc.Args[1])
	}
	e, ok = rangeOpCall(1, parser.OpSub, rng, rng)
	if fc, _ := e.(*FuncCall); !ok || fc == nil || fc.ReturnType != "int4range" {
		t.Errorf("range - range = %#v", e)
	}
	if _, ok := rangeOpCall(1, parser.OpEq, rng, rng); ok {
		t.Error("= stays an ordinary operator")
	}
	if _, ok := rangeOpCall(1, parser.OpAdd, &IntegerConst{Value: 1}, &IntegerConst{Value: 2}); ok {
		t.Error("no range operand")
	}
}
