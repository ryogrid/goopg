// Package cash ports PostgreSQL's money input and output functions
// (postgres/src/backend/utils/adt/cash.c cash_in / cash_out) over the monetary
// locale data in Conv. In and Out use the C / POSIX locale: frac_digits 2,
// currency symbol "$", decimal point ".", thousands separator "," grouped by 3,
// sign before the currency symbol.
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

// Conv is the monetary locale data cash_in and cash_out read
// (PGLC_localeconv). Char fields are as localeconv returns them, CHAR_MAX
// (127) or -1 when the locale leaves them unset.
type Conv struct {
	CurrencySymbol  string
	MonDecimalPoint string
	MonThousandsSep string
	MonGrouping     int
	PositiveSign    string
	NegativeSign    string
	FracDigits      int
	PCsPrecedes     int
	PSepBySpace     int
	NCsPrecedes     int
	NSepBySpace     int
	PSignPosn       int
	NSignPosn       int
}

// CLocale is the C and POSIX locale's monetary data.
var CLocale = Conv{MonGrouping: 127, FracDigits: 127, PCsPrecedes: 127, PSepBySpace: 127,
	NCsPrecedes: 127, NSepBySpace: 127, PSignPosn: 127, NSignPosn: 127}

// symbols applies cash_in's and cash_out's defaults to the locale data.
func (c Conv) symbols() (fpoint int, dsymbol byte, ssymbol, csymbol, psymbol, nsymbol string) {
	fpoint = c.FracDigits
	if fpoint < 0 || fpoint > 10 {
		fpoint = 2 // best guess, as cash.c says
	}
	dsymbol = '.'
	if len(c.MonDecimalPoint) == 1 {
		dsymbol = c.MonDecimalPoint[0]
	}
	ssymbol = c.MonThousandsSep
	if ssymbol == "" {
		ssymbol = ","
		if dsymbol == ',' {
			ssymbol = "."
		}
	}
	csymbol = c.CurrencySymbol
	if csymbol == "" {
		csymbol = "$"
	}
	psymbol = c.PositiveSign
	if psymbol == "" {
		psymbol = "+"
	}
	nsymbol = c.NegativeSign
	if nsymbol == "" {
		nsymbol = "-"
	}
	return
}

// FracDigits is the number of fraction digits a money value has under c.
func (c Conv) FractionDigits() int {
	f, _, _, _, _, _ := c.symbols()
	return f
}

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

// In is cash_in under the C locale.
func In(str string) (int64, error) { return InLocale(str, CLocale) }

