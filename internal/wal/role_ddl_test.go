package wal

// Encode/decode round trips for the role restart-persistence records
// (RecordKindRoleState / RecordKindDropRole, root-0021).

import "testing"

func TestEncodeDecodeRoleStateRoundTrip(t *testing.T) {
	cases := []RoleStatePayload{
		{Name: "wpuser", OID: 16401, CanLogin: true, Superuser: false,
			CredType: 3, Secret: "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy"},
		{Name: "admin_role", OID: 16385, CanLogin: false, Superuser: true,
			CredType: 0, Secret: ""},
		{Name: "md5user", OID: 20000, CanLogin: true, Superuser: false,
			CredType: 2, Secret: "md50123456789abcdef0123456789abcdef"},
		// Multi-byte role name + password survive the byte-length encoding.
		{Name: "日本語ロール", OID: 16999, CanLogin: true, Superuser: true,
			CredType: 1, Secret: "パスワード'with quote"},
		// OID 0 (unknown at emit time) round-trips as 0.
		{Name: "noid", OID: 0, CanLogin: true},
	}
	for _, want := range cases {
		got, err := DecodeRoleState(EncodeRoleState(want))
		if err != nil {
			t.Fatalf("%s: decode: %v", want.Name, err)
		}
		if got != want {
			t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
		}
	}
}

func TestDecodeRoleStateGuards(t *testing.T) {
	if _, err := DecodeRoleState(nil); err == nil {
		t.Fatal("nil payload: want error")
	}
	if _, err := DecodeRoleState([]byte{RecordKindDropRole, 0, 0}); err == nil {
		t.Fatal("wrong kind: want error")
	}
	full := EncodeRoleState(RoleStatePayload{Name: "r", Secret: "s"})
	for cut := 1; cut < len(full); cut++ {
		if _, err := DecodeRoleState(full[:cut]); err == nil {
			t.Fatalf("truncated at %d bytes: want error", cut)
		}
	}
}

func TestEncodeDecodeDropRoleRoundTrip(t *testing.T) {
	for _, name := range []string{"wpuser", "日本語ロール", ""} {
		got, err := DecodeDropRole(EncodeDropRole(name))
		if err != nil {
			t.Fatalf("%q: decode: %v", name, err)
		}
		if got != name {
			t.Fatalf("round trip: got %q want %q", got, name)
		}
	}
	if _, err := DecodeDropRole([]byte{RecordKindRoleState, 0, 0}); err == nil {
		t.Fatal("wrong kind: want error")
	}
}
