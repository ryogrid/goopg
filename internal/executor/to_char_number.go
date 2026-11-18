package executor

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// This file ports the numeric half of PostgreSQL's to_char from
// postgres/src/backend/utils/adt/formatting.c: NUM_keywords, parse_format in its
// NUM mode, NUMDesc_prepare, NUM_processor, NUM_numpart_to_char, and the
// int4, int8, float4, float8 and numeric entry points. The locale values are the
// C locale's, which is what NUM_prepare_locale produces there.

const (
	numFDecimal = 1 << (iota + 1)
	numFLDecimal
	numFZero
	numFBlank
	numFFillMode
	numFLSign
	numFBracket
	numFMinus
	numFPlus
	numFRoman
	numFMulti
	numFPlusPost
	numFMinusPost
	numFEEEE
)

const (
	numLSignPre  = -1
	numLSignPost = 1
)

type numID int

const (
	numComma numID = iota
	numDec
	num0
	num9
	numB
	numC
	numD
	numE
	numFM
	numG
	numL
	numMI
	numPL
	numPR
	numRN
	numSG
	numSP
	numS
	numTH
	numV
	numRNLower
	numTHLower
)

// numKeywords is NUM_keywords: the first entry that matches wins.
var numKeywords = []struct {
	text string
	id   numID
}{
	{",", numComma}, {".", numDec}, {"0", num0}, {"9", num9}, {"B", numB}, {"C", numC}, {"D", numD},
	{"EEEE", numE}, {"FM", numFM}, {"G", numG}, {"L", numL}, {"MI", numMI}, {"PL", numPL}, {"PR", numPR},
	{"RN", numRN}, {"SG", numSG}, {"SP", numSP}, {"S", numS}, {"TH", numTH}, {"V", numV},
	{"b", numB}, {"c", numC}, {"d", numD}, {"eeee", numE}, {"fm", numFM}, {"g", numG}, {"l", numL},
	{"mi", numMI}, {"pl", numPL}, {"pr", numPR}, {"rn", numRNLower}, {"sg", numSG}, {"sp", numSP},
	{"s", numS}, {"th", numTHLower}, {"v", numV},
}

// numNode is one FormatNode: a pattern, or one literal byte.
type numNode struct {
	action bool
	id     numID
	ch     byte
}

// numDesc is NUMDesc.
type numDesc struct {
	pre, post, lsign, preLSignNum, multi, zeroStart, zeroEnd, flag int
}

func (n *numDesc) has(flag int) bool { return n.flag&flag != 0 }

func numSyntaxError(message, detail string) error {
	return &ExecError{Code: "42601", Message: message, Detail: detail}
}

