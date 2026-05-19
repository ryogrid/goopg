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

// TestPgOpclassRowOidOpsMatchesFormPgOpclass pins the PG18-canonical
// 9-column FormData_pg_opclass byte layout for the oid_ops row.
// Field offsets follow `postgres/src/include/catalog/pg_opclass.h`
// and EncodeRowPG's PG-native alignment.
//
// The bool→oid jump between opcdefault (offset 88) and opckeytype
// (must be 4-aligned) creates 3 bytes of padding — encoded payload
// places opckeytype at offset 92.
func TestPgOpclassRowOidOpsMatchesFormPgOpclass(t *testing.T) {
	cols := pgOpclassColDefs()
	if got, want := len(cols), 9; got != want {
		t.Fatalf("pgOpclassColDefs len: got %d, want %d", got, want)
	}
	row := pgOpclassRow(pgOpclassEntry{
		OID: 1981, Method: 403, Name: "oid_ops",
		Namespace: 11, Owner: 10, Family: 1989, IntType: 26,
		Default: true, KeyType: 0,
	})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	le := binary.LittleEndian

	if oid := le.Uint32(payload[0:4]); oid != 1981 {
		t.Fatalf("oid: got %d, want 1981", oid)
	}
	if m := le.Uint32(payload[4:8]); m != 403 {
		t.Fatalf("opcmethod: got %d, want 403 (btree)", m)
	}
	want := make([]byte, 64)
	copy(want, []byte("oid_ops"))
	if !bytes.Equal(payload[8:72], want) {
		t.Fatalf("opcname: got % x, want NUL-padded 'oid_ops'", payload[8:72])
	}
	if ns := le.Uint32(payload[72:76]); ns != 11 {
		t.Fatalf("opcnamespace: got %d, want 11 (pg_catalog)", ns)
	}
	if owner := le.Uint32(payload[76:80]); owner != 10 {
		t.Fatalf("opcowner: got %d, want 10", owner)
	}
	if fam := le.Uint32(payload[80:84]); fam != 1989 {
		t.Fatalf("opcfamily: got %d, want 1989 (OID_BTREE_FAM_OID)", fam)
	}
	if it := le.Uint32(payload[84:88]); it != 26 {
		t.Fatalf("opcintype: got %d, want 26 (oid)", it)
	}
	if payload[88] != 1 {
		t.Fatalf("opcdefault: got %d, want true (1)", payload[88])
	}
	// opckeytype is oid (4-byte aligned); 3 padding bytes between
	// opcdefault[88] and opckeytype[92].
	if kt := le.Uint32(payload[92:96]); kt != 0 {
		t.Fatalf("opckeytype: got %d, want 0", kt)
	}
}

