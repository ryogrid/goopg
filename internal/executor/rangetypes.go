package executor

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Range type input, output and the range constructor functions.
//
// M0134-0173. Until this file existed goopg treated a range-typed value as
// OPAQUE TEXT: `evalCast` had no arm for any range type, so `'garbage'::int4range`
// succeeded and stored "garbage", `'[5,1)'::int4range` succeeded where PG raises
// 22000, and no discrete range was ever canonicalized — so `'[1,4]'::int4range`
// and `'[1,5)'::int4range` (the SAME value in PG) compared UNEQUAL. That is the
// fourth instance of the recurring "missing evalCast arm = unvalidated text"
// pattern (xid, circle, float8 were the first three) and, unlike a message-only
// gap, it is a silent wrong-answer bug: every equality, ORDER BY, btree index
// probe and exclusion-constraint check over a range column compared the raw
// literal spelling instead of the value.
//
// Upstream reference: postgres/src/backend/utils/adt/rangetypes.c —
// range_in (:90), range_out (:138), range_serialize (:1791), make_range (:2016),
// range_parse (:2386), range_parse_bound (:2492), range_deparse (:2571),
// range_bound_escape (:2601), int4range_canonical / int8range_canonical /
// daterange_canonical, and range_constructor2 / range_constructor3.
//
// The value model here is deliberately PG's TEXT rendering, not a new Datum
// kind: goopg carries range values as KindString throughout, so the invariant
// this file establishes is that the stored string is always the CANONICAL
// range_out spelling. That is what makes text comparison agree with PG's
// range_eq for the canonicalizing types. A native range Datum (with the
// operator family — @>, &&, lower(), upper(), …) remains unimplemented and is
// ledgered separately.

// Range flag bits — postgres/src/include/utils/rangetypes.h:38-42.
const (
	rangeFlagEmpty byte = 0x01
	rangeFlagLbInc byte = 0x02
	rangeFlagUbInc byte = 0x04
	rangeFlagLbInf byte = 0x08
	rangeFlagUbInf byte = 0x10
)

// rangeEmptyLiteral — RANGE_EMPTY_LITERAL (rangetypes.h:32).
const rangeEmptyLiteral = "empty"

func rangeHasLbound(flags byte) bool {
	return flags&(rangeFlagEmpty|rangeFlagLbInf) == 0
}

func rangeHasUbound(flags byte) bool {
	return flags&(rangeFlagEmpty|rangeFlagUbInf) == 0
}

// rangeCanonicalKind names the canonical procedure a range type carries in
// pg_range.rngcanonical. Only the three DISCRETE built-in range types have one
// (int4range_canonical, int8range_canonical, daterange_canonical); numrange,
// tsrange and tstzrange have rngcanonical = 0 and are stored exactly as given.
type rangeCanonicalKind int

const (
	rangeCanonicalNone rangeCanonicalKind = iota
	rangeCanonicalInt4
	rangeCanonicalInt8
	rangeCanonicalDate
)

// builtinRangeType describes one of the six in-tree range types
// (postgres/src/include/catalog/pg_range.dat). The subtype is the name goopg's
// evalCast / `::text` pair uses as that subtype's input and output function.
type builtinRangeType struct {
	subtype   string
	canonical rangeCanonicalKind
}

var builtinRangeTypes = map[string]builtinRangeType{
	"int4range": {subtype: "int4", canonical: rangeCanonicalInt4},
	"int8range": {subtype: "int8", canonical: rangeCanonicalInt8},
	"numrange":  {subtype: "numeric", canonical: rangeCanonicalNone},
	"daterange": {subtype: "date", canonical: rangeCanonicalDate},
	"tsrange":   {subtype: "timestamp", canonical: rangeCanonicalNone},
	"tstzrange": {subtype: "timestamptz", canonical: rangeCanonicalNone},
}

// lookupRangeTypeForIO resolves a type name to the subtype/canonical pair the
// I/O pipeline needs, covering both the six built-ins and user range types
// created with `CREATE TYPE … AS RANGE`. A user range type never canonicalizes:
// goopg writes pg_range.rngcanonical = 0 for them (see sys_pg_range.go), so
// applying one here would diverge from what the catalog advertises.
func lookupRangeTypeForIO(name string, ctx *Context) (builtinRangeType, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if after, ok := strings.CutPrefix(lower, "pg_catalog."); ok {
		lower = after
	}
	if info, ok := builtinRangeTypes[lower]; ok {
		return info, true
	}
	if ctx == nil || ctx.Catalog == nil {
		return builtinRangeType{}, false
	}
	if rt, ok := ctx.Catalog.LookupRangeType(lower); ok {
		return builtinRangeType{subtype: rt.SubtypeName, canonical: rangeCanonicalNone}, true
	}
	return builtinRangeType{}, false
}

