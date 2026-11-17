package postmaster

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fastpathMsg builds a FunctionCall payload the way libpq's
// pqFunctionCall3 does: one BINARY format code, then the arguments, then a
// BINARY result format.
func fastpathMsg(fn uint32, args ...[]byte) []byte {
	var b bytes.Buffer
	put := func(v any) { _ = binary.Write(&b, binary.BigEndian, v) }
	put(fn)
	put(int16(1))
	put(int16(1))
	put(int16(len(args)))
	for _, a := range args {
		if a == nil {
			put(int32(-1))
			continue
		}
		put(int32(len(a)))
		b.Write(a)
	}
	put(int16(1))
	return b.Bytes()
}

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

// TestFastpathLowering pins parse_fcall_arguments' decoding and the SELECT
// the call is lowered to for libpq's lo_* functions.
func TestFastpathLowering(t *testing.T) {
	s := &Server{}
	c, err := parseFastpathCall(fastpathMsg(955, be32(3), []byte("a'b\\")))
	if err != nil {
		t.Fatal(err)
	}
	if c.fnOID != 955 || c.rformat != 1 || len(c.args) != 2 || c.formats[1] != 1 {
		t.Fatalf("decoded %+v", c)
	}
	got, err := s.fastpathSQL(c)
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "pg_catalog"."lowrite"(3::int4, '\x6127625c'::bytea)`
	if got != want {
		t.Fatalf("lowered = %s, want %s", got, want)
	}
	if _, err := parseFastpathCall(fastpathMsg(957, be32(1))[:9]); err == nil {
		t.Fatal("truncated message accepted")
	}
	c, _ = parseFastpathCall(fastpathMsg(1, be32(1)))
	if _, err := s.fastpathSQL(c); err == nil || err.Error() != "function with OID 1 does not exist" {
		t.Fatalf("unknown OID: %v", err)
	}
	if v, err := fastpathResult(26, []byte("16384"), 1); err != nil || !bytes.Equal(v, be32(16384)) {
		t.Fatalf("oid result = %v %v", v, err)
	}
	if v, err := fastpathResult(17, []byte(`\x00ff`), 1); err != nil || !bytes.Equal(v, []byte{0, 0xff}) {
		t.Fatalf("bytea result = %v %v", v, err)
	}
}
