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

// currentTimeLiteralTexts reads every current-time literal of the plan
// (optimizer.CurrentTimeLiterals) under the statement's own clock and
// settings, and returns the text the type's output function prints for it.
// PG converts 'today' and friends into a Const at parse time, and EXPLAIN
// prints that value: `(t < '2026-11-22 00:00:00+00'::timestamp with time
// zone)`. goopg keeps evaluating them at run time — its plan cache reuses a
// plan across statements — so only the EXPLAIN text uses the values.
// M0149-0001h1a81.
func currentTimeLiteralTexts(n optimizer.Node, ctx *Context) map[optimizer.Expr]string {
	lits := optimizer.CurrentTimeLiterals(n)
	if len(lits) == 0 {
		return nil
	}
	out := make(map[optimizer.Expr]string, len(lits))
	for _, l := range lits {
		value := ""
		switch x := l.Lit.(type) {
		case *optimizer.StringConst:
			value = x.Value
		case *optimizer.TypedStringLit:
			value = x.Value
		}
		d, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: l.Type, Value: value}, ctx)
		if err != nil || d.Kind != KindTime {
			continue
		}
		out[l.Lit] = dateTimeLiteralText(l.Type, d, ctx)
	}
	return out
}
