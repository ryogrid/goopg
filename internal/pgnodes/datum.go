package pgnodes

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PostgreSQL type OIDs used by the scalar Const subset (pg_type.dat).
const (
	OidBool        = 16
	OidInt8        = 20
	OidInt2        = 21
	OidInt4        = 23
	OidText        = 25
	OidOid         = 26
	OidFloat4      = 700
	OidFloat8      = 701
	OidNumeric     = 1700
	OidDate        = 1082
	OidTimestamptz = 1184
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

// NewInt2Const builds a Const for an int2 (smallint) literal. A negative value
// sign-extends into the 8-byte datum word, reproducing Int16GetDatum. An int2
// Const only ever arises from folding an unknown-type string literal in an int2
// context (`'5'::int2` / `col int2 DEFAULT '5'`) — a bare integer literal `5` is
// int4-typed and PG wraps it in an int4→int2 cast FuncExpr (funcid 314), not an
// int2 Const, so this constructor is deliberately NOT reached from resolveIntLiteral.
func NewInt2Const(v int16) *Const {
	return &Const{
		ConstType: OidInt2, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 2, ConstByval: true, Location: -1,
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

// NewOidConst builds a Const for an oid literal folded from an unknown-type string
// (`'5'::oid` / `col oid DEFAULT '5'`). An Oid is a 32-bit UNSIGNED by-value type, so
// ObjectIdGetDatum ZERO-extends it into the 8-byte datum word (unlike a negative
// int4, which sign-extends) — the value is always taken as an unsigned 32-bit
// quantity here so the high four bytes stay 0.
func NewOidConst(v uint32) *Const {
	return &Const{
		ConstType: OidOid, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 4, ConstByval: true, Location: -1,
		Datum: byvalWord(int64(v)),
	}
}

// NewFloat8Const builds a Const for a float8 (double precision) literal folded from
// an unknown-type string. Float8GetDatum reinterprets the IEEE-754 double's bits as
// an int64 (Int64GetDatum), so the datum word is the raw 8-byte little-endian bit
// pattern. FLOAT8PASSBYVAL is true on the 64-bit build goopg targets, so the Const is
// by-value with constlen 8.
func NewFloat8Const(v float64) *Const {
	return &Const{
		ConstType: OidFloat8, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 8, ConstByval: true, Location: -1,
		Datum: byvalWord(int64(math.Float64bits(v))),
	}
}

// NewFloat4Const builds a Const for a float4 (real) literal folded from an
// unknown-type string. Float4GetDatum reinterprets the 32-bit IEEE-754 float's bits
// as an int32 and then Int32GetDatum SIGN-extends that int32 into the 8-byte datum
// word — so a float whose bit pattern has the high bit set (a negative float) fills
// the high four bytes with 0xFF, exactly as Int32GetDatum would for a negative int4.
// int32(bits) then widening to int64 reproduces that sign extension.
func NewFloat4Const(v float32) *Const {
	return &Const{
		ConstType: OidFloat4, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 4, ConstByval: true, Location: -1,
		Datum: byvalWord(int64(int32(math.Float32bits(v)))),
	}
}

// caseTypeMeta returns the on-disk (constlen, constbyval) for a type OID that
// this slice models as a CASE result / casetype, with ok=false for any other
// type. It backs two decisions: (1) a synthesized NULL defresult (a CASE with
// no ELSE) needs the common type's length/byval, and (2) collatable types
// (text/varchar/…) are deliberately ABSENT, so a CASE over them returns ok=false
// and degrades to SQL text rather than emitting a wrong non-zero casecollid
// (this subset always writes casecollid 0).
func caseTypeMeta(oid uint32) (constlen int32, constbyval bool, ok bool) {
	switch oid {
	case OidBool:
		return 1, true, true
	case OidInt2:
		return 2, true, true
	case OidInt4, OidOid:
		return 4, true, true
	case OidInt8:
		return 8, true, true
	case OidFloat4:
		return 4, true, true
	case OidFloat8:
		// FLOAT8PASSBYVAL is true on the 64-bit build goopg targets.
		return 8, true, true
	case OidNumeric:
		return -1, false, true
	case OidTimestamptz:
		return 8, true, true
	default:
		return 0, false, false
	}
}

// pgTrimSpace trims exactly the six ASCII whitespace characters C's isspace()
// recognises in the C locale (space, \t, \n, \v, \f, \r), matching the whitespace
// PostgreSQL's numeric/bool input functions skip. It deliberately does NOT use
// strings.TrimSpace, which trims the wider Unicode whitespace set and would accept
// literals PG's input functions reject.
func pgTrimSpace(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			return true
		}
		return false
	})
}

