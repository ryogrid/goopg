package pgnodes

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// PostgreSQL type OIDs used by the scalar Const subset (pg_type.dat).
const (
	OidBool    = 16
	OidInt8    = 20
	OidInt2    = 21
	OidInt4    = 23
	OidText    = 25
	OidOid     = 26
	OidNumeric = 1700
)

// DefaultCollationOid is DEFAULT_COLLATION_OID (pg_collation.dat "default"),
// which PostgreSQL stamps on text Consts as constcollid.
const DefaultCollationOid = 100

// byvalWord encodes an integer datum as the 8-byte little-endian Datum word
// PostgreSQL's outfuncs.c:outDatum walks for by-value types. The value is taken
// as a signed 64-bit quantity so sign-extension matches Int32GetDatum /
// Int16GetDatum (a negative int4 fills the high bytes with 0xFF), while an
// already-widened unsigned value (e.g. an Oid promoted to int64) zero-extends.
// outDatum ALWAYS emits sizeof(Datum) == 8 bytes regardless of constlen.
func byvalWord(v int64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))
	return b
}

// textVarlena builds the in-memory 4-byte-header varlena PostgreSQL keeps in a
// text Const's constvalue. The header is VARSIZE << 2 stored little-endian (the
// va_4byte form: low two bits are flags, both 0 for a plain 4-byte-header
// datum), followed by the raw string bytes. datumGetSize reports VARSIZE, so
// the emitted length prefix is len(s)+4.
func textVarlena(s string) []byte {
	total := len(s) + 4
	b := make([]byte, total)
	binary.LittleEndian.PutUint32(b[:4], uint32(total)<<2)
	copy(b[4:], s)
	return b
}

// NewInt4Const builds a Const for an int4 (int) literal. Negative values
// sign-extend into the 8-byte datum word, reproducing Int32GetDatum.
func NewInt4Const(v int32) *Const {
	return &Const{
		ConstType: OidInt4, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 4, ConstByval: true, Location: -1,
		Datum: byvalWord(int64(v)),
	}
}

// NewInt8Const builds a Const for an int8 (bigint) literal.
func NewInt8Const(v int64) *Const {
	return &Const{
		ConstType: OidInt8, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 8, ConstByval: true, Location: -1,
		Datum: byvalWord(v),
	}
}

// NewBoolConst builds a Const for a bool literal (constlen 1, datum word 0/1).
func NewBoolConst(v bool) *Const {
	var w int64
	if v {
		w = 1
	}
	return &Const{
		ConstType: OidBool, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 1, ConstByval: true, Location: -1,
		Datum: byvalWord(w),
	}
}

// NewTextConst builds a Const for a text literal (constcollid = 100, varlena).
func NewTextConst(s string) *Const {
	return &Const{
		ConstType: OidText, ConstTypmod: -1, ConstCollid: DefaultCollationOid,
		ConstLen: -1, ConstByval: false, Location: -1,
		Datum: textVarlena(s),
	}
}

// NumericData on-disk format constants (utils/adt/numeric.c). A numeric Const's
// constvalue is the packed varlena PostgreSQL writes to disk, which outfuncs.c
// dumps byte-for-byte — so reproducing these exact bytes is what makes goopg's
// pg_node_tree numeric Const byte-identical to real PG18 (adbin / ev_action).
const (
	numericSignMask            = 0xC000
	numericPos                 = 0x0000
	numericNeg                 = 0x4000
	numericShort               = 0x8000
	numericSpecial             = 0xC000
	numericShortSignMask       = 0x2000
	numericShortDscaleMask     = 0x1F80
	numericShortDscaleShift    = 7
	numericShortWeightSignMask = 0x0040
	numericShortWeightMask     = 0x003F
	numericDscaleMask          = 0x3FFF
	numericShortDscaleMax      = numericShortDscaleMask >> numericShortDscaleShift // 63
	numericShortWeightMax      = numericShortWeightMask                            // 63
	numericShortWeightMin      = -(numericShortWeightMask + 1)                     // -64
	numericDecDigits           = 4                                                 // decimal digits per NBASE=10000 digit
)

// numericVar is the NBASE=10000 decomposition PostgreSQL's NumericVar carries:
// sign, the power-of-10000 weight of digits[0], the display scale (fractional
// decimal digits), and the base-10000 digits most-significant-first.
type numericVar struct {
	negative bool
	weight   int
	dscale   int
	digits   []int16
}

