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

// TestPgAmopRowInt4LessMatchesFormPgAmop pins the PG18-canonical
// 9-column FormData_pg_amop byte layout for the int4 strategy-1
// (less-than) row. Field offsets follow `pg_amop.h`. The 1-byte
// amoppurpose at offset 18 is followed by 1 byte of padding so
// amopopr (4-byte aligned) lands at offset 20.
func TestPgAmopRowInt4LessMatchesFormPgAmop(t *testing.T) {
	cols := pgAmopColDefs()
	if got, want := len(cols), 9; got != want {
		t.Fatalf("pgAmopColDefs len: got %d, want %d", got, want)
	}
	row := pgAmopRow(pgAmopEntry{
		OID: 7000, Family: 1976, LeftType: 23, RightType: 23,
		Strategy: 1, Purpose: 's', Operator: 97, Method: 403,
		SortFamily: 0,
	})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	le := binary.LittleEndian

	if oid := le.Uint32(payload[0:4]); oid != 7000 {
		t.Fatalf("oid: got %d, want 7000", oid)
	}
	if fam := le.Uint32(payload[4:8]); fam != 1976 {
		t.Fatalf("amopfamily: got %d, want 1976 (INTEGER_BTREE_FAM_OID)", fam)
	}
	if l := le.Uint32(payload[8:12]); l != 23 {
		t.Fatalf("amoplefttype: got %d, want 23 (int4)", l)
	}
	if r := le.Uint32(payload[12:16]); r != 23 {
		t.Fatalf("amoprighttype: got %d, want 23 (int4)", r)
	}
	if s := le.Uint16(payload[16:18]); s != 1 {
		t.Fatalf("amopstrategy: got %d, want 1", s)
	}
	if payload[18] != 's' {
		t.Fatalf("amoppurpose: got %q, want 's'", payload[18])
	}
	// amopopr is oid (4-byte aligned). 1 padding byte between
	// amoppurpose[18] and amopopr[20].
	if op := le.Uint32(payload[20:24]); op != 97 {
		t.Fatalf("amopopr: got %d, want 97 (Int4LessOperator)", op)
	}
	if m := le.Uint32(payload[24:28]); m != 403 {
		t.Fatalf("amopmethod: got %d, want 403 (btree)", m)
	}
	if sf := le.Uint32(payload[28:32]); sf != 0 {
		t.Fatalf("amopsortfamily: got %d, want 0", sf)
	}
}

