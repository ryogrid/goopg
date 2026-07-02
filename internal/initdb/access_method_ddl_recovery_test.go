package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// accessMethodCatalog is a small test helper: the Runtime.Catalog field is
// the catalog.Catalog interface, but the access method registry lives on
// the concrete *catalog.InMemory implementation (mirrors eventTriggerCatalog).
func accessMethodCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestAccessMethodDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004, DU-002 slice 426 ledger resume
// point): a CREATE ACCESS METHOD WAL record written by a pre-crash run is
// replayed into the catalog's accessMethods registry on the post-crash Open
// path, so pg_am (pg_dump's getAccessMethods) round-trips after restart even
// though the goopg process never wrote a JSON snapshot. The OID/type/handler
// all survive the restart.
func TestAccessMethodDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40800)
	const wantHandlerOID = uint32(16460)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAccessMethod("myam", "i", wantOID, wantHandlerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-access-method: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the access method without any
	// JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	var found *catalog.AccessMethod
	for _, am := range accessMethodCatalog(t, rt2).ListAccessMethods() {
		if am.Name == "myam" {
			found = am
		}
	}
	if found == nil {
		t.Fatalf("after WAL replay, access method \"myam\" not found")
	}
	if found.OID != wantOID || found.HandlerOID != wantHandlerOID || found.AMType != "i" {
		t.Errorf("after WAL replay, access method = %+v, want OID=%d HandlerOID=%d AMType=i", found, wantOID, wantHandlerOID)
	}
}

// TestAccessMethodDDLRecoveryReplaysDropAfterCreate confirms a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestAccessMethodDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateAccessMethod("myam", "t", 40810, 16461)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropAccessMethod("myam")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	for _, am := range accessMethodCatalog(t, rt2).ListAccessMethods() {
		if am.Name == "myam" {
			t.Errorf("after CREATE + DROP replay, access method \"myam\" = %+v, want not found", am)
		}
	}
}

// TestReplayAccessMethodDDLRecordsHandlesMissingWalDir verifies the
// recovery hook is idempotent when invoked against a missing pg_wal
// directory (brand new initdb).
func TestReplayAccessMethodDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayAccessMethodDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListAccessMethods()) != 0 {
		t.Errorf("no-op replay should not register anything, got %+v", cat.ListAccessMethods())
	}
}

// TestReplayAccessMethodDDLRecordsHandlesNilCatalog verifies the recovery
// hook tolerates a nil catalog (mirrors the nil-registry guard the event
// trigger DDL recovery driver has for embedded test setups).
func TestReplayAccessMethodDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayAccessMethodDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