// parseNumericVar mirrors numeric.c:set_var_from_str (NBASE=10000, DEC_DIGITS=4)
// followed by strip_var. text is an unsigned decimal / scientific literal (the
// verbatim parser.NumericConst value); negative folds in an outer unary minus
// the way gram.y's doNegate produces a negative Const.
func parseNumericVar(text string, negative bool) (numericVar, error) {
	cp := text
	if len(cp) > 0 && (cp[0] == '+' || cp[0] == '-') {
		if cp[0] == '-' {
			negative = !negative
		}
		cp = cp[1:]
	}
	mant := cp
	exponent := 0
	if idx := strings.IndexAny(cp, "eE"); idx >= 0 {
		mant = cp[:idx]
		e, err := strconv.Atoi(cp[idx+1:])
		if err != nil {
			return numericVar{}, fmt.Errorf("pgnodes: invalid numeric exponent in %q: %w", text, err)
		}
		exponent = e
	}

	haveDp := false
	dweight := -1
	dscale := 0
	sawDigit := false
	// numericDecDigits leading zeros so the first NBASE digit aligns later.
	dec := make([]int, numericDecDigits, len(mant)+2*numericDecDigits)
	for i := 0; i < len(mant); i++ {
		ch := mant[i]
		switch {
		case ch >= '0' && ch <= '9':
			dec = append(dec, int(ch-'0'))
			sawDigit = true
			if !haveDp {
				dweight++
			} else {
				dscale++
			}
		case ch == '.':
			if haveDp {
				return numericVar{}, fmt.Errorf("pgnodes: invalid numeric literal %q", text)
			}
			haveDp = true
		default:
			return numericVar{}, fmt.Errorf("pgnodes: invalid numeric literal %q", text)
		}
	}
	if !sawDigit {
		return numericVar{}, fmt.Errorf("pgnodes: invalid numeric literal %q", text)
	}
	ddigits := len(dec) - numericDecDigits
	// Trailing zero padding for the final NBASE digit's decimal group.
	for i := 0; i < 2*numericDecDigits; i++ {
		dec = append(dec, 0)
	}

	if exponent != 0 {
		dweight += exponent
		dscale -= exponent
		if dscale < 0 {
			dscale = 0
		}
	}

	var weight int
	if dweight >= 0 {
		weight = (dweight+1+numericDecDigits-1)/numericDecDigits - 1
	} else {
		weight = -((-dweight-1)/numericDecDigits + 1)
	}
	offset := (weight+1)*numericDecDigits - (dweight + 1)
	ndigits := (ddigits + offset + numericDecDigits - 1) / numericDecDigits

	digits := make([]int16, 0, ndigits)
	i := numericDecDigits - offset
	for k := 0; k < ndigits; k++ {
		d := ((dec[i]*10+dec[i+1])*10+dec[i+2])*10 + dec[i+3]
		digits = append(digits, int16(d))
		i += numericDecDigits
	}

	v := numericVar{negative: negative, weight: weight, dscale: dscale, digits: digits}
	v.strip()
	return v, nil
}

// strip mirrors numeric.c:strip_var (also re-applied inside make_result): drop
// leading/trailing zero NBASE digits and force a zero value to weight 0 / POS.
func (v *numericVar) strip() {
	d := v.digits
	for len(d) > 0 && d[0] == 0 {
		d = d[1:]
		v.weight--
	}
	for len(d) > 0 && d[len(d)-1] == 0 {
		d = d[:len(d)-1]
	}
	if len(d) == 0 {
		v.negative = false
		v.weight = 0
	}
	v.digits = d
}

// varlena serializes to the packed on-disk NumericData (make_result_opt_error):
// a 4-byte varlena length header (VARSIZE<<2, little-endian like a plain 4-byte
// header) followed by the short (uint16 n_header) or long (uint16 n_sign_dscale
// + int16 n_weight) header and the int16 base-10000 digits, all little-endian to
// match an x86-64 PostgreSQL's in-memory representation that outfuncs dumps.
func (v numericVar) varlena() []byte {
	n := len(v.digits)
	short := v.dscale <= numericShortDscaleMax &&
		v.weight <= numericShortWeightMax && v.weight >= numericShortWeightMin

	var body []byte
	if short {
		h := uint16(numericShort)
		if v.negative {
			h |= numericShortSignMask
		}
		h |= uint16(v.dscale) << numericShortDscaleShift
		if v.weight < 0 {
			h |= numericShortWeightSignMask
		}
		h |= uint16(v.weight) & numericShortWeightMask
		body = make([]byte, 2+2*n)
		binary.LittleEndian.PutUint16(body[0:], h)
		for i, d := range v.digits {
			binary.LittleEndian.PutUint16(body[2+2*i:], uint16(d))
		}
	} else {
		signDscale := uint16(numericPos)
		if v.negative {
			signDscale = numericNeg
		}
		signDscale |= uint16(v.dscale) & numericDscaleMask
		body = make([]byte, 4+2*n)
		binary.LittleEndian.PutUint16(body[0:], signDscale)
		binary.LittleEndian.PutUint16(body[2:], uint16(int16(v.weight)))
		for i, d := range v.digits {
			binary.LittleEndian.PutUint16(body[4+2*i:], uint16(d))
		}
	}

	total := 4 + len(body)
	out := make([]byte, total)
	binary.LittleEndian.PutUint32(out[0:4], uint32(total)<<2)
	copy(out[4:], body)
	return out
}

