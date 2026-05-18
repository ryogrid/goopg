package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPostgresRoleHeapRowRolnameByteLayout pins the byte-level
// layout of the pg_authid heap tuples written by bootstrapPostgresRole.
//
// M0106-0010 Step 3de surfaced a working hypothesis from Step 3dd that
// the standby's SIGSEGV in btnamecmp+0x52 (during the first
// `get_role_oid → SearchSysCache1(AUTHNAME, "<os-user>")` lookup) might
// stem from a corrupt or NULL `rolname` payload inside the pg_authid
// heap rows themselves. This regression locks the byte layout down so
// any future change to the encoder's column ordering, NAME padding, or
// HeapTupleHeader infomask is caught at unit-test time instead of
// surfacing as a libc strncmp SEGV three frames removed from the bug.
//
// Heap-tuple invariants pinned here (PG18 src/include/catalog/pg_authid.h
// + src/include/access/htup_details.h):
//
//   - t_hoff = 24 (PG's MinHeapTupleSize when no null bitmap; pg_authid
//     bootstrap rows have natts=12 with rolpassword/rolvaliduntil seeded
//     to non-null defaults so HEAP_HASNULL stays clear).
//   - infomask2 low 11 bits = 12 (Natts_pg_authid).
//   - rolname column lives at payload offset 4..67 (64-byte NameData
//     immediately after the 4-byte oid column; NAME has typalign='c' so
//     no padding is inserted between oid and rolname).
//   - cstring portion of the NameData (bytes preceding the first NUL)
//     matches the seeded role name byte-for-byte.
//   - trailing 64 − len(rolname) bytes are zero-padded — the exact PG18
//     `namestrcpy` semantics our btnamecmp relies on for the strncmp.
//
// Without these invariants pinned, a regression that switched the
// encoder back to the original `name`-as-varlena path (see codec.go's
// duplicate "name" case in encodeValue) would silently desync the on-
// disk byte layout from PG's `Form_pg_authid` struct cast and any
// downstream `_bt_compare → btnamecmp → strncmp(64)` would walk past
// the actual NameData into adjacent payload bytes — exactly the kind of
// failure Step 3dd's LD_PRELOAD shim captured but couldn't attribute.
func TestBootstrapPostgresRoleHeapRowRolnameByteLayout(t *testing.T) {
	t.Setenv("USER", "ryo")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	entries, err := bootstrapPostgresRole(dir)
	if err != nil {
		t.Fatalf("bootstrapPostgresRole: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%d, want 2 (postgres + ryo)", len(entries))
	}

	raw, err := os.ReadFile(filepath.Join(dir, "global", "1260"))
	if err != nil {
		t.Fatalf("read global/1260: %v", err)
	}
	if len(raw) != storage.BlockSize {
		t.Fatalf("global/1260 size=%d, want %d", len(raw), storage.BlockSize)
	}

	le := binary.LittleEndian
	pdLower := le.Uint16(raw[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	if nItems != 2 {
		t.Fatalf("nItems=%d, want 2", nItems)
	}

	wantRolname := map[uint32]string{
		10:    "postgres",
		16384: "ryo",
	}

	for i := 0; i < nItems; i++ {
		lp := le.Uint32(raw[storage.SizeOfPageHeaderData+i*4:])
		lpOff := int(lp & 0x7FFF)
		lpLen := int((lp >> 17) & 0x7FFF)
		if lpOff+lpLen > len(raw) {
			t.Fatalf("item %d: lp_off=%d lp_len=%d overflows page", i, lpOff, lpLen)
		}
		tup := raw[lpOff : lpOff+lpLen]
		// HeapTupleHeader is 23 bytes (+ optional null bitmap, then payload at t_hoff).
		// PG layout: t_xmin(4) t_xmax(4) t_field3(4) t_ctid(6) t_infomask2(2) t_infomask(2) t_hoff(1)
		tHoff := int(tup[22])
		if tHoff != 24 {
			t.Errorf("item %d: t_hoff=%d, want 24", i, tHoff)
		}
		infomask2 := le.Uint16(tup[18:20])
		natts := int(infomask2 & 0x07FF)
		if natts != 12 {
			t.Errorf("item %d: natts=%d, want 12 (Natts_pg_authid)", i, natts)
		}
		infomask := le.Uint16(tup[20:22])
		// Bootstrap rows must NOT have HEAP_HASNULL set — every column
		// is seeded to a non-null value. If a future encoder change
		// flips a default to NULL, the null bitmap shifts t_hoff and
		// every offset below breaks.
		if infomask&0x0001 != 0 {
			t.Errorf("item %d: HEAP_HASNULL set (infomask=0x%04x), want clear", i, infomask)
		}

		payload := tup[tHoff:]
		if len(payload) < 4+64 {
			t.Fatalf("item %d: payload too short for oid+NameData: %d", i, len(payload))
		}
		oid := le.Uint32(payload[0:4])
		want, ok := wantRolname[oid]
		if !ok {
			t.Fatalf("item %d: unexpected oid=%d", i, oid)
		}
		// rolname NameData = payload[4..67]. First len(want) bytes match
		// the seeded role name; the rest must be zero-padded so strncmp
		// terminates at the NUL boundary.
		got := string(payload[4 : 4+len(want)])
		if got != want {
			t.Errorf("item %d: oid=%d rolname cstring prefix=%q, want %q", i, oid, got, want)
		}
		for j := 4 + len(want); j < 4+64; j++ {
			if payload[j] != 0 {
				t.Errorf("item %d: oid=%d NameData[%d]=0x%02x, want zero (NAME zero-pad)", i, oid, j-4, payload[j])
			}
		}
	}
}
