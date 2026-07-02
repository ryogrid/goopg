package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestDatabaseConfigRecoveryReplaysSet confirms the M0119-0004-ACLHEAP
// (ALTER DATABASE ... SET follow-up) hook: an ALTER DATABASE ... SET WAL
// record written by a pre-crash run is replayed into
// catalog.InMemory.dbRoleSettings on the post-crash Open path, so
// pg_db_role_setting (and hence pg_dump --create) reports the override
// after restart even though the goopg process never wrote a JSON snapshot.
func TestDatabaseConfigRecoveryReplaysSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterDatabaseSetConfig(catalog.FirstUserOID, "work_mem", "64MB")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append alter-database-set-config: %v", werr)
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

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	got := cat.DatabaseConfigEntries(catalog.FirstUserOID)
	if len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("after WAL replay, DatabaseConfigEntries = %v, want [\"work_mem=64MB\"]", got)
	}
}

// TestDatabaseConfigRecoveryReplaysSetThenReset confirms SET followed by
// RESET of the SAME name replays to no override (last-record-wins), while a
// RESET of a DIFFERENT name leaves the first SET intact.
func TestDatabaseConfigRecoveryReplaysSetThenReset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	appends := []struct{ payload []byte }{
		{wal.EncodeAlterDatabaseSetConfig(catalog.FirstUserOID, "work_mem", "64MB")},
		{wal.EncodeAlterDatabaseSetConfig(catalog.FirstUserOID, "search_path", "public")},
		{wal.EncodeAlterDatabaseResetConfig(catalog.FirstUserOID, "work_mem")},
	}
	for _, a := range appends {
		if _, _, werr := rt1.WAL.Append(a.payload); werr != nil {
			_ = rt1.Close()
			t.Fatalf("WAL.Append: %v", werr)
		}
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

	cat := rt2.Catalog.(*catalog.InMemory)
	got := cat.DatabaseConfigEntries(catalog.FirstUserOID)
	if len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("after SET+SET+RESET replay, DatabaseConfigEntries = %v, want [\"search_path=public\"]", got)
	}
}

// TestDatabaseConfigRecoveryReplaysResetAll confirms a RESET ALL record
// clears every prior SET for that database.
func TestDatabaseConfigRecoveryReplaysResetAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	appends := [][]byte{
		wal.EncodeAlterDatabaseSetConfig(catalog.FirstUserOID, "work_mem", "64MB"),
		wal.EncodeAlterDatabaseSetConfig(catalog.FirstUserOID, "search_path", "public"),
		wal.EncodeAlterDatabaseResetAllConfig(catalog.FirstUserOID),
	}
	for _, payload := range appends {
		if _, _, werr := rt1.WAL.Append(payload); werr != nil {
			_ = rt1.Close()
			t.Fatalf("WAL.Append: %v", werr)
		}
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

	cat := rt2.Catalog.(*catalog.InMemory)
	if got := cat.DatabaseConfigEntries(catalog.FirstUserOID); len(got) != 0 {
		t.Errorf("after RESET ALL replay, DatabaseConfigEntries = %v, want empty", got)
	}
}

// TestReplayDatabaseConfigRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (e.g.
// brand new initdb).
func TestReplayDatabaseConfigRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayDatabaseConfigRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if got := cat.DatabaseConfigEntries(catalog.FirstUserOID); len(got) != 0 {
		t.Errorf("no-op replay should leave DatabaseConfigEntries empty, got %v", got)
	}
}