// rangeBound mirrors upstream's RangeBound (rangetypes.h:60-66).
type rangeBound struct {
	val       Datum
	infinite  bool
	inclusive bool
	lower     bool
}

func rangeMalformed(input, detail string, pos int) error {
	return &ExecError{Code: "22P02", Pos: pos,
		Message: fmt.Sprintf("malformed range literal: %q", input),
		Detail:  detail}
}

// rangeParse is range_parse (rangetypes.c:2386): it splits the literal into
// flag bits plus the two raw bound strings, WITHOUT interpreting either bound.
// Bound whitespace is deliberately preserved — upstream hands it to the
// subtype's input function, which is what makes `[ 1 , 4 )` legal for int4range
// (int4in trims) and significant for textrange.
func rangeParse(input string, pos int) (flags byte, lbound, ubound string, err error) {
	i := 0
	for i < len(input) && isRangeSpace(input[i]) {
		i++
	}

	// "empty" is case-insensitive and may be surrounded by whitespace, but
	// nothing else may follow it.
	if len(input)-i >= len(rangeEmptyLiteral) &&
		strings.EqualFold(input[i:i+len(rangeEmptyLiteral)], rangeEmptyLiteral) {
		i += len(rangeEmptyLiteral)
		for i < len(input) && isRangeSpace(input[i]) {
			i++
		}
		if i != len(input) {
			return 0, "", "", rangeMalformed(input, `Junk after "empty" key word.`, pos)
		}
		return rangeFlagEmpty, "", "", nil
	}

	switch {
	case i < len(input) && input[i] == '[':
		flags |= rangeFlagLbInc
		i++
	case i < len(input) && input[i] == '(':
		i++
	default:
		return 0, "", "", rangeMalformed(input, "Missing left parenthesis or bracket.", pos)
	}

	var infinite bool
	lbound, infinite, i, err = rangeParseBound(input, i, pos)
	if err != nil {
		return 0, "", "", err
	}
	if infinite {
		flags |= rangeFlagLbInf
	}

	if i < len(input) && input[i] == ',' {
		i++
	} else {
		return 0, "", "", rangeMalformed(input, "Missing comma after lower bound.", pos)
	}

	ubound, infinite, i, err = rangeParseBound(input, i, pos)
	if err != nil {
		return 0, "", "", err
	}
	if infinite {
		flags |= rangeFlagUbInf
	}

	switch {
	case i < len(input) && input[i] == ']':
		flags |= rangeFlagUbInc
		i++
	case i < len(input) && input[i] == ')':
		i++
	default:
		// rangeParseBound only stops on ',' ')' ']' or end of input, so the
		// only way to land here is a second comma. Upstream says so too.
		return 0, "", "", rangeMalformed(input, "Too many commas.", pos)
	}

	for i < len(input) && isRangeSpace(input[i]) {
		i++
	}
	if i != len(input) {
		return 0, "", "", rangeMalformed(input, "Junk after right parenthesis or bracket.", pos)
	}
	return flags, lbound, ubound, nil
}

// rangeParseBound is range_parse_bound (rangetypes.c:2492): scan to the next
// unquoted ',' ')' or ']', de-quoting `""` pairs and `\` escapes on the way.
// A completely empty bound means "no bound" (infinite), NOT an empty string —
// `[,b)` is (,b) while `["",b)` keeps the empty string.
func rangeParseBound(input string, i, pos int) (bound string, infinite bool, next int, err error) {
	if i < len(input) && (input[i] == ',' || input[i] == ')' || input[i] == ']') {
		return "", true, i, nil
	}
	var buf strings.Builder
	inquote := false
	for inquote || !(i < len(input) && (input[i] == ',' || input[i] == ')' || input[i] == ']')) {
		if i >= len(input) {
			return "", false, 0, rangeMalformed(input, "Unexpected end of input.", pos)
		}
		ch := input[i]
		i++
		switch {
		case ch == '\\':
			if i >= len(input) {
				return "", false, 0, rangeMalformed(input, "Unexpected end of input.", pos)
			}
			buf.WriteByte(input[i])
			i++
		case ch == '"':
			if !inquote {
				inquote = true
			} else if i < len(input) && input[i] == '"' {
				// Doubled quote within a quoted sequence.
				buf.WriteByte(input[i])
				i++
			} else {
				inquote = false
			}
		default:
			buf.WriteByte(ch)
		}
	}
	return buf.String(), false, i, nil
}

