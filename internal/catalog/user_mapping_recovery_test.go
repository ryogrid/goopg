package catalog

import "testing"

// TestRegisterUserMappingDuringRecoveryPreservesOID pins the M0122-0007
// user-mapping registry restart-durability follow-up: replaying a CREATE
// USER MAPPING WAL record must re-populate the registry with the EXACT OID
// the pre-restart server assigned, not a freshly minted one.
func TestRegisterUserMappingDuringRecoveryPreservesOID(t *testing.T) {
	c := NewInMemory()
	const wantOID = uint32(40779)

	c.RegisterUserMappingDuringRecovery("someuser", "recovered_srv", []string{"user=remote"}, wantOID)

	list := c.ListUserMappings()
	if len(list) != 1 {
		t.Fatalf("ListUserMappings = %+v, want exactly 1 entry", list)
	}
	got := list[0]
	if got.OID != wantOID || got.UmUser != "someuser" || got.SrvName != "recovered_srv" ||
		len(got.Options) != 1 || got.Options[0] != "user=remote" {
		t.Fatalf("recovered mapping = %+v, want oid=%d user=someuser server=recovered_srv options=[user=remote]", got, wantOID)
	}

	// A subsequent fresh (non-recovery) registration must not collide with
	// the replayed OID — nextOID must have been bumped past it.
	fresh := c.RegisterUserMapping("otheruser", "otherserver", nil)
	if fresh.OID <= wantOID {
		t.Fatalf("fresh RegisterUserMapping minted OID %d, want > recovered OID %d (nextOID not bumped)", fresh.OID, wantOID)
	}
}

// TestUnregisterUserMappingDuringRecoveryRemovesEntry is the DROP counterpart.
func TestUnregisterUserMappingDuringRecoveryRemovesEntry(t *testing.T) {
	c := NewInMemory()
	c.RegisterUserMappingDuringRecovery("gone_user", "gone_srv", nil, 40780)
	if len(c.ListUserMappings()) != 1 {
		t.Fatalf("setup: ListUserMappings did not register the mapping")
	}

	c.UnregisterUserMappingDuringRecovery("gone_user", "gone_srv")

	if list := c.ListUserMappings(); len(list) != 0 {
		t.Fatalf("after UnregisterUserMappingDuringRecovery, ListUserMappings = %+v, want empty", list)
	}
	// Idempotent: unregistering an already-gone mapping must not panic or error.
	c.UnregisterUserMappingDuringRecovery("gone_user", "gone_srv")
}
