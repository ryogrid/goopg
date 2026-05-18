package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// TestPgProcRowBtreeHandlerMatchesFormPgProc pins the PG18-canonical
// 30-column FormData_pg_proc byte layout for the bthandler row.
// Field offsets are computed from `postgres/src/include/catalog/pg_proc.h`
// and the LE 64-bit alignment rules used by EncodeRowPG. Any
// reordering that breaks GETSTRUCT(tup)→Form_pg_proc will trip these
// offset checks.
func TestPgProcRowBtreeHandlerMatchesFormPgProc(t *testing.T) {
	cols := pgProcColDefs()
	if got, want := len(cols), 30; got != want {
		t.Fatalf("pgProcColDefs len: got %d, want %d", got, want)
	}
	row := pgProcRow(pgProcEntry{OID: 330, Name: "bthandler", RetType: 325, HandlerName: "bthandler"})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	le := binary.LittleEndian

	if oid := le.Uint32(payload[0:4]); oid != 330 {
		t.Fatalf("oid: got %d, want 330", oid)
	}
	want := make([]byte, 64)
	copy(want, []byte("bthandler"))
	if !bytes.Equal(payload[4:68], want) {
		t.Fatalf("proname: got % x, want NUL-padded 'bthandler'", payload[4:68])
	}
	if ns := le.Uint32(payload[68:72]); ns != 11 {
		t.Fatalf("pronamespace: got %d, want 11 (pg_catalog)", ns)
	}
	if owner := le.Uint32(payload[72:76]); owner != 10 {
		t.Fatalf("proowner: got %d, want 10 (bootstrap superuser)", owner)
	}
	if lang := le.Uint32(payload[76:80]); lang != 12 {
		t.Fatalf("prolang: got %d, want 12 (internal)", lang)
	}
	if provariadic := le.Uint32(payload[88:92]); provariadic != 0 {
		t.Fatalf("provariadic: got %d, want 0", provariadic)
	}
	if support := le.Uint32(payload[92:96]); support != 0 {
		t.Fatalf("prosupport: got %d, want 0", support)
	}
	if payload[96] != 'f' {
		t.Fatalf("prokind: got %q, want 'f'", payload[96])
	}
	if payload[97] != 0 {
		t.Fatalf("prosecdef: got %d, want false", payload[97])
	}
	if payload[99] != 1 {
		t.Fatalf("proisstrict: got %d, want true", payload[99])
	}
	if payload[101] != 'v' {
		t.Fatalf("provolatile: got %q, want 'v'", payload[101])
	}
	if payload[102] != 's' {
		t.Fatalf("proparallel: got %q, want 's'", payload[102])
	}
	if nargs := int16(le.Uint16(payload[104:106])); nargs != 1 {
		t.Fatalf("pronargs: got %d, want 1", nargs)
	}
	if ndefaults := int16(le.Uint16(payload[106:108])); ndefaults != 0 {
		t.Fatalf("pronargdefaults: got %d, want 0", ndefaults)
	}
	// prorettype is `oid` (4-byte aligned). After pronargdefaults
	// at offset 106..108, prorettype lands at offset 108 (already
	// 4-aligned, no padding needed).
	if rettype := le.Uint32(payload[108:112]); rettype != 325 {
		t.Fatalf("prorettype: got %d, want 325 (index_am_handler)", rettype)
	}
	// proargtypes is oidvector starting at offset 112 (4-aligned).
	// First 4 bytes = varlena uncompressed header carrying total
	// size 28 (24 header + 1*4 payload) shifted left by 2.
	if vh := le.Uint32(payload[112:116]); vh != 28<<2 {
		t.Fatalf("proargtypes varlena header: got %#x, want %#x", vh, 28<<2)
	}
	if ndim := le.Uint32(payload[116:120]); ndim != 1 {
		t.Fatalf("proargtypes ndim: got %d, want 1", ndim)
	}
	if elem := le.Uint32(payload[124:128]); elem != 26 {
		t.Fatalf("proargtypes elemtype: got %d, want 26 (oid)", elem)
	}
	if dim1 := le.Uint32(payload[128:132]); dim1 != 1 {
		t.Fatalf("proargtypes dim1: got %d, want 1", dim1)
	}
	if lb := le.Uint32(payload[132:136]); lb != 0 {
		t.Fatalf("proargtypes lbound1: got %d, want 0", lb)
	}
	if arg := le.Uint32(payload[136:140]); arg != 2281 {
		t.Fatalf("proargtypes[0]: got %d, want 2281 (internal)", arg)
	}
}