// isRangeSpace mirrors C isspace() for the ASCII set range_parse scans.
func isRangeSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// rangeDeparse is range_deparse (rangetypes.c:2571).
func rangeDeparse(flags byte, lbound, ubound string) string {
	if flags&rangeFlagEmpty != 0 {
		return rangeEmptyLiteral
	}
	var buf strings.Builder
	if flags&rangeFlagLbInc != 0 {
		buf.WriteByte('[')
	} else {
		buf.WriteByte('(')
	}
	if rangeHasLbound(flags) {
		buf.WriteString(rangeBoundEscape(lbound))
	}
	buf.WriteByte(',')
	if rangeHasUbound(flags) {
		buf.WriteString(rangeBoundEscape(ubound))
	}
	if flags&rangeFlagUbInc != 0 {
		buf.WriteByte(']')
	} else {
		buf.WriteByte(')')
	}
	return buf.String()
}

// rangeBoundEscape is range_bound_escape (rangetypes.c:2601): quote the bound
// when it is empty or contains a character that would confuse range_parse.
func rangeBoundEscape(value string) string {
	nq := value == ""
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '"' || ch == '\\' || ch == '(' || ch == ')' ||
			ch == '[' || ch == ']' || ch == ',' || isRangeSpace(ch) {
			nq = true
			break
		}
	}
	var buf strings.Builder
	if nq {
		buf.WriteByte('"')
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch == '"' || ch == '\\' {
			buf.WriteByte(ch)
		}
		buf.WriteByte(ch)
	}
	if nq {
		buf.WriteByte('"')
	}
	return buf.String()
}

// rangeMakeText is make_range (rangetypes.c:2016) followed by range_out: it
// validates the bound pair, applies the type's canonical procedure and returns
// the canonical text spelling.
//
// The validation half is range_serialize (rangetypes.c:1791): lower > upper is
// 22000, equal-but-not-both-inclusive collapses to `empty`, and an infinite
// bound is never inclusive.
func rangeMakeText(info builtinRangeType, lower, upper rangeBound, empty bool, pos int, ctx *Context) (string, error) {
	var flags byte
	if empty {
		flags = rangeFlagEmpty
	} else {
		cmp, err := rangeCmpBoundValues(lower, upper, pos)
		if err != nil {
			return "", err
		}
		if cmp > 0 {
			return "", &ExecError{Code: "22000", Pos: pos,
				Message: "range lower bound must be less than or equal to range upper bound"}
		}
		if cmp == 0 && !(lower.inclusive && upper.inclusive) {
			flags |= rangeFlagEmpty
		} else {
			if lower.infinite {
				flags |= rangeFlagLbInf
			} else if lower.inclusive {
				flags |= rangeFlagLbInc
			}
			if upper.infinite {
				flags |= rangeFlagUbInf
			} else if upper.inclusive {
				flags |= rangeFlagUbInc
			}
		}
	}

	if flags&rangeFlagEmpty == 0 {
		var err error
		flags, lower, upper, err = rangeCanonicalize(info, flags, lower, upper, pos)
		if err != nil {
			return "", err
		}
	}

	var lbText, ubText string
	if rangeHasLbound(flags) {
		var err error
		if lbText, err = rangeBoundOut(lower.val, pos, ctx); err != nil {
			return "", err
		}
	}
	if rangeHasUbound(flags) {
		var err error
		if ubText, err = rangeBoundOut(upper.val, pos, ctx); err != nil {
			return "", err
		}
	}
	return rangeDeparse(flags, lbText, ubText), nil
}

