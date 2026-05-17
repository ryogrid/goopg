package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrapPgIndexTuplesWritesEmptyPageToBase1And5 pins the
// M0106-0010 step 3f empty-page contract for pg_index. Vanilla
// PG's nailed-index initialisation calls
// `RelationOpenSmgr → mdopen → BasicOpenFile("base/<dboid>/2610")`
// during standby start-up; an absent file FATALs the backend. An
// initialised heap page with zero tuples is the minimum that lets
// the open succeed without faking later semantics — the per-index
// Form_pg_index rows land in a follow-up step.
func TestBootstrapPgIndexTuplesWritesEmptyPageToBase1And5(t *testing.T) {
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
		if got := len(raw); got != storage.BlockSize {
			t.Fatalf("%s: page size %d, want %d", path, got, storage.BlockSize)
		}
		// Page must be heap-initialised, not just zeroed — InitPage sets
		// pd_lower / pd_upper to BlockSize / BlockSize-MAXALIGN(SizeOfPageHeader)
		// so PG's PageInit ↔ PageIsNew check distinguishes a real heap page
		// from an unallocated extent.
		if isAllZero(raw) {
			t.Fatalf("%s: page is all zero — InitPage was skipped", path)
		}
	}
}

// TestPgIndexMinimalColDefsMatchesRelcacheAttrs ensures the empty-page
// schema agrees with the relcache init-file pgIndexAttrs() declaration
// — both list the same 4 columns in the same order so that, once the
// per-row encoder lands, PG's heap_deformtuple does not see a column
// count mismatch between the init file's TupleDesc and the heap page.
func TestPgIndexMinimalColDefsMatchesRelcacheAttrs(t *testing.T) {
	cols := pgIndexMinimalColDefs()
	attrs := pgIndexAttrs()
	if len(cols) != len(attrs) {
		t.Fatalf("col vs attr count: %d vs %d", len(cols), len(attrs))
	}
	for i, c := range cols {
		if c.Name != attrs[i].Name {
			t.Errorf("col[%d] name: %q vs %q", i, c.Name, attrs[i].Name)
		}
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