// TestPgOpclassInitialEntriesCoverNailedIndexNeeds asserts the
// entries returned by pgOpclassInitialEntries cover every btree
// opclass a nailed system index can possibly reference: oid_ops,
// name_ops, int2_ops, int4_ops, char_ops, oidvector_ops (plus the
// well-known integer/text variants).
//
// The OID set must include both the hardcoded constants from
// pg_opclass_d.h and the synthetic ones goopg pins.
func TestPgOpclassInitialEntriesCoverNailedIndexNeeds(t *testing.T) {
	entries := pgOpclassInitialEntries()
	byOID := make(map[uint32]pgOpclassEntry, len(entries))
	for _, e := range entries {
		byOID[e.OID] = e
	}
	required := map[uint32]string{
		1978: "int4_ops",
		1979: "int2_ops",
		1981: "oid_ops",
		3124: "int8_ops",
		3126: "text_ops",
		4217: "text_pattern_ops",
		4218: "varchar_pattern_ops",
		4219: "bpchar_pattern_ops",
		1986: "name_ops",
		1985: "char_ops",
		1987: "oidvector_ops",
		1984: "bool_ops",
	}
	for oid, name := range required {
		got, ok := byOID[oid]
		if !ok {
			t.Errorf("missing opclass entry for OID %d (%s)", oid, name)
			continue
		}
		if got.Name != name {
			t.Errorf("OID %d: name=%q, want %q", oid, got.Name, name)
		}
		if got.Method != 403 {
			t.Errorf("OID %d: method=%d, want 403 (btree)", oid, got.Method)
		}
		if got.Namespace != 11 {
			t.Errorf("OID %d: namespace=%d, want 11", oid, got.Namespace)
		}
	}
	// name_ops must store cstring keys (opckeytype = 2275).
	if got, ok := byOID[1986]; ok && got.KeyType != 2275 {
		t.Errorf("name_ops opckeytype: got %d, want 2275 (cstring)", got.KeyType)
	}
	// Canonical opfamily OIDs from pg_opfamily.dat. Step 3b set
	// three of these wrong; Step 3d corrected them — pin so a
	// future regression is caught immediately.
	wantFamily := map[uint32]struct {
		family uint32
		label  string
	}{
		1978: {1976, "int4_ops → INTEGER_BTREE_FAM_OID"},
		1979: {1976, "int2_ops → INTEGER_BTREE_FAM_OID"},
		3124: {1976, "int8_ops → INTEGER_BTREE_FAM_OID"},
		1981: {1989, "oid_ops → OID_BTREE_FAM_OID"},
		3126: {1994, "text_ops → TEXT_BTREE_FAM_OID"},
		1986: {1994, "name_ops → TEXT_BTREE_FAM_OID (shared)"},
		4217: {2095, "text_pattern_ops → TEXT_PATTERN_BTREE_FAM_OID"},
		4218: {2095, "varchar_pattern_ops → TEXT_PATTERN_BTREE_FAM_OID (shared)"},
		4219: {2097, "bpchar_pattern_ops → BPCHAR_PATTERN_BTREE_FAM_OID"},
		1985: {429, "char_ops → btree/char_ops"},
		1987: {1991, "oidvector_ops → btree/oidvector_ops"},
		1984: {424, "bool_ops → BOOL_BTREE_FAM_OID"},
	}
	for oid, want := range wantFamily {
		got, ok := byOID[oid]
		if !ok {
			continue
		}
		if got.Family != want.family {
			t.Errorf("OID %d (%s): opcfamily=%d, want %d", oid, want.label, got.Family, want.family)
		}
	}
}

// TestBootstrapPgOpclassTuplesWritesRowsToBase1And5 verifies the
// end-to-end bootstrap writes the heap to both template1 and
// postgres database directories, and the oid_ops row appears in
// the resulting page.
func TestBootstrapPgOpclassTuplesWritesRowsToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if _, err := bootstrapPgOpclassTuples(dir); err != nil {
		t.Fatalf("bootstrapPgOpclassTuples: %v", err)
	}
	needle := make([]byte, 64)
	copy(needle, []byte("oid_ops"))
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2616")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// 177 rows span 3 heap pages (each ~96-byte payload + overhead ~120 bytes → ~67/page).
		if got := len(raw); got == 0 || got%storage.BlockSize != 0 {
			t.Fatalf("%s: file size %d is not a multiple of BlockSize %d", path, got, storage.BlockSize)
		}
		if !bytes.Contains(raw, needle) {
			t.Fatalf("%s: oid_ops opcname not found in file", path)
		}
	}
}

// TestPgOpclassAttrsMatchesPg18FormPgOpclass pins the relcache
// init-file TupleDesc to the 9-column PG18 FormData_pg_opclass
// shape. Drift here would break PG's heap_deformtuple when
// SearchSysCache1(CLAOID, ...) reads opcdefault or opckeytype.
func TestPgOpclassAttrsMatchesPg18FormPgOpclass(t *testing.T) {
	attrs := pgOpclassAttrs()
	if got, want := len(attrs), 9; got != want {
		t.Fatalf("pgOpclassAttrs len: got %d, want %d", got, want)
	}
	type want struct {
		name    string
		typeOID uint32
		length  int16
	}
	expected := []want{
		{"oid", 26, 4},
		{"opcmethod", 26, 4},
		{"opcname", 19, 64},
		{"opcnamespace", 26, 4},
		{"opcowner", 26, 4},
		{"opcfamily", 26, 4},
		{"opcintype", 26, 4},
		{"opcdefault", 16, 1},
		{"opckeytype", 26, 4},
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
