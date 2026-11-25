package executor

import (
	"fmt"
	"strings"
)

// Bit-string operators and casts (M0149-0001h1a1), ported from
// utils/adt/varbit.c. goopg carries a bit / bit varying value as its '0'/'1'
// digit text; the planner resolves the operators on bit-typed operands to
// these functions (bitStringOpCall), so each sees digit strings.

// isBitStringBuiltin reports the varbit.c functions evalBitStringFunc runs.
func isBitStringBuiltin(name string) bool {
	switch name {
	case "bitand", "bitor", "bitxor", "bitnot", "bitshiftleft", "bitshiftright", "bitcat",
		"bitoctetlength", "bitlength", "bit_bit_count", "bitgetbit", "bitsetbit":
		return true
	}
	return false
}

// evalBitStringFunc evaluates one of them. All are strict.
func evalBitStringFunc(name string, args []Datum, pos int) (Datum, error) {
	want := 2
	switch name {
	case "bitnot", "bitoctetlength", "bitlength", "bit_bit_count":
		want = 1
	case "bitsetbit":
		want = 3
	}
	if len(args) != want {
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("function %s does not exist", name)}
	}
	if anyNull(args) {
		return NullDatum, nil
	}
	a := args[0].StringValue()
	switch name {
	case "bitnot":
		return NewStringDatum(bitMap(a, func(x byte) byte { return x ^ 1 })), nil
	case "bitcat":
		return NewStringDatum(a + args[1].StringValue()), nil
	case "bitlength":
		return NewIntDatum(int64(len(a))), nil
	case "bitoctetlength":
		return NewIntDatum(int64((len(a) + 7) / 8)), nil
	case "bit_bit_count":
		return NewIntDatum(int64(strings.Count(a, "1"))), nil
	case "bitgetbit", "bitsetbit":
		n := args[1].Int
		if n < 0 || n >= int64(len(a)) {
			return Datum{}, &ExecError{Code: "2202E", Message: fmt.Sprintf("bit index %d out of valid range (0..%d)", n, len(a)-1)}
		}
		if name == "bitgetbit" {
			return NewIntDatum(int64(a[n] - '0')), nil
		}
		v := args[2].Int
		if v != 0 && v != 1 {
			return Datum{}, &ExecError{Code: "22023", Message: "new bit must be 0 or 1"}
		}
		out := []byte(a)
		out[n] = byte('0' + v)
		return NewStringDatum(string(out)), nil
	case "bitshiftleft", "bitshiftright":
		n := args[1].Int
		if name == "bitshiftright" {
			n = -n
		}
		return NewStringDatum(bitShiftLeft(a, n)), nil
	}
	b := args[1].StringValue()
	verb := map[string]string{"bitand": "AND", "bitor": "OR", "bitxor": "XOR"}[name]
	if len(a) != len(b) {
		// bit_and / bit_or / bitxor: "cannot %s bit strings of different sizes".
		return Datum{}, &ExecError{Code: "22026", Message: fmt.Sprintf("cannot %s bit strings of different sizes", verb)}
	}
	out := make([]byte, len(a))
	for i := range out {
		x, y := a[i]-'0', b[i]-'0'
		var r byte
		switch name {
		case "bitand":
			r = x & y
		case "bitor":
			r = x | y
		default:
			r = x ^ y
		}
		out[i] = '0' + r
	}
	return NewStringDatum(string(out)), nil
}

// bitMap applies f to every digit (as 0/1).
func bitMap(s string, f func(byte) byte) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = '0' + f(s[i]-'0')
	}
	return string(out)
}

// bitShiftLeft is bitshiftleft: the length is kept, vacated positions are
// zero, a negative shift shifts right, and a shift of the whole length or
// more leaves all zeros.
func bitShiftLeft(s string, n int64) string {
	l := int64(len(s))
	if n >= l || -n >= l {
		return strings.Repeat("0", len(s))
	}
	if n >= 0 {
		return s[n:] + strings.Repeat("0", int(n))
	}
	return strings.Repeat("0", int(-n)) + s[:l+n]
}

// isBitSourceType reports a cast source typed bit or bit varying.
func isBitSourceType(t string) (bit, varbit bool) {
	switch strings.ToLower(t) {
	case "bit":
		return true, false
	case "varbit", "bit varying":
		return false, true
	}
	return false, false
}

// bitStringCast is the cast from a bit-typed source to an integer target
// (pg_cast.dat): bit → int4 (bittoint4) and bit → int8 (bittoint8) read the
// digits as an unsigned bit pattern into the target's two's complement,
// refusing more digits than the target has bits; there is no bit → int2
// cast, and none from bit varying (42846). ok is false for other targets.
func bitStringCast(v Datum, sourceType, target string, pos int) (Datum, bool, error) {
	isBit, isVarbit := isBitSourceType(sourceType)
	if (!isBit && !isVarbit) || v.IsNull() {
		return Datum{}, false, nil
	}
	var width int
	var display, rangeMsg string
	switch strings.ToLower(target) {
	case "int4", "integer", "int":
		width, display, rangeMsg = 32, "integer", "integer out of range"
	case "int8", "bigint":
		width, display, rangeMsg = 64, "bigint", "bigint out of range"
	case "int2", "smallint":
		display = "smallint"
	default:
		return Datum{}, false, nil
	}
	src := "bit"
	if isVarbit {
		src = "bit varying"
	}
	if width == 0 || isVarbit {
		return Datum{}, true, &ExecError{Code: "42846", Pos: pos, Message: fmt.Sprintf("cannot cast type %s to %s", src, display)}
	}
	digits := v.StringValue()
	if len(digits) > width {
		return Datum{}, true, &ExecError{Code: "22003", Message: rangeMsg}
	}
	var u uint64
	for i := 0; i < len(digits); i++ {
		u = u<<1 | uint64(digits[i]-'0')
	}
	if width == 32 {
		return NewIntDatum(int64(int32(uint32(u)))), true, nil
	}
	return NewIntDatum(int64(u)), true, nil
}

// bitExplicitLength is bit() / varbit() with isExplicit: digits truncated to
// n, and for bit(n) zero-padded on the right to exactly n.
func bitExplicitLength(digits string, n int, fixed bool) string {
	if len(digits) > n {
		return digits[:n]
	}
	if fixed && len(digits) < n {
		return digits + strings.Repeat("0", n-len(digits))
	}
	return digits
}
