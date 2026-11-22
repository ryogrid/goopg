package executor

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0149-0001h1a81a: a view's date/time literals are read when the view is
// created.
//
// PG turns `t = '2020-01-01 00:00'` into a timestamptz Const during parse
// analysis of CREATE VIEW, under the creating session's TimeZone and
// DateStyle; the stored rule keeps that instant, so selecting from the view
// in another zone compares with the same instant. goopg keeps the view's
// source text and re-plans it at each use, where canonicalizeDateTimeLiterals
// would read the literal in the querying session's zone. The literals are
// therefore rewritten in the stored text at creation: each date/time literal
// the canonicalizer converts under the creating session's settings is
// replaced by the converted text, printed in ISO style so that it reads back
// as the same value under any DateStyle (a timestamptz carries its offset).

type viewLiteralRewrite struct {
	start, end int // byte span of the quoted literal in the view text
	text       string
}

// bindViewDateTimeLiterals returns the view body with its date/time literals
// fixed under the creating session's TimeZone and DateStyle, and its parse;
// ok is false when nothing changed or the rewrite could not be verified, in
// which case the caller keeps the original text and query.
func bindViewDateTimeLiterals(ctx *Context, rawDef string, cat catalog.Catalog) (string, *parser.SelectStmt, bool) {
	if strings.TrimSpace(rawDef) == "" || ctx == nil {
		return "", nil, false
	}
	stmts, err := parser.Parse(rawDef)
	if err != nil || len(stmts) != 1 {
		return "", nil, false
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		return "", nil, false
	}
	settings := optimizer.DefaultPlannerSettings()
	settings.TimeZone = timeZoneFromCtx(ctx)
	settings.DateStyle = "ISO, " + dateStyleOrder(ctx)
	plan, err := optimizer.PlanWithSettings(sel, cat, settings)
	if err != nil || plan == nil {
		return "", nil, false
	}
	var rewrites []viewLiteralRewrite
	seen := map[int]bool{}
	note := func(pos int, value string) {
		if pos < 0 || pos >= len(rawDef) || seen[pos] {
			return
		}
		start, end, old, ok := quotedLiteralAt(rawDef, pos)
		if !ok || old == value {
			return
		}
		seen[pos] = true
		rewrites = append(rewrites, viewLiteralRewrite{start: start, end: end, text: "'" + strings.ReplaceAll(value, "'", "''") + "'"})
	}
	optimizer.WalkPlanExprsDeep(plan, func(e optimizer.Expr) {
		switch x := e.(type) {
		case *optimizer.TypedStringLit:
			if isDateTimeLiteralType(x.Type) {
				note(x.Pos(), x.Value)
			}
		case *optimizer.CastExpr:
			if sc, ok := x.Operand.(*optimizer.StringConst); ok && isDateTimeLiteralType(x.TargetType) {
				note(sc.Pos(), sc.Value)
			}
		}
	})
	if len(rewrites) == 0 {
		return "", nil, false
	}
	sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].start > rewrites[j].start })
	out := rawDef
	last := len(out) + 1
	for _, r := range rewrites {
		if r.end > last { // overlapping spans: give up rather than guess
			return "", nil, false
		}
		out = out[:r.start] + r.text + out[r.end:]
		last = r.start
	}
	reparsed, err := parser.Parse(out)
	if err != nil || len(reparsed) != 1 {
		return "", nil, false
	}
	newSel, ok := reparsed[0].(*parser.SelectStmt)
	if !ok {
		return "", nil, false
	}
	return out, newSel, true
}

// quotedLiteralAt finds the plain single-quoted string literal a literal
// node at pos stands for — at pos itself, or after a type name (`timestamptz
// '...'`) — and returns its span and decoded value.
func quotedLiteralAt(s string, pos int) (start, end int, value string, ok bool) {
	i := pos
	for i < len(s) && i < pos+64 && s[i] != '\'' {
		c := s[i]
		if c == ';' || c == ',' || c == ')' || c == '(' || c == '"' || c == '$' {
			return 0, 0, "", false
		}
		i++
	}
	if i >= len(s) || s[i] != '\'' {
		return 0, 0, "", false
	}
	if i > 0 && (s[i-1] == 'E' || s[i-1] == 'e' || s[i-1] == '&' || s[i-1] == 'B' || s[i-1] == 'X' || s[i-1] == 'b' || s[i-1] == 'x') {
		return 0, 0, "", false // escape, Unicode and bit strings are not rewritten
	}
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\'' {
			if j+1 < len(s) && s[j+1] == '\'' {
				b.WriteByte('\'')
				j++
				continue
			}
			return i, j + 1, b.String(), true
		}
		b.WriteByte(s[j])
	}
	return 0, 0, "", false
}

func isDateTimeLiteralType(t string) bool {
	switch strings.ToLower(t) {
	case "date", "time", "timetz", "timestamp", "timestamptz",
		"time without time zone", "time with time zone",
		"timestamp without time zone", "timestamp with time zone":
		return true
	}
	return false
}

// dateStyleOrder is the input field order of the session DateStyle (MDY,
// DMY or YMD).
func dateStyleOrder(ctx *Context) string {
	style := "ISO, MDY"
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("datestyle"); ok && v != "" {
			style = v
		}
	}
	up := strings.ToUpper(style)
	for _, o := range []string{"DMY", "YMD", "MDY"} {
		if strings.Contains(up, o) {
			return o
		}
	}
	if strings.Contains(up, "EURO") {
		return "DMY"
	}
	return "MDY"
}
