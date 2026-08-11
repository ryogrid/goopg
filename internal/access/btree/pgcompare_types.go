// pgcompare_types.go — the per-type binary datum comparators that a
// PGIndexKeyDesc installs in PGKeyAttr.Compare (M0130-S11.4 slice 3b-2a).
//
// WHY THIS FILE EXISTS
//
// Slice 3b-1 took upstream's own seam out of "nbtree knows no types": every
// ordering decision _bt_compare makes about an attribute is delegated to the
// opclass BTORDER_PROC that _bt_mkscankey installed in the scan key
// (postgres/src/backend/access/nbtree/nbtutils.c). PGAttrComparator is that
// seam. Until now the only implementation was the nil default — CompareKeys,
// i.e. bytes.Compare — which is correct exactly as long as the key bytes are
// goopg's ORDER-PRESERVING encodings (btree.EncodeInt4 and friends: sign-biased
// big-endian integers, escaped self-delimiting strings).
//
// Slice 3b-2 replaces those encodings with the datum's real PostgreSQL binary
// image, because that is what an IndexTupleData holds and what a PG 18.3
// attaching to this cluster will read. The moment that happens, bytewise
// ordering is WRONG for almost every type: an int4 is stored as four
// LITTLE-endian native bytes (store_att_byval → a plain memcpy of the Datum),
// so bytes.Compare puts 256 before 1; a negative int8 sorts above every
// positive one; a float8 is IEEE-754 with its own NaN rule; a numeric is a
// varlena of base-10000 digits that no byte order can linearize.
//
// So the comparators below are transcriptions of PG's btree opclass support
// function 1 for each type goopg indexes — btint4cmp, btfloat8cmp, bttextcmp
// (C collation), bpcharcmp, numeric_cmp, … — operating on exactly the byte
// image DeformPGIndexTuple hands back: attlen bytes for a by-value type, the
// complete varlena INCLUDING its header otherwise.
//
// ENDIANNESS. Every by-value comparator reads little-endian, because a datum's
// on-disk image is its NATIVE memory image and goopg's compatibility target is
// x86-64 PostgreSQL 18.3 — the same assumption internal/executor/codec.go's
// encodeValuePG already makes for heap tuples, and the same one the PG-validated
// bootstrap index encoders in internal/initdb are byte-compared against.
//
// MALFORMED INPUT. PGAttrComparator has no error return, by design: it sits in
// the innermost loop of a descent and upstream's sk_func cannot fail either.
// A datum whose length does not match its attlen is a corrupt tuple, which is
// amcheck's business and not something a comparator may panic on halfway
// through a page split. Every comparator therefore validates its operands and
// falls back to bytes.Compare when they do not hold: a deterministic, total
// order, so the caller still terminates, and the corruption is reported by the
// structural validators rather than by a nil-slice panic. Each such fallback is
// marked `// corrupt-operand fallback` below.

package btree

import (
	"bytes"
	"encoding/binary"
	"math"
)

// ---------------------------------------------------------------------------
// integers — btint2cmp / btint4cmp / btint8cmp / btoidcmp
// (postgres/src/backend/utils/adt/int.c, int8.c, oid.c)
// ---------------------------------------------------------------------------

// PGCompareInt2 is btint2cmp over a 2-byte little-endian int16 image. int2's
// btree opclass also serves smallint-shaped domains.
func PGCompareInt2(a, b []byte) int {
	if len(a) != 2 || len(b) != 2 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return cmpInt64(int64(int16(binary.LittleEndian.Uint16(a))), int64(int16(binary.LittleEndian.Uint16(b))))
}

// PGCompareInt4 is btint4cmp over a 4-byte little-endian int32 image. date
// (int4 days since 2000-01-01) shares it — date_cmp is int4 comparison, and the
// two special values (-infinity/+infinity) are the int32 extremes, which order
// correctly with no special case.
func PGCompareInt4(a, b []byte) int {
	if len(a) != 4 || len(b) != 4 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return cmpInt64(int64(int32(binary.LittleEndian.Uint32(a))), int64(int32(binary.LittleEndian.Uint32(b))))
}

