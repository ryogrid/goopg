package optimizer

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func timeArithCast(target string) Expr {
	return &CastExpr{Operand: &StringConst{Value: "x"}, TargetType: target, SourceType: "unknown"}
}

func TestTimeArithCallResolvesFunctions(t *testing.T) {
	cases := []struct {
		op       parser.OpCode
		l, r     string
		fn, kind string
		swapped  bool
	}{
		{parser.OpAdd, "time", "interval", "time_pl_interval", "time", false},
		{parser.OpSub, "timetz", "interval", "timetz_mi_interval", "timetz", false},
		{parser.OpAdd, "interval", "time", "time_pl_interval", "time", true},
		{parser.OpAdd, "interval", "timetz", "timetz_pl_interval", "timetz", true},
		{parser.OpSub, "time", "time", "time_mi_time", "interval", false},
		{parser.OpAdd, "date", "time", "datetime_pl", "timestamp", false},
		{parser.OpAdd, "time", "date", "datetime_pl", "timestamp", true},
		{parser.OpAdd, "timetz", "date", "datetimetz_pl", "timestamptz", true},
	}
	for _, c := range cases {
		l, r := timeArithCast(c.l), timeArithCast(c.r)
		e, ok, err := timeArithCall(1, c.op, l, r)
		if err != nil || !ok {
			t.Fatalf("%s %s %s: ok=%v err=%v", c.l, c.op, c.r, ok, err)
		}
		fc, isCall := e.(*FuncCall)
		if !isCall || fc.Name != c.fn || fc.ReturnType != c.kind {
			t.Errorf("%s %s %s = %#v", c.l, c.op, c.r, e)
			continue
		}
		first := l
		if c.swapped {
			first = r
		}
		if fc.Args[0] != first {
			t.Errorf("%s %s %s: wrong argument order", c.l, c.op, c.r)
		}
	}
}

func TestTimeArithCallImplicitInterval(t *testing.T) {
	e, ok, err := timeArithCall(1, parser.OpSub, timeArithCast("date"), timeArithCast("time"))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	b, isBinary := e.(*BinaryOp)
	if !isBinary || b.ResultType != "timestamp" {
		t.Fatalf("got %#v", e)
	}
	if c, isCast := b.Right.(*CastExpr); !isCast || c.TargetType != "interval" || c.SourceType != "time" {
		t.Errorf("right operand = %#v", b.Right)
	}
}

func TestTimeArithCallRejects(t *testing.T) {
	_, _, err := timeArithCall(1, parser.OpAdd, timeArithCast("timetz"), &StringConst{Value: "1 hour"})
	var pe *PlanError
	if !errors.As(err, &pe) || pe.Code != "42725" {
		t.Errorf("timetz + unknown: %v", err)
	}
	_, _, err = timeArithCall(1, parser.OpSub, timeArithCast("time"), timeArithCast("date"))
	if !errors.As(err, &pe) || pe.Code != "42883" {
		t.Errorf("time - date: %v", err)
	}
	if _, ok, err := timeArithCall(1, parser.OpAdd, timeArithCast("timestamp"), timeArithCast("interval")); ok || err != nil {
		t.Errorf("timestamp + interval must stay an ordinary operator: ok=%v err=%v", ok, err)
	}
}
