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

// TestPgAmprocRowInt4CmpMatchesFormPgAmproc pins the PG18-canonical
// 6-column FormData_pg_amproc byte layout for the int4 cmp
// (support proc 1) row. Field offsets follow `pg_amproc.h`. The
// 2-byte amprocnum at offset 16 is followed by 2 bytes of padding
// so amproc (regproc, 4-byte aligned) lands at offset 20.
func TestPgAmprocRowInt4CmpMatchesFormPgAmproc(t *testing.T) {
	cols := pgAmprocColDefs()
	if got, want := len(cols), 6; got != want {
		t.Fatalf("pgAmprocColDefs len: got %d, want %d", got, want)
	}
	row := pgAmprocRow(pgAmprocEntry{
		OID: 7100, Family: 1976, LeftType: 23, RightType: 23,
		Num: 1, Proc: 351,
	})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	le := binary.LittleEndian

	if oid := le.Uint32(payload[0:4]); oid != 7100 {
		t.Fatalf("oid: got %d, want 7100", oid)
	}
	if fam := le.Uint32(payload[4:8]); fam != 1976 {
		t.Fatalf("amprocfamily: got %d, want 1976 (INTEGER_BTREE_FAM_OID)", fam)
	}
	if l := le.Uint32(payload[8:12]); l != 23 {
		t.Fatalf("amproclefttype: got %d, want 23 (int4)", l)
	}
	if r := le.Uint32(payload[12:16]); r != 23 {
		t.Fatalf("amprocrighttype: got %d, want 23 (int4)", r)
	}
	if n := le.Uint16(payload[16:18]); n != 1 {
		t.Fatalf("amprocnum: got %d, want 1 (BTORDER_PROC)", n)
	}
	// amproc is regproc (4-byte aligned). 2 padding bytes between
	// amprocnum[16] and amproc[20].
	if p := le.Uint32(payload[20:24]); p != 351 {
		t.Fatalf("amproc: got %d, want 351 (btint4cmp)", p)
	}
}

// TestPgAmprocInitialEntriesCoverPinnedOpclasses asserts every
// pinned default opclass has its canonical comparison support
// function 1 wired to a real pg_proc.dat OID. amprocnum is
// restricted to the three PG18 btree support categories goopg
// seeds at bootstrap: 1 (cmp), 2 (sortsupport), 4 (equalimage).
func TestPgAmprocInitialEntriesCoverPinnedOpclasses(t *testing.T) {
	entries := pgAmprocInitialEntries()
	type key struct {
		family    uint32
		lefttype  uint32
		righttype uint32
		num       int16
	}
	byKey := make(map[key]pgAmprocEntry, len(entries))
	for _, e := range entries {
		switch e.Num {
		case 1, 2, 4:
		default:
			t.Errorf("entry %+v: only amprocnum 1/2/4 are seeded", e)
		}
		k := key{e.Family, e.LeftType, e.RightType, e.Num}
		if _, dup := byKey[k]; dup {
			t.Errorf("duplicate (family=%d, left=%d, right=%d, num=%d)", e.Family, e.LeftType, e.RightType, e.Num)
		}
		byKey[k] = e
	}
	// (family, lefttype, expected proc OID).
	want := []struct {
		family   uint32
		lefttype uint32
		proc     uint32
		label    string
	}{
		{1976, 23, 351, "btint4cmp"},
		{1976, 21, 350, "btint2cmp"},
		{1976, 20, 842, "btint8cmp"},
		{1989, 26, 356, "btoidcmp"},
		{1994, 25, 360, "bttextcmp"},
		{1994, 19, 359, "btnamecmp"},
		{2095, 25, 2166, "bttext_pattern_cmp"},
		{424, 16, 1693, "btboolcmp"},
		{429, 18, 358, "btcharcmp"},
		{1991, 30, 404, "btoidvectorcmp"},
		{2097, 1042, 2180, "btbpchar_pattern_cmp"},
	}
	for _, w := range want {
		got, ok := byKey[key{w.family, w.lefttype, w.lefttype, 1}]
		if !ok {
			t.Errorf("%s: missing entry for family=%d lefttype=%d", w.label, w.family, w.lefttype)
			continue
		}
		if got.Proc != w.proc {
			t.Errorf("%s: proc=%d, want %d", w.label, got.Proc, w.proc)
		}
	}
	// 11 cmp + 8 sortsupport (no sortsupport for bool/char/oidvector)
	// + 11 equalimage = 30 rows.
	if got, want := len(entries), 30; got != want {
		t.Errorf("entry count: got %d, want %d", got, want)
	}
}

