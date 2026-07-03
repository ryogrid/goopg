package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestRoleConfigRecoveryReplaysSet confirms the M0119-0004-ACLHEAP (ALTER
// ROLE ... SET follow-up) hook: an ALTER ROLE ... SET WAL record written by
// a pre-crash run is replayed into catalog.InMemory.roleSettings on the
// post-crash Open path, so pg_db_role_setting (and hence pg_dump --create)
// reports the override after restart even though the goopg process never
// wrote a JSON snapshot.
func TestRoleConfigRecoveryReplaysSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const roleOid = 16385
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterRoleSetConfig(roleOid, 0, "work_mem", "64MB")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append alter-role-set-config: %v", werr)
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
	got := cat.RoleConfigEntries(roleOid, 0)
	if len(got) != 1 || got[0] != "work_mem=64MB" {
		t.Errorf("after WAL replay, RoleConfigEntries = %v, want [\"work_mem=64MB\"]", got)
	}
}

// TestRoleConfigRecoveryReplaysSetThenReset confirms SET followed by RESET
// of the SAME name replays to no override (last-record-wins), while a RESET
// of a DIFFERENT name leaves the first SET intact. Also exercises that the
// cluster-wide (dbOid=0) and IN-DATABASE (dbOid != 0) scopes for the same
// role replay independently.
func TestRoleConfigRecoveryReplaysSetThenReset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const roleOid = 16385
	appends := [][]byte{
		wal.EncodeAlterRoleSetConfig(roleOid, 0, "work_mem", "64MB"),
		wal.EncodeAlterRoleSetConfig(roleOid, 0, "search_path", "public"),
		wal.EncodeAlterRoleSetConfig(roleOid, catalog.FirstUserOID, "work_mem", "1MB"),
		wal.EncodeAlterRoleResetConfig(roleOid, 0, "work_mem"),
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
	if got := cat.RoleConfigEntries(roleOid, 0); len(got) != 1 || got[0] != "search_path=public" {
		t.Errorf("after SET+SET+RESET replay, RoleConfigEntries(role, 0) = %v, want [\"search_path=public\"]", got)
	}
	if got := cat.RoleConfigEntries(roleOid, catalog.FirstUserOID); len(got) != 1 || got[0] != "work_mem=1MB" {
		t.Errorf("IN-DATABASE scope should be untouched by a cluster-wide RESET: %v, want [\"work_mem=1MB\"]", got)
	}
}

// TestRoleConfigRecoveryReplaysResetAll confirms a RESET ALL record clears
// every prior SET for that (role, database) scope.
func TestRoleConfigRecoveryReplaysResetAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const roleOid = 16385
	appends := [][]byte{
		wal.EncodeAlterRoleSetConfig(roleOid, 0, "work_mem", "64MB"),
		wal.EncodeAlterRoleSetConfig(roleOid, 0, "search_path", "public"),
		wal.EncodeAlterRoleResetAllConfig(roleOid, 0),
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
	if got := cat.RoleConfigEntries(roleOid, 0); len(got) != 0 {
		t.Errorf("after RESET ALL replay, RoleConfigEntries = %v, want empty", got)
	}
}

// TestReplayRoleConfigRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (e.g.
// brand new initdb).
func TestReplayRoleConfigRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayRoleConfigRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if got := cat.RoleConfigEntries(16385, 0); len(got) != 0 {
		t.Errorf("no-op replay should leave RoleConfigEntries empty, got %v", got)
	}
}