// prepare is NUMDesc_prepare.
func (n *numDesc) prepare(id numID) error {
	if n.has(numFEEEE) && id != numE {
		return numSyntaxError(`"EEEE" must be the last pattern used`, "")
	}
	switch id {
	case num9:
		if n.has(numFBracket) {
			return numSyntaxError(`"9" must be ahead of "PR"`, "")
		}
		switch {
		case n.has(numFMulti):
			n.multi++
		case n.has(numFDecimal):
			n.post++
		default:
			n.pre++
		}
	case num0:
		if n.has(numFBracket) {
			return numSyntaxError(`"0" must be ahead of "PR"`, "")
		}
		if !n.has(numFZero) && !n.has(numFDecimal) {
			n.flag |= numFZero
			n.zeroStart = n.pre + 1
		}
		if !n.has(numFDecimal) {
			n.pre++
		} else {
			n.post++
		}
		n.zeroEnd = n.pre + n.post
	case numB:
		if n.pre == 0 && n.post == 0 && !n.has(numFZero) {
			n.flag |= numFBlank
		}
	case numD, numDec:
		if id == numD {
			n.flag |= numFLDecimal
		}
		if n.has(numFDecimal) {
			return numSyntaxError("multiple decimal points", "")
		}
		if n.has(numFMulti) {
			return numSyntaxError(`cannot use "V" and decimal point together`, "")
		}
		n.flag |= numFDecimal
	case numFM:
		n.flag |= numFFillMode
	case numS:
		if n.has(numFLSign) {
			return numSyntaxError(`cannot use "S" twice`, "")
		}
		if n.has(numFPlus) || n.has(numFMinus) || n.has(numFBracket) {
			return numSyntaxError(`cannot use "S" and "PL"/"MI"/"SG"/"PR" together`, "")
		}
		if !n.has(numFDecimal) {
			n.lsign = numLSignPre
			n.preLSignNum = n.pre
			n.flag |= numFLSign
		} else if n.lsign == 0 {
			n.lsign = numLSignPost
			n.flag |= numFLSign
		}
	case numMI:
		if n.has(numFLSign) {
			return numSyntaxError(`cannot use "S" and "MI" together`, "")
		}
		n.flag |= numFMinus
		if n.has(numFDecimal) {
			n.flag |= numFMinusPost
		}
	case numPL:
		if n.has(numFLSign) {
			return numSyntaxError(`cannot use "S" and "PL" together`, "")
		}
		n.flag |= numFPlus
		if n.has(numFDecimal) {
			n.flag |= numFPlusPost
		}
	case numSG:
		if n.has(numFLSign) {
			return numSyntaxError(`cannot use "S" and "SG" together`, "")
		}
		n.flag |= numFMinus | numFPlus
	case numPR:
		if n.has(numFLSign) || n.has(numFPlus) || n.has(numFMinus) {
			return numSyntaxError(`cannot use "PR" and "S"/"PL"/"MI"/"SG" together`, "")
		}
		n.flag |= numFBracket
	case numRN, numRNLower:
		if n.has(numFRoman) {
			return numSyntaxError(`cannot use "RN" twice`, "")
		}
		n.flag |= numFRoman
	case numV:
		if n.has(numFDecimal) {
			return numSyntaxError(`cannot use "V" and decimal point together`, "")
		}
		n.flag |= numFMulti
	case numE:
		if n.has(numFEEEE) {
			return numSyntaxError(`cannot use "EEEE" twice`, "")
		}
		if n.has(numFBlank) || n.has(numFFillMode) || n.has(numFLSign) || n.has(numFBracket) ||
			n.has(numFMinus) || n.has(numFPlus) || n.has(numFRoman) || n.has(numFMulti) {
			return numSyntaxError(`"EEEE" is incompatible with other formats`,
				`"EEEE" may only be used together with digit and decimal point patterns.`)
		}
		n.flag |= numFEEEE
	}
	if n.has(numFRoman) && n.flag&^(numFRoman|numFFillMode) != 0 {
		return numSyntaxError(`"RN" is incompatible with other formats`,
			`"RN" may only be used together with "FM".`)
	}
	return nil
}

// parseNumFormat is parse_format for a number template.
func parseNumFormat(format string) ([]numNode, *numDesc, error) {
	desc := &numDesc{}
	nodes := make([]numNode, 0, len(format))
	literal := func(c byte) { nodes = append(nodes, numNode{ch: c}) }
next:
	for i := 0; i < len(format); {
		for _, k := range numKeywords {
			if strings.HasPrefix(format[i:], k.text) {
				if err := desc.prepare(k.id); err != nil {
					return nil, nil, err
				}
				nodes = append(nodes, numNode{action: true, id: k.id})
				i += len(k.text)
				continue next
			}
		}
		if format[i] == '"' {
			// A quoted literal; a backslash quotes the next character.
			for i++; i < len(format); i++ {
				if format[i] == '"' {
					i++
					break
				}
				if format[i] == '\\' && i+1 < len(format) {
					i++
				}
				literal(format[i])
			}
			continue
		}
		// Outside quotes a backslash is special only before a double quote.
		if format[i] == '\\' && i+1 < len(format) && format[i+1] == '"' {
			i++
		}
		literal(format[i])
		i++
	}
	return nodes, desc, nil
}

