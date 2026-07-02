package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// eventTriggerCatalog is a small test helper: the Runtime.Catalog field is
// the catalog.Catalog interface, but the event trigger registry lives on
// the concrete *catalog.InMemory implementation (mirrors how PubSub-related
// tests use rt.PubSub directly).
func eventTriggerCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestEventTriggerDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004, loop #70 ledger resume point): a
// CREATE EVENT TRIGGER WAL record written by a pre-crash run is replayed
// into the catalog's eventTriggers registry on the post-crash Open path, so
// pg_event_trigger (pg_dump's getEventTriggers) round-trips after restart
// even though the goopg process never wrote a JSON snapshot. The
// OID/owner/funcOID/tags all survive the restart.
func TestEventTriggerDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40700)
	const wantOwnerOID = uint32(16401)
	const wantFuncOID = uint32(16450)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateEventTrigger("mytrig", "ddl_command_start", []string{"CREATE TABLE"}, wantOID, wantOwnerOID, wantFuncOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-event-trigger: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the event trigger without any
	// JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	et, ok := eventTriggerCatalog(t, rt2).LookupEventTrigger("mytrig")
	if !ok {
		t.Fatalf("after WAL replay, event trigger \"mytrig\" not found")
	}
	if et.OID != wantOID || et.Owner != wantOwnerOID || et.FuncOID != wantFuncOID || et.Event != "ddl_command_start" || et.Enabled != "O" {
		t.Errorf("after WAL replay, event trigger = %+v, want OID=%d Owner=%d FuncOID=%d Event=ddl_command_start Enabled=O", et, wantOID, wantOwnerOID, wantFuncOID)
	}
	if len(et.Tags) != 1 || et.Tags[0] != "CREATE TABLE" {
		t.Errorf("after WAL replay, event trigger.Tags = %v, want [CREATE TABLE]", et.Tags)
	}
}

// TestEventTriggerDDLRecoveryReplaysDropAfterCreate confirms a CREATE
// followed by a DROP cancels out — the registry agrees with the most
// recent durable record, not the first one.
func TestEventTriggerDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateEventTrigger("mytrig", "sql_drop", nil, 40710, 10, 16451)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropEventTrigger("mytrig")); werr != nil {
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

	if et, ok := eventTriggerCatalog(t, rt2).LookupEventTrigger("mytrig"); ok {
		t.Errorf("after CREATE + DROP replay, event trigger \"mytrig\" = %+v, want not found", et)
	}
}

// TestEventTriggerDDLRecoveryReplaysAlterAfterCreate confirms
// DISABLE/RENAME/OWNER TO all replay in order on top of the CREATE record.
func TestEventTriggerDDLRecoveryReplaysAlterAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40720)
	const wantOwnerOID = uint32(16402)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateEventTrigger("origtrig", "ddl_command_end", nil, wantOID, 10, 16452)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterEventTriggerEnabled("origtrig", 'D')); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append enabled: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterEventTriggerRename("origtrig", "renamedtrig")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterEventTriggerOwner("renamedtrig", wantOwnerOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append owner: %v", werr)
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

	im := eventTriggerCatalog(t, rt2)
	if et, ok := im.LookupEventTrigger("origtrig"); ok {
		t.Errorf("after RENAME replay, old name \"origtrig\" still present: %+v", et)
	}
	et, ok := im.LookupEventTrigger("renamedtrig")
	if !ok {
		t.Fatalf("after CREATE+DISABLE+RENAME+OWNER replay, \"renamedtrig\" not found")
	}
	if et.OID != wantOID {
		t.Errorf("after replay, OID = %d, want %d (rename/owner change must not disturb OID)", et.OID, wantOID)
	}
	if et.Enabled != "D" {
		t.Errorf("after replay, Enabled = %q, want \"D\"", et.Enabled)
	}
	if et.Owner != wantOwnerOID {
		t.Errorf("after replay, Owner = %d, want %d", et.Owner, wantOwnerOID)
	}
}

// TestReplayEventTriggerDDLRecordsHandlesMissingWalDir verifies the
// recovery hook is idempotent when invoked against a missing pg_wal
// directory (brand new initdb).
func TestReplayEventTriggerDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayEventTriggerDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListEventTriggers()) != 0 {
		t.Errorf("no-op replay should not register anything, got %+v", cat.ListEventTriggers())
	}
}

// TestReplayEventTriggerDDLRecordsHandlesNilCatalog verifies the recovery
// hook tolerates a nil catalog (mirrors the nil-registry guard the PubSub
// DDL recovery driver has for embedded test setups).
func TestReplayEventTriggerDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayEventTriggerDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
