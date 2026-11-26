package executor

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// arraySendBinary is array_send (arrayfuncs.c) for a one-dimensional array
// column: ndim (int4), the has-nulls flag (int4), the element type OID
// (int4), then per dimension its length and lower bound (int4 each), then
// each element as an int4 length (-1 for NULL) followed by the element's
// own send format. An empty array is ndim 0 with no dimension entries.
// elemType is the column's catalog.Type with IsArray cleared.
func arraySendBinary(t catalog.Type, d Datum) ([]byte, error) {
	elemType := t
	elemType.IsArray = false
	elemOID, _, _, _, ok := array.ElemTypeInfo(elemType.Name)
	if !ok {
		return nil, rejectBinaryCopyArray(t)
	}
	text := d.Format()
	if d.Kind == KindString {
		text = d.StringValue()
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{{") {
		// Explicit bounds or more than one dimension: not ported.
		return nil, &ExecError{Code: "0A000", Message: fmt.Sprintf("binary COPY of multi-dimensional or explicitly bounded %s[] arrays is not supported", t.Name)}
	}
	elems := parseTextArrayElems(text)
	out := make([]byte, 0, 20+8*len(elems))
	if len(elems) == 0 {
		out = appendInt32(out, 0)
		out = appendInt32(out, 0)
		out = appendInt32(out, int32(elemOID))
		return out, nil
	}
	hasNull := false
	for _, e := range elems {
		if !e.Quoted && strings.EqualFold(e.Text, "NULL") {
			hasNull = true
		}
	}
	out = appendInt32(out, 1)
	if hasNull {
		out = appendInt32(out, 1)
	} else {
		out = appendInt32(out, 0)
	}
	out = appendInt32(out, int32(elemOID))
	out = appendInt32(out, int32(len(elems)))
	out = appendInt32(out, 1) // lower bound
	for _, e := range elems {
		if !e.Quoted && strings.EqualFold(e.Text, "NULL") {
			out = appendInt32(out, -1)
			continue
		}
		// COPY's text input for the element type (array_in's element
		// input), so every element arrives as the Datum its send expects.
		ed, err := copyTextToDatum(elemType, []byte(e.Text), "")
		if err != nil {
			return nil, err
		}
		b, err := datumToCopyBinary(elemType, ed)
		if err != nil {
			return nil, err
		}
		out = appendInt32(out, int32(len(b)))
		out = append(out, b...)
	}
	return out, nil
}

// arrayRecvBinary is array_recv for a one-dimensional array: it checks the
// element type OID against the column's element type, as array_recv does
// ("binary data has array element type %u (%s) instead of expected %u
// (%s)"), decodes each element with its receive function, and returns
// array_out's text, which is how goopg carries an array value.
func arrayRecvBinary(t catalog.Type, payload []byte) (Datum, error) {
	elemType := t
	elemType.IsArray = false
	elemOID, _, _, _, ok := array.ElemTypeInfo(elemType.Name)
	if !ok {
		return Datum{}, rejectBinaryCopyArray(t)
	}
	bad := func() (Datum, error) {
		return Datum{}, &ExecError{Code: "22P03", Message: "incorrect binary data format"}
	}
	if len(payload) < 12 {
		return bad()
	}
	ndim := int32(binary.BigEndian.Uint32(payload[0:4]))
	gotOID := binary.BigEndian.Uint32(payload[8:12])
	if gotOID != elemOID {
		return Datum{}, &ExecError{Code: "42804", Message: fmt.Sprintf("binary data has array element type %d instead of expected %d", gotOID, elemOID)}
	}
	if ndim == 0 {
		return NewStringDatum("{}"), nil
	}
	if ndim != 1 {
		return Datum{}, &ExecError{Code: "0A000", Message: fmt.Sprintf("binary COPY of multi-dimensional %s[] arrays is not supported", t.Name)}
	}
	if len(payload) < 20 {
		return bad()
	}
	n := int(int32(binary.BigEndian.Uint32(payload[12:16])))
	off := 20
	elems := make([]string, n)
	nulls := make([]bool, n)
	for i := 0; i < n; i++ {
		if off+4 > len(payload) {
			return bad()
		}
		l := int(int32(binary.BigEndian.Uint32(payload[off : off+4])))
		off += 4
		if l < 0 {
			nulls[i] = true
			continue
		}
		if off+l > len(payload) {
			return bad()
		}
		ed, err := copyBinaryToDatum(elemType, payload[off:off+l])
		if err != nil {
			return Datum{}, err
		}
		off += l
		// array_out's element output: the element type's text form in the
		// canonical ISO / hex styles goopg stores arrays in.
		txt, err := datumToCopyText(elemType, ed, "ISO", "MDY", "", "hex", nil, false)
		if err != nil {
			return Datum{}, err
		}
		elems[i] = txt
	}
	if off != len(payload) {
		return bad()
	}
	return NewStringDatum(formatTextArrayWithNulls(elems, nulls)), nil
}
