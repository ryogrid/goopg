package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// goopg carries an array value as its text form, so array_in's work for date and
// time elements has to happen on that text: each element goes through the element
// type's input function and comes back in the type's output form. Left alone, an
// element keeps the spelling it was written in, and a later comparison reads it
// without the session time zone.

// datetimeArrayElem returns the element type of a date or time array type name.
func datetimeArrayElem(targetType string) (string, bool) {
	name, isArray := strings.CutSuffix(strings.ToLower(strings.TrimSpace(targetType)), "[]")
	if !isArray {
		return "", false
	}
	return datetimeElemName(name)
}

func datetimeElemName(name string) (string, bool) {
	switch name {
	case "date", "time", "timetz", "timestamp", "timestamptz", "interval":
		return name, true
	case "time without time zone":
		return "time", true
	case "time with time zone":
		return "timetz", true
	case "timestamp without time zone":
		return "timestamp", true
	case "timestamp with time zone":
		return "timestamptz", true
	}
	return "", false
}

// datetimeElemText is the element type's output function for a value of that type.
func datetimeElemText(d Datum, elem string, ctx *Context) string {
	if elem == "time" {
		return timeOfDayText(d)
	}
	return formatDatumDateStyle(d, ctx)
}

// timeOfDayText is time_out: the clock, with fractional digits only when present.
func timeOfDayText(d Datum) string {
	t := d.TimeValue().UTC()
	text := t.Format("15:04:05")
	if micros := t.Nanosecond() / 1000; micros != 0 {
		text += strings.TrimRight(fmt.Sprintf(".%06d", micros), "0")
	}
	return text
}

// timeTZText is timetz_out: the local clock and its offset from UTC, with minutes
// and seconds only when they are not zero.
func timeTZText(d Datum) string {
	offset := d.TimeTZOffsetSecs()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	text := fmt.Sprintf("%s%s%02d", timeOfDayText(d), sign, offset/3600)
	if offset%3600 != 0 {
		text += fmt.Sprintf(":%02d", offset%3600/60)
		if offset%60 != 0 {
			text += fmt.Sprintf(":%02d", offset%60)
		}
	}
	return text
}

// normalizeDatetimeArray is array_in for a date or time element type. Text that is
// not a plain brace literal, such as one with explicit bounds, is returned as is.
func normalizeDatetimeArray(text, elem string, pos int, ctx *Context) (string, error) {
	s := strings.TrimSpace(text)
	if !strings.HasPrefix(s, "{") {
		return text, nil
	}
	malformed := func() (string, error) {
		return "", &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("malformed array literal: %q", text)}
	}
	var out strings.Builder
	i := 0
	skipSpace := func() {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
	}
	writeElem := func(raw string) error {
		v, err := evalCast(NewStringDatum(raw), elem, pos, ctx)
		if err != nil {
			return err
		}
		out.WriteString(quoteArrayTextElem(datetimeElemText(v, elem, ctx)))
		return nil
	}
	var level func() bool
	var elemErr error
	level = func() bool {
		i++ // the opening brace
		out.WriteByte('{')
		skipSpace()
		if i < len(s) && s[i] == '}' {
			i++
			out.WriteByte('}')
			return true
		}
		for {
			skipSpace()
			if i >= len(s) {
				return false
			}
			switch s[i] {
			case '{':
				if !level() {
					return false
				}
			case '"':
				var raw strings.Builder
				closed := false
				for i++; i < len(s); i++ {
					if s[i] == '"' {
						i++
						closed = true
						break
					}
					if s[i] == '\\' && i+1 < len(s) {
						i++
					}
					raw.WriteByte(s[i])
				}
				if !closed {
					return false
				}
				if elemErr = writeElem(raw.String()); elemErr != nil {
					return false
				}
			default:
				start := i
				for i < len(s) && s[i] != ',' && s[i] != '}' && s[i] != '{' && s[i] != '"' {
					i++
				}
				raw := strings.TrimSpace(s[start:i])
				if raw == "" {
					return false
				}
				if strings.EqualFold(raw, "NULL") {
					out.WriteString("NULL")
				} else if elemErr = writeElem(raw); elemErr != nil {
					return false
				}
			}
			skipSpace()
			if i >= len(s) {
				return false
			}
			switch s[i] {
			case ',':
				i++
				out.WriteByte(',')
			case '}':
				i++
				out.WriteByte('}')
				return true
			default:
				return false
			}
		}
	}
	ok := level()
	if elemErr != nil {
		return "", elemErr
	}
	skipSpace()
	if !ok || i != len(s) {
		return malformed()
	}
	return out.String(), nil
}

// normalizeConstructedDatetimeArray applies array_in's rule to ARRAY[...] when its
// element type is a date or time type, so an untyped element takes that type too.
func normalizeConstructedDatetimeArray(x *optimizer.FuncCall, text string, ctx *Context) (string, error) {
	t := optimizer.ExprType(x)
	elem, ok := datetimeElemName(strings.TrimSuffix(strings.ToLower(t.Name), "[]"))
	if !ok {
		return text, nil
	}
	return normalizeDatetimeArray(text, elem, x.Pos(), ctx)
}
