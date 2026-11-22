package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// CanonicalDateTimeLiteral reads a date, time, timetz, timestamp or
// timestamptz literal under the given TimeZone and DateStyle and returns the
// text its output function prints, which reads back as the same value in any
// session. The executor registers it; false leaves the literal unchanged.
var CanonicalDateTimeLiteral func(typ, value, zone, dateStyle string) (string, bool)

// canonicalizeDateTimeLiterals fixes every date/time literal of a plan when
// the statement is planned. PG converts such a literal into a Const during
// parse analysis, so a prepared statement keeps the session TimeZone it was
// prepared under, and EXPLAIN prints the converted value. Literals naming the
// current time (now, today, tomorrow, yesterday) are left to run time: goopg's
// plan cache reuses a plan across statements, where PG parses again.
func canonicalizeDateTimeLiterals(n Node, settings PlannerSettings) {
	if CanonicalDateTimeLiteral == nil || n == nil {
		return
	}
	fix := func(typ string, value *string) {
		if !castTargetTakesStringLiteral(typ) || namesCurrentTime(*value) {
			return
		}
		if text, ok := CanonicalDateTimeLiteral(typ, *value, settings.TimeZone, settings.DateStyle); ok {
			*value = text
		}
	}
	done := map[*TypedStringLit]bool{}
	WalkPlanExprsDeep(n, func(e Expr) {
		if b, ok := e.(*BinaryOp); ok && isDateTimeComparison(b.Op) {
			// An untyped literal compared with a date/time operand takes the
			// operand's type, as coerce_type does during parse analysis.
			typeLiteral(&b.Left, b.Right, settings, done)
			typeLiteral(&b.Right, b.Left, settings, done)
			return
		}
		if lit, ok := e.(*TypedStringLit); ok && !lit.CacheValid && !done[lit] {
			done[lit] = true
			fix(lit.Type, &lit.Value)
			return
		}
		if c, ok := e.(*CastExpr); ok && (c.SourceType == "" || c.SourceType == "unknown") {
			if sc, ok := c.Operand.(*StringConst); ok {
				fix(strings.ToLower(c.TargetType), &sc.Value)
			}
		}
	})
}

// typeLiteral replaces *side, a string literal, with a literal of other's
// date/time type holding the canonical text.
func typeLiteral(side *Expr, other Expr, settings PlannerSettings, done map[*TypedStringLit]bool) {
	sc, ok := (*side).(*StringConst)
	if !ok {
		return
	}
	t := exprType(other)
	if t.IsArray {
		return
	}
	typ := commonTypeCanonical(t.Name)
	if !castTargetTakesStringLiteral(typ) {
		return
	}
	if namesCurrentTime(sc.Value) {
		// Typed as the operand, as coerce_type types it, but the value is
		// still read at run time (see canonicalizeDateTimeLiterals).
		// M0149-0001h1a81.
		lit := &TypedStringLit{pos: sc.Pos(), Type: typ, Value: sc.Value}
		done[lit] = true
		*side = lit
		return
	}
	if text, ok := CanonicalDateTimeLiteral(typ, sc.Value, settings.TimeZone, settings.DateStyle); ok {
		lit := &TypedStringLit{pos: sc.Pos(), Type: typ, Value: text}
		done[lit] = true
		*side = lit
	}
}

func namesCurrentTime(value string) bool {
	v := strings.ToLower(value)
	// allballs is a constant (00:00:00), read like any other literal.
	for _, w := range []string{"now", "today", "tomorrow", "yesterday"} {
		if strings.Contains(v, w) {
			return true
		}
	}
	return false
}

func isDateTimeComparison(op parser.OpCode) bool {
	switch op {
	case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		return true
	}
	return false
}

// CurrentTimeLiteral is one literal naming the current time (now, today,
// tomorrow, yesterday) that canonicalizeDateTimeLiterals leaves to run time,
// with the date/time type it is read as. Lit is the *TypedStringLit or the
// *StringConst holding the text.
type CurrentTimeLiteral struct {
	Lit  Expr
	Type string
}

// CurrentTimeLiterals lists the current-time literals of a plan, found the
// way canonicalizeDateTimeLiterals finds the literals it converts: typed
// literals, string casts, and strings compared with a date/time operand.
// EXPLAIN prints them as the instant they denote, as PG's parse-time Const
// does (M0149-0001h1a81).
func CurrentTimeLiterals(n Node) []CurrentTimeLiteral {
	if n == nil {
		return nil
	}
	var out []CurrentTimeLiteral
	seen := map[Expr]bool{}
	add := func(lit Expr, typ, value string) {
		typ = strings.ToLower(typ)
		if seen[lit] || !castTargetTakesStringLiteral(typ) || !namesCurrentTime(value) {
			return
		}
		seen[lit] = true
		out = append(out, CurrentTimeLiteral{Lit: lit, Type: typ})
	}
	WalkPlanExprsDeep(n, func(e Expr) {
		switch x := e.(type) {
		case *BinaryOp:
			if !isDateTimeComparison(x.Op) {
				return
			}
			for _, pair := range [][2]Expr{{x.Left, x.Right}, {x.Right, x.Left}} {
				if sc, ok := pair[0].(*StringConst); ok {
					t := exprType(pair[1])
					if !t.IsArray {
						add(sc, commonTypeCanonical(t.Name), sc.Value)
					}
				}
			}
		case *TypedStringLit:
			add(x, x.Type, x.Value)
		case *CastExpr:
			if sc, ok := x.Operand.(*StringConst); ok && (x.SourceType == "" || x.SourceType == "unknown") {
				add(sc, x.TargetType, sc.Value)
			}
		}
	})
	return out
}
