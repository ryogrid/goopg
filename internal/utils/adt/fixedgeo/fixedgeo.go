// Package fixedgeo holds PostgreSQL's fixed-length on-disk images of the
// geometric types point, lseg, box, line and circle, and of tid (M0149-0001k).
//
// goopg keeps these values in memory as their canonical output text; only the
// heap image changes. PG stores them as fixed-length, pass-by-reference
// structs (postgres/src/include/utils/geo_decls.h, storage/itemptr.h):
//
//	point  Point  {float8 x, y}                      typlen 16, typalign 'd'
//	lseg   LSEG   {Point p[2]}                       typlen 32, typalign 'd'
//	box    BOX    {Point high, low}                  typlen 32, typalign 'd'
//	line   LINE   {float8 A, B, C}                   typlen 24, typalign 'd'
//	circle CIRCLE {Point center; float8 radius}      typlen 24, typalign 'd'
//	tid    ItemPointerData {uint16 bi_hi, bi_lo;     typlen 6,  typalign 's'
//	                        uint16 ip_posid}
//
// The package is a leaf so the executor's heap codec and the pgoutput decoder
// (internal/access/transam/xlog, which cannot import the executor) share one
// encoder and one renderer — the two must never disagree (Hard-won Rule #2).
// Rendering uses catalog.PGFloatOut, the same float8out port the executor's
// canonical-text builders (pointCanonicalText etc.) use, so a decoded value is
// byte-identical to the text the column was written from.
package fixedgeo

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// Width returns the fixed byte length and alignment of a lowered type name,
// or ok=false when the type is not one of this package's.
func Width(lower string) (size, align int, ok bool) {
	switch lower {
	case "point":
		return 16, 8, true
	case "lseg", "box":
		return 32, 8, true
	case "line", "circle":
		return 24, 8, true
	case "tid":
		return 6, 2, true
	}
	return 0, 0, false
}

// floatCount is the number of float8 fields in each geometric image.
func floatCount(lower string) int {
	size, _, _ := Width(lower)
	return size / 8
}

// Encode returns the fixed-length image of a value given as text (the
// canonical output form goopg keeps in memory; any spacing is tolerated).
func Encode(lower, text string) ([]byte, error) {
	if lower == "tid" {
		return encodeTID(text)
	}
	n := floatCount(lower)
	if n == 0 {
		return nil, fmt.Errorf("fixedgeo: unsupported type %q", lower)
	}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '(', ')', '[', ']', '{', '}', '<', '>', ',', ' ', '\t', '\n':
			return true
		}
		return false
	})
	if len(fields) != n {
		return nil, fmt.Errorf("invalid input syntax for type %s: %q", lower, text)
	}
	buf := make([]byte, 8*n)
	for i, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil && !isRangeErr(err) {
			return nil, fmt.Errorf("invalid input syntax for type %s: %q", lower, text)
		}
		binary.LittleEndian.PutUint64(buf[8*i:], math.Float64bits(v))
	}
	return buf, nil
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// encodeTID packs "(block,offset)" as ItemPointerData: BlockIdData's two
// 16-bit halves (high first), then the 16-bit offset, all native-endian.
// A negative block wraps to its unsigned 32-bit value, as tidin does.
func encodeTID(text string) ([]byte, error) {
	s := strings.TrimSpace(text)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	blk, off, ok := strings.Cut(s, ",")
	if !ok {
		return nil, fmt.Errorf("invalid input syntax for type tid: %q", text)
	}
	b, err := strconv.ParseInt(strings.TrimSpace(blk), 10, 64)
	if err != nil || b < math.MinInt32 || b > math.MaxUint32 {
		return nil, fmt.Errorf("invalid input syntax for type tid: %q", text)
	}
	o, err := strconv.ParseUint(strings.TrimSpace(off), 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid input syntax for type tid: %q", text)
	}
	block := uint32(b)
	buf := make([]byte, 6)
	binary.LittleEndian.PutUint16(buf[0:2], uint16(block>>16))
	binary.LittleEndian.PutUint16(buf[2:4], uint16(block))
	binary.LittleEndian.PutUint16(buf[4:6], uint16(o))
	return buf, nil
}

// Decode renders the fixed-length image at the head of data as the type's
// canonical output text and reports the bytes consumed.
func Decode(lower string, data []byte) (string, int, error) {
	size, _, ok := Width(lower)
	if !ok {
		return "", 0, fmt.Errorf("fixedgeo: unsupported type %q", lower)
	}
	if len(data) < size {
		return "", 0, fmt.Errorf("truncated %s", lower)
	}
	if lower == "tid" {
		block := uint32(binary.LittleEndian.Uint16(data[0:2]))<<16 | uint32(binary.LittleEndian.Uint16(data[2:4]))
		return fmt.Sprintf("(%d,%d)", block, binary.LittleEndian.Uint16(data[4:6])), size, nil
	}
	f := make([]string, size/8)
	for i := range f {
		f[i] = catalog.PGFloatOut(math.Float64frombits(binary.LittleEndian.Uint64(data[8*i:])), 64)
	}
	var s string
	switch lower {
	case "point":
		s = "(" + f[0] + "," + f[1] + ")"
	case "lseg":
		s = "[(" + f[0] + "," + f[1] + "),(" + f[2] + "," + f[3] + ")]"
	case "box":
		s = "(" + f[0] + "," + f[1] + "),(" + f[2] + "," + f[3] + ")"
	case "line":
		s = "{" + f[0] + "," + f[1] + "," + f[2] + "}"
	case "circle":
		s = "<(" + f[0] + "," + f[1] + ")," + f[2] + ">"
	}
	return s, size, nil
}