// rangeCmpBoundValues is range_cmp_bound_values (rangetypes.c:2057) reduced to
// the make_range caller's needs: a lower bound that is -infinity is below
// everything, an upper bound that is +infinity is above everything, and two
// finite bounds compare by value. Exclusive-vs-inclusive does NOT enter here —
// range_serialize handles the equal case explicitly.
func rangeCmpBoundValues(lower, upper rangeBound, pos int) (int, error) {
	if lower.infinite {
		if upper.infinite {
			return -1, nil
		}
		return -1, nil
	}
	if upper.infinite {
		return -1, nil
	}
	return compareDatum(lower.val, upper.val, pos)
}

// rangeCanonicalize applies the discrete range types' canonical procedure:
// every int4range / int8range / daterange is normalised to `[)`
// (int4range_canonical & friends, rangetypes.c). Overflow at the subtype's
// maximum is an error upstream, not a wraparound.
func rangeCanonicalize(info builtinRangeType, flags byte, lower, upper rangeBound, pos int) (byte, rangeBound, rangeBound, error) {
	if info.canonical == rangeCanonicalNone {
		return flags, lower, upper, nil
	}
	// Lower bound: exclusive → inclusive by adding one step.
	if rangeHasLbound(flags) && flags&rangeFlagLbInc == 0 {
		v, err := rangeBoundStep(info.canonical, lower.val, pos)
		if err != nil {
			return 0, lower, upper, err
		}
		lower.val = v
		flags |= rangeFlagLbInc
	}
	// Upper bound: inclusive → exclusive by adding one step.
	if rangeHasUbound(flags) && flags&rangeFlagUbInc != 0 {
		v, err := rangeBoundStep(info.canonical, upper.val, pos)
		if err != nil {
			return 0, lower, upper, err
		}
		upper.val = v
		flags &^= rangeFlagUbInc
	}
	return flags, lower, upper, nil
}

// rangeBoundStep adds one to a discrete bound value, raising the subtype's own
// out-of-range error at the top of its domain exactly as
// int4range_canonical / int8range_canonical / daterange_canonical do.
func rangeBoundStep(kind rangeCanonicalKind, d Datum, pos int) (Datum, error) {
	switch kind {
	case rangeCanonicalInt4:
		if d.Kind != KindInt {
			return d, nil
		}
		if d.Int == math.MaxInt32 {
			return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "integer out of range"}
		}
		return Datum{Kind: KindInt, Int: d.Int + 1}, nil
	case rangeCanonicalInt8:
		if d.Kind != KindInt {
			return d, nil
		}
		if d.Int == math.MaxInt64 {
			return Datum{}, &ExecError{Code: "22003", Pos: pos, Message: "bigint out of range"}
		}
		return Datum{Kind: KindInt, Int: d.Int + 1}, nil
	case rangeCanonicalDate:
		if d.Kind != KindTime {
			return d, nil
		}
		// ±infinity dates are NOT stepped (DATE_NOT_FINITE guard in
		// daterange_canonical).
		if d.Int == math.MaxInt64 || d.Int == math.MinInt64 {
			return d, nil
		}
		next := d.TimeValue().AddDate(0, 0, 1)
		// IS_VALID_DATE — upstream's upper limit is 5874898-01-01 exclusive.
		if next.Year() > 5874897 {
			return Datum{}, &ExecError{Code: "22008", Pos: pos, Message: "date out of range"}
		}
		return NewDateDatum(time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, time.UTC)), nil
	}
	return d, nil
}

// rangeBoundIn runs the subtype's input function over one raw bound string.
// Its errors are the SUBTYPE's errors, verbatim — `'[a,4)'::int4range` is
// `invalid input syntax for type integer: "a"` in PG, not a range error.
func rangeBoundIn(info builtinRangeType, s string, pos int, ctx *Context) (Datum, error) {
	return evalCast(NewStringDatum(s), info.subtype, pos, ctx)
}

// rangeBoundOut runs the subtype's output function over one bound value.
// `::text` is goopg's spelling of "the type's output function".
func rangeBoundOut(d Datum, pos int, ctx *Context) (string, error) {
	out, err := evalCast(d, "text", pos, ctx)
	if err != nil {
		return "", err
	}
	if out.IsNull() {
		return "", nil
	}
	// evalCast's "text" arm converts the kinds that need a session GUC to
	// render (KindTime needs DateStyle/TimeZone, KindBytes needs
	// bytea_output) and returns every OTHER kind unchanged — KindNumeric and
	// KindInterval come back as themselves, not as a string. Datum.Format is
	// the renderer for those, so take it whenever the cast did not actually
	// produce a string; calling StringValue on a KindNumeric yields "".
	if s, ok := datumAsString(out); ok {
		return s, nil
	}
	return out.Format(), nil
}

