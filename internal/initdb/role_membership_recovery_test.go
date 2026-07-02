package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestRoleMembershipRecoveryReplaysGrant confirms the M0119-0004-ACLHEAP
// GRANT ROLE hook: a GRANT <role> TO <member> WAL record written by a
// pre-crash run is replayed into catalog.InMemory.roleMembers on the
// post-crash Open path, so pg_auth_members reports the membership after
// restart even though the goopg process never wrote a JSON snapshot.
func TestRoleMembershipRecoveryReplaysGrant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const roleOid, memberOid, grantorOid = 16385, 16386, 10
	admin := true
	if _, _, werr := rt1.WAL.Append(wal.EncodeGrantRoleMembership(roleOid, memberOid, grantorOid, &admin, nil, nil)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append grant-role-membership: %v", werr)
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
	got := cat.RoleMembershipEntries()
	if len(got) != 1 {
		t.Fatalf("after WAL replay, RoleMembershipEntries = %+v, want 1 entry", got)
	}
	if got[0].RoleOID != roleOid || got[0].MemberOID != memberOid ||
		got[0].GrantorOID != grantorOid || !got[0].AdminOption {
		t.Errorf("replayed entry = %+v, want role=%d member=%d grantor=%d admin=true",
			got[0], roleOid, memberOid, grantorOid)
	}
}

// TestRoleMembershipRecoveryReplaysGrantThenRevoke confirms a REVOKE record
// following a GRANT removes the row, and a subsequent REVOKE ADMIN OPTION
// FOR-only record (adminOptionOnly=true) merely clears the flag rather than
// deleting a still-live row.
func TestRoleMembershipRecoveryReplaysGrantThenRevoke(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const roleOid1, memberOid1 = 16385, 16386
	const roleOid2, memberOid2 = 16387, 16388
	adminFalse, adminTrue := false, true
	appends := [][]byte{
		wal.EncodeGrantRoleMembership(roleOid1, memberOid1, 10, &adminFalse, nil, nil),
		wal.EncodeGrantRoleMembership(roleOid2, memberOid2, 10, &adminTrue, nil, nil),
		wal.EncodeRevokeRoleMembership(roleOid1, memberOid1, false),
		wal.EncodeRevokeRoleMembership(roleOid2, memberOid2, true),
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
	got := cat.RoleMembershipEntries()
	if len(got) != 1 {
		t.Fatalf("after grant+grant+full-revoke+admin-only-revoke replay, entries = %+v, want 1", got)
	}
	if got[0].RoleOID != roleOid2 || got[0].MemberOID != memberOid2 || got[0].AdminOption {
		t.Errorf("surviving entry = %+v, want role=%d member=%d admin=false", got[0], roleOid2, memberOid2)
	}
}

// TestReplayRoleMembershipRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (e.g.
// brand new initdb).
func TestReplayRoleMembershipRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayRoleMembershipRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if got := cat.RoleMembershipEntries(); len(got) != 0 {
		t.Errorf("no-op replay should leave RoleMembershipEntries empty, got %+v", got)
	}
}
