package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// TestPBKDF2HMACSHA256KnownAnswer pins the PBKDF2 implementation to a
// published RFC 7914 / RFC 6070 known-answer pair. SCRAM correctness
// depends on this primitive being right; if this fails, every other
// SCRAM test will be unhelpfully broken.
func TestPBKDF2HMACSHA256KnownAnswer(t *testing.T) {
	// RFC 7914 §11 test vector for PBKDF2-HMAC-SHA-256.
	got := pbkdf2HMACSHA256([]byte("passwd"), []byte("salt"), 1, 64)
	want, _ := hex.DecodeString(
		"55ac046e56e3089fec1691c22544b605" +
			"f94185216dde0465e68b9d57c20dacbc" +
			"49ca9cccf179b645991664b39d77ef31" +
			"7c71b845b1e30bd509112041d3a19783",
	)
	if string(got) != string(want) {
		t.Fatalf("PBKDF2-HMAC-SHA-256 RFC 7914 vector mismatch:\n got  %x\n want %x", got, want)
	}
}

// TestSCRAMSecretRoundTrip ensures upstream-format secrets parse and
// re-serialise identically (modulo base64 padding, which we don't
// strip).
func TestSCRAMSecretRoundTrip(t *testing.T) {
	s, err := NewSCRAMSecret("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	encoded := s.String()
	if !strings.HasPrefix(encoded, "SCRAM-SHA-256$4096:") {
		t.Errorf("encoded prefix wrong: %q", encoded)
	}
	parsed, err := ParseSCRAMSecret(encoded)
	if err != nil {
		t.Fatalf("ParseSCRAMSecret: %v", err)
	}
	if parsed.Iterations != s.Iterations ||
		string(parsed.Salt) != string(s.Salt) ||
		string(parsed.StoredKey) != string(s.StoredKey) ||
		string(parsed.ServerKey) != string(s.ServerKey) {
		t.Fatalf("round-trip differs:\n got  %+v\n want %+v", parsed, s)
	}
}

func TestSCRAMSecretVerifyPlainPassword(t *testing.T) {
	s, err := NewSCRAMSecret("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !s.VerifySCRAMSecretFromPassword("hunter2") {
		t.Error("correct password rejected")
	}
	if s.VerifySCRAMSecretFromPassword("wrong") {
		t.Error("wrong password accepted")
	}
}

// TestSCRAMExchangeSuccess drives the full server state machine with a
// hand-rolled client to verify both halves of the exchange agree.
func TestSCRAMExchangeSuccess(t *testing.T) {
	const username, password = "alice", "hunter2"
	secret, err := NewSCRAMSecret(password)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewSCRAMServer(username, secret)
	if err != nil {
		t.Fatal(err)
	}

	// Client-first-message: gs2 header "n,," + client-first-bare.
	clientNonce := "rOprNGfwEbeRWgbNEkqO"
	clientFirstBare := "n=" + username + ",r=" + clientNonce
	clientFirst := "n,," + clientFirstBare

	serverFirstBytes, done, err := srv.Step([]byte(clientFirst))
	if err != nil || done {
		t.Fatalf("Step1: err=%v done=%v", err, done)
	}
	serverFirst := string(serverFirstBytes)
	attrs, err := parseSCRAMAttrs(serverFirst)
	if err != nil {
		t.Fatalf("parse server-first: %v", err)
	}
	combinedNonce := attrs["r"]
	if !strings.HasPrefix(combinedNonce, clientNonce) {
		t.Fatalf("server nonce did not extend client nonce: %q", combinedNonce)
	}
	salt, _ := base64.StdEncoding.DecodeString(attrs["s"])
	iter := 0
	for _, c := range attrs["i"] {
		iter = iter*10 + int(c-'0')
	}

	// Compute client proof per RFC 5802.
	saltedPassword := pbkdf2HMACSHA256([]byte(password), salt, iter, scramKeyLen)
	clientKey := hmacSHA256(saltedPassword, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)
	gs2Header := base64.StdEncoding.EncodeToString([]byte("n,,"))
	clientFinalWithoutProof := "c=" + gs2Header + ",r=" + combinedNonce
	authMessage := clientFirstBare + "," + serverFirst + "," + clientFinalWithoutProof
	clientSignature := hmacSHA256(storedKey, []byte(authMessage))
	clientProof := xor(clientKey, clientSignature)
	clientFinal := clientFinalWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(clientProof)

	serverFinal, done, err := srv.Step([]byte(clientFinal))
	if err != nil {
		t.Fatalf("Step2: %v", err)
	}
	if !done {
		t.Fatal("Step2: done=false")
	}
	if !strings.HasPrefix(string(serverFinal), "v=") {
		t.Fatalf("server-final missing v=: %s", serverFinal)
	}

	// Verify ServerSignature matches the spec.
	serverKey := hmacSHA256(saltedPassword, []byte("Server Key"))
	expectedSig := hmacSHA256(serverKey, []byte(authMessage))
	gotSig, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(serverFinal), "v="))
	if !hmac.Equal(gotSig, expectedSig) {
		t.Fatalf("ServerSignature mismatch:\n got  %x\n want %x", gotSig, expectedSig)
	}
}