// TestPgProcInitialEntriesCoverAMHandlers makes sure the bootstrap
// list keeps the seven PG18 AM handler functions in lockstep with
// pg_am: heap_tableam_handler (3), bthandler (330), hashhandler (331),
// gisthandler (332), ginhandler (333), spghandler (334), brinhandler
// (335). The mapping mirrors pgAmInitialEntries.
func TestPgProcInitialEntriesCoverAMHandlers(t *testing.T) {
	entries := pgProcInitialEntries()
	if got, want := len(entries), 7; got != want {
		t.Fatalf("pgProcInitialEntries len: got %d, want %d", got, want)
	}
	wantNames := map[uint32]string{
		3:   "heap_tableam_handler",
		330: "bthandler",
		331: "hashhandler",
		332: "gisthandler",
		333: "ginhandler",
		334: "spghandler",
		335: "brinhandler",
	}
	wantRet := map[uint32]uint32{
		3:   269, // table_am_handler
		330: 325, // index_am_handler
		331: 325, 332: 325, 333: 325, 334: 325, 335: 325,
	}
	for _, e := range entries {
		if e.Name != wantNames[e.OID] {
			t.Errorf("oid=%d: name=%q, want %q", e.OID, e.Name, wantNames[e.OID])
		}
		if e.HandlerName != wantNames[e.OID] {
			t.Errorf("oid=%d: handler=%q, want %q", e.OID, e.HandlerName, wantNames[e.OID])
		}
		if e.RetType != wantRet[e.OID] {
			t.Errorf("oid=%d: rettype=%d, want %d", e.OID, e.RetType, wantRet[e.OID])
		}
	}
	if t.Failed() {
		t.Logf("entries: %+v", entries)
	}
}

// TestBootstrapPgProcTuplesWritesRowsToBase1And5 exercises the full
// bootstrap path end-to-end: the 7 handler rows must land in both
// base/1/1255 and base/5/1255 (template1 and postgres database) as a
// single page-sized file with at least one row matching the
// NUL-padded "bthandler" proname.
func TestBootstrapPgProcTuplesWritesRowsToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if _, err := bootstrapPgProcTuples(dir); err != nil {
		t.Fatalf("bootstrapPgProcTuples: %v", err)
	}
	needle := make([]byte, 64)
	copy(needle, []byte("bthandler"))
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "1255")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if got := len(raw); got != storage.BlockSize {
			t.Fatalf("%s: page size %d, want %d", path, got, storage.BlockSize)
		}
		if !bytes.Contains(raw, needle) {
			t.Fatalf("%s: bthandler proname not found in page", path)
		}
	}
}

// TestPgProcAttrsMatchesPg18FormPgProc pins the relcache init-file
// TupleDesc to the 30-column PG18 FormData_pg_proc shape. Drift here
// would break PG's heap_deformtuple when SearchSysCache1(PROCOID, ...)
// tries to read prosrc (attnum 26) via the descriptor.
func TestPgProcAttrsMatchesPg18FormPgProc(t *testing.T) {
	attrs := pgProcAttrs()
	if got, want := len(attrs), 30; got != want {
		t.Fatalf("pgProcAttrs len: got %d, want %d", got, want)
	}
	type want struct {
		name    string
		typeOID uint32
		length  int16
	}
	expected := []want{
		{"oid", 26, 4},
		{"proname", 19, 64},
		{"pronamespace", 26, 4},
		{"proowner", 26, 4},
		{"prolang", 26, 4},
		{"procost", 700, 4},
		{"prorows", 700, 4},
		{"provariadic", 26, 4},
		{"prosupport", 24, 4},
		{"prokind", 18, 1},
		{"prosecdef", 16, 1},
		{"proleakproof", 16, 1},
		{"proisstrict", 16, 1},
		{"proretset", 16, 1},
		{"provolatile", 18, 1},
		{"proparallel", 18, 1},
		{"pronargs", 21, 2},
		{"pronargdefaults", 21, 2},
		{"prorettype", 26, 4},
		{"proargtypes", 30, -1},
		{"proallargtypes", 1028, -1},
		{"proargmodes", 1002, -1},
		{"proargnames", 1009, -1},
		{"proargdefaults", 194, -1},
		{"protrftypes", 1028, -1},
		{"prosrc", 25, -1},
		{"probin", 25, -1},
		{"prosqlbody", 194, -1},
		{"proconfig", 1009, -1},
		{"proacl", 1034, -1},
	}
	for i, e := range expected {
		got := attrs[i]
		if got.Name != e.name || got.TypeOID != e.typeOID || got.Len != e.length || int(got.Num) != i+1 {
			t.Errorf("attr[%d]: got (%q, %d, %d, num=%d), want (%q, %d, %d, num=%d)",
				i, got.Name, got.TypeOID, got.Len, got.Num,
				e.name, e.typeOID, e.length, i+1)
		}
	}
}
