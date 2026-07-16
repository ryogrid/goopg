package executor

// operators_from_unnest.go — FROM unnest(array_expr) alias(col).
// Expands an array expression into one row per element.
// M0097-0035.

import (
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

type fromUnnestOp struct {
	plan *planner.FromUnnest
	ctx  *Context
	rows []Row
	idx  int
}

func newFromUnnestOp(p *planner.FromUnnest) *fromUnnestOp {
	return &fromUnnestOp{plan: p}
}

func (o *fromUnnestOp) Schema() planner.Schema { return o.plan.Output() }

func (o *fromUnnestOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.idx = 0
	o.rows = nil

	// Evaluate the array expression(s) using outer rows if this is lateral.
	var outerRow Row
	if len(ctx.OuterRows) > 0 {
		outerRow = ctx.OuterRows[len(ctx.OuterRows)-1]
	}

	if len(o.plan.ArrExprs) >= 2 {
		// Multi-arg unnest: zip N arrays, NULL-padding shorter ones.
		arrays := make([][]Datum, len(o.plan.ArrExprs))
		maxLen := 0
		for i, arrExpr := range o.plan.ArrExprs {
			arrD, err := evalExpr(arrExpr, outerRow, ctx)
			if err != nil {
				return err
			}
			if arrD.IsNull() {
				arrays[i] = nil
			} else {
				arrays[i] = expandArrayDatum(arrD)
			}
			if len(arrays[i]) > maxLen {
				maxLen = len(arrays[i])
			}
		}
		o.rows = make([]Row, maxLen)
		out := o.plan.Output()
		for pos := range maxLen {
			row := make(Row, len(arrays))
			for j, arr := range arrays {
				if pos < len(arr) {
					elem := arr[pos]
					if j < len(out) {
						elem = coerceUnnestElem(elem, out[j].Type.Name)
					}
					row[j] = elem
				} else {
					row[j] = NullDatum
				}
			}
			o.rows[pos] = row
		}
		return nil
	}

	// Single-arg form.
	arrD, err := evalExpr(o.plan.ArrExpr, outerRow, ctx)
	if err != nil {
		return err
	}
	if arrD.IsNull() {
		return nil
	}

	// Expand the array into individual element rows.
	var elemType string
	if out := o.plan.Output(); len(out) > 0 {
		elemType = out[0].Type.Name
	}
	elems := expandArrayDatum(arrD)
	o.rows = make([]Row, len(elems))
	for i, elem := range elems {
		o.rows[i] = Row{coerceUnnestElem(elem, elemType)}
	}
	return nil
}

// coerceUnnestElem casts a text element produced by expandArrayDatum to the
// element type declared in the unnest output schema. expandArrayDatum returns
// every element as a text KindString, but for non-text element types a
// KindString value derives a DIFFERENT key in datumKey (the hash/merge-join key
// derivation) — and compares differently under cross-kind '=' — than the same
// value held by a catalog column of that type. That divergence makes a join
// such as pg_dump's getTableAttrs
//
//	unnest('{16403}'::oid[]) AS src(tbloid) JOIN pg_attribute a
//	  ON src.tbloid = a.attrelid
//
// hash "s:16403" against the numeric key and match nothing. Casting the element
// to its declared type keeps the FROM-unnest path in sync with how scalar
// values of that type are produced elsewhere. Only the numeric/oid/bool family
// (where the divergence exists) is coerced; text-family elements stay strings
// (both join sides are already strings), and a cast failure falls back to the
// raw element so a malformed synthetic array never aborts the scan.
func coerceUnnestElem(d Datum, typeName string) Datum {
	if d.IsNull() || !unnestElemNeedsTyping(typeName) {
		return d
	}
	if c, err := evalCast(d, typeName, 0, nil); err == nil {
		return c
	}
	return d
}

func unnestElemNeedsTyping(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "oid", "xid", "xid8",
		"float", "float4", "real", "float8", "double precision":
		return true
	}
	return isIntegerTypeName(typeName) || isNumericType(typeName) || isBoolTypeName(typeName)
}

func (o *fromUnnestOp) Close() error { return nil }

func (o *fromUnnestOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

// expandArrayDatum parses a Datum containing an array and returns its scalar elements.
// For multi-dimensional arrays (e.g. {{1},{2},{3}}), elements are recursively flattened
// to scalars, matching PostgreSQL's unnest() semantics. M0097-0035.
func expandArrayDatum(d Datum) []Datum {
	var elems []Datum
	var sv string
	switch d.Kind {
	case KindString:
		sv = d.StringValue()
	case KindBytes:
		sv = string(d.BytesValue())
	default:
		return elems
	}
	parts := parseTextArray(sv)
	for _, p := range parts {
		if p == "NULL" {
			elems = append(elems, NullDatum)
		} else if len(p) >= 2 && p[0] == '{' && p[len(p)-1] == '}' {
			// Nested array element — recursively flatten (PG unnest flattens all dims).
			inner := expandArrayDatum(NewStringDatum(p))
			elems = append(elems, inner...)
		} else {
			elems = append(elems, NewStringDatum(p))
		}
	}
	return elems
}
