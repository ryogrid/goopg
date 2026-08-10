package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The uuid-column storage slice of M0119-0006. Until it landed, a `uuid`
// column was stored through varlenaTextBytes as the 36-character canonical
// TEXT — 37 on-disk bytes behind a varlena header — while goopg's OWN
// pg_attribute row for the column said attlen 16, attalign 'c', attstorage 'p'
// (userTypeAttrsForOID, from the pg_type OID 2950 seed). Unlike the interval
// slice, no goopg answer was wrong: lowercase-hex text compares in the same
// order as uuid_cmp's memcmp, so the defect is invisible from inside goopg and
// only shows up in a reader that trusts the descriptor — a PG standby, or
// pg_amcheck's heap tier.
//
// Layout reference: postgres/src/include/utils/uuid.h
// (`typedef struct pg_uuid_t { unsigned char data[UUID_LEN]; }`, UUID_LEN 16)
// and uuid.c string_to_uuid / uuid_out.

func uuidType() catalog.Type { return catalog.Type{Name: "uuid"} }

// TestUUIDColumnStoresSixteenRawBytes pins the on-disk bytes: the leftmost text
// hex pair is byte 0, no header, no dashes, exactly 16 bytes. Asserting the
// bytes directly (not just the round trip) is the point — the old text form
// round-tripped through goopg's own decoder perfectly.
func TestUUIDColumnStoresSixteenRawBytes(t *testing.T) {
	enc, err := encodeValuePG(uuidType(), NewStringDatum("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"))
	if err != nil {
		t.Fatalf("encodeValuePG(uuid): %v", err)
	}
	if len(enc) != 16 {
		t.Fatalf("encoded %d bytes, want 16 (pg_type OID 2950 typlen); text storage wrote 37", len(enc))
	}
	want := []byte{
		0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8,
		0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11,
	}
	for i := range want {
		if enc[i] != want[i] {
			t.Fatalf("on-disk bytes = %x, want %x", enc, want)
		}
	}
}

// TestUUIDColumnIsFixedWidthNotVarlena pins the two predicates the tuple
// builder consults against the pg_attribute row goopg already publishes for a
// uuid column. This is the assertion that actually fails on the pre-slice
// engine in a way the round trip cannot: EncodeRowPG and the decode walker
// consult the SAME predicates, so a matched pair of wrong answers is
// self-consistent inside goopg and diverges only for a PG reader.
func TestUUIDColumnIsFixedWidthNotVarlena(t *testing.T) {
	attrs := userTypeAttrsForOID(catalog.OIDUUID)
	if attrs.TypLen != 16 || attrs.TypAlign != 'c' {
		t.Fatalf("pg_attribute for uuid says attlen=%d attalign=%q; test premise is stale",
			attrs.TypLen, string(rune(attrs.TypAlign)))
	}
	if got := physicalPGTypeAlign(uuidType()); got != 1 {
		t.Errorf("physicalPGTypeAlign(uuid) = %d, want 1 (typalign 'c' — the heap must agree with attalign)", got)
	}
	if pgPhysicalTypeIsVarlena(uuidType()) {
		t.Error("pgPhysicalTypeIsVarlena(uuid) = true, want false (typlen 16, typstorage 'p')")
	}
}

// TestUUIDColumnRoundTripsEveryInputFormat: the three spellings uuid_in accepts
// all collapse to the same 16 bytes, and decode renders uuid_out's canonical
// lowercase-with-dashes form regardless of what was typed.
func TestUUIDColumnRoundTripsEveryInputFormat(t *testing.T) {
	const canonical = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	cases := []struct {
		name, in string
	}{
		{"canonical", canonical},
		{"uppercase", "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"},
		{"braces", "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}"},
		{"no hyphens", "a0eebc999c0b4ef8bb6d6bb9bd380a11"},
		{"mixed case no hyphens", "A0eeBC999c0B4Ef8bb6D6bB9bd380A11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := encodeValuePG(uuidType(), NewStringDatum(tc.in))
			if err != nil {
				t.Fatalf("encodeValuePG(%q): %v", tc.in, err)
			}
			if len(enc) != 16 {
				t.Fatalf("encoded %d bytes, want 16", len(enc))
			}
			got, n, err := decodePhysicalPGValueMctx(uuidType(), enc, nil)
			if err != nil {
				t.Fatalf("decodePhysicalPGValueMctx: %v", err)
			}
			if n != 16 {
				t.Fatalf("decode consumed %d bytes, want 16", n)
			}
			if got.Kind != KindString {
				t.Fatalf("decoded kind=%v, want KindString (the engine's uuid Datum shape is unchanged)", got.Kind)
			}
			if got.StringValue() != canonical {
				t.Errorf("round trip = %q, want %q (uuid_out form)", got.StringValue(), canonical)
			}
		})
	}
}

