// Package cash ports PostgreSQL's money input and output functions
// (postgres/src/backend/utils/adt/cash.c cash_in / cash_out) for the C / POSIX
// monetary locale: frac_digits 2, currency symbol "$", decimal point ".",
// thousands separator "," grouped by 3, sign before the currency symbol
// (p/n_sign_posn CHAR_MAX → case 1, cs_precedes, no separating space).
//
// A money value is an int64 count of cents (Cash). goopg's executor keeps it
// as a scale-2 numeric Datum whose mantissa is that count, so comparison,
// ordering and numeric arithmetic are exact. M0149-0001k1.
package cash

import (
	"fmt"
	"math"
	"strings"
)

const (
	fpoint  = 2
	dsymbol = '.'
	ssymbol = ","
	csymbol = "$"
	psymbol = "+"
	nsymbol = "-"
)

// RangeError is cash_in's 22003 "value … is out of range for type money".
type RangeError struct{ Input string }

func (e *RangeError) Error() string {
	return fmt.Sprintf("value \"%s\" is out of range for type money", e.Input)
}

// SyntaxError is cash_in's 22P02 "invalid input syntax for type money".
type SyntaxError struct{ Input string }

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("invalid input syntax for type money: \"%s\"", e.Input)
}

func isSpace(c byte) bool { return c == ' ' || (c >= '\t' && c <= '\r') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// In is cash_in: it parses str to a count of cents. Like PG it accumulates
// the amount in the negative so the most negative value is reachable.
func In(str string) (int64, error) {
	s := str
	skipSpace := func() {
		for len(s) > 0 && isSpace(s[0]) {
			s = s[1:]
		}
	}
	skipSpace()
	s = strings.TrimPrefix(s, csymbol)
	skipSpace()
	var sgn int64 = 1
	switch {
	case strings.HasPrefix(s, nsymbol):
		sgn = -1
		s = s[len(nsymbol):]
	case strings.HasPrefix(s, "("):
		sgn = -1
		s = s[1:]
	case strings.HasPrefix(s, psymbol):
		s = s[len(psymbol):]
	}
	skipSpace()
	s = strings.TrimPrefix(s, csymbol)
	skipSpace()

	var value int64
	dec := 0
	seenDot := false
	for ; len(s) > 0; s = s[1:] {
		c := s[0]
		if isDigit(c) && (!seenDot || dec < fpoint) {
			d := int64(c - '0')
			if value < (math.MinInt64+d)/10 {
				return 0, &RangeError{str}
			}
			value = value*10 - d
			if seenDot {
				dec++
			}
		} else if c == dsymbol && !seenDot {
			seenDot = true
		} else if strings.HasPrefix(s, ssymbol) {
			s = s[len(ssymbol)-1:]
		} else {
			break
		}
	}
	// Round off if there's another digit.
	if len(s) > 0 && isDigit(s[0]) && s[0] >= '5' {
		if value == math.MinInt64 {
			return 0, &RangeError{str}
		}
		value--
	}
	for ; dec < fpoint; dec++ {
		if value < math.MinInt64/10 {
			return 0, &RangeError{str}
		}
		value *= 10
	}
	for len(s) > 0 && isDigit(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 {
		switch {
		case isSpace(s[0]) || s[0] == ')':
			s = s[1:]
		case strings.HasPrefix(s, nsymbol):
			sgn = -1
			s = s[len(nsymbol):]
		case strings.HasPrefix(s, psymbol):
			s = s[len(psymbol):]
		case strings.HasPrefix(s, csymbol):
			s = s[len(csymbol):]
		default:
			return 0, &SyntaxError{str}
		}
	}
	if sgn > 0 {
		if value == math.MinInt64 {
			return 0, &RangeError{str}
		}
		return -value, nil
	}
	return value, nil
}

// Out is cash_out: "$1,234.56", "-$1,234.56".
func Out(value int64) string {
	var u uint64
	sign := ""
	if value < 0 {
		sign = nsymbol
		u = uint64(-(value + 1)) + 1
	} else {
		u = uint64(value)
	}
	const monGroup = 3
	buf := make([]byte, 0, 32) // built right to left, reversed below
	digitPos := fpoint
	for {
		if digitPos == 0 {
			buf = append(buf, dsymbol)
		} else if digitPos < 0 && digitPos%monGroup == 0 {
			buf = append(buf, ssymbol...)
		}
		buf = append(buf, byte('0'+u%10))
		u /= 10
		digitPos--
		if u == 0 && digitPos < 0 {
			break
		}
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return sign + csymbol + string(buf)
}
