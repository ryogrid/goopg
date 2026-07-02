package wal

import "testing"

// TestEncodeDecodeGrantRoleMembershipRoundTrip pins the M0119-0004-ACLHEAP
// GRANT ROLE membership restart-persistence WAL record format.
func TestEncodeDecodeGrantRoleMembershipRoundTrip(t *testing.T) {
	cases := []struct {
		roleOid, memberOid, grantorOid uint32
		adminOption                    bool
	}{
		{16385, 16386, 10, false},
		{16385, 16386, 10, true},
		{4294967295, 4294967295, 4294967295, true},
	}
	for _, c := range cases {
		raw := EncodeGrantRoleMembership(c.roleOid, c.memberOid, c.grantorOid, c.adminOption)
		if raw[0] != RecordKindGrantRoleMembership {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindGrantRoleMembership)
			continue
		}
		gotRole, gotMember, gotGrantor, gotAdmin, err := DecodeGrantRoleMembership(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if gotRole != c.roleOid || gotMember != c.memberOid || gotGrantor != c.grantorOid || gotAdmin != c.adminOption {
			t.Errorf("decoded (%d, %d, %d, %v), want (%d, %d, %d, %v)",
				gotRole, gotMember, gotGrantor, gotAdmin, c.roleOid, c.memberOid, c.grantorOid, c.adminOption)
		}
	}
}

// TestDecodeGrantRoleMembershipRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeGrantRoleMembershipRejectsTruncated(t *testing.T) {
	raw := EncodeGrantRoleMembership(16385, 16386, 10, true)
	if _, _, _, _, err := DecodeGrantRoleMembership(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, _, err := DecodeGrantRoleMembership([]byte{RecordKindRevokeRoleMembership, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}

// TestEncodeDecodeRevokeRoleMembershipRoundTrip is the REVOKE counterpart.
func TestEncodeDecodeRevokeRoleMembershipRoundTrip(t *testing.T) {
	cases := []struct {
		roleOid, memberOid uint32
		adminOptionOnly    bool
	}{
		{16385, 16386, false},
		{16385, 16386, true},
		{4294967295, 4294967295, true},
	}
	for _, c := range cases {
		raw := EncodeRevokeRoleMembership(c.roleOid, c.memberOid, c.adminOptionOnly)
		if raw[0] != RecordKindRevokeRoleMembership {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindRevokeRoleMembership)
			continue
		}
		gotRole, gotMember, gotAdminOnly, err := DecodeRevokeRoleMembership(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if gotRole != c.roleOid || gotMember != c.memberOid || gotAdminOnly != c.adminOptionOnly {
			t.Errorf("decoded (%d, %d, %v), want (%d, %d, %v)",
				gotRole, gotMember, gotAdminOnly, c.roleOid, c.memberOid, c.adminOptionOnly)
		}
	}
}

// TestDecodeRevokeRoleMembershipRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeRevokeRoleMembershipRejectsTruncated(t *testing.T) {
	raw := EncodeRevokeRoleMembership(16385, 16386, true)
	if _, _, _, err := DecodeRevokeRoleMembership(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, err := DecodeRevokeRoleMembership([]byte{RecordKindGrantRoleMembership, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}