// numProc is the to_char side of NUMProc. number ends in a NUL, as the C string
// does, because the digit writer can run into it.
type numProc struct {
	num          *numDesc
	sign         byte
	signWrote    bool
	numCount     int
	numIn        bool
	numCurr      int
	outPreSpaces int
	number       []byte
	numberP      int
	lastRelevant int
	out          []byte
}

func (np *numProc) isPredecSpace() bool {
	return !np.num.has(numFZero) && np.numberP == 0 && np.number[0] == '0' && np.num.post != 0
}

func (np *numProc) lastRelevantIsPoint() bool {
	return np.lastRelevant >= 0 && np.number[np.lastRelevant] == '.'
}

func (np *numProc) writeLocaleSign() {
	if np.sign == '-' {
		np.out = append(np.out, '-')
	} else {
		np.out = append(np.out, '+')
	}
}

// digitPart is NUM_numpart_to_char.
func (np *numProc) digitPart(id numID) {
	n := np.num
	if n.has(numFRoman) {
		return
	}
	np.numIn = false
	if !np.signWrote &&
		(np.numCurr >= np.outPreSpaces || (n.has(numFZero) && n.zeroStart == np.numCurr)) &&
		(!np.isPredecSpace() || np.lastRelevantIsPoint()) {
		switch {
		case n.has(numFLSign):
			if n.lsign == numLSignPre {
				np.writeLocaleSign()
				np.signWrote = true
			}
		case n.has(numFBracket):
			if np.sign == '+' {
				np.out = append(np.out, ' ')
			} else {
				np.out = append(np.out, '<')
			}
			np.signWrote = true
		case np.sign == '+':
			if !n.has(numFFillMode) {
				np.out = append(np.out, ' ')
			}
			np.signWrote = true
		case np.sign == '-':
			np.out = append(np.out, '-')
			np.signWrote = true
		}
	}

	if id == num9 || id == num0 || id == numD || id == numDec {
		switch {
		case np.numCurr < np.outPreSpaces && (n.zeroStart > np.numCurr || !n.has(numFZero)):
			if !n.has(numFFillMode) {
				np.out = append(np.out, ' ')
			}
		case n.has(numFZero) && np.numCurr < np.outPreSpaces && n.zeroStart <= np.numCurr:
			np.out = append(np.out, '0')
			np.numIn = true
		default:
			if np.number[np.numberP] == '.' {
				if !np.lastRelevantIsPoint() || n.has(numFFillMode) {
					np.out = append(np.out, '.')
				}
			} else {
				switch {
				case np.lastRelevant >= 0 && np.numberP > np.lastRelevant && id != num0:
				case np.isPredecSpace():
					if !n.has(numFFillMode) {
						np.out = append(np.out, ' ')
					} else if np.lastRelevantIsPoint() {
						np.out = append(np.out, '0')
					}
				default:
					np.out = append(np.out, np.number[np.numberP])
					np.numIn = true
				}
			}
			if np.number[np.numberP] != 0 {
				np.numberP++
			}
		}

		end := np.numCount
		if np.outPreSpaces != 0 {
			end++
		}
		if n.has(numFDecimal) {
			end++
		}
		if np.lastRelevant >= 0 && np.lastRelevant == np.numberP {
			end = np.numCurr
		}
		if np.numCurr+1 == end {
			if np.signWrote && n.has(numFBracket) {
				if np.sign == '+' {
					np.out = append(np.out, ' ')
				} else {
					np.out = append(np.out, '>')
				}
			} else if n.has(numFLSign) && n.lsign == numLSignPost {
				np.writeLocaleSign()
			}
		}
	}
	np.numCurr++
}

