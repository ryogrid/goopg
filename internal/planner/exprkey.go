package planner

// Structural fallback for parserExprKey (M0125-0009).
//
// parserExprKey hand-writes a key for the expression node types the planner
// cares most about (constants, ColumnRef, BinaryOp, FuncCall, …) so it can
// apply PG-faithful normalisations — most notably dropping the table/schema
// qualifier from a ColumnRef so `lower(c)` and `lower(t.c)` match one GROUP BY
// entry (M0097-0003). Everything it did NOT enumerate used to fall through to
//
//	return fmt.Sprintf("expr:%T", e)
//
// — the Go TYPE NAME, carrying no expression content whatsoever. Seventeen
// parser.Expr types shared that fallback, so every `*parser.CaseExpr` in a
// query hashed to one key. Since aggregate dedup (aggregateCallKey) and GROUP
// BY matching both key off this string, the 2nd..Nth `sum(CASE …)` of a SELECT
// were discarded as duplicates and each sibling pivot column read the FIRST
// aggregate's slot. TPC-DS Q97 makes the corruption self-evident: its three
// columns are disjoint by construction, yet goopg returned the same count in
// all three. Ten TPC-DS queries were affected (Q2 Q21 Q40 Q43 Q50 Q59 Q62 Q66
// Q97 Q99) — always with the row count intact, which is why it survived every
// row-count gate.
//
// The same failure mode had already been patched twice as single instances
// (M0097-0003 ColumnRef, M0097-0032 count(*) vs count(*) FILTER). This file
// closes the CLASS instead: the fallback is now a structural walk over the
// node's exported fields, so an expression type nobody enumerated still gets a
// content-bearing key, and a newly added parser.Expr type is handled the day it
// is added. Upstream PG has the same property for the same reason — nodeFuncs
// equal() compares every field of every node tag, and a tag missing from
// equalfuncs.c is a hard elog, never a silent "equal".
//
// Positions are deliberately excluded: every parser node stores its source
// offset in an UNEXPORTED `pos` field, and the walk skips unexported fields, so
// two textually identical expressions written at different offsets (the SELECT
// target and its GROUP BY twin) still produce the same key. That is required —
// GROUP BY matching depends on it.

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// parserExprType is the reflect.Type of the parser.Expr interface, used to
// hand nested expression nodes back to parserExprKey so they pick up its
// normalisations rather than being walked structurally.
var parserExprType = reflect.TypeOf((*parser.Expr)(nil)).Elem()

// maxStructuralKeyDepth bounds the walk. Real parse trees are nowhere near
// this deep; the cap only exists so a pathological (or maliciously nested)
// input cannot blow the stack. Cycles are handled separately and exactly, by
// path-marking, so this is a backstop and not a correctness mechanism.
const maxStructuralKeyDepth = 200

// structuralExprKey builds a content-bearing key for an expression node that
// parserExprKey's explicit switch does not enumerate. The key encodes the
// concrete type plus every exported field, recursively; nested parser.Expr
// values are routed back through parserExprKey so normalisations apply
// uniformly at every level.
func structuralExprKey(e parser.Expr) string {
	var b strings.Builder
	b.WriteString("expr:")
	w := &structuralKeyWriter{b: &b, onPath: make(map[uintptr]bool, 8)}
	w.write(reflect.ValueOf(e), 0)
	return b.String()
}

// funcCallTailKey renders the parts of a FuncCall that carry meaning but sit
// outside `name(args)` — FILTER (WHERE …), OVER (…), the in-argument ORDER BY,
// WITHIN GROUP (ORDER BY …), and the VARIADIC markers. parserExprKey's explicit
// FuncCall case and aggregateCallKey both used to build their key from the name
// and args alone, which is the very same content-dropping bug M0125-0009 fixes
// in the fallback: `string_agg(x, ',' ORDER BY a)` and
// `string_agg(x, ',' ORDER BY b)` in one SELECT hashed equal and collapsed onto
// a single aggregate slot. (FILTER was patched as a one-off instance by
// M0097-0032; it is folded in here so there is one place that knows the tail.)
//
// Returns "" when every tail field is empty, so the key of a plain call is
// byte-for-byte what it was before.
func funcCallTailKey(fc *parser.FuncCall) string {
	if fc.Filter == nil && fc.Over == nil &&
		len(fc.OrderBy) == 0 && len(fc.WithinGroup) == 0 && len(fc.Variadic) == 0 {
		return ""
	}
	var b strings.Builder
	w := &structuralKeyWriter{b: &b, onPath: make(map[uintptr]bool, 4)}
	// depth 1 (not 0) so a nested expression node is routed back through
	// parserExprKey rather than walked structurally.
	for _, part := range []struct {
		name string
		val  any
	}{
		{"filter", fc.Filter},
		{"over", fc.Over},
		{"orderby", fc.OrderBy},
		{"withingroup", fc.WithinGroup},
		{"variadic", fc.Variadic},
	} {
		b.WriteString(part.name)
		b.WriteByte('=')
		w.write(reflect.ValueOf(part.val), 1)
		b.WriteByte('|')
	}
	return b.String()
}

