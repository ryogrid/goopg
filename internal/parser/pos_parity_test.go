package parser

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Position parity — the differential suite's structural blind spot.
//
// canonDump prints no positions, so every AST node the grammar built with
// pos 0 (or with the wrong token's offset) compared EQUAL to legacy's. That
// is not cosmetic: AlterTableAction.pos, ColumnDef.pos and every expression
// node's pos ARE the errposition goopg reports in the wire ErrorResponse, and
// psql renders a caret from it. P7.1 found FuncCall carrying pos 0 for every
// call in the language this way.
//
// collectPositions walks a statement by reflection and returns "path=pos"
// for every unexported `pos int` field it reaches, in a stable order.
func collectPositions(v any) []string {
	var out []string
	seen := map[uintptr]bool{}
	var walk func(rv reflect.Value, path string, depth int)
	walk = func(rv reflect.Value, path string, depth int) {
		if depth > 40 || !rv.IsValid() {
			return
		}
		switch rv.Kind() {
		case reflect.Ptr, reflect.Interface:
			if rv.IsNil() {
				return
			}
			if rv.Kind() == reflect.Ptr {
				p := rv.Pointer()
				if seen[p] {
					return
				}
				seen[p] = true
			}
			walk(rv.Elem(), path, depth+1)
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				walk(rv.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1)
			}
		case reflect.Struct:
			t := rv.Type()
			for i := 0; i < rv.NumField(); i++ {
				f := t.Field(i)
				if f.Name == "pos" && f.Type.Kind() == reflect.Int {
					out = append(out, fmt.Sprintf("%s.%s=%d", path, t.Name(), rv.Field(i).Int()))
					continue
				}
				fv := rv.Field(i)
				if !fv.CanInterface() {
					// Unexported non-pos field: reachable only via the
					// addressable copy trick, and none of the AST's payload
					// lives in one, so skipping it loses nothing.
					continue
				}
				walk(fv, path+"."+f.Name, depth+1)
			}
		}
	}
	walk(reflect.ValueOf(v), "", 0)
	sort.Strings(out)
	return out
}

// posParityCeiling is how many harvested statements still carry a node
// position the LALR parser threads differently from ddl.go/select.go. It is a
// RATCHET and fails in BOTH directions: it may only be lowered, and lowering
// it is required when a fix drops the count.
//
// It started at 2058 the moment this test existed — the class had been
// completely invisible until then, because canonDump prints no positions.
//
// Two systemic causes account for most of the ground covered since:
//
//  1. lastConsumedPos() where the rule is a DEFAULT REDUCTION. It returns
//     prevPos, which is only the current token's offset when a lookahead has
//     actually been read; where none was, it names the token BEFORE. That
//     alone zeroed every FuncCall in the language (via qualified_name) and
//     put SelectStmt's position PAST THE END of the statement. $<p>N is the
//     symbol's own captured offset and is right either way.
//  2. Anchoring a node at its LEFT OPERAND instead of at its OPERATOR.
//     select.go puts BinaryOp / InExpr / IsDistinctFrom / CastExpr on the
//     operator token, not on what precedes it.
//
// The rest were constructors that simply never took a position:
// ObjectName, ColumnDef, ColumnType, AlterTableAction, FunctionArg,
// UpdateAssign. Note that a few nodes legitimately carry ZERO — SelectStmt
// and the transaction-control statements — and matching legacy there means
// passing 0, not inventing an offset.
//
// The remaining tail is tracked in docs/design/not_ralph/TODO.md.
const posParityCeiling = 534

func TestPositionParity(t *testing.T) {
	var bad []string
	for _, q := range harvestSQLLiterals(t) {
		toks, err := Lex(q)
		if err != nil {
			continue
		}
		frags := SplitStatements(toks)
		if len(frags) != 1 || !fragmentRouted(frags[0]) {
			continue
		}
		ls, lerr := parseLegacyOnly(q)
		ys, yerr := ParseOneSrc(q, toks)
		if lerr != nil || yerr != nil || len(ls) != 1 || len(ys) != 1 {
			continue
		}
		lp, yp := collectPositions(ls[0]), collectPositions(ys[0])
		if strings.Join(lp, "|") == strings.Join(yp, "|") {
			continue
		}
		first := ""
		for i := 0; i < len(lp) && i < len(yp); i++ {
			if lp[i] != yp[i] {
				first = fmt.Sprintf("  L=%s  Y=%s", lp[i], yp[i])
				break
			}
		}
		bad = append(bad, q+"\n"+first)
	}
	if len(bad) > posParityCeiling {
		shown := bad
		if len(shown) > 20 {
			shown = shown[:20]
		}
		t.Errorf("position parity regressed to %d (ceiling %d); first offenders:\n%s",
			len(bad), posParityCeiling, strings.Join(shown, "\n"))
	}
	if len(bad) < posParityCeiling {
		t.Errorf("position parity improved to %d — lower posParityCeiling to match", len(bad))
	}
}