// numOrdinal is get_th.
func numOrdinal(number []byte, upper bool) (string, error) {
	last := number[len(number)-1]
	if last < '0' || last > '9' {
		return "", &ExecError{Code: "22P02", Message: fmt.Sprintf("%q is not a number", string(number))}
	}
	suffix := "th"
	if len(number) < 2 || number[len(number)-2] != '1' {
		switch last {
		case '1':
			suffix = "st"
		case '2':
			suffix = "nd"
		case '3':
			suffix = "rd"
		}
	}
	if upper {
		suffix = strings.ToUpper(suffix)
	}
	return suffix, nil
}

// numProcess is NUM_processor for to_char. A digit pattern past the end of the
// number writes the terminating NUL, and the C result ends at the first NUL.
func numProcess(nodes []numNode, n *numDesc, number string, outPreSpaces int, sign byte) (string, error) {
	if n.zeroStart != 0 {
		n.zeroStart--
	}
	if n.has(numFEEEE) {
		return number, nil
	}
	np := &numProc{num: n, sign: sign, number: append([]byte(number), 0), lastRelevant: -1}
	if n.has(numFPlus) || n.has(numFMinus) {
		np.signWrote = !(n.has(numFPlus) && !n.has(numFMinus))
	} else {
		if sign != '-' && n.has(numFFillMode) {
			n.flag &^= numFBracket
		}
		np.signWrote = sign == '+' && n.has(numFFillMode) && !n.has(numFLSign)
		if n.lsign == numLSignPre && n.pre == n.preLSignNum {
			n.lsign = numLSignPost
		}
	}
	np.numCount = n.post + n.pre - 1
	np.outPreSpaces = outPreSpaces
	if n.has(numFFillMode) && n.has(numFDecimal) {
		np.lastRelevant = lastRelevantDecimal(number)
		if np.lastRelevant >= 0 && n.zeroEnd > outPreSpaces {
			lastZero := min(len(number)-1, n.zeroEnd-outPreSpaces)
			if np.lastRelevant < lastZero {
				np.lastRelevant = lastZero
			}
		}
	}
	if !np.signWrote && outPreSpaces == 0 {
		np.numCount++
	}

	fill := n.has(numFFillMode)
	// separator writes a group separator, or a blank before the first digit.
	separator := func() {
		if np.numIn {
			np.out = append(np.out, ',')
		} else if !fill {
			np.out = append(np.out, ' ')
		}
	}
	for _, node := range nodes {
		if !node.action {
			np.out = append(np.out, node.ch)
			continue
		}
		switch node.id {
		case num9, num0, numDec, numD:
			np.digitPart(node.id)
		case numComma, numG:
			separator()
		case numL:
			np.out = append(np.out, ' ')
		case numRN, numRNLower:
			roman := string(np.number[np.numberP : len(np.number)-1])
			if node.id == numRNLower {
				roman = strings.ToLower(roman)
			}
			if !fill {
				roman = fmt.Sprintf("%15s", roman)
			}
			np.out = append(np.out, roman...)
		case numTH, numTHLower:
			if n.has(numFRoman) || np.number[0] == '#' || sign == '-' || n.has(numFDecimal) {
				continue
			}
			suffix, err := numOrdinal(np.number[:len(np.number)-1], node.id == numTH)
			if err != nil {
				return "", err
			}
			np.out = append(np.out, suffix...)
		case numMI, numPL:
			mark := byte('-')
			if node.id == numPL {
				mark = '+'
			}
			if sign == mark {
				np.out = append(np.out, mark)
			} else if !fill {
				np.out = append(np.out, ' ')
			}
		case numSG:
			np.out = append(np.out, sign)
		}
	}
	out := string(np.out)
	if i := strings.IndexByte(out, 0); i >= 0 {
		out = out[:i]
	}
	return out, nil
}