// TestPgAmopInitialEntriesCoverPinnedOpclasses asserts every
// pinned default opclass family/lefttype pair has the canonical
// five strategy operator rows (<, <=, =, >=, >) keyed by
// pg_operator.dat OIDs. It also verifies no (family, left, right,
// strategy) duplicate exists across all 945 entries (btree + hash +
// gist + gin + spgist + brin).
func TestPgAmopInitialEntriesCoverPinnedOpclasses(t *testing.T) {
	entries := pgAmopInitialEntries()
	type key struct {
		family    uint32
		lefttype  uint32
		righttype uint32
		strategy  int16
	}
	byKey := make(map[key]pgAmopEntry, len(entries))
	for _, e := range entries {
		// method and purpose are AM-specific; we don't enforce a single value.
		if e.Purpose != 's' && e.Purpose != 'o' {
			t.Errorf("entry %+v: purpose=%q, want 's' or 'o'", e, e.Purpose)
		}
		k := key{e.Family, e.LeftType, e.RightType, e.Strategy}
		if _, dup := byKey[k]; dup {
			t.Errorf("duplicate (family=%d, left=%d, right=%d, strategy=%d)", e.Family, e.LeftType, e.RightType, e.Strategy)
		}
		byKey[k] = e
	}
	// (family, lefttype, righttype, expected op OID per strategy 1..5).
	want := []struct {
		family    uint32
		lefttype  uint32
		righttype uint32
		ops       [5]uint32
		label     string
	}{
		{1976, 23, 23, [5]uint32{97, 523, 96, 525, 521}, "int4"},
		{1976, 21, 21, [5]uint32{95, 522, 94, 524, 520}, "int2"},
		{1976, 20, 20, [5]uint32{412, 414, 410, 415, 413}, "int8"},
		// Cross-type integer_ops — pg_amop.dat int24/int28/int42/int48/int82/int84.
		{1976, 21, 23, [5]uint32{534, 540, 532, 542, 536}, "int24"},
		{1976, 21, 20, [5]uint32{1864, 1866, 1862, 1867, 1865}, "int28"},
		{1976, 23, 21, [5]uint32{535, 541, 533, 543, 537}, "int42"},
		{1976, 23, 20, [5]uint32{37, 80, 15, 82, 76}, "int48"},
		{1976, 20, 21, [5]uint32{1870, 1872, 1868, 1873, 1871}, "int82"},
		{1976, 20, 23, [5]uint32{418, 420, 416, 430, 419}, "int84"},
		{1989, 26, 26, [5]uint32{609, 611, 607, 612, 610}, "oid"},
		{1994, 25, 25, [5]uint32{664, 665, 98, 667, 666}, "text"},
		{1994, 19, 19, [5]uint32{660, 661, 93, 663, 662}, "name"},
		{2095, 25, 25, [5]uint32{2314, 2315, 98, 2317, 2318}, "text_pattern"},
		{424, 16, 16, [5]uint32{58, 1694, 91, 1695, 59}, "bool"},
		{429, 18, 18, [5]uint32{631, 632, 92, 634, 633}, "char"},
		{1991, 30, 30, [5]uint32{645, 647, 649, 648, 646}, "oidvector"},
		{2097, 1042, 1042, [5]uint32{2326, 2327, 1054, 2329, 2330}, "bpchar_pattern"},
	}
	for _, w := range want {
		for i := 0; i < 5; i++ {
			got, ok := byKey[key{w.family, w.lefttype, w.righttype, int16(i + 1)}]
			if !ok {
				t.Errorf("%s strategy %d: missing entry for family=%d lefttype=%d righttype=%d",
					w.label, i+1, w.family, w.lefttype, w.righttype)
				continue
			}
			if got.Operator != w.ops[i] {
				t.Errorf("%s strategy %d: opr=%d, want %d", w.label, i+1, got.Operator, w.ops[i])
			}
		}
	}
	// Total row count matches pg_amop.dat: 945 entries across all AMs.
	if got, want := len(entries), 945; got != want {
		t.Errorf("entry count: got %d, want %d", got, want)
	}
}

// TestBootstrapPgAmopTuplesWritesRowsToBase1And5 verifies the
// end-to-end bootstrap writes the pg_amop heap to both template1
// and postgres database directories, and the int4 less-than
// strategy row appears in the resulting page.
func TestBootstrapPgAmopTuplesWritesRowsToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := bootstrapPgAmopTuples(dir); err != nil {
		t.Fatalf("bootstrapPgAmopTuples: %v", err)
	}
	// Build the needle: oid=7000 || family=1976 || left=23 || right=23.
	le := binary.LittleEndian
	needle := make([]byte, 16)
	le.PutUint32(needle[0:4], 7000)
	le.PutUint32(needle[4:8], 1976)
	le.PutUint32(needle[8:12], 23)
	le.PutUint32(needle[12:16], 23)
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2602")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// 945 rows × ~56 bytes/tuple ≈ 7 pages; must be a multiple of BlockSize.
		if got := len(raw); got == 0 || got%storage.BlockSize != 0 {
			t.Fatalf("%s: file size %d is not a positive multiple of BlockSize %d", path, got, storage.BlockSize)
		}
		if !bytes.Contains(raw, needle) {
			t.Fatalf("%s: int4 strategy-1 row prefix not found in heap", path)
		}
	}
}

// TestPgAmopAttrsMatchesPg18FormPgAmop pins the relcache
// init-file TupleDesc to the 9-column PG18 FormData_pg_amop
// shape. Drift here would break PG's heap_deformtuple when
// SearchSysCache scans pg_amop.
func TestPgAmopAttrsMatchesPg18FormPgAmop(t *testing.T) {
	attrs := pgAmopAttrs()
	if got, want := len(attrs), 9; got != want {
		t.Fatalf("pgAmopAttrs len: got %d, want %d", got, want)
	}
	type want struct {
		name    string
		typeOID uint32
		length  int16
	}
	expected := []want{
		{"oid", 26, 4},
		{"amopfamily", 26, 4},
		{"amoplefttype", 26, 4},
		{"amoprighttype", 26, 4},
		{"amopstrategy", 21, 2},
		{"amoppurpose", 18, 1},
		{"amopopr", 26, 4},
		{"amopmethod", 26, 4},
		{"amopsortfamily", 26, 4},
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
