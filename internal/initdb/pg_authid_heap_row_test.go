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
	entries, err := bootstrapPostgresRole(dir, "postgres")
	if err != nil {
		t.Fatalf("bootstrapPostgresRole: %v", err)
	}
	// 2 bootstrap rows (postgres + ryo) + 16 predefined roles = 18 total.
	const wantEntries = 18
	if len(entries) != wantEntries {
		t.Fatalf("entries=%d, want %d (2 bootstrap + 16 predefined)", len(entries), wantEntries)
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
	if nItems != wantEntries {
		t.Fatalf("nItems=%d, want %d", nItems, wantEntries)
	}

	// Bootstrap rows: OID→expected rolname.
	bootstrapRolname := map[uint32]string{
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
		// PG layout: t_xmin(4) t_xmax(4) t_field3(4) t_ctid(6) t_infomask2(2) t_infomask(2) t_hoff(1)
		tHoff := int(tup[22])
		infomask2 := le.Uint16(tup[18:20])
		natts := int(infomask2 & 0x07FF)
		if natts != 12 {
			t.Errorf("item %d: natts=%d, want 12 (Natts_pg_authid)", i, natts)
		}
		infomask := le.Uint16(tup[20:22])

		payload := tup[tHoff:]
		if len(payload) < 4+64 {
			t.Fatalf("item %d: payload too short for oid+NameData: %d", i, len(payload))
		}
		oid := le.Uint32(payload[0:4])

		if want, isBootstrap := bootstrapRolname[oid]; isBootstrap {
			// Bootstrap rows must NOT have HEAP_HASNULL — every column is
			// seeded non-null so t_hoff stays at 24 (no null bitmap).
			if tHoff != 24 {
				t.Errorf("item %d (bootstrap oid=%d): t_hoff=%d, want 24", i, oid, tHoff)
			}
			if infomask&0x0001 != 0 {
				t.Errorf("item %d (bootstrap oid=%d): HEAP_HASNULL set (infomask=0x%04x), want clear", i, oid, infomask)
			}
			// rolname NameData = payload[4..67].
			got := string(payload[4 : 4+len(want)])
			if got != want {
				t.Errorf("item %d: oid=%d rolname prefix=%q, want %q", i, oid, got, want)
			}
			for j := 4 + len(want); j < 4+64; j++ {
				if payload[j] != 0 {
					t.Errorf("item %d: oid=%d NameData[%d]=0x%02x, want zero", i, oid, j-4, payload[j])
				}
			}
		} else {
			// Predefined-role rows must have HEAP_HASNULL (null bitmap) and
			// t_hoff=32 (24-byte header + 2-byte bitmap rounded to MAXALIGN=8).
			if tHoff != 32 {
				t.Errorf("item %d (predefined oid=%d): t_hoff=%d, want 32", i, oid, tHoff)
			}
			if infomask&0x0001 == 0 {
				t.Errorf("item %d (predefined oid=%d): HEAP_HASNULL not set (infomask=0x%04x)", i, oid, infomask)
			}
		}
	}
}