// InLocale is cash_in: it parses str to a Cash value, in units of
// 10^-frac_digits. Like PG it accumulates the amount in the negative so the
// most negative value is reachable.
func InLocale(str string, conv Conv) (int64, error) {
	fpoint, dsymbol, ssymbol, csymbol, psymbol, nsymbol := conv.symbols()
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

// Out is cash_out under the C locale: "$1,234.56", "-$1,234.56".
func Out(value int64) string { return OutLocale(value, CLocale) }

// OutLocale is cash_out: the digits with the locale's separators, then the
// currency symbol and sign placed by p/n_sign_posn, p/n_cs_precedes and
// p/n_sep_by_space.
func OutLocale(value int64, conv Conv) string {
	points, dsymbol, ssymbol, csymbol, _, nsymbol := conv.symbols()
	monGroup := conv.MonGrouping
	if monGroup <= 0 || monGroup > 6 {
		monGroup = 3
	}
	var u uint64
	signsymbol := conv.PositiveSign
	signPosn, csPrecedes, sepBySpace := conv.PSignPosn, conv.PCsPrecedes, conv.PSepBySpace
	if value < 0 {
		signsymbol = nsymbol
		signPosn, csPrecedes, sepBySpace = conv.NSignPosn, conv.NCsPrecedes, conv.NSepBySpace
		u = uint64(-(value + 1)) + 1
	} else {
		u = uint64(value)
	}
	buf := make([]byte, 0, 32) // built right to left, reversed below
	digitPos := points
	for {
		if points != 0 && digitPos == 0 {
			buf = append(buf, dsymbol)
		} else if digitPos < 0 && digitPos%monGroup == 0 {
			for i := len(ssymbol) - 1; i >= 0; i-- {
				buf = append(buf, ssymbol[i])
			}
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
	num := string(buf)
	sp := func(n int) string {
		if sepBySpace == n {
			return " "
		}
		return ""
	}
	cs := csPrecedes != 0
	switch signPosn {
	case 0:
		if cs {
			return "(" + csymbol + sp(1) + num + ")"
		}
		return "(" + num + sp(1) + csymbol + ")"
	case 2:
		if cs {
			return csymbol + sp(1) + num + sp(2) + signsymbol
		}
		return num + sp(1) + csymbol + sp(2) + signsymbol
	case 3:
		if cs {
			return signsymbol + sp(2) + csymbol + sp(1) + num
		}
		return num + sp(1) + signsymbol + sp(2) + csymbol
	case 4:
		if cs {
			return csymbol + sp(2) + signsymbol + sp(1) + num
		}
		return num + sp(1) + csymbol + sp(2) + signsymbol
	}
	if cs {
		return signsymbol + sp(2) + csymbol + sp(1) + num
	}
	return signsymbol + sp(2) + num + sp(1) + csymbol
}

var smallWords = [...]string{
	"zero", "one", "two", "three", "four", "five", "six", "seven",
	"eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen",
	"fifteen", "sixteen", "seventeen", "eighteen", "nineteen", "twenty",
	"thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety",
}

// appendNumWord is cash.c append_num_word for 0..999.
func appendNumWord(b *strings.Builder, value uint64) {
	big := smallWords[18:]
	tu := value % 100
	if value <= 20 {
		b.WriteString(smallWords[value])
		return
	}
	if tu == 0 {
		b.WriteString(smallWords[value/100] + " hundred")
		return
	}
	if value > 99 {
		switch {
		case value%10 == 0 && tu > 10:
			b.WriteString(smallWords[value/100] + " hundred " + big[tu/10])
		case tu < 20:
			b.WriteString(smallWords[value/100] + " hundred and " + smallWords[tu])
		default:
			b.WriteString(smallWords[value/100] + " hundred " + big[tu/10] + " " + smallWords[tu%10])
		}
		return
	}
	switch {
	case value%10 == 0 && tu > 10:
		b.WriteString(big[tu/10])
	case tu < 20:
		b.WriteString(smallWords[tu])
	default:
		b.WriteString(big[tu/10] + " " + smallWords[tu%10])
	}
}

// Words is cash_words: the amount spelled out in English, first letter
// capitalised ("One hundred twenty three dollars and zero cents").
func Words(value int64) string {
	var b strings.Builder
	if value < 0 {
		value = -value // wraps at the minimum, as the C code does
		b.WriteString("minus ")
	}
	val := uint64(value)
	dollars := val / 100
	m0 := val % 100
	parts := []struct {
		div  uint64
		name string
	}{
		{100000000000000000, " quadrillion "}, {100000000000000, " trillion "},
		{100000000000, " billion "}, {100000000, " million "}, {100000, " thousand "},
	}
	for _, p := range parts {
		if m := (val / p.div) % 1000; m != 0 {
			appendNumWord(&b, m)
			b.WriteString(p.name)
		}
	}
	if m1 := (val / 100) % 1000; m1 != 0 {
		appendNumWord(&b, m1)
	}
	if dollars == 0 {
		b.WriteString("zero")
	}
	if dollars == 1 {
		b.WriteString(" dollar and ")
	} else {
		b.WriteString(" dollars and ")
	}
	appendNumWord(&b, m0)
	if m0 == 1 {
		b.WriteString(" cent")
	} else {
		b.WriteString(" cents")
	}
	s := b.String()
	if s != "" && s[0] >= 'a' && s[0] <= 'z' {
		s = string(s[0]-'a'+'A') + s[1:]
	}
	return s
}