// parseIntFromString reproduces the DECIMAL subset of PG's pg_strtoint16/32/64_safe
// (utils/adt/numutils.c) that backs int2in/int4in/int8in: leading/trailing ASCII
// whitespace, an optional +/- sign, then one-or-more base-10 digits, range-checked
// to the target width (bits = 16/32/64). It intentionally excludes the non-decimal
// base prefixes (0x/0o/0b) and '_' digit separators PG 16+ also accepts — those
// forms exist but are folded to SQL text (all-or-nothing), so any string accepted
// here yields the byte-identical value PG's input function would compute. Returns
// ok=false for an empty/sign-only/non-decimal string or an out-of-range value (a
// value PG's input function would itself reject with an error).
func parseIntFromString(s string, bits int) (int64, bool) {
	t := pgTrimSpace(s)
	if t == "" {
		return 0, false
	}
	body := t
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(t, 10, bits)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseOidFromString reproduces the UNSIGNED-DECIMAL subset of PG's oidin →
// oidin_subr (utils/adt/oid.c), which strtoul-parses base 10 and range-checks the
// result to the 32-bit Oid width. Accepts leading/trailing ASCII whitespace, an
// optional '+' sign, then one-or-more base-10 digits in [0, 4294967295]. It
// intentionally excludes the '-' sign strtoul would accept (which wraps around modulo
// 2^32 — an obscure form) and any non-decimal base, so a string accepted here yields
// the byte-identical Oid PG's input function would compute; anything else degrades to
// SQL text (all-or-nothing).
func parseOidFromString(s string) (uint32, bool) {
	t := pgTrimSpace(s)
	if t == "" {
		return 0, false
	}
	body := t
	if body[0] == '+' {
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(body, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// isDecimalFloatText reports whether t (already whitespace-trimmed) is a plain
// C-decimal floating-point spelling that BOTH strtod/strtof (PG float8in/float4in)
// and Go's strconv.ParseFloat parse to the identical IEEE-754 value: only the
// characters [0-9 + - . e E] appear and at least one digit is present. Positional
// validity ("1e", "1.2.3", "+-5") is left to ParseFloat, so an accepted-here string
// that ParseFloat also accepts is a finite decimal float. It deliberately rejects the
// special spellings (Inf/Infinity/NaN — a distinct non-finite datum), hexadecimal
// floats (0x…p…, which contain 'x'/'p'), and underscore separators, because those are
// either non-finite or parsed differently; both correctly-rounded parsers then agree
// on the bits of every accepted string.
func isDecimalFloatText(t string) bool {
	if t == "" {
		return false
	}
	hasDigit := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c == '+' || c == '-' || c == '.' || c == 'e' || c == 'E':
			// positional validity is enforced by strconv.ParseFloat below
		default:
			return false
		}
	}
	return hasDigit
}

// parseFloat8FromString reproduces the FINITE-DECIMAL subset of PG's float8in →
// float8in_internal (utils/adt/float.c), which parses with the platform strtod. Both
// strtod and Go's correctly-rounded strconv.ParseFloat round the identical decimal
// string to the same IEEE-754 double, so an accepted string folds byte-identically.
// Returns ok=false for a non-decimal spelling, a special (Inf/NaN), or an
// out-of-range value ParseFloat flags with ErrRange (which PG's input function would
// itself reject) — all of which degrade to SQL text (all-or-nothing).
func parseFloat8FromString(s string) (float64, bool) {
	t := pgTrimSpace(s)
	if !isDecimalFloatText(t) {
		return 0, false
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseFloat4FromString is the float4 (real) analogue of parseFloat8FromString,
// backing PG's float4in → float4in_internal (strtof). strconv.ParseFloat with a
// bitSize of 32 returns the float64 nearest to the correctly-rounded float32 value,
// so float32(result) is exactly the single-precision value strtof would produce.
func parseFloat4FromString(s string) (float32, bool) {
	t := pgTrimSpace(s)
	if !isDecimalFloatText(t) {
		return 0, false
	}
	f, err := strconv.ParseFloat(t, 32)
	if err != nil {
		return 0, false
	}
	return float32(f), true
}

// parseBoolLiteral reproduces PG boolin → parse_bool_with_len (utils/adt/bool.c):
// after stripping surrounding whitespace the (case-insensitive) value must be a
// UNIQUE PREFIX of an accepted spelling — true/yes/on/1 → true, false/no/off/0 →
// false. Any prefix of "true" (t, tr, tru, true) is true, of "false" (f, fa, …)
// is false, and so on; "on"/"off" require at least two characters to disambiguate
// (a lone "o" is rejected), while "1"/"0" must be exactly one character. Returns
// ok=false for anything else — the strings PG's boolin would reject with an error.
func parseBoolLiteral(s string) (value bool, ok bool) {
	v := strings.ToLower(pgTrimSpace(s))
	if v == "" {
		return false, false
	}
	switch v[0] {
	case 't':
		if strings.HasPrefix("true", v) {
			return true, true
		}
	case 'f':
		if strings.HasPrefix("false", v) {
			return false, true
		}
	case 'y':
		if strings.HasPrefix("yes", v) {
			return true, true
		}
	case 'n':
		if strings.HasPrefix("no", v) {
			return false, true
		}
	case 'o':
		// "on"/"off" share the prefix "o"; PG requires >= 2 chars to disambiguate.
		if len(v) >= 2 {
			if strings.HasPrefix("on", v) {
				return true, true
			}
			if strings.HasPrefix("off", v) {
				return false, true
			}
		}
	case '1':
		if len(v) == 1 {
			return true, true
		}
	case '0':
		if len(v) == 1 {
			return false, true
		}
	}
	return false, false
}

// newNullConst builds the typed NULL Const PG synthesizes for a CASE without an
// ELSE branch (transformCaseExpr coerces an untyped NULL A_Const to the common
// type): constisnull=true with no datum, but the target type's constlen/byval
// and constcollid 0.
func newNullConst(oid uint32, constlen int32, constbyval bool) *Const {
	return &Const{
		ConstType: oid, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: constlen, ConstByval: constbyval, ConstIsNull: true,
		Location: -1, Datum: nil,
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
	numericSignMask = 0xC000
	numericPos      = 0x0000
	numericNeg      = 0x4000
	numericShort    = 0x8000
	numericSpecial  = 0xC000
	// NUMERIC_SPECIAL sub-classes (numeric.c NUMERIC_EXT_SIGN_MASK): NaN and the two
	// infinities carry no digits/weight/dscale — the full uint16 n_header IS the value.
	numericExtSignMask         = 0xF000
	numericNaN                 = 0xC000 // NUMERIC_NAN
	numericPInf                = 0xD000 // NUMERIC_PINF (+Infinity)
	numericNInf                = 0xF000 // NUMERIC_NINF (-Infinity)
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
	// special is 0 for a finite value; otherwise the NUMERIC_SPECIAL n_header
	// (numericNaN / numericPInf / numericNInf). A special carries no digits.
	special uint16
}

// parseNumericVar mirrors numeric.c:set_var_from_str (NBASE=10000, DEC_DIGITS=4)
// followed by strip_var. text is an unsigned decimal / scientific literal (the
// verbatim parser.NumericConst value); negative folds in an outer unary minus
// the way gram.y's doNegate produces a negative Const.
func parseNumericVar(text string, negative bool) (numericVar, error) {
	// numeric_in recognizes the NaN / ±Infinity specials before set_var_from_str,
	// producing a digitless NUMERIC_SPECIAL varlena. Detect them first; a finite
	// literal (digit or '.' after the optional sign) falls through unchanged.
	if sp, ok := parseNumericSpecial(text, negative); ok {
		return sp, nil
	}
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

// parseNumericSpecial reproduces the NaN / ±Infinity recognition numeric_in performs
// before set_var_from_str (utils/adt/numeric.c). It mirrors PG's exact rules so the
// fold is byte-identical and, crucially, never accepts a spelling PG rejects:
//   - the sign is read from the FIRST non-space char; the specials are only tried
//     when the next char is neither a digit nor '.';
//   - "NaN" is matched from BEFORE the sign (numstart), so a signed NaN never matches
//     (numeric_in mandates NaN carry no sign);
//   - "Infinity" (8 chars) and "inf" (3 chars) are matched AFTER the sign, which
//     selects +Inf vs -Inf; matching is case-insensitive and prefix-only;
//   - only whitespace may follow the matched token.
//
// `negative` is the outer doNegate sign (a numeric negate of ±Inf flips it, of NaN is
// NaN). Returns ok=false for anything not a special, so parseNumericVar's finite path
// takes over (and ultimately the fold degrades to SQL text on a genuine syntax error).
func parseNumericSpecial(text string, negative bool) (numericVar, bool) {
	cp := pgTrimSpace(text)
	numstart := cp
	negSign := false
	if len(cp) > 0 && cp[0] == '+' {
		cp = cp[1:]
	} else if len(cp) > 0 && cp[0] == '-' {
		negSign = true
		cp = cp[1:]
	}
	// Specials only apply when the char after the sign is neither a digit nor '.'.
	if len(cp) == 0 || cp[0] == '.' || (cp[0] >= '0' && cp[0] <= '9') {
		return numericVar{}, false
	}
	var header uint16
	var rest string
	switch {
	case ciHasPrefix(numstart, "NaN"): // sign-inclusive: a signed NaN cannot match
		header = numericNaN
		rest = numstart[len("NaN"):]
	case ciHasPrefix(cp, "Infinity"):
		header = numericPInf
		rest = cp[len("Infinity"):]
	case ciHasPrefix(cp, "inf"):
		header = numericPInf
		rest = cp[len("inf"):]
	default:
		return numericVar{}, false
	}
	if pgTrimSpace(rest) != "" { // trailing junk (non-space) → not a special
		return numericVar{}, false
	}
	if header != numericNaN {
		if negSign {
			header = numericNInf
		}
		if negative { // outer doNegate flips the infinity
			if header == numericPInf {
				header = numericNInf
			} else {
				header = numericPInf
			}
		}
	}
	return numericVar{special: header}, true
}

// ciHasPrefix reports whether s begins with prefix, case-insensitively (ASCII —
// mirroring pg_strncasecmp's prefix comparison over the special-value spellings).
func ciHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
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
	if v.special != 0 {
		// NUMERIC_SPECIAL: a 6-byte varlena — a 4-byte length header (VARSIZE<<2) plus
		// the uint16 n_header, no weight/dscale/digits (make_result for const_nan /
		// const_pinf / const_ninf, size NUMERIC_HDRSZ_SHORT = VARHDRSZ + sizeof(uint16)).
		out := make([]byte, 6)
		binary.LittleEndian.PutUint32(out[0:4], uint32(6)<<2)
		binary.LittleEndian.PutUint16(out[4:], v.special)
		return out
	}
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
// literal OR a NaN/±Infinity special spelling (parseNumericVar recognizes both).
// numeric is a non-collatable varlena, so constcollid 0, constlen -1, constbyval
// false — matching PG's make_const for a T_Float/decimal token.
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
// into a numericVar. NaN/Infinity (NUMERIC_SPECIAL) decode to a digitless numericVar
// carrying only the ext-sign header in .special; a header with unexpected low bits is
// rejected.
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
		// NUMERIC_SPECIAL: NaN / +Inf / -Inf carry only the ext-sign header (no digits).
		switch header & numericExtSignMask {
		case numericNaN, numericPInf, numericNInf:
			return numericVar{special: header & numericExtSignMask}, nil
		}
		return numericVar{}, fmt.Errorf("pgnodes: numeric special value (unknown header 0x%04x)", header)
	}
	rest := data[off:]
	n := len(rest) / 2
	v.digits = make([]int16, n)
	for i := 0; i < n; i++ {
		v.digits[i] = int16(binary.LittleEndian.Uint16(rest[2*i:]))
	}
	return v, nil
}

// specialText renders a NUMERIC_SPECIAL back to the canonical string spelling
// numeric_out would emit (used by Rebuild to re-derive the string literal that
// re-folds to this same Const — the fixed point).
func (v numericVar) specialText() string {
	switch v.special {
	case numericPInf:
		return "Infinity"
	case numericNInf:
		return "-Infinity"
	default:
		return "NaN"
	}
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

// timestamptz (OID 1184) on-disk / in-memory form. PostgreSQL stores a
// timestamptz as a by-value int64 count of microseconds relative to
// POSTGRES_EPOCH_JDATE (2000-01-01 00:00:00 UTC) — TimestampTz in
// timestamp.h / datetime.c. A DEFAULT literal like '2024-01-15 10:30:00+00' is
// coerced from an "unknown" string literal to timestamptz *at parse time*
// (coerce_type's stringTypeToConst path calls timestamptz_in immediately), so
// PG's stored pg_attrdef.adbin / pg_rewrite.ev_action holds a folded Const, not
// a cast FuncExpr — outfuncs.c:outDatum dumps its 8 little-endian bytes.
const (
	// Julian day numbers PostgreSQL pins in timestamp.h.
	postgresEpochJDate = 2451545 // 2000-01-01
	unixEpochJDate     = 2440588 // 1970-01-01
	usecsPerDay        = int64(86400) * 1000000
)

// unixEpochMicros is timestamptz 'epoch' (1970-01-01 00:00:00 UTC) expressed as
// microseconds since the PostgreSQL epoch — negative because it predates 2000.
const unixEpochMicros = int64(unixEpochJDate-postgresEpochJDate) * 86400 * 1000000

// date2j is PostgreSQL's Gregorian date → Julian day number (datetime.c:date2j),
// exact integer arithmetic (no floating point, correct across the proleptic
// calendar). Used to convert a parsed timestamp's calendar fields to a day count.
func date2j(y, m, d int) int {
	if m > 2 {
		m++
		y += 4800
	} else {
		m += 13
		y += 4799
	}
	century := y / 100
	julian := y*365 - 32167
	julian += y/4 - century + century/4
	julian += 7834*m/256 + d
	return julian
}

// j2date is the inverse (datetime.c:j2date): Julian day number → Gregorian
// (year, month, day). Used by the rebuild path to render a stored μs value back
// into a UTC timestamp literal.
func j2date(jd int) (year, month, day int) {
	julian := uint32(jd)
	julian += 32044
	quad := julian / 146097
	extra := (julian-quad*146097)*4 + 3
	julian += 60 + quad*3 + extra/146097
	quad = julian / 1461
	julian -= quad * 1461
	y := int(julian * 4 / 1461)
	if y != 0 {
		julian = (julian + 305) % 365
	} else {
		julian = (julian + 306) % 366
	}
	julian += 123
	y += int(quad) * 4
	year = y - 4800
	quad = julian * 2141 / 65536
	day = int(julian) - int(7834*quad/256)
	month = int(quad+10)%12 + 1
	return year, month, day
}

// NewTimestamptzConst builds a Const for a timestamptz value (μs since the
// PostgreSQL epoch). Like the integer Consts it is by-value (constlen 8,
// constbyval true, constcollid 0) and sign-extends into the 8-byte datum word so
// a pre-2000 value fills the high bytes with 0xFF (Int64GetDatum).
func NewTimestamptzConst(usec int64) *Const {
	return &Const{
		ConstType: OidTimestamptz, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 8, ConstByval: true, Location: -1,
		Datum: byvalWord(usec),
	}
}

// NewDateConst builds a Const for a date value (DateADT: signed int32 days since
// the PostgreSQL epoch, 2000-01-01). It is by-value (constlen 4, constbyval true,
// constcollid 0) and sign-extends into the 8-byte datum word so a pre-2000 date
// fills the high bytes with 0xFF (Int32GetDatum / DateADTGetDatum), while a
// post-2000 date zero-extends — exactly the int4 Const wire form but consttype
// 1082.
func NewDateConst(days int32) *Const {
	return &Const{
		ConstType: OidDate, ConstTypmod: -1, ConstCollid: 0,
		ConstLen: 4, ConstByval: true, Location: -1,
		Datum: byvalWord(int64(days)),
	}
}

// parseDateDays parses a "YYYY-MM-DD" date literal into DateADT days-since-2000,
// returning ok=false for anything outside that deterministic subset (the resolver
// then degrades to SQL text). A plain ISO date is TimeZone-independent — unlike a
// timestamptz literal — so date_in folds it to a Const at parse time regardless
// of session GUCs. The round-trip guard (j2date∘date2j is the identity ONLY on a
// valid calendar date) rejects a non-canonical field triple like month 13 / day
// 32 so only a genuine date literal folds.
func parseDateDays(s string) (int32, bool) {
	s = strings.TrimSpace(s)
	y, m, d, ok := parseDateFields(s)
	if !ok || y < 1 {
		return 0, false
	}
	jd := date2j(y, m, d)
	if ry, rm, rd := j2date(jd); ry != y || rm != m || rd != d {
		return 0, false
	}
	return int32(jd - postgresEpochJDate), true
}

// formatDate renders a stored DateADT day count back into the canonical
// "YYYY-MM-DD" literal, the inverse of parseDateDays (used by the rebuild path so
// a re-resolve is a fixed point).
func formatDate(days int32) string {
	y, m, d := j2date(int(days) + postgresEpochJDate)
	return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
}

// parseTimestamptzMicros parses a timestamptz literal into microseconds since the
// PostgreSQL epoch, returning ok=false for any form outside the deterministic
// subset (which the resolver then degrades to SQL text). PG folds such a literal
// at parse time using the session TimeZone GUC, so only forms whose UTC instant
// is unambiguous are supported: an ISO date+time with an EXPLICIT numeric offset
// (or 'Z'), plus the 'epoch' keyword. A bare date, or a date+time without an
// offset, depends on the server's TimeZone and is left to SQL-text fallback.
func parseTimestamptzMicros(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "epoch") {
		return unixEpochMicros, true
	}

	// Split the date from the "time+offset" tail on the first space or 'T'.
	sep := strings.IndexAny(s, " Tt")
	if sep < 0 {
		return 0, false
	}
	dateStr, rest := s[:sep], s[sep+1:]

	y, mo, d, ok := parseDateFields(dateStr)
	if !ok {
		return 0, false
	}

	// Locate the timezone offset: the first '+', '-', or 'Z'/'z' in the tail
	// (the time part itself contains only digits, ':' and '.'). An explicit
	// offset is REQUIRED for determinism.
	off := strings.IndexAny(rest, "+-Zz")
	if off < 0 {
		return 0, false
	}
	timeStr, offStr := rest[:off], rest[off:]

	hh, mi, ss, fracUsec, ok := parseTimeFields(timeStr)
	if !ok {
		return 0, false
	}
	offSec, ok := parseTZOffsetSeconds(offStr)
	if !ok {
		return 0, false
	}

	days := int64(date2j(y, mo, d) - postgresEpochJDate)
	// UTC instant = wall-clock at the given offset minus that offset.
	totalSec := days*86400 + int64(hh)*3600 + int64(mi)*60 + int64(ss) - offSec
	return totalSec*1000000 + fracUsec, true
}

// parseDateFields parses "YYYY-MM-DD" (each field a plain non-negative integer;
// BC / variable-form years are out of the supported subset).
func parseDateFields(s string) (y, m, d int, ok bool) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var err error
	if y, err = strconv.Atoi(parts[0]); err != nil || y < 0 {
		return 0, 0, 0, false
	}
	if m, err = strconv.Atoi(parts[1]); err != nil || m < 1 || m > 12 {
		return 0, 0, 0, false
	}
	if d, err = strconv.Atoi(parts[2]); err != nil || d < 1 || d > 31 {
		return 0, 0, 0, false
	}
	return y, m, d, true
}

// parseTimeFields parses "HH:MM[:SS[.frac]]" into hours/minutes/seconds and the
// fractional part as microseconds (rounded to μs, matching PG's rint). Seconds
// default to 0 when omitted.
func parseTimeFields(s string) (hh, mi, ss int, fracUsec int64, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, 0, false
	}
	var err error
	if hh, err = strconv.Atoi(parts[0]); err != nil || hh < 0 || hh > 23 {
		return 0, 0, 0, 0, false
	}
	if mi, err = strconv.Atoi(parts[1]); err != nil || mi < 0 || mi > 59 {
		return 0, 0, 0, 0, false
	}
	if len(parts) == 3 {
		secStr := parts[2]
		if dot := strings.IndexByte(secStr, '.'); dot >= 0 {
			fracUsec, ok = fracToMicros(secStr[dot+1:])
			if !ok {
				return 0, 0, 0, 0, false
			}
			secStr = secStr[:dot]
		}
		if ss, err = strconv.Atoi(secStr); err != nil || ss < 0 || ss > 60 {
			return 0, 0, 0, 0, false
		}
	}
	return hh, mi, ss, fracUsec, true
}

// fracToMicros converts fractional-second digits to microseconds, rounding a
// >6-digit fraction to the nearest μs (PG rounds sub-microsecond precision).
func fracToMicros(frac string) (int64, bool) {
	if frac == "" {
		return 0, false
	}
	for i := 0; i < len(frac); i++ {
		if frac[i] < '0' || frac[i] > '9' {
			return 0, false
		}
	}
	if len(frac) > 6 {
		v, _ := strconv.ParseInt(frac[:6], 10, 64)
		if frac[6] >= '5' {
			v++ // round half-up; a carry to 1000000 simply adds a second downstream
		}
		return v, true
	}
	padded := frac + strings.Repeat("0", 6-len(frac))
	v, err := strconv.ParseInt(padded, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseTZOffsetSeconds parses a timezone offset ('Z'/'z' = UTC, or
// (+|-)HH[[:]MM[[:]SS]]) into signed seconds east of UTC.
func parseTZOffsetSeconds(s string) (int64, bool) {
	if s == "Z" || s == "z" {
		return 0, true
	}
	if len(s) < 2 || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	neg := s[0] == '-'
	body := s[1:]
	var oh, om, os int
	var err error
	if strings.ContainsRune(body, ':') {
		parts := strings.Split(body, ":")
		if len(parts) < 1 || len(parts) > 3 {
			return 0, false
		}
		if oh, err = strconv.Atoi(parts[0]); err != nil {
			return 0, false
		}
		if len(parts) >= 2 {
			if om, err = strconv.Atoi(parts[1]); err != nil {
				return 0, false
			}
		}
		if len(parts) == 3 {
			if os, err = strconv.Atoi(parts[2]); err != nil {
				return 0, false
			}
		}
	} else {
		// Packed form: HH, HHMM, or HHMMSS.
		switch len(body) {
		case 2:
			oh, err = strconv.Atoi(body)
		case 4:
			oh, err = strconv.Atoi(body[:2])
			if err == nil {
				om, err = strconv.Atoi(body[2:4])
			}
		case 6:
			oh, err = strconv.Atoi(body[:2])
			if err == nil {
				om, err = strconv.Atoi(body[2:4])
			}
			if err == nil {
				os, err = strconv.Atoi(body[4:6])
			}
		default:
			return 0, false
		}
		if err != nil {
			return 0, false
		}
	}
	if oh < 0 || oh > 15 || om < 0 || om > 59 || os < 0 || os > 59 {
		return 0, false
	}
	total := int64(oh)*3600 + int64(om)*60 + int64(os)
	if neg {
		total = -total
	}
	return total, true
}

// formatTimestamptzUTC renders a μs-since-epoch value back into a canonical UTC
// timestamp literal ("YYYY-MM-DD HH:MM:SS[.ffffff]+00", trailing fractional
// zeros trimmed). It is the rebuild-time inverse: re-resolving the result (with
// its explicit +00 offset) reproduces the identical μs, so
// resolve→Rebuild→re-resolve is a fixed point. The rendering need not match
// timestamptz_out's session-TimeZone form — only round-trip to the same instant.
func formatTimestamptzUTC(usec int64) string {
	day := usec / usecsPerDay
	rem := usec % usecsPerDay
	if rem < 0 {
		rem += usecsPerDay
		day--
	}
	y, mo, d := j2date(int(day) + postgresEpochJDate)
	sec := rem / 1000000
	fracUsec := rem % 1000000
	hh := sec / 3600
	mi := (sec % 3600) / 60
	ss := sec % 60

	var sb strings.Builder
	fmt.Fprintf(&sb, "%04d-%02d-%02d %02d:%02d:%02d", y, mo, d, hh, mi, ss)
	if fracUsec > 0 {
		frac := fmt.Sprintf("%06d", fracUsec)
		frac = strings.TrimRight(frac, "0")
		sb.WriteByte('.')
		sb.WriteString(frac)
	}
	sb.WriteString("+00")
	return sb.String()
}
