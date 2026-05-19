package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgOperatorInitialEntriesCount pins that pgOperatorAllEntries returns
// the expected 799 rows parsed from postgres/src/include/catalog/pg_operator.dat.
func TestPgOperatorInitialEntriesCount(t *testing.T) {
	entries := pgOperatorInitialEntries()
	if got, want := len(entries), 799; got != want {
		t.Fatalf("pgOperatorInitialEntries len: got %d, want %d", got, want)
	}
}

// TestPgOperatorInitialEntriesNoDuplicateOIDs guards against duplicate OIDs
// in the generated seed data.
func TestPgOperatorInitialEntriesNoDuplicateOIDs(t *testing.T) {
	seen := make(map[uint32]string)
	for _, e := range pgOperatorInitialEntries() {
		if prev, dup := seen[e.OID]; dup {
			t.Fatalf("duplicate OID %d: first %q, second %q", e.OID, prev, e.Name)
		}
		seen[e.OID] = e.Name
	}
}

// TestPgOperatorCanonicalEntries spot-checks the most load-bearing operator
// rows.  Values sourced from postgres/src/include/catalog/pg_operator.dat.
func TestPgOperatorCanonicalEntries(t *testing.T) {
	byOID := make(map[uint32]operatorEntry)
	for _, e := range pgOperatorInitialEntries() {
		byOID[e.OID] = e
	}

	type want struct {
		name       string
		leftType   uint32 // oprleft
		rightType  uint32 // oprright
		resultType uint32 // oprresult — 16=bool
		canMerge   bool
		canHash    bool
		kind       byte
	}
	cases := map[uint32]want{
		// int4 equality (most-used operator in query planning)
		96: {"=", 23, 23, 16, true, true, 'b'},
		// int4 less-than (sort key)
		97: {"<", 23, 23, 16, false, false, 'b'},
		// text equality
		98: {"=", 25, 25, 16, true, true, 'b'},
		// OID equality
		607: {"=", 26, 26, 16, true, true, 'b'},
		// bool equality
		91: {"=", 16, 16, 16, true, true, 'b'},
	}
	for oid, w := range cases {
		e, ok := byOID[oid]
		if !ok {
			t.Fatalf("OID %d missing from pgOperatorInitialEntries", oid)
		}
		if e.Name != w.name {
			t.Errorf("OID %d: name=%q, want %q", oid, e.Name, w.name)
		}
		if e.LeftType != w.leftType {
			t.Errorf("OID %d (%s): oprleft=%d, want %d", oid, e.Name, e.LeftType, w.leftType)
		}
		if e.RightType != w.rightType {
			t.Errorf("OID %d (%s): oprright=%d, want %d", oid, e.Name, e.RightType, w.rightType)
		}
		if e.ResultType != w.resultType {
			t.Errorf("OID %d (%s): oprresult=%d, want %d", oid, e.Name, e.ResultType, w.resultType)
		}
		if e.CanMerge != w.canMerge {
			t.Errorf("OID %d (%s): oprcanmerge=%v, want %v", oid, e.Name, e.CanMerge, w.canMerge)
		}
		if e.CanHash != w.canHash {
			t.Errorf("OID %d (%s): oprcanhash=%v, want %v", oid, e.Name, e.CanHash, w.canHash)
		}
		if e.Kind != w.kind {
			t.Errorf("OID %d (%s): oprkind=%q, want %q", oid, e.Name, e.Kind, w.kind)
		}
		// Every operator must have a namespace (11=pg_catalog) and owner (10=bootstrap).
		if e.Namespace != 11 {
			t.Errorf("OID %d (%s): oprnamespace=%d, want 11", oid, e.Name, e.Namespace)
		}
		if e.Owner != 10 {
			t.Errorf("OID %d (%s): oprowner=%d, want 10", oid, e.Name, e.Owner)
		}
	}
}

// TestPgOperatorCommutatorAndNegatorResolved verifies that the cross-reference
// fields (Commutator, Negator) are non-zero for well-known symmetric operators.
// Zero would indicate a resolver failure in the code generator.
func TestPgOperatorCommutatorAndNegatorResolved(t *testing.T) {
	byOID := make(map[uint32]operatorEntry)
	for _, e := range pgOperatorInitialEntries() {
		byOID[e.OID] = e
	}
	// OID 96 (int4 =): commutator is itself (96), negator is 518 (<>)
	e := byOID[96]
	if e.Commutator != 96 {
		t.Errorf("OID 96 Commutator=%d, want 96 (self-commuting)", e.Commutator)
	}
	if e.Negator == 0 {
		t.Errorf("OID 96 Negator=0, want non-zero (<> operator)")
	}
	// OID 97 (int4 <): commutator is 521 (>), negator is 525 (>=)
	e = byOID[97]
	if e.Commutator == 0 {
		t.Errorf("OID 97 (<) Commutator=0, want non-zero (> operator)")
	}
	if e.Negator == 0 {
		t.Errorf("OID 97 (<) Negator=0, want non-zero (>= operator)")
	}
}

// TestBootstrapPgOperatorTuplesWritesHeapFiles exercises the full
// bootstrap path: rows must land in both base/1/2617 and base/5/2617
// as a multi-page heap file whose size is a non-zero multiple of BlockSize.
func TestBootstrapPgOperatorTuplesWritesHeapFiles(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	tids, err := bootstrapPgOperatorTuples(dir)
	if err != nil {
		t.Fatalf("bootstrapPgOperatorTuples: %v", err)
	}
	if got, want := len(tids), 799; got != want {
		t.Fatalf("TID map len: got %d, want %d", got, want)
	}
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2617")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if got := len(raw); got == 0 || got%storage.BlockSize != 0 {
			t.Fatalf("%s: file size %d, want non-zero multiple of %d", path, got, storage.BlockSize)
		}
	}
}
