package planner

// Qualifier-aware GROUP BY keys (M0125-0044).
//
// parserExprKey drops a ColumnRef's table/schema qualifier on purpose: GROUP BY
// c has to satisfy SELECT t.c, and GROUP BY lower(c) has to satisfy SELECT
// lower(t.c), and at parser level — before column resolution — the qualifier is
// the only thing telling those apart (M0097-0003). The price is that every
// alias of a self-joined table hashes to ONE key. With
//
//	GROUP BY d1.d_year, d2.d_year
//
// both items key as "c:d_year", the map that answers "which output slot does
// this expression occupy" keeps only the last, and both SELECT columns read
// that slot. TPC-DS Q64 then reported first-sales years as sold years, with the
// group count and the row count intact — the same silent shape M0125-0009
// found for aggregate slots.
//
// The fix does not change parserExprKey. It adds a SECOND key that keeps the
// qualifiers, consulted only where the first one is contested, so the ordinary
// unqualified-satisfies-qualified behaviour is untouched.
//
// The qualifiers ride as a suffix rather than being folded into parserExprKey's
// switch. Two expressions with equal parserExprKey have, by construction, the
// same tree shape, so their ColumnRefs align positionally and comparing the
// qualifier sequences compares like with like. That also means one reflective
// walk covers every expression type — including the seventeen that reach
// structuralExprKey — instead of a second hand-written switch that would have
// to be kept in step with the first.

import (
	"reflect"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// qualifiedGroupKey is parserExprKey plus the table/schema qualifiers of every
// ColumnRef in the expression, in walk order. `d1.y + 0` and `d2.y + 0` share a
// parserExprKey and differ here.
func qualifiedGroupKey(e parser.Expr) string {
	var b strings.Builder
	b.WriteString(parserExprKey(e))
	b.WriteByte('#')
	appendRefQualifiers(&b, reflect.ValueOf(e), 0)
	return b.String()
}

// qualifiedAggregateCallKey is aggregateCallKey plus the table/schema
// qualifiers of every ColumnRef inside the call — arguments, FILTER, the
// in-argument ORDER BY — in walk order. aggregateCallKey builds its argument
// keys from parserExprKey, so `count(d1.y)` and `count(d2.y)` share it and
// differ only here. The aggregate half of the M0125-0044 collapse
// (M0125-0045); consulted only where the blind key is contested.
func qualifiedAggregateCallKey(fc *parser.FuncCall) string {
	var b strings.Builder
	b.WriteString(aggregateCallKey(fc))
	b.WriteByte('#')
	appendRefQualifiers(&b, reflect.ValueOf(fc), 0)
	return b.String()
}

// appendRefQualifiers walks a parser expression and writes `|schema.table` for
// each ColumnRef it reaches. Unexported fields are skipped for the same reason
// structuralExprKey skips them: the only one in the parser AST is the source
// offset, and the SELECT-list copy of an expression sits at a different offset
// from its GROUP BY twin. maxStructuralKeyDepth is shared as the same backstop
// against a pathologically nested input; parse trees are trees, so unlike
// structuralExprKey this walk needs no cycle marking.
func appendRefQualifiers(b *strings.Builder, v reflect.Value, depth int) {
	if depth > maxStructuralKeyDepth || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		if cr, ok := v.Interface().(*parser.ColumnRef); ok {
			b.WriteByte('|')
			b.WriteString(strings.ToLower(cr.Schema))
			b.WriteByte('.')
			b.WriteString(strings.ToLower(cr.Table))
			return
		}
		appendRefQualifiers(b, v.Elem(), depth+1)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath != "" {
				continue
			}
			appendRefQualifiers(b, v.Field(i), depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			appendRefQualifiers(b, v.Index(i), depth+1)
		}
	}
}