// NewNumericConst builds a numeric (OID 1700) Const from a decimal/scientific
// literal. numeric is a non-collatable varlena, so constcollid 0, constlen -1,
// constbyval false — matching PG's make_const for a T_Float/decimal token.
func NewNumericConst(text string, negative bool) (*Const, error) {
	v, err := parseNumericVar(text, negative)
	if err != nil {
		return nil, err
	}
	return &Const{
		ConstType: OidNumeric, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: -1, ConstByval: false, ConstIsNull: false, Location: -1,
		Datum: v.varlena(),
	}, nil
}

// decodeNumericVar inverts varlena: it reads the packed on-disk NumericData back
// into a numericVar. NaN/Infinity (NUMERIC_SPECIAL) are rejected — the scalar
// subset only ever encodes finite literals.
func decodeNumericVar(data []byte) (numericVar, error) {
	if len(data) < 6 {
		return numericVar{}, fmt.Errorf("pgnodes: numeric datum too short (%d bytes)", len(data))
	}
	header := binary.LittleEndian.Uint16(data[4:])
	var v numericVar
	var off int
	switch header & numericSignMask {
	case numericShort:
		v.negative = header&numericShortSignMask != 0
		v.dscale = int((header & numericShortDscaleMask) >> numericShortDscaleShift)
		w := int(header & numericShortWeightMask)
		if header&numericShortWeightSignMask != 0 {
			w -= numericShortWeightMask + 1 // sign-extend the 7-bit weight field
		}
		v.weight = w
		off = 6
	case numericPos, numericNeg:
		v.negative = header&numericSignMask == numericNeg
		v.dscale = int(header & numericDscaleMask)
		if len(data) < 8 {
			return numericVar{}, fmt.Errorf("pgnodes: numeric long datum too short (%d bytes)", len(data))
		}
		v.weight = int(int16(binary.LittleEndian.Uint16(data[6:])))
		off = 8
	default:
		return numericVar{}, fmt.Errorf("pgnodes: numeric special value (NaN/Inf) not supported")
	}
	rest := data[off:]
	n := len(rest) / 2
	v.digits = make([]int16, n)
	for i := 0; i < n; i++ {
		v.digits[i] = int16(binary.LittleEndian.Uint16(rest[2*i:]))
	}
	return v, nil
}

// text renders the unsigned decimal string with exactly dscale fractional
// digits, mirroring numeric.c:get_str_from_var (sans the sign, which the caller
// re-applies as an outer unary minus). Round-tripping requires preserving
// trailing fractional zeros so a re-resolve reproduces the same dscale.
func (v numericVar) text() string {
	var sb strings.Builder
	d := 0
	if v.weight < 0 {
		d = v.weight + 1
		sb.WriteByte('0')
	} else {
		for ; d <= v.weight; d++ {
			dig := 0
			if d < len(v.digits) {
				dig = int(v.digits[d])
			}
			putit := d > 0
			d1 := dig / 1000
			dig -= d1 * 1000
			putit = putit || d1 > 0
			if putit {
				sb.WriteByte(byte(d1) + '0')
			}
			d1 = dig / 100
			dig -= d1 * 100
			putit = putit || d1 > 0
			if putit {
				sb.WriteByte(byte(d1) + '0')
			}
			d1 = dig / 10
			dig -= d1 * 10
			putit = putit || d1 > 0
			if putit {
				sb.WriteByte(byte(d1) + '0')
			}
			sb.WriteByte(byte(dig) + '0')
		}
	}
	if v.dscale > 0 {
		sb.WriteByte('.')
		var frac strings.Builder
		for i := 0; i < v.dscale; i, d = i+numericDecDigits, d+1 {
			dig := 0
			if d >= 0 && d < len(v.digits) {
				dig = int(v.digits[d])
			}
			d1 := dig / 1000
			dig -= d1 * 1000
			frac.WriteByte(byte(d1) + '0')
			d1 = dig / 100
			dig -= d1 * 100
			frac.WriteByte(byte(d1) + '0')
			d1 = dig / 10
			dig -= d1 * 10
			frac.WriteByte(byte(d1) + '0')
			frac.WriteByte(byte(dig) + '0')
		}
		fs := frac.String()
		if len(fs) > v.dscale {
			fs = fs[:v.dscale] // truncate the excess DEC_DIGITS-1 digits
		}
		sb.WriteString(fs)
	}
	return sb.String()
}