// lastRelevantDecimal is get_last_relevant_decnum, as an index, or -1.
func lastRelevantDecimal(number string) int {
	result := strings.IndexByte(number, '.')
	if result < 0 {
		return -1
	}
	for i := result + 1; i < len(number); i++ {
		if number[i] != '0' {
			result = i
		}
	}
	return result
}

// numOverflowText is the '#' string printed when the digits do not fit.
func numOverflowText(n *numDesc) string {
	b := []byte(strings.Repeat("#", n.pre+n.post+1))
	b[n.pre] = '.'
	return string(b)
}

// numNotFiniteEEEE is the '#' string EEEE prints for NaN and the infinities.
func numNotFiniteEEEE(n *numDesc) string {
	b := []byte(strings.Repeat("#", n.pre+n.post+6))
	b[0] = ' '
	b[n.pre+1] = '.'
	return string(b)
}

// numFit applies the padding and overflow rules shared by the entry points.
func numFit(n *numDesc, number string, preLen int) (string, int) {
	if preLen < n.pre {
		return number, n.pre - preLen
	}
	if preLen > n.pre {
		return numOverflowText(n), 0
	}
	return number, 0
}

func numSplitSign(text string) (string, byte) {
	if strings.HasPrefix(text, "-") {
		return text[1:], '-'
	}
	return text, '+'
}

func numIntegerPart(number string) int {
	if i := strings.IndexByte(number, '.'); i >= 0 {
		return i
	}
	return len(number)
}

// numIntegerText is the common tail of int4_to_char and int8_to_char.
func numIntegerText(n *numDesc, text string) (string, int, byte) {
	digits, sign := numSplitSign(text)
	preLen := len(digits)
	if n.post != 0 {
		digits += "." + strings.Repeat("0", n.post)
	}
	number, spaces := numFit(n, digits, preLen)
	return number, spaces, sign
}

// numIntToRoman is int_to_roman.
func numIntToRoman(number int32) string {
	if number > 3999 || number < 1 {
		return strings.Repeat("#", 15)
	}
	ones := [...]string{"", "I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX"}
	tens := [...]string{"", "X", "XX", "XXX", "XL", "L", "LX", "LXX", "LXXX", "XC"}
	hundreds := [...]string{"", "C", "CC", "CCC", "CD", "D", "DC", "DCC", "DCCC", "CM"}
	return strings.Repeat("M", int(number/1000)) + hundreds[number/100%10] + tens[number/10%10] + ones[number%10]
}

func numBigIntOutOfRange() error {
	return &ExecError{Code: "22003", Message: "bigint out of range"}
}

// numRoundDecimal rescales mantissa*10^-scale to target digits after the point,
// rounding halves away from zero as numeric_round does.
func numRoundDecimal(mantissa *big.Int, scale, target int) *big.Int {
	if scale <= target {
		factor := new(big.Int).Exp(bigTen, big.NewInt(int64(target-scale)), nil)
		return new(big.Int).Mul(mantissa, factor)
	}
	div := new(big.Int).Exp(bigTen, big.NewInt(int64(scale-target)), nil)
	q, r := new(big.Int).QuoRem(new(big.Int).Abs(mantissa), div, new(big.Int))
	if r.Lsh(r, 1).Cmp(div) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if mantissa.Sign() < 0 {
		q.Neg(q)
	}
	return q
}

// numDecimalText is numeric_out for a value with exactly scale fractional digits.
func numDecimalText(mantissa *big.Int, scale int) string {
	digits := new(big.Int).Abs(mantissa).String()
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if mantissa.Sign() < 0 {
		return "-" + digits
	}
	return digits
}

// numScientific is get_str_from_var_sci.
func numScientific(mantissa *big.Int, scale, rscale int) string {
	exponent, significandScale := 0, 0
	if mantissa.Sign() != 0 {
		significandScale = len(new(big.Int).Abs(mantissa).String()) - 1
		exponent = significandScale - scale
	}
	rounded := numRoundDecimal(mantissa, significandScale, rscale)
	return fmt.Sprintf("%se%+03d", numDecimalText(rounded, rscale), exponent)
}