// TestSCRAMExchangeRejectsBadProof: a wrong client proof yields
// ErrInvalidPassword from Step.
func TestSCRAMExchangeRejectsBadProof(t *testing.T) {
	secret, err := NewSCRAMSecret("realpw")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewSCRAMServer("alice", secret)
	if err != nil {
		t.Fatal(err)
	}
	clientFirst := "n,,n=alice,r=ABCDEFGH"
	if _, _, err := srv.Step([]byte(clientFirst)); err != nil {
		t.Fatalf("step1: %v", err)
	}
	// Submit a structurally-valid client-final-message with a bogus proof.
	bogus := base64.StdEncoding.EncodeToString(make([]byte, scramKeyLen))
	gs2 := base64.StdEncoding.EncodeToString([]byte("n,,"))
	// Combined nonce: client appended server nonce in step1; we don't
	// have it here, but Step builds authMessage from what we send. Use
	// the format the server expects: c=,r=clientNonce+server.serverNonce.
	combined := "ABCDEFGH" + srv.serverNonce
	clientFinal := "c=" + gs2 + ",r=" + combined + ",p=" + bogus
	_, _, err = srv.Step([]byte(clientFinal))
	if err == nil {
		t.Fatal("expected ErrInvalidPassword, got nil")
	}
	if _, ok := err.(ErrInvalidPassword); !ok {
		t.Fatalf("got %T (%v), want ErrInvalidPassword", err, err)
	}
}

// TestSCRAMServerDoomedFailsCleanly verifies that constructing a
// server with a nil secret (unknown user) runs the full state machine
// to its proof check and then returns ErrInvalidPassword without
// panicking.
func TestSCRAMServerDoomedFailsCleanly(t *testing.T) {
	srv, err := NewSCRAMServer("nobody", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.doomed {
		t.Fatal("expected doomed=true for nil secret")
	}
	if _, _, err := srv.Step([]byte("n,,n=nobody,r=AAAA")); err != nil {
		t.Fatalf("step1: %v", err)
	}
	bogus := base64.StdEncoding.EncodeToString(make([]byte, scramKeyLen))
	gs2 := base64.StdEncoding.EncodeToString([]byte("n,,"))
	clientFinal := "c=" + gs2 + ",r=AAAA" + srv.serverNonce + ",p=" + bogus
	_, _, err = srv.Step([]byte(clientFinal))
	if _, ok := err.(ErrInvalidPassword); !ok {
		t.Fatalf("got %T (%v), want ErrInvalidPassword", err, err)
	}
}

// Sanity check that sha256Sum agrees with crypto/sha256 directly.
func TestSHA256SumAgrees(t *testing.T) {
	got := sha256Sum([]byte("test"))
	want := sha256.Sum256([]byte("test"))
	if string(got) != string(want[:]) {
		t.Fatalf("sha256Sum diverged from crypto/sha256")
	}
}