// PGCompareInt8 is btint8cmp over an 8-byte little-endian int64 image.
// timestamp, timestamptz and time all share it: each is an int64 microsecond
// count and its _cmp is plain integer comparison (timestamp.c:timestamp_cmp
// → timestamp_cmp_internal), infinities again being the int64 extremes.
func PGCompareInt8(a, b []byte) int {
	if len(a) != 8 || len(b) != 8 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return cmpInt64(int64(binary.LittleEndian.Uint64(a)), int64(binary.LittleEndian.Uint64(b)))
}

// PGCompareOID is btoidcmp — an UNSIGNED 4-byte comparison. The distinction
// from PGCompareInt4 is real: OIDs above 2^31 exist (they wrap), and PG orders
// them as unsigned.
func PGCompareOID(a, b []byte) int {
	if len(a) != 4 || len(b) != 4 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return cmpUint64(uint64(binary.LittleEndian.Uint32(a)), uint64(binary.LittleEndian.Uint32(b)))
}

// PGCompareBool is btboolcmp (postgres/src/backend/utils/adt/bool.c): a
// single byte, false < true.
func PGCompareBool(a, b []byte) int {
	if len(a) != 1 || len(b) != 1 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	av, bv := a[0] != 0, b[0] != 0
	switch {
	case av == bv:
		return 0
	case !av:
		return -1
	}
	return 1
}

