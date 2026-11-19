package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

func init() {
	optimizer.CanonicalDateTimeLiteral = canonicalDateTimeLiteral
}

// canonicalDateTimeLiteral reads value as typ under zone and dateStyle, as
// the type's input function does at parse time, and returns the output
// function's text under the same settings. That text must read back as the
// same value; when it does not, the ISO form is tried, and failing that the
// literal is left as written.
func canonicalDateTimeLiteral(typ, value, zone, dateStyle string) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	settings := func(style string) *Context {
		return &Context{CmdID: storage.InvalidCommandId, GetSetting: func(name string) (string, bool) {
			switch strings.ToLower(name) {
			case "timezone":
				return zone, zone != ""
			case "datestyle":
				return style, style != ""
			}
			return "", false
		}}
	}
	read := func(v string, ctx *Context) (Datum, bool) {
		d, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: typ, Value: v}, ctx)
		return d, err == nil && d.Kind == KindTime
	}
	ctx := settings(dateStyle)
	d, ok := read(value, ctx)
	if !ok {
		return "", false
	}
	for _, style := range []string{dateStyle, "ISO, MDY"} {
		c := settings(style)
		out := dateTimeLiteralText(typ, d, c)
		if back, ok := read(out, ctx); ok && back.Int == d.Int && back.Scale == d.Scale && back.TimeSub == d.TimeSub {
			return out, true
		}
	}
	return "", false
}

func dateTimeLiteralText(typ string, d Datum, ctx *Context) string {
	switch typ {
	case "time":
		return timeOfDayText(d)
	case "timetz":
		return timeTZText(d)
	}
	return formatDatumDateStyle(d, ctx)
}