// TestUUIDColumnEdgeValues covers all-zero and all-ones, where a length or
// sign slip in the hex packing is most visible.
func TestUUIDColumnEdgeValues(t *testing.T) {
	for _, s := range []string{
		"00000000-0000-0000-0000-000000000000",
		"ffffffff-ffff-ffff-ffff-ffffffffffff",
		"00112233-4455-6677-8899-aabbccddeeff",
	} {
		enc, err := encodeValuePG(uuidType(), NewStringDatum(s))
		if err != nil {
			t.Fatalf("encodeValuePG(%q): %v", s, err)
		}
		got, _, err := decodePhysicalPGValueMctx(uuidType(), enc, nil)
		if err != nil {
			t.Fatalf("decode(%q): %v", s, err)
		}
		if got.StringValue() != s {
			t.Errorf("round trip %q = %q", s, got.StringValue())
		}
	}
}

// TestUUIDColumnRejectsInvalidText keeps the 22P02 the text arm raised: the
// storage flip must not turn a bad literal into a silent partial pack.
func TestUUIDColumnRejectsInvalidText(t *testing.T) {
	for _, s := range []string{
		"not-a-uuid",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1",   // one hex digit short
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1zz", // non-hex
		"",
	} {
		_, err := encodeValuePG(uuidType(), NewStringDatum(s))
		if err == nil {
			t.Errorf("encodeValuePG(%q) succeeded, want 22P02", s)
			continue
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
			t.Errorf("encodeValuePG(%q) err = %v, want ExecError 22P02", s, err)
		}
	}
}

// TestUUIDColumnPreservesByteOrderComparison is the non-regression half: PG's
// uuid_cmp is a memcmp over the 16 bytes, and the canonical lowercase-hex text
// the Datum carries sorts identically. This is why the storage flip moves no
// answer — pinning it stops a later slice from "fixing" the Datum to raw bytes
// (KindBytes) without also revisiting every index-key and comparison site that
// assumes uuid is a string.
func TestUUIDColumnPreservesByteOrderComparison(t *testing.T) {
	store := func(s string) Datum {
		t.Helper()
		enc, err := encodeValuePG(uuidType(), NewStringDatum(s))
		if err != nil {
			t.Fatalf("encodeValuePG(%q): %v", s, err)
		}
		d, _, err := decodePhysicalPGValueMctx(uuidType(), enc, nil)
		if err != nil {
			t.Fatalf("decode(%q): %v", s, err)
		}
		return d
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000001", -1},
		{"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", 0},
		// 'f' > '9' both bytewise and in hex text.
		{"f0000000-0000-0000-0000-000000000000", "90000000-0000-0000-0000-000000000000", 1},
	}
	for _, tc := range cases {
		got, err := compareDatum(store(tc.a), store(tc.b), 0)
		if err != nil {
			t.Fatalf("compareDatum(%q,%q): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("compareDatum(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestUUIDColumnRowRoundTripKeepsNeighbourColumns walks a whole tuple with uuid
// columns between neighbours of every other width class. A width mistake (16 vs
// 37) lands the walker's cursor mid-field for every following column, which is
// exactly what a PG standby experienced against the old text storage.
func TestUUIDColumnRowRoundTripKeepsNeighbourColumns(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int2"}},
		{Name: "u", Type: uuidType()},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "int8"}},
		{Name: "v", Type: uuidType()},
		{Name: "d", Type: catalog.Type{Name: "int4"}},
	}
	const u1 = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	const u2 = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	row := Row{
		NewIntDatum(7),
		NewStringDatum(u1),
		NewStringDatum("neighbour"),
		NewIntDatum(1234567890123),
		NewStringDatum(u2),
		NewIntDatum(42),
	}
	enc, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	out := make(Row, len(cols))
	if err := decodePhysicalPGRowIntoMctx(out, cols, enc, nil); err != nil {
		t.Fatalf("decodePhysicalPGRowIntoMctx: %v", err)
	}
	if out[0].Int != 7 {
		t.Errorf("col a = %d, want 7", out[0].Int)
	}
	if got := out[1].StringValue(); got != u1 {
		t.Errorf("col u = %q, want %q", got, u1)
	}
	if got := out[2].StringValue(); got != "neighbour" {
		t.Errorf("col b = %q, want %q (uuid width/align shifted it)", got, "neighbour")
	}
	if out[3].Int != 1234567890123 {
		t.Errorf("col c = %d, want 1234567890123", out[3].Int)
	}
	if got := out[4].StringValue(); got != u2 {
		t.Errorf("col v = %q, want %q", got, u2)
	}
	if out[5].Int != 42 {
		t.Errorf("col d = %d, want 42", out[5].Int)
	}
}
