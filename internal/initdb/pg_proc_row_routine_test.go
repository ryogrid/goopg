package initdb

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// TestParseOIDVector pins proargtypes decoding for routines a PostgreSQL
// primary created (M0149-0032a): the heap holds the raw oidvector array
// image (ndim, dataoffset, elemtype 26, dims, lbounds, uint32 elements),
// which the text-form parser used to turn into garbage.
func TestParseOIDVector(t *testing.T) {
	img := func(oids ...uint32) string {
		b := make([]byte, 20+4*len(oids))
		binary.LittleEndian.PutUint32(b[0:], 1)  // ndim
		binary.LittleEndian.PutUint32(b[8:], 26) // elemtype oid
		binary.LittleEndian.PutUint32(b[12:], uint32(len(oids)))
		for i, o := range oids {
			binary.LittleEndian.PutUint32(b[20+4*i:], o)
		}
		return string(b)
	}
	for _, tc := range []struct {
		in   string
		want []uint32
	}{
		{img(), []uint32{}},
		{img(23, 25), []uint32{23, 25}},
		{"23 25", []uint32{23, 25}},
		{"", nil},
	} {
		got, ok := parseOIDVector(tc.in)
		if !ok || (len(got) != 0 || len(tc.want) != 0) && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseOIDVector(%q) = %v, %v; want %v", tc.in, got, ok, tc.want)
		}
	}
	if _, ok := parseOIDVector("23 x"); ok {
		t.Error("garbage text accepted")
	}
}