// numericParts reads d as a numeric: a mantissa and scale, or the text of NaN or
// an infinity.
func numericParts(d Datum) (*big.Int, int, string, error) {
	switch d.Kind {
	case KindNumeric:
		if d.Flags&flagBigNumeric != 0 {
			return d.NumericBigValue(), int(d.NumericScaleValue()), "", nil
		}
		return big.NewInt(d.NumericMantissaValue()), int(d.NumericScaleValue()), "", nil
	case KindInt:
		return big.NewInt(d.Int), 0, "", nil
	}
	text := strings.TrimSpace(d.Format())
	switch strings.ToLower(text) {
	case "nan":
		return nil, 0, "NaN", nil
	case "infinity", "inf", "+infinity", "+inf":
		return nil, 0, "Infinity", nil
	case "-infinity", "-inf":
		return nil, 0, "-Infinity", nil
	}
	mantissa, scale, err := parseNumeric(text)
	if err != nil {
		return nil, 0, "", &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type numeric: %q", text)}
	}
	return mantissa, int(scale), "", nil
}

// numericToCharText is the type-dependent part of numeric_to_char.
func numericToCharText(n *numDesc, d Datum) (string, int, byte, error) {
	mantissa, scale, special, err := numericParts(d)
	if err != nil {
		return "", 0, 0, err
	}
	switch {
	case n.has(numFRoman):
		value := int32(math.MaxInt32)
		if special == "" {
			rounded := numRoundDecimal(mantissa, scale, 0)
			if rounded.IsInt64() && rounded.Int64() >= math.MinInt32 && rounded.Int64() <= math.MaxInt32 {
				value = int32(rounded.Int64())
			}
		}
		return numIntToRoman(value), 0, 0, nil
	case n.has(numFEEEE):
		if special != "" {
			return numNotFiniteEEEE(n), 0, 0, nil
		}
		text := numScientific(mantissa, scale, n.post)
		if !strings.HasPrefix(text, "-") {
			text = " " + text
		}
		return text, 0, 0, nil
	}
	var text string
	if n.has(numFMulti) {
		n.pre += n.multi
		scale -= n.multi
	}
	if special != "" {
		text = special
	} else {
		text = numDecimalText(numRoundDecimal(mantissa, scale, n.post), n.post)
	}
	number, sign := numSplitSign(text)
	number, spaces := numFit(n, number, numIntegerPart(number))
	return number, spaces, sign, nil
}

func int4ToCharText(n *numDesc, value int32) (string, int, byte) {
	switch {
	case n.has(numFRoman):
		return numIntToRoman(value), 0, 0
	case n.has(numFEEEE):
		return numFloatScientific(float64(value), n.post), 0, 0
	}
	if n.has(numFMulti) {
		// (int32) pow(10, multi): out of range converts to INT_MIN on x86-64.
		factor := int32(math.MinInt32)
		if p := math.Pow10(n.multi); p < math.MaxInt32 {
			factor = int32(p)
		}
		value *= factor
		n.pre += n.multi
	}
	return numIntegerText(n, strconv.FormatInt(int64(value), 10))
}

func int8ToCharText(n *numDesc, value int64) (string, int, byte, error) {
	switch {
	case n.has(numFRoman):
		roman := int32(math.MaxInt32)
		if value >= math.MinInt32 && value <= math.MaxInt32 {
			roman = int32(value)
		}
		return numIntToRoman(roman), 0, 0, nil
	case n.has(numFEEEE):
		text := numScientific(big.NewInt(value), 0, n.post)
		if !strings.HasPrefix(text, "-") {
			text = " " + text
		}
		return text, 0, 0, nil
	}
	if n.has(numFMulti) {
		if n.multi > 18 {
			return "", 0, 0, numBigIntOutOfRange()
		}
		product := new(big.Int).Mul(big.NewInt(value), new(big.Int).Exp(bigTen, big.NewInt(int64(n.multi)), nil))
		if !product.IsInt64() {
			return "", 0, 0, numBigIntOutOfRange()
		}
		value = product.Int64()
		n.pre += n.multi
	}
	number, spaces, sign := numIntegerText(n, strconv.FormatInt(value, 10))
	return number, spaces, sign, nil
}

