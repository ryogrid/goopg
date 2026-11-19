package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/optimizer"
)

// Range operators: the function bodies of rangetypes.c over goopg's text
// carrier of a range value. The planner resolves each operator by type
// (optimizer.rangeOpCall) and names the pg_proc function.

// rangeValue is a deserialized range (range_deserialize).
type rangeValue struct {
	info         builtinRangeType
	typeName     string
	empty        bool
	lower, upper rangeBound
}

func rangeDeserialize(typeName string, text string, pos int, ctx *Context) (rangeValue, error) {
	info, ok := lookupRangeTypeForIO(typeName, ctx)
	if !ok {
		return rangeValue{}, fmt.Errorf("not a range type: %s", typeName)
	}
	flags, lb, ub, err := rangeParse(text, pos)
	if err != nil {
		return rangeValue{}, err
	}
	v := rangeValue{info: info, typeName: typeName, empty: flags&rangeFlagEmpty != 0}
	v.lower = rangeBound{lower: true, infinite: flags&rangeFlagLbInf != 0, inclusive: flags&rangeFlagLbInc != 0}
	v.upper = rangeBound{lower: false, infinite: flags&rangeFlagUbInf != 0, inclusive: flags&rangeFlagUbInc != 0}
	if rangeHasLbound(flags) {
		if v.lower.val, err = rangeBoundIn(info, lb, pos, ctx); err != nil {
			return rangeValue{}, err
		}
	}
	if rangeHasUbound(flags) {
		if v.upper.val, err = rangeBoundIn(info, ub, pos, ctx); err != nil {
			return rangeValue{}, err
		}
	}
	return v, nil
}

// rangeCmpBounds is range_cmp_bounds: bounds ordered by value, with an
// exclusive lower bound after and an exclusive upper bound before the value.
func rangeCmpBounds(b1, b2 rangeBound, pos int) (int, error) {
	switch {
	case b1.infinite && b2.infinite:
		if b1.lower == b2.lower {
			return 0, nil
		}
		return infiniteSide(b1.lower), nil
	case b1.infinite:
		return infiniteSide(b1.lower), nil
	case b2.infinite:
		return -infiniteSide(b2.lower), nil
	}
	c, err := compareDatum(b1.val, b2.val, pos)
	if err != nil || c != 0 {
		return c, err
	}
	switch {
	case !b1.inclusive && !b2.inclusive:
		if b1.lower == b2.lower {
			return 0, nil
		}
		return exclusiveSide(b1.lower), nil
	case !b1.inclusive:
		return exclusiveSide(b1.lower), nil
	case !b2.inclusive:
		return -exclusiveSide(b2.lower), nil
	}
	return 0, nil
}

// rangeCmpBoundValuesOnly is range_cmp_bound_values: values and infinities,
// not inclusivity.
func rangeCmpBoundValuesOnly(b1, b2 rangeBound, pos int) (int, error) {
	switch {
	case b1.infinite && b2.infinite:
		if b1.lower == b2.lower {
			return 0, nil
		}
		return infiniteSide(b1.lower), nil
	case b1.infinite:
		return infiniteSide(b1.lower), nil
	case b2.infinite:
		return -infiniteSide(b2.lower), nil
	}
	return compareDatum(b1.val, b2.val, pos)
}

func infiniteSide(lower bool) int {
	if lower {
		return -1
	}
	return 1
}

func exclusiveSide(lower bool) int {
	if lower {
		return 1
	}
	return -1
}

func (v rangeValue) make(lower, upper rangeBound, pos int, ctx *Context) (Datum, error) {
	lower.lower, upper.lower = true, false
	text, err := rangeMakeText(v.info, lower, upper, false, pos, ctx)
	if err != nil {
		return Datum{}, err
	}
	return NewStringDatum(text), nil
}

func emptyRangeDatum() Datum { return NewStringDatum(rangeEmptyLiteral) }

// rangeContainsElem is range_contains_elem_internal.
func rangeContainsElem(r rangeValue, elem Datum, pos int) (bool, error) {
	if r.empty {
		return false, nil
	}
	if !r.lower.infinite {
		c, err := compareDatum(r.lower.val, elem, pos)
		if err != nil || c > 0 || (c == 0 && !r.lower.inclusive) {
			return false, err
		}
	}
	if !r.upper.infinite {
		c, err := compareDatum(r.upper.val, elem, pos)
		if err != nil || c < 0 || (c == 0 && !r.upper.inclusive) {
			return false, err
		}
	}
	return true, nil
}

// rangeContains is range_contains_internal.
func rangeContains(r1, r2 rangeValue, pos int) (bool, error) {
	if r2.empty {
		return true, nil
	}
	if r1.empty {
		return false, nil
	}
	if c, err := rangeCmpBounds(r1.lower, r2.lower, pos); err != nil || c > 0 {
		return false, err
	}
	c, err := rangeCmpBounds(r1.upper, r2.upper, pos)
	return err == nil && c >= 0, err
}

