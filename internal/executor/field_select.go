package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// evalFieldSelect evaluates optimizer.FieldSelectFuncName — the run-time half
// of `(expr).field` (M0146-0047b): the operand is a composite value in its
// text form, Args[1] the 0-based field position and ReturnType the field's
// type. A NULL composite or a NULL field yields NULL, as PG's FieldSelect
// does (ExecEvalFieldSelect returns NULL for a NULL tuple).
func evalFieldSelect(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) < 2 {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: "malformed field selection"}
	}
	v, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil || v.IsNull() {
		return NullDatum, err
	}
	idxConst, ok := x.Args[1].(*optimizer.IntegerConst)
	if !ok {
		return NullDatum, &ExecError{Code: "XX000", Pos: x.Pos(), Message: "malformed field selection"}
	}
	text := v.StringValue()
	fields, nulls, perr := parseRecordText(text)
	if perr != nil {
		return NullDatum, &ExecError{Code: "22P02", Pos: x.Pos(), Message: fmt.Sprintf("malformed record literal: %q", text), Detail: perr.Error()}
	}
	idx := int(idxConst.Value)
	if idx < 0 || idx >= len(fields) || nulls[idx] {
		return NullDatum, nil
	}
	if x.ReturnType == "" || strings.EqualFold(x.ReturnType, "text") {
		return NewStringDatum(fields[idx]), nil
	}
	return evalCast(NewStringDatum(fields[idx]), x.ReturnType, x.Pos(), ctx)
}

// parseRecordText splits a composite value's text form into its fields —
// record_in's tokenizer (postgres/src/backend/utils/adt/rowtypes.c): fields
// are separated by commas inside the outer parentheses; an empty unquoted
// field is NULL; double quotes group, a doubled quote inside quotes is a
// literal quote, and a backslash escapes the next character anywhere.
func parseRecordText(s string) ([]string, []bool, error) {
	i := 0
	for i < len(s) && isRecordSpace(s[i]) {
		i++
	}
	if i >= len(s) || s[i] != '(' {
		return nil, nil, fmt.Errorf("missing left parenthesis")
	}
	i++
	var fields []string
	var nulls []bool
	// "()" is a record with no columns.
	if i < len(s) && s[i] == ')' {
		return fields, nulls, nil
	}
	for {
		if i < len(s) && (s[i] == ',' || s[i] == ')') {
			fields = append(fields, "")
			nulls = append(nulls, true)
		} else {
			var b strings.Builder
			inQuote := false
			for {
				if i >= len(s) {
					return nil, nil, fmt.Errorf("unexpected end of input")
				}
				ch := s[i]
				if !inQuote && (ch == ',' || ch == ')') {
					break
				}
				i++
				switch {
				case ch == '\\':
					if i >= len(s) {
						return nil, nil, fmt.Errorf("unexpected end of input")
					}
					b.WriteByte(s[i])
					i++
				case ch == '"':
					if !inQuote {
						inQuote = true
					} else if i < len(s) && s[i] == '"' {
						b.WriteByte('"')
						i++
					} else {
						inQuote = false
					}
				default:
					b.WriteByte(ch)
				}
			}
			fields = append(fields, b.String())
			nulls = append(nulls, false)
		}
		if s[i] == ')' {
			return fields, nulls, nil
		}
		i++ // the comma
	}
}

func isRecordSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
