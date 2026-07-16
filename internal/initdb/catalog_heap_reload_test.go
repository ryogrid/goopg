package initdb

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

// TestCatalogRowLive pins the B0.1 unified reload visibility filter
// (doc 02a §2.3) — the exact rules the pre-B0.1 pg_class/pg_attribute
// scans implemented inline:
//
//	xmin Invalid → dead; any non-zero xmax → dead; aborted xmin → dead
//	(every layout); out-of-range/unknown xmin passes (basebackup
//	pass-through) UNLESS requireCommittedXmin (legacy-layout pg_class rows).
func TestCatalogRowLive(t *testing.T) {
	dir := t.TempDir()
	clog, err := mvcc.OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatal(err)
	}
	const committedXid = storage.TransactionID(10)
	const abortedXid = storage.TransactionID(11)
	const unknownXid = storage.TransactionID(4000) // never stamped — out-of-range analog
	if err := clog.SetCommitted(committedXid); err != nil {
		t.Fatal(err)
	}
	if err := clog.SetAborted(abortedXid); err != nil {
		t.Fatal(err)
	}

	ht := func(xmin, xmax storage.TransactionID) storage.HeapTuple {
		var h storage.HeapTuple
		h.Header.Xmin = xmin
		h.Header.Xmax = xmax
		return h
	}

	cases := []struct {
		name             string
		ht               storage.HeapTuple
		requireCommitted bool
		want             bool
	}{
		{"invalid xmin", ht(storage.InvalidTransactionID, 0), false, false},
		{"deleted (nonzero xmax)", ht(committedXid, committedXid), false, false},
		{"deleted even by aborted xmax (B0.1 rule; upgraded in B0.2)", ht(committedXid, abortedXid), false, false},
		{"committed xmin", ht(committedXid, 0), false, true},
		{"aborted xmin", ht(abortedXid, 0), false, false},
		{"aborted xmin, lax mode still dead", ht(abortedXid, 0), false, false},
		{"unknown xmin passes (basebackup pass-through)", ht(unknownXid, 0), false, true},
		{"unknown xmin dead when committed required (legacy layout)", ht(unknownXid, 0), true, false},
		{"committed xmin ok when committed required", ht(committedXid, 0), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := catalogRowLive(clog, c.ht, c.requireCommitted); got != c.want {
				t.Fatalf("catalogRowLive = %v, want %v", got, c.want)
			}
		})
	}

	// nil clog (no recovery context): everything with valid xmin + zero xmax lives.
	if !catalogRowLive(nil, ht(unknownXid, 0), true) {
		t.Fatal("nil clog must pass valid tuples")
	}
}

// TestRunCatalogReloadsOrderAndFatality pins the descriptor registry: Slot
// order execution, warn-and-continue for non-Fatal errors, immediate abort
// for Fatal ones.
func TestRunCatalogReloadsOrderAndFatality(t *testing.T) {
	var ran []string
	mk := func(name string, slot int, fatal bool, fail error) catalogReloadDesc {
		return catalogReloadDesc{
			Name: name, Slot: slot, Fatal: fatal,
			Reload: func(*storage.Manager, *catalog.InMemory, *mvcc.CLog, uint32, uint32) error {
				ran = append(ran, name)
				return fail
			},
		}
	}

	// Registered out of order; Slot must win. The non-Fatal failure warns
	// and continues; the run completes.
	var warned []string
	descs := []catalogReloadDesc{
		mk("late", 30, true, nil),
		mk("early", 10, true, nil),
		mk("mid-warns", 20, false, errNonFatalReload),
	}
	if err := runCatalogReloads(nil, nil, nil, 0, 0, descs, func(name string, _ error) {
		warned = append(warned, name)
	}); err != nil {
		t.Fatalf("non-fatal failure must not abort: %v", err)
	}
	if want := []string{"early", "mid-warns", "late"}; !equalStrings(ran, want) {
		t.Fatalf("run order = %v, want %v", ran, want)
	}
	if !equalStrings(warned, []string{"mid-warns"}) {
		t.Fatalf("warned = %v, want [mid-warns]", warned)
	}

	// A Fatal failure aborts before later slots run.
	ran = nil
	descs = []catalogReloadDesc{
		mk("boom", 10, true, errNonFatalReload),
		mk("never", 20, true, nil),
	}
	if err := runCatalogReloads(nil, nil, nil, 0, 0, descs, nil); err == nil {
		t.Fatal("fatal failure must abort")
	}
	if !equalStrings(ran, []string{"boom"}) {
		t.Fatalf("run order = %v, want [boom]", ran)
	}
}

var errNonFatalReload = fmt.Errorf("synthetic reload failure")

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
