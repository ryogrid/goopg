package initdb

// Role/auth restart persistence tests (root-0021): the pg_authid heap file is
// the durable BASE (SyncPgAuthidFile / LoadRolesFromAuthidHeap) and the role
// WAL records the crash TAIL (replayRoleDDLRecords), mirroring PostgreSQL's
// pg_authid-plus-WAL split.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/wal"
)

const testVerifier = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0$c3RvcmVka2V5c3RvcmVk:c2VydmVya2V5c2VydmVy"

// TestPgAuthidSyncLoadRoundTrip: a role registered + synced to the pg_authid
// heap file is restored (name, OID, attributes, verifier) by a fresh Open,
// with no role WAL records involved — proving the heap file alone is a
// sufficient durable base (survives WAL segment pruning).
func TestPgAuthidSyncLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	cat1 := rt1.Catalog.(*catalog.InMemory)
	cat1.RegisterRole("wpuser")
	oid, _ := cat1.RoleOID("wpuser")
	cat1.SetRoleAttrs("wpuser", catalog.RoleAttrs{
		CanLogin: true, Superuser: false, CredType: 3, Secret: testVerifier,
		CreateDB: true, CreateRole: true, Replication: true, BypassRLS: true, ConnLimit: 42,
		ValidUntil: "2030-01-01 00:00:00+00",
	})
	if err := executor.SyncPgAuthidFile(dir, cat1); err != nil {
		_ = rt1.Close()
		t.Fatalf("SyncPgAuthidFile: %v", err)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()
	cat2 := rt2.Catalog.(*catalog.InMemory)

	if !cat2.RoleExists("wpuser") {
		t.Fatal("wpuser not restored from pg_authid heap")
	}
	if gotOID, ok := cat2.RoleOID("wpuser"); !ok || gotOID != oid {
		t.Errorf("wpuser OID = %d (ok=%v), want %d (OID not preserved)", gotOID, ok, oid)
	}
	attrs, ok := cat2.LookupRoleAttrs("wpuser")
	if !ok {
		t.Fatal("wpuser attrs not restored")
	}
	if !attrs.CanLogin || attrs.Superuser || attrs.CredType != 3 || attrs.Secret != testVerifier {
		t.Errorf("wpuser attrs = %+v, want CanLogin=true Super=false CredType=3 verifier preserved", attrs)
	}
	// CreateDB/CreateRole/Replication/BypassRLS/ConnLimit round-trip through
	// the pg_authid heap file itself (DU-002 slice 439 follow-up); ValidUntil
	// now round-trips too (DU-002 slice 439 triage item 1 follow-up —
	// buildAuthidUserRow/ReadPgAuthidRows encode/decode a real timestamptz).
	if !attrs.CreateDB || !attrs.CreateRole || !attrs.Replication || !attrs.BypassRLS || attrs.ConnLimit != 42 {
		t.Errorf("wpuser attrs = %+v, want CreateDB/CreateRole/Replication/BypassRLS=true ConnLimit=42", attrs)
	}
	if attrs.ValidUntil != "2030-01-01 00:00:00+00" {
		t.Errorf("wpuser attrs.ValidUntil = %q, want %q", attrs.ValidUntil, "2030-01-01 00:00:00+00")
	}
	// Predefined pg_* roles resolve (RoleExists/RoleOID — M0119-0004-ACLHEAP's
	// dedicated predefinedRoles map, populated at InMemory construction, not
	// by this heap load) but must never become a `roles` registry entry: the
	// pg_authid heap loader still skips them (pg_authid_load.go's "predefined
	// — heap-only, not a registry role" branch), so they stay absent from
	// AllRoleStates and pg_authid heap sync's own dedicated predefined-role
	// writer never sees a duplicate row.
	if !cat2.RoleExists("pg_monitor") {
		t.Error("predefined role pg_monitor must resolve via RoleExists")
	}
	for _, s := range cat2.AllRoleStates() {
		if s.Name == "pg_monitor" {
			t.Error("predefined role pg_monitor leaked into the AllRoleStates registry")
		}
	}
}

// TestPgAuthidBootstrapVerifierRestored: the bootstrap superuser's --pwfile
// verifier (written to pg_authid at initdb) is surfaced through the attrs
// sidecar after Open, so cmd/goopg can seed the auth UserStore and `postgres`
// keeps authenticating across restarts without a hand-written pg_auth file.
func TestPgAuthidBootstrapVerifierRestored(t *testing.T) {
	tmp := t.TempDir()
	pwFile := filepath.Join(tmp, "pwfile")
	if err := os.WriteFile(pwFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatalf("write pwfile: %v", err)
	}
	dir := filepath.Join(tmp, "data")
	if err := Init(Options{DataDir: dir, PwFile: pwFile}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rt.Close()
	cat := rt.Catalog.(*catalog.InMemory)
	attrs, ok := cat.LookupRoleAttrs("postgres")
	if !ok {
		t.Fatal("postgres attrs not loaded from pg_authid heap")
	}
	if attrs.CredType != 3 || attrs.Secret == "" {
		t.Errorf("postgres credential = type %d secret %q, want a SCRAM verifier", attrs.CredType, attrs.Secret)
	}
}

// TestRoleDDLRecoveryWALTail: a role WAL record appended AFTER the last heap
// sync (crash window) is replayed on top of the heap base; a later drop
// record wins over an earlier state record.
func TestRoleDDLRecoveryWALTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// Simulate the crash window: records land in WAL, heap file never synced.
	appendRec := func(payload []byte) {
		t.Helper()
		if _, _, werr := rt1.WAL.Append(payload); werr != nil {
			_ = rt1.Close()
			t.Fatalf("WAL.Append: %v", werr)
		}
	}
	appendRec(wal.EncodeRoleState(wal.RoleStatePayload{
		Name: "tailrole", OID: 17001, CanLogin: true, CredType: 3, Secret: testVerifier,
	}))
	appendRec(wal.EncodeRoleState(wal.RoleStatePayload{
		Name: "droppedrole", OID: 17002, CanLogin: true,
	}))
	appendRec(wal.EncodeDropRole("droppedrole"))
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

	if !cat.RoleExists("tailrole") {
		t.Fatal("tailrole not replayed from the WAL tail")
	}
	if gotOID, ok := cat.RoleOID("tailrole"); !ok || gotOID != 17001 {
		t.Errorf("tailrole OID = %d (ok=%v), want 17001", gotOID, ok)
	}
	if attrs, ok := cat.LookupRoleAttrs("tailrole"); !ok || attrs.Secret != testVerifier {
		t.Errorf("tailrole attrs = %+v (ok=%v), want verifier preserved", attrs, ok)
	}
	if cat.RoleExists("droppedrole") {
		t.Error("droppedrole resurrected — drop record did not win")
	}
}