// TestPgAmprocInitialEntriesCoverSortsupportAndEqualimage pins
// every PG18 default-type sortsupport (amprocnum=2) and
// equalimage (amprocnum=4) row that ships in pg_amproc.dat for
// the pinned btree opclass families. The OIDs come from
// pg_proc.dat (btintNsortsupport, btoidsortsupport,
// bttextsortsupport, btnamesortsupport,
// bttext_pattern_sortsupport, btbpchar_pattern_sortsupport,
// btequalimage, btvarstrequalimage). Without these rows PG's
// LookupOpclassInfo would fall back to the slow cmp-only sort
// path and disable btree page deduplication for any index whose
// opclass goopg pinned at bootstrap.
func TestPgAmprocInitialEntriesCoverSortsupportAndEqualimage(t *testing.T) {
	entries := pgAmprocInitialEntries()
	type key struct {
		family    uint32
		lefttype  uint32
		righttype uint32
		num       int16
	}
	byKey := make(map[key]pgAmprocEntry, len(entries))
	for _, e := range entries {
		byKey[key{e.Family, e.LeftType, e.RightType, e.Num}] = e
	}

	type want struct {
		family   uint32
		lefttype uint32
		num      int16
		proc     uint32
		label    string
	}
	wants := []want{
		// sortsupport (amprocnum=2) — only for families where PG
		// ships a sortsupport proc in pg_amproc.dat.
		{1976, 21, 2, 3129, "btint2sortsupport"},
		{1976, 23, 2, 3130, "btint4sortsupport"},
		{1976, 20, 2, 3131, "btint8sortsupport"},
		{1989, 26, 2, 3134, "btoidsortsupport"},
		{1994, 25, 2, 3255, "bttextsortsupport"},
		{1994, 19, 2, 3135, "btnamesortsupport"},
		{2095, 25, 2, 3332, "bttext_pattern_sortsupport"},
		{2097, 1042, 2, 3333, "btbpchar_pattern_sortsupport"},
		// equalimage (amprocnum=4) — one per pinned default
		// opclass. text/name use btvarstrequalimage (5050); every
		// other type uses the generic btequalimage (5051).
		{1976, 21, 4, 5051, "int2 btequalimage"},
		{1976, 23, 4, 5051, "int4 btequalimage"},
		{1976, 20, 4, 5051, "int8 btequalimage"},
		{1989, 26, 4, 5051, "oid btequalimage"},
		{1994, 25, 4, 5050, "text btvarstrequalimage"},
		{1994, 19, 4, 5050, "name btvarstrequalimage"},
		{2095, 25, 4, 5051, "text_pattern btequalimage"},
		{424, 16, 4, 5051, "bool btequalimage"},
		{429, 18, 4, 5051, "char btequalimage"},
		{1991, 30, 4, 5051, "oidvector btequalimage"},
		{2097, 1042, 4, 5051, "bpchar_pattern btequalimage"},
	}
	for _, w := range wants {
		got, ok := byKey[key{w.family, w.lefttype, w.lefttype, w.num}]
		if !ok {
			t.Errorf("%s: missing entry for family=%d lefttype=%d num=%d",
				w.label, w.family, w.lefttype, w.num)
			continue
		}
		if got.Proc != w.proc {
			t.Errorf("%s: proc=%d, want %d", w.label, got.Proc, w.proc)
		}
	}
}

// TestBootstrapPgAmprocTuplesWritesRowsToBase1And5 verifies the
// end-to-end bootstrap writes pg_amproc to both template1 and
// postgres database directories, and the int4 cmp row appears in
// the resulting page.
func TestBootstrapPgAmprocTuplesWritesRowsToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := bootstrapPgAmprocTuples(dir); err != nil {
		t.Fatalf("bootstrapPgAmprocTuples: %v", err)
	}
	// Needle: oid=7100 || family=1976 || left=23 || right=23.
	le := binary.LittleEndian
	needle := make([]byte, 16)
	le.PutUint32(needle[0:4], 7100)
	le.PutUint32(needle[4:8], 1976)
	le.PutUint32(needle[8:12], 23)
	le.PutUint32(needle[12:16], 23)
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2603")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if got := len(raw); got != storage.BlockSize {
			t.Fatalf("%s: page size %d, want %d", path, got, storage.BlockSize)
		}
		if !bytes.Contains(raw, needle) {
			t.Fatalf("%s: int4 cmp row prefix not found in page", path)
		}
	}
}

// TestPgAmprocAttrsMatchesPg18FormPgAmproc pins the relcache
// init-file TupleDesc to the 6-column PG18 FormData_pg_amproc
// shape. Drift here would break PG's heap_deformtuple during
// LookupOpclassInfo's pg_amproc scan.
func TestPgAmprocAttrsMatchesPg18FormPgAmproc(t *testing.T) {
	attrs := pgAmprocAttrs()
	if got, want := len(attrs), 6; got != want {
		t.Fatalf("pgAmprocAttrs len: got %d, want %d", got, want)
	}
	type want struct {
		name    string
		typeOID uint32
		length  int16
	}
	expected := []want{
		{"oid", 26, 4},
		{"amprocfamily", 26, 4},
		{"amproclefttype", 26, 4},
		{"amprocrighttype", 26, 4},
		{"amprocnum", 21, 2},
		{"amproc", 24, 4}, // regproc OID 24
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
