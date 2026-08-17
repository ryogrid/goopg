package auth

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

// TestPlaintextCredentialVerify covers the simple exact-match branch.
func TestPlaintextCredentialVerify(t *testing.T) {
	c := NewPlaintextCredential("hunter2")
	if !c.VerifyCleartext("alice", "hunter2") {
		t.Error("correct password rejected")
	}
	if c.VerifyCleartext("alice", "wrong") {
		t.Error("wrong password accepted")
	}
}

// TestMD5CredentialMatchesUpstream pins NewMD5Credential to the upstream
// pg_md5_encrypt(password, username) recipe so a credential generated
// by goopg is bit-for-bit equal to what `psql` would store in
// pg_authid.rolpassword.
func TestMD5CredentialMatchesUpstream(t *testing.T) {
	const user, password = "alice", "hunter2"
	sum := md5.Sum([]byte(password + user))
	want := "md5" + hex.EncodeToString(sum[:])

	c := NewMD5Credential(user, password)
	if c.Type != PasswordMD5 {
		t.Fatalf("type = %v, want PasswordMD5", c.Type)
	}
	if c.Secret != want {
		t.Fatalf("secret = %q, want %q", c.Secret, want)
	}

	// VerifyCleartext on an MD5 credential should reproduce the
	// shadow on the fly.
	if !c.VerifyCleartext(user, password) {
		t.Error("VerifyCleartext rejected the original password")
	}
	if c.VerifyCleartext(user, "wrong") {
		t.Error("VerifyCleartext accepted wrong password")
	}
}

// TestMD5ChallengeReproducesClientResponse reproduces what a libpq
// client computes (postgres/src/backend/libpq/crypt.c:md5_crypt_verify)
// and verifies VerifyMD5Challenge accepts that exact byte sequence.
// This is the load-bearing test for md5 auth interop.
func TestMD5ChallengeReproducesClientResponse(t *testing.T) {
	const user, password = "alice", "hunter2"
	salt := [4]byte{0xAA, 0xBB, 0xCC, 0xDD}

	// Client-side computation (matches what libpq does):
	innerSum := md5.Sum([]byte(password + user))
	inner := hex.EncodeToString(innerSum[:]) // 32 hex chars
	outerSum := md5.Sum([]byte(inner + string(salt[:])))
	clientResponse := "md5" + hex.EncodeToString(outerSum[:])

	// Server-side: we have the *shadowed* credential.
	stored := NewMD5Credential(user, password)
	if !stored.VerifyMD5Challenge(user, salt, clientResponse) {
		t.Fatalf("md5 challenge rejected; got %q", clientResponse)
	}
	if stored.VerifyMD5Challenge(user, salt, "md5"+inner /* salt-less, wrong */) {
		t.Error("md5 challenge accepted a salt-less response")
	}

	// Plaintext credential should also verify the same client response,
	// because we shadow the password on the fly.
	plain := NewPlaintextCredential(password)
	if !plain.VerifyMD5Challenge(user, salt, clientResponse) {
		t.Error("plaintext credential failed md5 challenge")
	}
}

func TestParseMD5Stored(t *testing.T) {
	good := "md5" + hex.EncodeToString(make([]byte, 16))
	if _, err := ParseMD5Stored(good); err != nil {
		t.Fatalf("good shadow rejected: %v", err)
	}
	for _, bad := range []string{
		"plaintext",
		"md5short",
		"md5" + "ZZ" + hex.EncodeToString(make([]byte, 15)), // non-hex
	} {
		if _, err := ParseMD5Stored(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestMapUserStore(t *testing.T) {
	s := NewMapUserStore()
	if _, ok := s.Lookup("alice"); ok {
		t.Error("empty store returned ok=true")
	}
	s.Set("alice", NewPlaintextCredential("hunter2"))
	c, ok := s.Lookup("alice")
	if !ok || c.Secret != "hunter2" {
		t.Errorf("Lookup = (%v, %v), want (hunter2, true)", c, ok)
	}
}