type structuralKeyWriter struct {
	b *strings.Builder
	// onPath holds the pointers currently on the recursion path. Marking on
	// entry and clearing on exit detects real cycles without mistaking a DAG
	// (the same node pointer reachable twice, which planner rewrites do
	// produce) for one.
	onPath map[uintptr]bool
}

func (w *structuralKeyWriter) write(v reflect.Value, depth int) {
	if depth > maxStructuralKeyDepth {
		w.b.WriteString("<depth>")
		return
	}
	if !v.IsValid() {
		w.b.WriteString("nil")
		return
	}

	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			w.b.WriteString("nil")
			return
		}
		w.write(v.Elem(), depth+1)

	case reflect.Pointer:
		if v.IsNil() {
			w.b.WriteString("nil")
			return
		}
		// Hand a nested expression node back to parserExprKey so its explicit
		// cases (and their normalisations) win over the structural walk. depth
		// > 0 keeps the root — which arrived here precisely because it has no
		// explicit case — from recursing into itself forever.
		if depth > 0 && v.Type().Implements(parserExprType) {
			w.b.WriteString(parserExprKey(v.Interface().(parser.Expr)))
			return
		}
		ptr := v.Pointer()
		if w.onPath[ptr] {
			w.b.WriteString("<cycle>")
			return
		}
		w.onPath[ptr] = true
		w.write(v.Elem(), depth+1)
		delete(w.onPath, ptr)

	case reflect.Struct:
		t := v.Type()
		w.b.WriteString(t.String())
		w.b.WriteByte('{')
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// Unexported fields are skipped. In the parser AST that is exactly
			// the `pos int` source offset, which must NOT participate: the
			// SELECT-list copy and the GROUP BY copy of one expression differ
			// only by position and have to key equal.
			if f.PkgPath != "" {
				continue
			}
			w.b.WriteString(f.Name)
			w.b.WriteByte('=')
			w.write(v.Field(i), depth+1)
			w.b.WriteByte(',')
		}
		w.b.WriteByte('}')

	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			w.b.WriteString("nil")
			return
		}
		w.b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			w.write(v.Index(i), depth+1)
			w.b.WriteByte(',')
		}
		w.b.WriteByte(']')

	case reflect.Map:
		if v.IsNil() {
			w.b.WriteString("nil")
			return
		}
		// Sort by rendered key so the result is deterministic across runs.
		keys := v.MapKeys()
		rendered := make([]string, 0, len(keys))
		for _, k := range keys {
			var sub strings.Builder
			ksub := &structuralKeyWriter{b: &sub, onPath: w.onPath}
			ksub.write(k, depth+1)
			var vsub strings.Builder
			vsubw := &structuralKeyWriter{b: &vsub, onPath: w.onPath}
			vsubw.write(v.MapIndex(k), depth+1)
			rendered = append(rendered, sub.String()+":"+vsub.String())
		}
		sort.Strings(rendered)
		w.b.WriteByte('{')
		for _, r := range rendered {
			w.b.WriteString(r)
			w.b.WriteByte(',')
		}
		w.b.WriteByte('}')

	case reflect.String:
		// Length-prefixed so "ab"+"c" cannot render the same as "a"+"bc".
		s := v.String()
		w.b.WriteString(strconv.Itoa(len(s)))
		w.b.WriteByte('\'')
		w.b.WriteString(s)
		w.b.WriteByte('\'')

	case reflect.Bool:
		if v.Bool() {
			w.b.WriteByte('t')
		} else {
			w.b.WriteByte('f')
		}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		w.b.WriteString(strconv.FormatInt(v.Int(), 10))

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		w.b.WriteString(strconv.FormatUint(v.Uint(), 10))

	case reflect.Float32, reflect.Float64:
		w.b.WriteString(strconv.FormatFloat(v.Float(), 'g', -1, 64))

	default:
		// Chan/Func/UnsafePointer do not occur in the parser AST. Render
		// something stable rather than panicking if one ever appears.
		w.b.WriteString(fmt.Sprintf("<%s>", v.Kind()))
	}
}
