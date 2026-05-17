package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgIndexTuplesWritesHeapPagesToBase1And5 pins the
// M0106-0010 step 3g contract: bootstrap writes BOTH per-database
// pg_index files (template1=1 + postgres=5) as a heap-initialised
// page (or pages) with the full nailed-index row set. Vanilla PG's
// nailed-index initialisation calls
// `RelationOpenSmgr → mdopen → BasicOpenFile("base/<dboid>/2610")`
// during standby start-up, then loads each critical index by OID
// via SearchSysCache1(INDEXRELID, ...). An absent file or empty
// page FATALs the backend with "cache lookup failed for index <oid>".
func TestBootstrapPgIndexTuplesWritesHeapPagesToBase1And5(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"base/1", "base/5"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	if err := bootstrapPgIndexTuples(dir); err != nil {
		t.Fatalf("bootstrapPgIndexTuples: %v", err)
	}
	for _, sub := range []string{"base/1", "base/5"} {
		path := filepath.Join(dir, sub, "2610")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if got := len(raw); got == 0 || got%storage.BlockSize != 0 {
			t.Fatalf("%s: page size %d not a positive multiple of %d", path, got, storage.BlockSize)
		}
		// Page must be heap-initialised, not just zeroed — InitPage sets
		// pd_lower / pd_upper to non-zero, and PageAddHeapTuple writes
		// at least one item pointer + tuple body per row.
		if isAllZero(raw) {
			t.Fatalf("%s: page is all zero — InitPage was skipped", path)
		}
	}
}

// TestPgIndexColDefsMatchesRelcacheAttrs ensures the heap-tuple schema
// agrees with the relcache init-file pgIndexAttrs() declaration. PG's
// heap_deformtuple casts the raw heap tuple as Form_pg_index; the
// init-file TupleDesc must declare the same column count, names, and
// order as the on-disk row layout, otherwise indkey / indclass land
// at the wrong attnum and RelationInitIndexAccessInfo reads garbage.
// M0106-0010 step 3g expands both sides from 4 to the full 21 columns
// of upstream FormData_pg_index.
func TestPgIndexColDefsMatchesRelcacheAttrs(t *testing.T) {
	cols := pgIndexColDefs()
	attrs := pgIndexAttrs()
	if len(cols) != len(attrs) {
		t.Fatalf("col vs attr count: %d vs %d", len(cols), len(attrs))
	}
	for i, c := range cols {
		if c.Name != attrs[i].Name {
			t.Errorf("col[%d] name: %q vs %q", i, c.Name, attrs[i].Name)
		}
	}
	if got, want := len(cols), 21; got != want {
		t.Fatalf("pg_index column count: %d, want %d", got, want)
	}
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
