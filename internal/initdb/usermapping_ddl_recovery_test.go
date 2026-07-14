package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestUserMappingDDLRecoveryReplaysCreate confirms the M0122-0007
// user-mapping registry restart-durability fix: a CREATE USER MAPPING WAL
// record written by a pre-crash run is replayed into the catalog's
// user-mapping registry on the post-crash Open path, so `pg_user_mappings`
// lookups resolve after restart even though the goopg process never wrote a
// JSON snapshot. The OID is preserved across the restart.
func TestUserMappingDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40781)
	options := []string{"user=remote", "password=secret"}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateUserMapping("someuser", "myserver", options, wantOID, catalog.DefaultDBOid)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-user-mapping: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the mapping in the registry
	// without any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	list := cat.ListUserMappings()
	if len(list) != 1 {
		t.Fatalf("after WAL replay, ListUserMappings = %+v, want exactly 1 entry", list)
	}
	got := list[0]
	if got.OID != wantOID || got.UmUser != "someuser" || got.SrvName != "myserver" {
		t.Errorf("after WAL replay, mapping = %+v, want oid=%d user=someuser server=myserver", got, wantOID)
	}
}

// TestUserMappingDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestUserMappingDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateUserMapping("scratch_user", "scratch_srv", nil, 40782, catalog.DefaultDBOid)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropUserMapping("scratch_user", "scratch_srv", catalog.DefaultDBOid)); werr != nil {
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

	cat := rt2.Catalog.(*catalog.InMemory)
	if list := cat.ListUserMappings(); len(list) != 0 {
		t.Errorf("after CREATE + DROP replay, ListUserMappings = %+v, want empty", list)
	}
}

// TestUserMappingDDLRecoveryPreservesDistinctDBOidAfterRestart confirms the
// M0122-0007 4e follow-up 37 fix: two same-named (user, server) CREATE USER
// MAPPINGs under distinct dbOids must both survive a restart as independent
// registry entries (the pre-fix bare-name key silently collapsed them onto
// one OID, last-writer-wins).
func TestUserMappingDDLRecoveryPreservesDistinctDBOidAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const dbA, dbB = uint32(16401), uint32(16402)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateUserMapping("shared_user", "shared_srv", nil, 40790, dbA)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create (dbA): %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateUserMapping("shared_user", "shared_srv", nil, 40791, dbB)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create (dbB): %v", werr)
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
	listA := cat.ListUserMappings(dbA)
	if len(listA) != 1 || listA[0].OID != 40790 {
		t.Errorf("after WAL replay, dbA's ListUserMappings = %+v, want exactly 1 entry with OID 40790", listA)
	}
	listB := cat.ListUserMappings(dbB)
	if len(listB) != 1 || listB[0].OID != 40791 {
		t.Errorf("after WAL replay, dbB's ListUserMappings = %+v, want exactly 1 entry with OID 40791 (must survive dbA's same-named mapping)", listB)
	}
}

// TestReplayUserMappingDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayUserMappingDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayUserMappingDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if list := cat.ListUserMappings(); len(list) != 0 {
		t.Errorf("no-op replay unexpectedly registered a mapping: %+v", list)
	}
}