// rangeIn is range_in (rangetypes.c:90): parse, run the subtype's input
// function over each present bound, then make_range. The result is the
// canonical text spelling, which is how goopg carries a range value.
func rangeIn(typeName, input string, pos int, ctx *Context) (string, error) {
	info, ok := lookupRangeTypeForIO(typeName, ctx)
	if !ok {
		return "", fmt.Errorf("not a range type: %s", typeName)
	}
	flags, lbStr, ubStr, err := rangeParse(input, pos)
	if err != nil {
		return "", err
	}
	var lower, upper rangeBound
	lower.lower = true
	if rangeHasLbound(flags) {
		if lower.val, err = rangeBoundIn(info, lbStr, pos, ctx); err != nil {
			return "", err
		}
	}
	if rangeHasUbound(flags) {
		if upper.val, err = rangeBoundIn(info, ubStr, pos, ctx); err != nil {
			return "", err
		}
	}
	lower.infinite = flags&rangeFlagLbInf != 0
	lower.inclusive = flags&rangeFlagLbInc != 0
	upper.infinite = flags&rangeFlagUbInf != 0
	upper.inclusive = flags&rangeFlagUbInc != 0
	return rangeMakeText(info, lower, upper, flags&rangeFlagEmpty != 0, pos, ctx)
}

// rangeConstructorFlags decodes the third argument of range_constructor3
// (rangetypes.c range_constructor3 / range_get_flags): exactly one of "[]",
// "[)", "(]", "()".
func rangeConstructorFlags(s string, pos int) (lowerInc, upperInc bool, err error) {
	switch s {
	case "[]":
		return true, true, nil
	case "[)":
		return true, false, nil
	case "(]":
		return false, true, nil
	case "()":
		return false, false, nil
	}
	return false, false, &ExecError{Code: "22P02", Pos: pos,
		Message: "invalid range bound flags",
		Hint:    `Valid values are "[]", "[)", "(]", and "()".`}
}

// evalRangeConstructor implements range_constructor2 / range_constructor3 for
// the six built-in range types (pg_proc.dat OIDs 3840/3841, 3844/3845,
// 3933/3934, 3937/3938, 3941/3942, 3945/3946 — all already present in goopg's
// pg_proc seed, which is why they resolve in the catalog but used to 42883 at
// execution: the catalog advertised a function the executor never implemented).
//
// Both are proisstrict = 'f': a NULL bound means an INFINITE bound, not a NULL
// result. A NULL flags argument, however, is its own error.
func evalRangeConstructor(name string, args []Datum, pos int, ctx *Context) (Datum, error) {
	info, ok := builtinRangeTypes[name]
	if !ok {
		return Datum{}, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("function %s does not exist", name)}
	}
	if len(args) < 2 || len(args) > 3 {
		return Datum{}, &ExecError{Code: "42883", Pos: pos,
			Message: fmt.Sprintf("function %s(%d args) does not exist", name, len(args))}
	}
	lowerInc, upperInc := true, false
	if len(args) == 3 {
		if args[2].IsNull() {
			return Datum{}, &ExecError{Code: "22004", Pos: pos,
				Message: "range constructor flags argument must not be null"}
		}
		var err error
		if lowerInc, upperInc, err = rangeConstructorFlags(args[2].Format(), pos); err != nil {
			return Datum{}, err
		}
	}
	var lower, upper rangeBound
	lower.lower = true
	if args[0].IsNull() {
		lower.infinite = true
	} else {
		v, err := evalCast(args[0], info.subtype, pos, ctx)
		if err != nil {
			return Datum{}, err
		}
		lower.val = v
		lower.inclusive = lowerInc
	}
	if args[1].IsNull() {
		upper.infinite = true
	} else {
		v, err := evalCast(args[1], info.subtype, pos, ctx)
		if err != nil {
			return Datum{}, err
		}
		upper.val = v
		upper.inclusive = upperInc
	}
	text, err := rangeMakeText(info, lower, upper, false, pos, ctx)
	if err != nil {
		return Datum{}, err
	}
	return NewStringDatum(text), nil
}