// rangeOverlaps is range_overlaps_internal.
func rangeOverlaps(r1, r2 rangeValue, pos int) (bool, error) {
	if r1.empty || r2.empty {
		return false, nil
	}
	within := func(a, b rangeValue) (bool, error) {
		c1, err := rangeCmpBounds(a.lower, b.lower, pos)
		if err != nil || c1 < 0 {
			return false, err
		}
		c2, err := rangeCmpBounds(a.lower, b.upper, pos)
		return err == nil && c2 <= 0, err
	}
	if ok, err := within(r1, r2); ok || err != nil {
		return ok, err
	}
	return within(r2, r1)
}

// boundsAdjacent is bounds_adjacent: upper bound a meets lower bound b with
// nothing between them.
func boundsAdjacent(r rangeValue, a, b rangeBound, pos int, ctx *Context) (bool, error) {
	c, err := rangeCmpBoundValuesOnly(a, b, pos)
	if err != nil {
		return false, err
	}
	switch {
	case c < 0:
		if r.info.canonical == rangeCanonicalNone {
			return false, nil
		}
		// A discrete type: adjacent when the range between them is empty.
		lo, hi := a, b
		lo.inclusive, hi.inclusive = !a.inclusive, !b.inclusive
		d, err := r.make(lo, hi, pos, ctx)
		if err != nil {
			return false, err
		}
		return d.StringValue() == rangeEmptyLiteral, nil
	case c == 0:
		return a.inclusive != b.inclusive, nil
	}
	return false, nil
}

// rangeCmp is range_cmp: empty first, then by lower and upper bound.
func rangeCmp(r1, r2 rangeValue, pos int) (int, error) {
	switch {
	case r1.empty && r2.empty:
		return 0, nil
	case r1.empty:
		return -1, nil
	case r2.empty:
		return 1, nil
	}
	c, err := rangeCmpBounds(r1.lower, r2.lower, pos)
	if err != nil || c != 0 {
		return c, err
	}
	return rangeCmpBounds(r1.upper, r2.upper, pos)
}

func evalRangeOperator(name, rangeType string, x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return NullDatum, nil
	}
	pos := x.Pos()
	a, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	b, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if a.IsNull() || b.IsNull() {
		return NullDatum, nil
	}
	deser := func(_ optimizer.Expr, d Datum) (rangeValue, error) {
		return rangeDeserialize(rangeType, d.StringValue(), pos, ctx)
	}
	elemOf := func(r rangeValue, d Datum) (Datum, error) {
		return evalCast(d, r.info.subtype, pos, ctx)
	}
	boolean := func(ok bool, err error) (Datum, error) {
		if err != nil {
			return Datum{}, err
		}
		return NewBoolDatum(ok), nil
	}
	switch name {
	case "range_contains_elem":
		r, err := deser(x.Args[0], a)
		if err != nil {
			return Datum{}, err
		}
		e, err := elemOf(r, b)
		if err != nil {
			return Datum{}, err
		}
		return boolean(rangeContainsElem(r, e, pos))
	case "elem_contained_by_range":
		r, err := deser(x.Args[1], b)
		if err != nil {
			return Datum{}, err
		}
		e, err := elemOf(r, a)
		if err != nil {
			return Datum{}, err
		}
		return boolean(rangeContainsElem(r, e, pos))
	}
	r1, err := deser(x.Args[0], a)
	if err != nil {
		return Datum{}, err
	}
	r2, err := deser(x.Args[1], b)
	if err != nil {
		return Datum{}, err
	}
	cmp := func(f func(int) bool) (Datum, error) {
		c, err := rangeCmp(r1, r2, pos)
		return boolean(f(c), err)
	}
	bound := func(b1, b2 rangeBound, f func(int) bool) (Datum, error) {
		if r1.empty || r2.empty {
			return NewBoolDatum(false), nil
		}
		c, err := rangeCmpBounds(b1, b2, pos)
		return boolean(f(c), err)
	}
	switch name {
	case "range_contains":
		return boolean(rangeContains(r1, r2, pos))
	case "range_contained_by":
		return boolean(rangeContains(r2, r1, pos))
	case "range_overlaps":
		return boolean(rangeOverlaps(r1, r2, pos))
	case "range_before":
		return bound(r1.upper, r2.lower, func(c int) bool { return c < 0 })
	case "range_after":
		return bound(r1.lower, r2.upper, func(c int) bool { return c > 0 })
	case "range_overleft":
		return bound(r1.upper, r2.upper, func(c int) bool { return c <= 0 })
	case "range_overright":
		return bound(r1.lower, r2.lower, func(c int) bool { return c >= 0 })
	case "range_adjacent":
		if r1.empty || r2.empty {
			return NewBoolDatum(false), nil
		}
		ok, err := boundsAdjacent(r1, r1.upper, r2.lower, pos, ctx)
		if err != nil || ok {
			return boolean(ok, err)
		}
		return boolean(boundsAdjacent(r1, r2.upper, r1.lower, pos, ctx))
	case "range_lt":
		return cmp(func(c int) bool { return c < 0 })
	case "range_le":
		return cmp(func(c int) bool { return c <= 0 })
	case "range_gt":
		return cmp(func(c int) bool { return c > 0 })
	case "range_ge":
		return cmp(func(c int) bool { return c >= 0 })
	case "range_union":
		return rangeUnion(r1, r2, pos, ctx)
	case "range_intersect":
		return rangeIntersect(r1, r2, pos, ctx)
	case "range_minus":
		return rangeMinus(r1, r2, pos, ctx)
	}
	return NullDatum, nil
}

