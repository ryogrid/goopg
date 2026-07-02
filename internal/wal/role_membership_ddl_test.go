package wal

import "testing"

func boolPtr(b bool) *bool { return &b }

// TestEncodeDecodeGrantRoleMembershipRoundTrip pins the M0119-0004-ACLHEAP
// GRANT ROLE membership restart-persistence WAL record format, including the
// tri-state (nil = unspecified) ADMIN/INHERIT/SET option bytes.
func TestEncodeDecodeGrantRoleMembershipRoundTrip(t *testing.T) {
	cases := []struct {
		roleOid, memberOid, grantorOid uint32
		admin, inherit, set            *bool
	}{
		{16385, 16386, 10, nil, nil, nil},
		{16385, 16386, 10, boolPtr(false), nil, nil},
		{16385, 16386, 10, boolPtr(true), boolPtr(true), boolPtr(true)},
		{16385, 16386, 10, boolPtr(false), boolPtr(false), boolPtr(false)},
		{16385, 16386, 10, nil, boolPtr(true), boolPtr(false)},
		{4294967295, 4294967295, 4294967295, boolPtr(true), nil, boolPtr(false)},
	}
	for _, c := range cases {
		raw := EncodeGrantRoleMembership(c.roleOid, c.memberOid, c.grantorOid, c.admin, c.inherit, c.set)
		if raw[0] != RecordKindGrantRoleMembership {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindGrantRoleMembership)
			continue
		}
		gotRole, gotMember, gotGrantor, gotAdmin, gotInherit, gotSet, err := DecodeGrantRoleMembership(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if gotRole != c.roleOid || gotMember != c.memberOid || gotGrantor != c.grantorOid ||
			!triBoolEqual(gotAdmin, c.admin) || !triBoolEqual(gotInherit, c.inherit) || !triBoolEqual(gotSet, c.set) {
			t.Errorf("decoded (%d, %d, %d, %v, %v, %v), want (%d, %d, %d, %v, %v, %v)",
				gotRole, gotMember, gotGrantor, triBoolStr(gotAdmin), triBoolStr(gotInherit), triBoolStr(gotSet),
				c.roleOid, c.memberOid, c.grantorOid, triBoolStr(c.admin), triBoolStr(c.inherit), triBoolStr(c.set))
		}
	}
}

func triBoolEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func triBoolStr(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}

// TestDecodeGrantRoleMembershipRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeGrantRoleMembershipRejectsTruncated(t *testing.T) {
	raw := EncodeGrantRoleMembership(16385, 16386, 10, boolPtr(true), nil, nil)
	if _, _, _, _, _, _, err := DecodeGrantRoleMembership(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, _, _, _, err := DecodeGrantRoleMembership([]byte{RecordKindRevokeRoleMembership, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}

// TestEncodeDecodeRevokeRoleMembershipRoundTrip is the REVOKE counterpart,
// covering the full revoke ("") plus all three OPTION FOR selectors.
func TestEncodeDecodeRevokeRoleMembershipRoundTrip(t *testing.T) {
	cases := []struct {
		roleOid, memberOid uint32
		revokeOption       string
	}{
		{16385, 16386, ""},
		{16385, 16386, "admin"},
		{16385, 16386, "inherit"},
		{16385, 16386, "set"},
		{4294967295, 4294967295, "admin"},
	}
	for _, c := range cases {
		raw := EncodeRevokeRoleMembership(c.roleOid, c.memberOid, c.revokeOption)
		if raw[0] != RecordKindRevokeRoleMembership {
			t.Errorf("kind byte = %d, want %d", raw[0], RecordKindRevokeRoleMembership)
			continue
		}
		gotRole, gotMember, gotOpt, err := DecodeRevokeRoleMembership(raw)
		if err != nil {
			t.Errorf("decode err: %v", err)
			continue
		}
		if gotRole != c.roleOid || gotMember != c.memberOid || gotOpt != c.revokeOption {
			t.Errorf("decoded (%d, %d, %q), want (%d, %d, %q)",
				gotRole, gotMember, gotOpt, c.roleOid, c.memberOid, c.revokeOption)
		}
	}
}

// TestDecodeRevokeRoleMembershipRejectsTruncated pins the defensive
// truncated/wrong-kind guards.
func TestDecodeRevokeRoleMembershipRejectsTruncated(t *testing.T) {
	raw := EncodeRevokeRoleMembership(16385, 16386, "admin")
	if _, _, _, err := DecodeRevokeRoleMembership(raw[:len(raw)-1]); err == nil {
		t.Error("truncated payload should error")
	}
	if _, _, _, err := DecodeRevokeRoleMembership([]byte{RecordKindGrantRoleMembership, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("wrong-kind payload should error")
	}
}
