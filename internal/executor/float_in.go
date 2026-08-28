package executor

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// float_in.go — PG-faithful float4/float8 text input (M0134-0166).
//
// Ported from float8in_internal / float4in_internal
// (postgres/src/backend/utils/adt/float.c:395 and :180). Before this file
// goopg had FOUR independent, mutually inconsistent float-input paths and one
// of them did no parsing at all:
//
//	evalCast          `'x'::float8`      — NO KindString arm; returned the
//	                                      input datum UNCHANGED, so
//	                                      `'N A N'::float8` "succeeded" as the
//	                                      string "N A N" and `'10e400'::float8`
//	                                      stored an out-of-range text.
//	evalTypedStringLit `float8 'x'`      — validated with strconv.ParseFloat but
//	                                      then fell back to the raw spelling for
//	                                      NaN/Infinity ("NAN" stayed "NAN").
//	pgFloatFromDatum   column assignment — trimmed first and reported the
//	                                      TRIMMED text in its 22P02, and mapped
//	                                      strconv's ErrRange to 22P02 instead of
//	                                      PG's dedicated 22003.
//	pg_input_error_info                  — its own third message spelling.
//
// They are twins under Hard-won Rule #2, so they now share this one function.
//
// The behaviour reproduced here (see float8in_internal's own header comment):
//  1. leading AND trailing whitespace are skipped;
//  2. an empty (or all-whitespace) input is a syntax error;
//  3. a syntax error reports the ORIGINAL, untrimmed string;
//  4. an out-of-range magnitude is 22003 and reports only the numeric TOKEN
//     (leading whitespace and trailing junk already stripped), and says
//     "double precision"/"real" per the width;
//  5. a value that underflows to a denormal — nonzero but ERANGE on some
//     platforms — is NOT an error, only a true zero/infinite result is.

// floatInWhitespace is C isspace() for the "C" locale, which is what
// float8in_internal's isspace() loop and strtod's own leading-space skip use.
const floatInWhitespace = " \t\n\v\f\r"

// strtodToken returns the longest prefix of s that strtod() would consume,
// or "" if strtod would consume nothing (endptr == num). It accepts the C99
// forms PG relies on: [+-]?(digits[.digits] | .digits)([eE][+-]?digits)? and
// the [+-]?(inf|infinity|nan) spellings float8in_internal checks by hand.
// Hexadecimal float input ("0x1p3") is deliberately NOT accepted — see the
// "we consider these forms unportable" comment in float8in_internal.
func strtodToken(s string) string {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	rest := s[i:]
	if len(rest) >= 8 && strings.EqualFold(rest[:8], "infinity") {
		return s[:i+8]
	}
	if len(rest) >= 3 && strings.EqualFold(rest[:3], "inf") {
		return s[:i+3]
	}
	if len(rest) >= 3 && strings.EqualFold(rest[:3], "nan") {
		return s[:i+3]
	}
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return ""
	}
	// An exponent is only consumed when at least one exponent digit follows;
	// otherwise strtod backs up and leaves the 'e' as trailing junk.
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j {
			i = k
		}
	}
	return s[:i]
}

// floatInTypeName is the type name PG puts in float input errors: float4in
// says "real", float8in says "double precision".
func floatInTypeName(bits int) string {
	if bits == 32 {
		return "real"
	}
	return "double precision"
}

// floatIn is float8in_internal (bits == 64) / float4in_internal (bits == 32)
// with endptr_p == NULL, i.e. trailing junk is an error. orig is the string as
// the user wrote it; it is what a syntax error reports.
func floatIn(orig string, bits int) (float64, *ExecError) {
	typeName := floatInTypeName(bits)
	syntaxErr := func() *ExecError {
		return &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}

	// float8in_internal:402 — skip leading whitespace, then reject an
	// empty string up front "to avoid the vagaries of strtod()".
	num := strings.TrimLeft(orig, floatInWhitespace)
	if num == "" {
		return 0, syntaxErr()
	}
	token := strtodToken(num)
	if token == "" {
		return 0, syntaxErr()
	}
	v, err := strconv.ParseFloat(token, bits)
	if err == nil && v == 0 && !floatTokenIsZero(token) {
		// strconv.ParseFloat reports ErrRange only on OVERFLOW; a value that
		// underflows all the way to zero comes back as a clean 0. glibc's
		// strtod sets ERANGE for it, so float8in_internal's `val == 0.0`
		// branch fires and PG raises 22003 — `SELECT '10e-400'::float8` is an
		// error upstream, not 0. Reconstruct that here: a token whose
		// significand has a nonzero digit but evaluates to zero underflowed.
		return 0, &ExecError{Code: "22003",
			Message: fmt.Sprintf("%q is out of range for type %s", token, typeName)}
	}
	if err != nil {
		if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
			// float8in_internal:469 — ERANGE is only a real overflow when the
			// result is zero or (±)HUGE_VAL; a denormal keeps its value. The
			// message names the numeric token, not orig_string.
			if v == 0 || math.IsInf(v, 0) {
				return 0, &ExecError{Code: "22003",
					Message: fmt.Sprintf("%q is out of range for type %s", token, typeName)}
			}
		} else {
			return 0, syntaxErr()
		}
	}
	// float8in_internal:494 — skip trailing whitespace, then complain about
	// anything left over. Note PG reaches this AFTER the range check, so
	// "10e400junk" reports the overflow, not the junk.
	if strings.TrimLeft(num[len(token):], floatInWhitespace) != "" {
		return 0, syntaxErr()
	}
	if math.IsNaN(v) {
		// Canonical quiet NaN, matching get_float8_nan()
		// (postgres/src/include/utils/float.h). strconv.ParseFloat("NaN")
		// sets the payload bit, which is byte-visible to a PG standby reading
		// goopg's heap and to float8send — see pgFloatFromDatum's note.
		v = math.Float64frombits(0x7ff8000000000000)
	}
	return v, nil
}

// floatInDatum runs floatIn and packages the result the way goopg carries
// floats: KindNumeric for finite values, KindString for NaN/±Infinity, both
// rendered through PGFloatOut's float8out/float4out-faithful shortest
// round-trip text (floatTextDatum). pos is stamped onto the error so the wire
// ErrorResponse carries an errposition.
func floatInDatum(orig string, bits int, pos int) (Datum, *ExecError) {
	v, err := floatIn(orig, bits)
	if err != nil {
		err.Pos = pos
		return Datum{}, err
	}
	return floatTextDatum(PGFloatOut(v, bits)), nil
}

// floatInDatumOrErr is floatInDatum for callers on an `error`-returning
// signature (evalCast, evalTypedStringLit). A typed-nil *ExecError in an
// `error` interface is non-nil, so the nil case must be returned explicitly.
func floatInDatumOrErr(orig string, bits, pos int) (Datum, error) {
	d, err := floatInDatum(orig, bits, pos)
	if err != nil {
		return Datum{}, err
	}
	return d, nil
}

// floatTokenIsZero reports whether a decimal float token's significand is all
// zero digits, i.e. the token denotes a genuine zero ("0", "-0.000", "0e5")
// rather than a value that merely underflowed to zero ("10e-400"). Only the
// mantissa matters; the exponent cannot turn a nonzero significand into an
// exact zero.
func floatTokenIsZero(token string) bool {
	mant := token
	if i := strings.IndexAny(mant, "eE"); i >= 0 {
		mant = mant[:i]
	}
	for _, c := range mant {
		if c >= '1' && c <= '9' {
			return false
		}
	}
	return true
}
