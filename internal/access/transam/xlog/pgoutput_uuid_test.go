package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// pgoDecodePhysicalValue is the SECOND decoder of goopg's heap layout (the
// executor's decodePhysicalPGValueMctx is the first), and the two must agree —
// .ralph/PROMPT.md hard-won rule #2. When uuid columns moved from varlena text
// to PG's native 16-byte pg_uuid_t, an unrouted uuid here would have read those
// bytes through the varlena fall-through: the first raw byte taken as a length
// header, so a replicated uuid would reach the subscriber as garbage of an
// arbitrary length rather than failing.
//
// The expected text is uuid_out's form (postgres/src/backend/utils/adt/uuid.c):
// 32 lowercase hex digits with hyphens after bytes 4, 6, 8 and 10.
func TestPgoDecodeUUIDMatchesPGNativeLayout(t *testing.T) {
	typ := catalog.Type{Name: "uuid"}
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{
			"canonical",
			[]byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8,
				0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11},
			"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		},
		{
			"nil uuid",
			make([]byte, 16),
			"00000000-0000-0000-0000-000000000000",
		},
		{
			"all ones",
			[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
			"ffffffff-ffff-ffff-ffff-ffffffffffff",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n, err := pgoDecodePhysicalValue(typ, tc.raw, nil)
			if err != nil {
				t.Fatalf("pgoDecodePhysicalValue(uuid): %v", err)
			}
			if n != 16 {
				t.Fatalf("consumed %d bytes, want 16 (pg_type OID 2950 typlen)", n)
			}
			if string(got) != tc.want {
				t.Errorf("decoded %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPgoPhysicalAlignUUID pins typalign 'c'. A 4-byte answer here decodes the
// uuid correctly and shifts every following column of the replicated row, so it
// cannot be caught by the value test above.
func TestPgoPhysicalAlignUUID(t *testing.T) {
	typ := catalog.Type{Name: "uuid"}
	for _, off := range []int{1, 3, 5, 17} {
		if got := pgoPhysicalAlign(off, typ); got != off {
			t.Errorf("pgoPhysicalAlign(%d, uuid) = %d, want %d (typalign 'c' — no padding)", off, got, off)
		}
	}
}
