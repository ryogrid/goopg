package pgnodes

import "encoding/binary"

// PostgreSQL type OIDs used by the scalar Const subset (pg_type.dat).
const (
	OidBool = 16
	OidInt8 = 20
	OidInt2 = 21
	OidInt4 = 23
	OidText = 25
	OidOid  = 26
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