// rangeUnion is range_union_internal with strict = true.
func rangeUnion(r1, r2 rangeValue, pos int, ctx *Context) (Datum, error) {
	if r1.empty {
		return NewStringDatum(rangeText(r2, pos, ctx)), nil
	}
	if r2.empty {
		return NewStringDatum(rangeText(r1, pos, ctx)), nil
	}
	ov, err := rangeOverlaps(r1, r2, pos)
	if err != nil {
		return Datum{}, err
	}
	if !ov {
		adj, err := boundsAdjacent(r1, r1.upper, r2.lower, pos, ctx)
		if err != nil {
			return Datum{}, err
		}
		if !adj {
			if adj, err = boundsAdjacent(r1, r2.upper, r1.lower, pos, ctx); err != nil {
				return Datum{}, err
			}
		}
		if !adj {
			return Datum{}, &ExecError{Code: "22000", Message: "result of range union would not be contiguous"}
		}
	}
	lower, upper := r1.lower, r1.upper
	if c, err := rangeCmpBounds(r1.lower, r2.lower, pos); err != nil {
		return Datum{}, err
	} else if c > 0 {
		lower = r2.lower
	}
	if c, err := rangeCmpBounds(r1.upper, r2.upper, pos); err != nil {
		return Datum{}, err
	} else if c < 0 {
		upper = r2.upper
	}
	return r1.make(lower, upper, pos, ctx)
}

// rangeIntersect is range_intersect_internal.
func rangeIntersect(r1, r2 rangeValue, pos int, ctx *Context) (Datum, error) {
	ov, err := rangeOverlaps(r1, r2, pos)
	if err != nil {
		return Datum{}, err
	}
	if !ov {
		return emptyRangeDatum(), nil
	}
	lower, upper := r1.lower, r1.upper
	if c, err := rangeCmpBounds(r1.lower, r2.lower, pos); err != nil {
		return Datum{}, err
	} else if c < 0 {
		lower = r2.lower
	}
	if c, err := rangeCmpBounds(r1.upper, r2.upper, pos); err != nil {
		return Datum{}, err
	} else if c > 0 {
		upper = r2.upper
	}
	return r1.make(lower, upper, pos, ctx)
}

// rangeMinus is range_minus_internal.
func rangeMinus(r1, r2 rangeValue, pos int, ctx *Context) (Datum, error) {
	if r1.empty || r2.empty {
		return NewStringDatum(rangeText(r1, pos, ctx)), nil
	}
	c := func(b1, b2 rangeBound) (int, error) { return rangeCmpBounds(b1, b2, pos) }
	l1l2, err := c(r1.lower, r2.lower)
	if err != nil {
		return Datum{}, err
	}
	l1u2, _ := c(r1.lower, r2.upper)
	u1l2, _ := c(r1.upper, r2.lower)
	u1u2, _ := c(r1.upper, r2.upper)
	if l1l2 < 0 && u1u2 > 0 {
		return Datum{}, &ExecError{Code: "22000", Message: "result of range difference would not be contiguous"}
	}
	if l1u2 > 0 || u1l2 < 0 {
		return NewStringDatum(rangeText(r1, pos, ctx)), nil
	}
	if l1l2 >= 0 && u1u2 <= 0 {
		return emptyRangeDatum(), nil
	}
	if l1l2 <= 0 && u1l2 >= 0 && u1u2 <= 0 {
		upper := r2.lower
		upper.inclusive = !upper.inclusive
		return r1.make(r1.lower, upper, pos, ctx)
	}
	if l1l2 >= 0 && u1u2 >= 0 && l1u2 <= 0 {
		lower := r2.upper
		lower.inclusive = !lower.inclusive
		return r1.make(lower, r1.upper, pos, ctx)
	}
	return Datum{}, &ExecError{Code: "XX000", Pos: pos, Message: "unexpected case in range_minus"}
}

// rangeText is a range value's canonical text.
func rangeText(r rangeValue, pos int, ctx *Context) string {
	if r.empty {
		return rangeEmptyLiteral
	}
	text, err := rangeMakeText(r.info, r.lower, r.upper, false, pos, ctx)
	if err != nil {
		return rangeEmptyLiteral
	}
	return text
}