// PGCompareCharType is btcharcmp for the one-byte "char" type
// (postgres/src/backend/utils/adt/char.c). Upstream's comment is explicit that
// it compares as UNSIGNED char, so that the ordering does not depend on whether
// the platform's `char` is signed — which is why this cannot just reuse
// PGCompareInt2's signed path on one byte.
func PGCompareCharType(a, b []byte) int {
	if len(a) != 1 || len(b) != 1 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return cmpUint64(uint64(a[0]), uint64(b[0]))
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// floats — btfloat4cmp / btfloat8cmp
// (postgres/src/backend/utils/adt/float.c: float4_cmp_internal /
//  float8_cmp_internal, both delegating to float8_cmp/float4_cmp's
//  "NaN is larger than any non-NaN, and equal to itself" rule)
// ---------------------------------------------------------------------------

// PGCompareFloat4 is btfloat4cmp over a 4-byte little-endian IEEE-754 image.
func PGCompareFloat4(a, b []byte) int {
	if len(a) != 4 || len(b) != 4 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return compareFloatPG(
		float64(math.Float32frombits(binary.LittleEndian.Uint32(a))),
		float64(math.Float32frombits(binary.LittleEndian.Uint32(b))),
	)
}

// PGCompareFloat8 is btfloat8cmp over an 8-byte little-endian IEEE-754 image.
func PGCompareFloat8(a, b []byte) int {
	if len(a) != 8 || len(b) != 8 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return compareFloatPG(
		math.Float64frombits(binary.LittleEndian.Uint64(a)),
		math.Float64frombits(binary.LittleEndian.Uint64(b)),
	)
}

// compareFloatPG is float8_cmp_internal. Two rules a bit-pattern comparison
// gets wrong and a naive `<`/`>` chain gets wrong differently:
//
//   - NaN is treated as LARGER than every non-NaN value and EQUAL to itself, so
//     that a btree over a float column is a total order (a C `a < b` chain
//     would report "unordered" three times and make NaN compare equal to
//     everything).
//   - negative zero equals positive zero, which their bit patterns do not.
//     Comparing the float64 values rather than the raw words gets this for free.
func compareFloatPG(a, b float64) int {
	aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
	switch {
	case aNaN && bNaN:
		return 0
	case aNaN:
		return 1
	case bNaN:
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// varlena string/binary types
// ---------------------------------------------------------------------------

// PGCompareBytea is byteacmp (postgres/src/backend/utils/adt/varlena.c):
// memcmp over the common length, then the shorter datum first. Because a
// varlena's header is a LENGTH and not part of the value, the payload must be
// extracted first — comparing whole varlenas would order by header form and
// then by length, which is not byteacmp at all.
func PGCompareBytea(a, b []byte) int {
	ap, aok := pgVarlenaPayload(a)
	bp, bok := pgVarlenaPayload(b)
	if !aok || !bok {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return bytes.Compare(ap, bp)
}

// PGCompareTextC is bttextcmp under the C collation — varstr_cmp's
// strcmp fast path (varlena.c: "if the collation is C, we can use memcmp").
// It is deliberately NOT named PGCompareText: a non-C collation orders through
// strcoll/ICU and is NOT modelled here (see the slice-3b-1 ledger row on
// collations). A descriptor for a collated column must install its own
// comparator rather than silently getting C ordering.
func PGCompareTextC(a, b []byte) int { return PGCompareBytea(a, b) }

// PGCompareBpcharC is bpcharcmp under the C collation
// (postgres/src/backend/utils/adt/varchar.c). bpchar is blank-PADDED: its
// comparison first strips TRAILING SPACES from both operands (bcTruelen), so
// 'ab   ' and 'ab' are equal. Reusing PGCompareTextC for a bpchar column is a
// silent wrong answer on every padded value, which is why this exists as its
// own function even though the rest of the body is identical.
func PGCompareBpcharC(a, b []byte) int {
	ap, aok := pgVarlenaPayload(a)
	bp, bok := pgVarlenaPayload(b)
	if !aok || !bok {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return bytes.Compare(bcTruelen(ap), bcTruelen(bp))
}

// bcTruelen is varchar.c's bcTruelen: the length of a bpchar value ignoring
// trailing ASCII spaces.
func bcTruelen(s []byte) []byte {
	n := len(s)
	for n > 0 && s[n-1] == ' ' {
		n--
	}
	return s[:n]
}

// PGCompareName is btnamecmp (postgres/src/backend/utils/adt/name.c). name is
// a FIXED 64-byte NameData, NUL-padded, and upstream compares it with strcmp —
// so the bytes after the first NUL are not part of the value. A plain
// bytes.Compare over the 64-byte image happens to agree (NUL is the lowest
// byte and the padding is all NUL), but only as long as the padding is really
// zeroed; truncating at the NUL makes that independent of the writer.
func PGCompareName(a, b []byte) int {
	return bytes.Compare(cstringBytes(a), cstringBytes(b))
}

func cstringBytes(s []byte) []byte {
	if i := bytes.IndexByte(s, 0); i >= 0 {
		return s[:i]
	}
	return s
}

// PGCompareUUID is uuid_cmp (postgres/src/backend/utils/adt/uuid.c): memcmp
// over the 16-byte image. uuid is a fixed-width NON-by-value type, so the datum
// is those 16 bytes with no header.
func PGCompareUUID(a, b []byte) int {
	if len(a) != 16 || len(b) != 16 {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	return bytes.Compare(a, b)
}

// PGCompareTimeTZ is timetz_cmp (postgres/src/backend/utils/adt/date.c) over
// the 12-byte TimeTzADT image: an int64 microsecond time followed by an int32
// zone offset in seconds WEST of GMT.
//
// The ordering is by GMT-equivalent time (time + zone*USECS_PER_SEC), NOT by
// the local time — '12:00+01' sorts before '12:00+00'. Only when those are
// equal does upstream fall back to the local time and then to the zone, so that
// two spellings of the same instant get a deterministic (and non-equal) order.
func PGCompareTimeTZ(a, b []byte) int {
	at, az, aok := timeTzParts(a)
	bt, bz, bok := timeTzParts(b)
	if !aok || !bok {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	const usecsPerSec = 1000000
	agmt := at + int64(az)*usecsPerSec
	bgmt := bt + int64(bz)*usecsPerSec
	if c := cmpInt64(agmt, bgmt); c != 0 {
		return c
	}
	if c := cmpInt64(at, bt); c != 0 {
		return c
	}
	return cmpInt64(int64(az), int64(bz))
}

func timeTzParts(v []byte) (usec int64, zone int32, ok bool) {
	if len(v) != 12 {
		return 0, 0, false
	}
	return int64(binary.LittleEndian.Uint64(v[0:8])), int32(binary.LittleEndian.Uint32(v[8:12])), true
}

// ---------------------------------------------------------------------------
// numeric — numeric_cmp / cmp_numerics / cmp_var_common / cmp_abs_common
// (postgres/src/backend/utils/adt/numeric.c)
// ---------------------------------------------------------------------------

// on-disk NumericData header bits (postgres/src/backend/utils/adt/numeric.c;
// the same constants internal/pgnodes/datum.go writes when it serializes a
// numeric Const, kept local so the btree package stays leaf-level).
const (
	numFlagMask       = 0xC000 // NUMERIC_SIGN_MASK / NUMERIC_FLAGBITS
	numPos            = 0x0000
	numNeg            = 0x4000
	numShort          = 0x8000
	numSpecial        = 0xC000
	numExtMask        = 0xF000 // distinguishes the three specials
	numNaN            = 0xC000
	numPInf           = 0xD000
	numNInf           = 0xF000
	numShortSignMask  = 0x2000
	numShortWeightSgn = 0x0040
	numShortWeightMsk = 0x003F
)

// numericParts is the decoded on-disk NumericData a comparison needs: sign,
// weight (in NBASE=10000 digits) and the digit array. dscale is deliberately
// NOT decoded — it is display scale only and does not affect ordering
// (1.0 = 1.00 in PG).
type numericParts struct {
	special  uint16 // 0 when finite, else numNaN/numPInf/numNInf
	negative bool
	weight   int
	digits   []int16
}

// PGCompareNumeric is numeric_cmp over the on-disk NumericData varlena.
//
// This is the comparator that most obviously cannot be bytewise: the value is a
// base-10000 digit array with an independent weight and sign, so 2 and 10 and
// -1 have no byte order relationship at all.
//
// Special-value ordering follows cmp_numerics exactly:
// -Infinity < every finite value < +Infinity < NaN, with each special equal to
// itself. NaN sorting ABOVE +Infinity is upstream's deliberate choice (it makes
// numeric a total order for btree just like the float NaN rule above), not an
// accident of the encoding.
func PGCompareNumeric(a, b []byte) int {
	an, aok := decodeNumericParts(a)
	bn, bok := decodeNumericParts(b)
	if !aok || !bok {
		return bytes.Compare(a, b) // corrupt-operand fallback
	}
	if an.special != 0 || bn.special != 0 {
		return cmpNumericSpecial(an, bn)
	}
	return cmpNumericVar(an, bn)
}

// cmpNumericSpecial is cmp_numerics' special-value ladder. Written as a rank
// rather than upstream's nested ifs: NaN=3, +Inf=2, finite=1, -Inf=0 reproduces
// every one of its nine cases, including "NaN = NaN" and "PINF < NAN".
func cmpNumericSpecial(a, b numericParts) int {
	return cmpInt64(int64(numericSpecialRank(a)), int64(numericSpecialRank(b)))
}

func numericSpecialRank(n numericParts) int {
	switch n.special {
	case numNaN:
		return 3
	case numPInf:
		return 2
	case numNInf:
		return 0
	}
	return 1 // finite
}

// cmpNumericVar is cmp_var_common: sign first (with zero — an empty digit
// array — treated as neither positive nor negative), then magnitude.
func cmpNumericVar(a, b numericParts) int {
	if len(a.digits) == 0 {
		switch {
		case len(b.digits) == 0:
			return 0
		case b.negative:
			return 1
		}
		return -1
	}
	if len(b.digits) == 0 {
		if a.negative {
			return -1
		}
		return 1
	}
	if !a.negative {
		if b.negative {
			return 1
		}
		return cmpNumericAbs(a, b)
	}
	if !b.negative {
		return -1
	}
	return -cmpNumericAbs(a, b)
}

// cmpNumericAbs is cmp_abs_common: align the two digit arrays by weight, then
// compare digit by digit. A higher weight only wins if the digits it holds
// above the other's range are actually non-zero — on-disk numerics are stripped
// (make_result calls strip_var), but upstream still checks, and so does this,
// because a leading zero digit must not make 1 compare greater than 2.
func cmpNumericAbs(a, b numericParts) int {
	i1, i2 := 0, 0
	w1, w2 := a.weight, b.weight
	for w1 > w2 && i1 < len(a.digits) {
		if a.digits[i1] != 0 {
			return 1
		}
		i1++
		w1--
	}
	for w2 > w1 && i2 < len(b.digits) {
		if b.digits[i2] != 0 {
			return -1
		}
		i2++
		w2--
	}
	if w1 == w2 {
		for i1 < len(a.digits) && i2 < len(b.digits) {
			if d := int(a.digits[i1]) - int(b.digits[i2]); d != 0 {
				if d > 0 {
					return 1
				}
				return -1
			}
			i1++
			i2++
		}
	}
	// One side has digits the other does not: the extra digits decide, but only
	// if any of them is non-zero (trailing zeros are still equal).
	for ; i1 < len(a.digits); i1++ {
		if a.digits[i1] != 0 {
			return 1
		}
	}
	for ; i2 < len(b.digits); i2++ {
		if b.digits[i2] != 0 {
			return -1
		}
	}
	return 0
}

// decodeNumericParts reads the packed on-disk NumericData behind a varlena
// header — the inverse of internal/pgnodes' numericVar.varlena(). Both the
// short (uint16 n_header) and long (uint16 n_sign_dscale + int16 n_weight)
// forms are handled; the short form's weight is a SIGN-EXTENDED 7-bit field
// (NUMERIC_WEIGHT's `~NUMERIC_SHORT_WEIGHT_MASK` branch), which is the one
// place a plain mask silently yields a positive weight for a value below 1.
func decodeNumericParts(raw []byte) (numericParts, bool) {
	body, ok := pgVarlenaPayload(raw)
	if !ok || len(body) < 2 {
		return numericParts{}, false
	}
	header := binary.LittleEndian.Uint16(body[0:2])

	if header&numFlagMask == numSpecial {
		switch header & numExtMask {
		case numNaN, numPInf, numNInf:
			return numericParts{special: header & numExtMask}, true
		}
		return numericParts{}, false
	}

	var (
		negative bool
		weight   int
		digitsAt int
	)
	if header&numShort == numShort {
		negative = header&numShortSignMask != 0
		w := int(header & numShortWeightMsk)
		if header&numShortWeightSgn != 0 {
			w |= ^numShortWeightMsk // sign-extend the 7th bit
		}
		weight = w
		digitsAt = 2
	} else {
		switch header & numFlagMask {
		case numPos:
			negative = false
		case numNeg:
			negative = true
		default:
			return numericParts{}, false
		}
		if len(body) < 4 {
			return numericParts{}, false
		}
		weight = int(int16(binary.LittleEndian.Uint16(body[2:4])))
		digitsAt = 4
	}

	rest := body[digitsAt:]
	if len(rest)%2 != 0 {
		return numericParts{}, false
	}
	digits := make([]int16, len(rest)/2)
	for i := range digits {
		digits[i] = int16(binary.LittleEndian.Uint16(rest[2*i:]))
	}
	return numericParts{negative: negative, weight: weight, digits: digits}, true
}

// ---------------------------------------------------------------------------
// shared varlena payload accessor
// ---------------------------------------------------------------------------

// pgVarlenaPayload strips a varlena header and returns the value bytes.
//
// Only the two UNCOMPRESSED header forms are accepted — 1-byte short and
// 4-byte long. A compressed or external datum is rejected (ok=false) rather
// than compared as ciphertext: upstream's index_form_tuple de-toasts through
// TOAST_INDEX_HACK before an index tuple is ever built, so a compressed datum
// inside an index tuple is a bug, not a case to support. (goopg's own
// non-implementation of TOAST_INDEX_HACK is the slice-1 ledger row.)
func pgVarlenaPayload(v []byte) ([]byte, bool) {
	if len(v) == 0 {
		return nil, false
	}
	if pgVarattIs1BE(v) {
		return nil, false // external TOAST pointer
	}
	if pgVarattIs1B(v) {
		sz := int(v[0] >> 1) // VARSIZE_SHORT — includes the 1 header byte
		if sz < varHdrSzShort || sz > len(v) {
			return nil, false
		}
		return v[varHdrSzShort:sz], true
	}
	if len(v) < varHdrSz {
		return nil, false
	}
	word := binary.LittleEndian.Uint32(v[0:4])
	if word&0x03 != 0x00 {
		return nil, false // 4-byte COMPRESSED header
	}
	sz := int(word >> 2)
	if sz < varHdrSz || sz > len(v) {
		return nil, false
	}
	return v[varHdrSz:sz], true
}