// numFloatScientific is psprintf("%+.*e") with the plus sign turned into a space.
func numFloatScientific(value float64, post int) string {
	text := strconv.FormatFloat(value, 'e', post, 64)
	if !strings.HasPrefix(text, "-") {
		text = " " + text
	}
	return text
}

// numFloatFixed is psprintf("%.*f"), including how PG's snprintf spells the
// values that are not finite.
func numFloatFixed(value float64, post int) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(value, 'f', post, 64)
}

// floatToCharText is float4_to_char (digits 6) and float8_to_char (digits 15).
func floatToCharText(n *numDesc, value float64, single bool) (string, int, byte) {
	digits := 15
	if single {
		digits = 6
	}
	switch {
	case n.has(numFRoman):
		rounded := math.RoundToEven(value)
		roman := int32(math.MaxInt32)
		if !math.IsNaN(rounded) && rounded >= math.MinInt32 && rounded < -math.MinInt32 {
			roman = int32(rounded)
		}
		return numIntToRoman(roman), 0, 0
	case n.has(numFEEEE):
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return numNotFiniteEEEE(n), 0, 0
		}
		return numFloatScientific(value, n.post), 0, 0
	}
	if n.has(numFMulti) {
		if single {
			value = float64(float32(value) * float32(math.Pow10(n.multi)))
		} else {
			value *= math.Pow10(n.multi)
		}
		n.pre += n.multi
	}
	preLen := len(numFloatFixed(math.Abs(value), 0))
	if preLen >= digits {
		n.post = 0
	} else if preLen+n.post > digits {
		n.post = digits - preLen
	}
	number, sign := numSplitSign(numFloatFixed(value, n.post))
	number, spaces := numFit(n, number, numIntegerPart(number))
	return number, spaces, sign
}

// toCharNumber is to_char for a number. typeName is the argument's SQL type; the
// value's Datum kind does not separate int4 from int8 or float8 from numeric, and
// those format differently.
func toCharNumber(d Datum, typeName, format string) (string, error) {
	if format == "" {
		return "", nil
	}
	nodes, n, err := parseNumFormat(format)
	if err != nil {
		return "", err
	}
	var (
		number string
		spaces int
		sign   byte
	)
	asFloat := func(bits int) (float64, error) {
		text := strings.TrimSpace(d.Format())
		value, err := strconv.ParseFloat(text, bits)
		if err != nil && !math.IsInf(value, 0) {
			return 0, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type double precision: %q", text)}
		}
		return value, nil
	}
	switch typeName {
	case "int4", "integer", "int":
		number, spaces, sign = int4ToCharText(n, int32(d.Int))
	case "int8", "bigint":
		number, spaces, sign, err = int8ToCharText(n, d.Int)
	case "float4", "real":
		var value float64
		if value, err = asFloat(32); err == nil {
			number, spaces, sign = floatToCharText(n, value, true)
		}
	case "float8", "double precision", "float", "int2", "smallint":
		// No to_char(int2) exists; float8 is the preferred numeric type, so an
		// int2 argument resolves to it.
		var value float64
		if value, err = asFloat(64); err == nil {
			number, spaces, sign = floatToCharText(n, value, false)
		}
	default:
		if d.Kind == KindInt {
			number, spaces, sign, err = int8ToCharText(n, d.Int)
		} else {
			number, spaces, sign, err = numericToCharText(n, d)
		}
	}
	if err != nil {
		return "", err
	}
	return numProcess(nodes, n, number, spaces, sign)
}
