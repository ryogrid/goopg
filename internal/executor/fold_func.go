package executor

import (
	"strconv"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

func init() {
	optimizer.EvalConstFunc = foldConstFuncCall
}

// foldConstFuncCall is the planner's function folding (optimizer.EvalConstFunc,
// M0146-0123): evaluate_function (clauses.c) runs an immutable built-in over
// constant arguments at plan time, through the same evalFuncCall dispatch a
// row would use, so the folded value is exactly the run-time value. The call
// is IMMUTABLE by the caller's pg_proc check, so it reads no snapshot, clock
// or session state and a bare Context suffices; a panic or an error leaves
// the call unfolded for run time. Only Datums that round-trip exactly
// through resultType's literal node are returned.
func foldConstFuncCall(fc *optimizer.FuncCall, resultType string) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	d, err := evalFuncCall(fc, nil, &Context{CmdID: storage.InvalidCommandId})
	if err != nil || d.IsNull() {
		return "", false
	}
	switch resultType {
	case "int2", "int4", "int8":
		if d.Kind == KindInt {
			return strconv.FormatInt(d.Int, 10), true
		}
	case "numeric":
		switch d.Kind {
		case KindNumeric:
			return numericText(d), true
		case KindInt:
			return strconv.FormatInt(d.Int, 10), true
		}
	case "bool":
		if d.Kind == KindBool {
			return strconv.FormatBool(d.BoolValue()), true
		}
	case "text", "varchar":
		if d.Kind == KindString {
			return d.StringValue(), true
		}
	}
	return "", false
}