// TestBootstrapPredefinedRolesHaveNullBitmapAndFrozenXmin pins the key
// invariants for the 16 predefined-role rows that batched-11 adds:
//   - null bitmap byte 0 = 0xFF (cols 0-7 not null)
//   - null bitmap byte 1 = 0x03 (cols 8-9 not null, cols 10-11 null)
//   - t_hoff = 32 (MAXALIGN(23-byte header + 2-byte bitmap) = 32)
//   - xmin = FrozenTransactionID (2) for permanent visibility
//   - each expected OID and rolname is present in the page
func TestBootstrapPredefinedRolesHaveNullBitmapAndFrozenXmin(t *testing.T) {
	t.Setenv("USER", "postgres") // keep bootstrap to 1 row so slot index = simple
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	entries, err := bootstrapPostgresRole(dir, "postgres")
	if err != nil {
		t.Fatalf("bootstrapPostgresRole: %v", err)
	}
	// USER=="postgres" → 1 bootstrap row + 16 predefined = 17 total.
	if len(entries) != 17 {
		t.Fatalf("entries=%d, want 17 (1 bootstrap + 16 predefined)", len(entries))
	}

	raw, err := os.ReadFile(filepath.Join(dir, "global", "1260"))
	if err != nil {
		t.Fatalf("read global/1260: %v", err)
	}

	le := binary.LittleEndian
	pdLower := le.Uint16(raw[12:14])
	nItems := int((pdLower - storage.SizeOfPageHeaderData) / 4)
	if nItems != 17 {
		t.Fatalf("nItems=%d, want 17", nItems)
	}

	// Expected predefined roles: OID → rolname.
	wantPredefined := map[uint32]string{
		6171: "pg_database_owner",
		6181: "pg_read_all_data",
		6182: "pg_write_all_data",
		3373: "pg_monitor",
		3374: "pg_read_all_settings",
		3375: "pg_read_all_stats",
		3377: "pg_stat_scan_tables",
		4569: "pg_read_server_files",
		4570: "pg_write_server_files",
		4571: "pg_execute_server_program",
		4200: "pg_signal_backend",
		4544: "pg_checkpoint",
		6337: "pg_maintain",
		4550: "pg_use_reserved_connections",
		6304: "pg_create_subscription",
		6392: "pg_signal_autovacuum_worker",
	}
	foundPredefined := map[uint32]bool{}

	for i := 0; i < nItems; i++ {
		lp := le.Uint32(raw[storage.SizeOfPageHeaderData+i*4:])
		lpOff := int(lp & 0x7FFF)
		lpLen := int((lp >> 17) & 0x7FFF)
		tup := raw[lpOff : lpOff+lpLen]
		tHoff := int(tup[22])
		infomask := le.Uint16(tup[20:22])

		payload := tup[tHoff:]
		oid := le.Uint32(payload[0:4])

		wantName, isPredefined := wantPredefined[oid]
		if !isPredefined {
			continue // bootstrap row (OID 10)
		}

		foundPredefined[oid] = true

		// t_hoff = 32: header(23) + bitmap(2) rounded to MAXALIGN(8) = 32.
		if tHoff != 32 {
			t.Errorf("predefined oid=%d: t_hoff=%d, want 32", oid, tHoff)
		}

		// HEAP_HASNULL must be set.
		if infomask&0x0001 == 0 {
			t.Errorf("predefined oid=%d: HEAP_HASNULL not set (infomask=0x%04x)", oid, infomask)
		}

		// xmin = FrozenTransactionID (2).
		xmin := le.Uint32(tup[0:4])
		if xmin != 2 {
			t.Errorf("predefined oid=%d: xmin=%d, want 2 (FrozenTransactionID)", oid, xmin)
		}

		// Null bitmap: byte 0 = 0xFF (cols 0-7 not null),
		// byte 1 = 0x03 (cols 8-9 not null, cols 10-11 null).
		// PG places the bitmap immediately after the 23-byte fixed header.
		bm0 := tup[23]
		bm1 := tup[24]
		if bm0 != 0xFF {
			t.Errorf("predefined oid=%d: bitmap[0]=0x%02x, want 0xFF", oid, bm0)
		}
		if bm1 != 0x03 {
			t.Errorf("predefined oid=%d: bitmap[1]=0x%02x, want 0x03", oid, bm1)
		}

		// rolname NameData = payload[4..67].
		if len(payload) < 4+64 {
			t.Fatalf("predefined oid=%d: payload too short: %d", oid, len(payload))
		}
		got := string(payload[4 : 4+len(wantName)])
		if got != wantName {
			t.Errorf("predefined oid=%d: rolname prefix=%q, want %q", oid, got, wantName)
		}
		for j := 4 + len(wantName); j < 4+64; j++ {
			if payload[j] != 0 {
				t.Errorf("predefined oid=%d: NameData[%d]=0x%02x, want zero", oid, j-4, payload[j])
			}
		}
	}

	// All 16 predefined roles must be present.
	for oid, name := range wantPredefined {
		if !foundPredefined[oid] {
			t.Errorf("predefined role oid=%d (%s) missing from page", oid, name)
		}
	}
}
