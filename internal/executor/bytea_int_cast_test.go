package executor

// bytea_int_cast_test.go — unit coverage for the six explicit int↔bytea casts
// (pg_cast.dat:323-335, castfuncs 6367-6372: int2_bytea/int4_bytea/int8_bytea
// and bytea_int2/bytea_int4/bytea_int8, varlena.c:4139-4233).
//
// int→bytea (intNsend): big-endian two's-complement at exactly the source
// type's fixed width. The width comes from CastExpr.SourceType via
// evalCastTyped (evalCast alone has no source-type parameter).
// bytea→int (bytea_intN): big-endian MSB-first, len > width → 22003 with no
// errposition, short payloads zero-extended, and the unsigned accumulation
// re-interpreted through the signed type so min-value bit patterns wrap to
// the negative minimum. M0134-0070.

import (
	"testing"
)

func TestByteaIntCast(t *testing.T) {
	t.Run("int2 to bytea positive", func(t *testing.T) {
		got, err := evalCastTyped(NewIntDatum(0x1234), "bytea", "int2", 0, nil)
		if err != nil {
			t.Fatalf("0x1234::int2::bytea unexpected error: %v", err)
		}
		want := []byte{0x12, 0x34}
		if got.Kind != KindBytes || string(got.BytesValue()) != string(want) {
			t.Errorf("0x1234::int2::bytea = %x, want %x", got.BytesValue(), want)
		}
	})
	t.Run("int2 to bytea negative", func(t *testing.T) {
		got, err := evalCastTyped(NewIntDatum(-0x1234), "bytea", "int2", 0, nil)
		if err != nil {
			t.Fatalf("(-0x1234)::int2::bytea unexpected error: %v", err)
		}
		want := []byte{0xed, 0xcc}
		if got.Kind != KindBytes || string(got.BytesValue()) != string(want) {
			t.Errorf("(-0x1234)::int2::bytea = %x, want %x", got.BytesValue(), want)
		}
	})
	t.Run("int4 to bytea positive", func(t *testing.T) {
		got, err := evalCastTyped(NewIntDatum(0x12345678), "bytea", "int4", 0, nil)
		if err != nil {
			t.Fatalf("0x12345678::int4::bytea unexpected error: %v", err)
		}
		want := []byte{0x12, 0x34, 0x56, 0x78}
		if got.Kind != KindBytes || string(got.BytesValue()) != string(want) {
			t.Errorf("0x12345678::int4::bytea = %x, want %x", got.BytesValue(), want)
		}
	})
	t.Run("int4 to bytea negative", func(t *testing.T) {
		got, err := evalCastTyped(NewIntDatum(-0x12345678), "bytea", "int4", 0, nil)
		if err != nil {
			t.Fatalf("(-0x12345678)::int4::bytea unexpected error: %v", err)
		}
		want := []byte{0xed, 0xcb, 0xa9, 0x88}
		if got.Kind != KindBytes || string(got.BytesValue()) != string(want) {
			t.Errorf("(-0x12345678)::int4::bytea = %x, want %x", got.BytesValue(), want)
		}
	})
	t.Run("int8 to bytea positive", func(t *testing.T) {
		got, err := evalCastTyped(NewIntDatum(0x1122334455667788), "bytea", "int8", 0, nil)
		if err != nil {
			t.Fatalf("0x1122334455667788::int8::bytea unexpected error: %v", err)
		}
		want := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
		if got.Kind != KindBytes || string(got.BytesValue()) != string(want) {
			t.Errorf("0x1122334455667788::int8::bytea = %x, want %x", got.BytesValue(), want)
		}
	})
	t.Run("int8 to bytea negative", func(t *testing.T) {
		got, err := evalCastTyped(NewIntDatum(-0x1122334455667788), "bytea", "int8", 0, nil)
		if err != nil {
			t.Fatalf("(-0x1122334455667788)::int8::bytea unexpected error: %v", err)
		}
		want := []byte{0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88, 0x78}
		if got.Kind != KindBytes || string(got.BytesValue()) != string(want) {
			t.Errorf("(-0x1122334455667788)::int8::bytea = %x, want %x", got.BytesValue(), want)
		}
	})
	t.Run("source type SQL display spellings", func(t *testing.T) {
		// CastExpr.SourceType is the operand's declared catalog type name,
		// which may be either spelling; both must select the same width.
		got, err := evalCastTyped(NewIntDatum(0x1234), "bytea", "smallint", 0, nil)
		if err != nil {
			t.Fatalf("0x1234::smallint::bytea unexpected error: %v", err)
		}
		if got.Kind != KindBytes || string(got.BytesValue()) != string([]byte{0x12, 0x34}) {
			t.Errorf("0x1234::smallint::bytea = %x, want 1234", got.BytesValue())
		}
		got, err = evalCastTyped(NewIntDatum(0x12345678), "bytea", "integer", 0, nil)
		if err != nil {
			t.Fatalf("0x12345678::integer::bytea unexpected error: %v", err)
		}
		if got.Kind != KindBytes || string(got.BytesValue()) != string([]byte{0x12, 0x34, 0x56, 0x78}) {
			t.Errorf("0x12345678::integer::bytea = %x, want 12345678", got.BytesValue())
		}
		got, err = evalCastTyped(NewIntDatum(0x1122334455667788), "bytea", "bigint", 0, nil)
		if err != nil {
			t.Fatalf("0x1122334455667788::bigint::bytea unexpected error: %v", err)
		}
		if got.Kind != KindBytes || string(got.BytesValue()) != string([]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}) {
			t.Errorf("0x1122334455667788::bigint::bytea = %x, want 1122334455667788", got.BytesValue())
		}
	})
	t.Run("null int to bytea stays null", func(t *testing.T) {
		got, err := evalCastTyped(NullDatum, "bytea", "int4", 0, nil)
		if err != nil {
			t.Fatalf("NULL::int4::bytea unexpected error: %v", err)
		}
		if !got.IsNull() {
			t.Errorf("NULL::int4::bytea = %v, want NULL", got)
		}
	})

	// bytea → intN.
	type byteaIntCase struct {
		name    string
		payload []byte
		target  string
		want    int64
		wantErr bool
		wantMsg string
	}
	intCases := []byteaIntCase{
		{"bytea empty to int2", []byte{}, "int2", 0, false, ""},
		{"bytea short to int2", []byte{0x12}, "int2", 18, false, ""},
		{"bytea to int2", []byte{0x12, 0x34}, "int2", 4660, false, ""},
		{"bytea min to int2", []byte{0x80, 0x00}, "int2", -32768, false, ""},
		{"bytea max to int2", []byte{0x7f, 0xff}, "int2", 32767, false, ""},
		{"bytea overflow to int2", []byte{0x12, 0x34, 0x56}, "int2", 0, true, "smallint out of range"},
		{"bytea empty to int4", []byte{}, "int4", 0, false, ""},
		{"bytea short to int4", []byte{0x12}, "int4", 18, false, ""},
		{"bytea to int4", []byte{0x12, 0x34, 0x56, 0x78}, "int4", 305419896, false, ""},
		{"bytea min to int4", []byte{0x80, 0x00, 0x00, 0x00}, "int4", -2147483648, false, ""},
		{"bytea max to int4", []byte{0x7f, 0xff, 0xff, 0xff}, "int4", 2147483647, false, ""},
		{"bytea overflow to int4", []byte{0x12, 0x34, 0x56, 0x78, 0x9a}, "int4", 0, true, "integer out of range"},
		{"bytea empty to int8", []byte{}, "int8", 0, false, ""},
		{"bytea short to int8", []byte{0x12}, "int8", 18, false, ""},
		{"bytea to int8", []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}, "int8", 1234605616436508552, false, ""},
		{"bytea min to int8", []byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, "int8", -9223372036854775808, false, ""},
		{"bytea max to int8", []byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, "int8", 9223372036854775807, false, ""},
		{"bytea overflow to int8", []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}, "int8", 0, true, "bigint out of range"},
	}
	for _, c := range intCases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := evalCast(NewBytesDatum(c.payload), c.target, 0, nil)
			if c.wantErr {
				if err == nil {
					t.Fatalf("evalCast(%x::%s) expected error, got nil", c.payload, c.target)
				}
				ee, ok := err.(*ExecError)
				if !ok {
					t.Fatalf("evalCast(%x::%s) error is not *ExecError: %v", c.payload, c.target, err)
				}
				if ee.Code != "22003" {
					t.Errorf("evalCast(%x::%s) code = %q, want 22003", c.payload, c.target, ee.Code)
				}
				if ee.Message != c.wantMsg {
					t.Errorf("evalCast(%x::%s) message = %q, want %q", c.payload, c.target, ee.Message, c.wantMsg)
				}
				if ee.Pos != 0 {
					t.Errorf("evalCast(%x::%s) Pos = %d, want 0 (no errposition)", c.payload, c.target, ee.Pos)
				}
				return
			}
			if err != nil {
				t.Fatalf("evalCast(%x::%s) unexpected error: %v", c.payload, c.target, err)
			}
			if got.Kind != KindInt || got.Int != c.want {
				t.Errorf("evalCast(%x::%s) = %d, want %d", c.payload, c.target, got.Int, c.want)
			}
		})
	}
	t.Run("bytea null to int stays null", func(t *testing.T) {
		got, err := evalCast(NullDatum, "int8", 0, nil)
		if err != nil {
			t.Fatalf("NULL::bytea::int8 unexpected error: %v", err)
		}
		if !got.IsNull() {
			t.Errorf("NULL::bytea::int8 = %v, want NULL", got)
		}
	})
}
